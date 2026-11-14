package main

// 电视（Android 4.4 / armv7）专用的 HTTP 基础层。
//
// 原稿用 http.Get(url)，在这台电视上有三个致命问题：
//  1. 无 UA：站点直接返验证页；
//  2. 无超时：源站掐线时 goroutine 永久挂起，TVBox 端表现为「一直转圈」；
//  3. 无 CA：Go 的 crypto/x509 不认 Android 的散装 /system/etc/security/cacerts/*.0，
//     HTTPS 全部报 "certificate signed by unknown authority"；
//  4. 无 DNS：电视没有 /etc/resolv.conf，Go 纯解析器直接失败。
//
// 因此这里：内嵌 Mozilla CA 包 + 显式问路由器 DNS + 全链路超时 + 重试 + 模拟浏览器 UA。
// 抓取默认「直连」，不走代理（代理只在安装 Go 环境/下载依赖时使用）。
// 若确需代理，可 -proxy http://<电脑局域网IP>:7890 显式打开。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

//go:embed cacert.pem
var caBundle []byte

const UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// Accept 不能带 application/json：部分源站会因此返回 JSON 转义过的 HTML，
// 导致后面所有解析失配。
const acceptHdr = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"

var (
	proxyAddr   string
	insecureTLS bool

	rootCAsOnce sync.Once
	rootCAs     *x509.CertPool

	clientOnce   sync.Once
	httpClient   *http.Client
	insecureOnce sync.Once
	insecureCli  *http.Client

	textCache sync.Map // "METHOD url" -> cacheEntry
)

type cacheEntry struct {
	body   string
	expire time.Time
}

type fetchOpts struct {
	Method  string
	Body    string
	Headers map[string]string
	Referer string
	TTL     int // 秒；<0 表示禁用缓存
	Timeout time.Duration
}

func loadRootCAs() *x509.CertPool {
	rootCAsOnce.Do(func() {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(caBundle)
		// 叠加系统证书：内嵌包可能缺国产 CA，Android 系统里反而有
		for _, dir := range []string{"/system/etc/security/cacerts", "/etc/ssl/certs", "/etc/pki/tls/certs"} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
					pool.AppendCertsFromPEM(b)
				}
			}
		}
		rootCAs = pool
	})
	return rootCAs
}

// setupResolver 电视上没有 /etc/resolv.conf，改用 getprop net.dns1 询问路由器。
func setupResolver() string {
	dns := "223.5.5.5"
	if out, err := exec.Command("/system/bin/getprop", "net.dns1").Output(); err == nil {
		if ip := net.ParseIP(strings.TrimSpace(string(out))); ip != nil {
			dns = strings.TrimSpace(string(out))
		}
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", dns+":53")
		},
	}
	return dns
}

// proxyFromFlag 默认返回 nil = 直连。只有显式 -proxy 才启用。
func proxyFromFlag() func(*http.Request) (*url.URL, error) {
	if proxyAddr == "" {
		return nil
	}
	u, err := url.Parse(proxyAddr)
	if err != nil {
		log.Printf("[proxy] bad addr %q: %v, fallback to direct", proxyAddr, err)
		return nil
	}
	return http.ProxyURL(u)
}

func getClient() *http.Client {
	clientOnce.Do(func() {
		tr := &http.Transport{
			Proxy:                 proxyFromFlag(),
			DialContext:           (&net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   12 * time.Second,
			ResponseHeaderTimeout: 25 * time.Second,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   6,
			IdleConnTimeout:       60 * time.Second,
			ForceAttemptHTTP2:     false, // 老内核 h2 偶发握手卡住
			TLSClientConfig: &tls.Config{
				RootCAs:            loadRootCAs(),
				InsecureSkipVerify: insecureTLS,
			},
		}
		httpClient = &http.Client{
			Transport: tr,
			Timeout:   40 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 6 {
					return errors.New("too many redirects")
				}
				return nil
			},
		}
	})
	return httpClient
}

func getInsecureClient() *http.Client {
	insecureOnce.Do(func() {
		tr := &http.Transport{
			Proxy:                 proxyFromFlag(),
			DialContext:           (&net.Dialer{Timeout: 12 * time.Second}).DialContext,
			TLSHandshakeTimeout:   12 * time.Second,
			ResponseHeaderTimeout: 25 * time.Second,
			ForceAttemptHTTP2:     false,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		}
		insecureCli = &http.Client{Transport: tr, Timeout: 40 * time.Second}
	})
	return insecureCli
}

func fetchText(rawurl string, o fetchOpts) (string, error) {
	if o.Method == "" {
		o.Method = "GET"
	}
	if o.TTL == 0 {
		o.TTL = 120
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}

	key := o.Method + " " + rawurl
	if o.TTL > 0 {
		if v, ok := textCache.Load(key); ok {
			e := v.(cacheEntry)
			if time.Now().Before(e.expire) {
				return e.body, nil
			}
		}
	}

	body, err := doFetch(rawurl, o)
	if err != nil {
		return "", err
	}
	if o.TTL > 0 {
		textCache.Store(key, cacheEntry{body: body, expire: time.Now().Add(time.Duration(o.TTL) * time.Second)})
	}
	return body, nil
}

func doFetch(rawurl string, o fetchOpts) (string, error) {
	var lastErr error
	delays := []time.Duration{600 * time.Millisecond, 1500 * time.Millisecond}
	for attempt := 0; attempt <= len(delays); attempt++ {
		if attempt > 0 {
			time.Sleep(delays[attempt-1])
		}
		body, status, err := doOnce(rawurl, o, nil)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if isCertError(err) {
			if b2, _, err2 := doOnce(rawurl, o, getInsecureClient()); err2 == nil {
				log.Printf("[tls] cert fallback: %s", shortURL(rawurl))
				return b2, nil
			}
		}
		if status >= 400 && status < 500 && status != 429 {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("fetch failed: %s", rawurl)
	}
	return "", lastErr
}

func doOnce(rawurl string, o fetchOpts, cli *http.Client) (string, int, error) {
	var rd io.Reader
	if o.Body != "" {
		rd = strings.NewReader(o.Body)
	}
	req, err := http.NewRequest(o.Method, rawurl, rd)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", UA)
	req.Header.Set("Accept", acceptHdr)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	for k, v := range o.Headers {
		req.Header.Set(k, v)
	}
	if o.Referer != "" {
		req.Header.Set("Referer", o.Referer)
	}
	if cli == nil {
		c := *getClient()
		c.Timeout = o.Timeout
		cli = &c
	}
	resp, err := cli.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, rerr := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, fmt.Errorf("HTTP %d %s", resp.StatusCode, rawurl)
	}
	if rerr != nil {
		return "", resp.StatusCode, rerr
	}
	return string(b), resp.StatusCode, nil
}

// fetchDoc 用 goquery 解析 HTML（替代原稿里手写的 http.Get + NewDocumentFromReader）。
func fetchDoc(rawurl string, o fetchOpts) (*goquery.Document, error) {
	html, err := fetchText(rawurl, o)
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func isCertError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "x509") || strings.Contains(s, "unknown authority") ||
		strings.Contains(s, "not trusted") || strings.Contains(s, "failed to verify certificate")
}

func fetchBinary(rawurl string, headers map[string]string, timeout time.Duration) ([]byte, string, error) {
	req, err := http.NewRequest("GET", rawurl, nil)
	if err != nil {
		return nil, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	cli := *getClient()
	cli.Timeout = timeout
	resp, err := cli.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("upstream %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("empty body")
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	return data, ct, nil
}

func shortURL(u string) string {
	if len(u) > 80 {
		return u[:80] + "..."
	}
	return u
}
