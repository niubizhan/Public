package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 内嵌 Mozilla CA 包。
//
// Android 的根证书放在 /system/etc/security/cacerts/*.0（散装 PEM），
// 而 Go 的 crypto/x509 只认 /etc/ssl/certs/ca-certificates.crt 这类桌面路径，
// 在电视上会直接报 "certificate signed by unknown authority"。
// 只有 linux/arm 交叉编译时才会踩到，桌面调试看不出问题。
//
//go:embed cacert.pem
var caBundle []byte

var (
	rootCAsOnce sync.Once
	rootCAs     *x509.CertPool
	// insecureTLS 供 -insecure 打开：个别源站证书链断裂时的最后手段
	insecureTLS bool
)

func loadRootCAs() *x509.CertPool {
	rootCAsOnce.Do(func() {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBundle) {
			fmt.Println("[warn] embedded CA bundle parse failed")
		}
		// 再叠加系统证书：内嵌包可能缺某些国产 CA，系统里反而有
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

// 模拟浏览器 UA。注意部分源站会对可疑 UA 直接返回验证页。
const UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// Accept 里不能带 application/json —— 部分源站（如飘花）会因此返回
// JSON 转义过的 HTML（\uXXXX + \"），导致后面所有正则失配。
const acceptHdr = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"

type fetchOpts struct {
	Method  string
	Body    string
	Headers map[string]string
	Referer string
	TTL     int           // 秒；<=0 表示不缓存
	Timeout time.Duration // 单次请求超时
}

type cacheEntry struct {
	body   string
	expire time.Time
}

var (
	textCache  sync.Map // "METHOD url" -> cacheEntry
	httpClient *http.Client
	httpOnce   sync.Once
)

// 电视上没有任何 /etc/resolv.conf，Go 的纯 Go 解析器会直接失败，
// 所以启动时必须把 Resolver 换成「显式问路由器 DNS」的实现。
func setupResolver() string {
	dns := "223.5.5.5"
	if out, err := runGetprop("net.dns1"); err == nil {
		if ip := net.ParseIP(strings.TrimSpace(out)); ip != nil {
			dns = strings.TrimSpace(out)
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

func getClient() *http.Client {
	httpOnce.Do(func() {
		tr := &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   12 * time.Second,
			ResponseHeaderTimeout: 25 * time.Second,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   6,
			IdleConnTimeout:       60 * time.Second,
			ForceAttemptHTTP2:     false, // 老内核上 h2 偶发握手卡住，固定走 http/1.1
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

// fetchText 抓取文本。失败重试 3 次（退避 600ms / 1500ms），4xx（除 429）不重试。
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

		// 证书链问题：换一个不校验的 client 再试一次。
		// 灰产源站经常只下发 leaf 证书、缺中间证书，桌面系统能靠系统缓存补上，
		// 电视上没有这层缓存就直接 x509 报错。只对失败请求降级，正常请求仍走校验。
		if isCertError(err) {
			if b2, _, err2 := doOnce(rawurl, o, getInsecureClient()); err2 == nil {
				log.Printf("[tls] cert verify failed, used insecure fallback: %s", shortURL(rawurl))
				return b2, nil
			}
		}

		if status >= 400 && status < 500 && status != 429 {
			break // 4xx 不值得重试
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("fetch failed: %s", rawurl)
	}
	return "", lastErr
}

// doOnce 发一次请求。cli 为 nil 时用默认 client。
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

func isCertError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "x509") || strings.Contains(s, "certificate signed by unknown authority") ||
		strings.Contains(s, "certificate is not trusted") || strings.Contains(s, "failed to verify certificate")
}

var (
	insecureOnce sync.Once
	insecureCli  *http.Client
)

// getInsecureClient 只在证书校验失败时兜底使用。
func getInsecureClient() *http.Client {
	insecureOnce.Do(func() {
		tr := &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   12 * time.Second,
			ResponseHeaderTimeout: 25 * time.Second,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       60 * time.Second,
			ForceAttemptHTTP2:     false,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		}
		insecureCli = &http.Client{Transport: tr, Timeout: 40 * time.Second}
	})
	return insecureCli
}

var wsRe = regexp.MustCompile(`[\s\x{00A0}\x{3000}]+`)

// decodeEntities 用标准库 html 包，覆盖命名实体与数字实体。
func decodeEntities(s string) string {
	return html.UnescapeString(s)
}

// stripTags 去标签 + 实体解码 + 压缩空白（与 JS 版 stripTags 等价）。
func stripTags(s string) string {
	if s == "" {
		return ""
	}
	s = tagRe.ReplaceAllString(s, "")
	s = decodeEntities(s)
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

var (
	tagRe     = regexp.MustCompile(`<[^>]*>`)
	attrNameR = regexp.MustCompile(`[A-Za-z_][\w\-]*`)
)

// attr 从标签串里取属性值。
// JS 版用了负向后行断言 (?<![\w-]) 避免匹配到 data-src 里的 src；
// Go 的 regexp 是 RE2，不支持后行断言，改成要求属性前是空白或标签开头 —— 效果一致。
func attr(tag, name string) string {
	if tag == "" || !attrNameR.MatchString(name) {
		return ""
	}
	re := regexp.MustCompile(`(?:^|[\s"'])` + regexp.QuoteMeta(name) + `\s*=\s*"([^"]*)"`)
	if m := re.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return ""
}

// absURL 相对地址转绝对地址。
func absURL(base, u string) string {
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(u, "/")
}

var (
	jsKeyRe  = regexp.MustCompile(`([{,]\s*)([A-Za-z_$][A-Za-z0-9_$]*)(\s*:)`)
	jsStrRe  = regexp.MustCompile(`'((?:[^'\\]|\\.)*)'`)
	trailCom = regexp.MustCompile(`,\s*([}\]])`)
)

// jsLiteralToJSON 把 JS 单引号对象字面量转成 JSON 文本（纯文本替换，不执行代码）。
func jsLiteralToJSON(text string) string {
	s := jsKeyRe.ReplaceAllString(text, `$1"$2"$3`)

	// 单引号字符串 -> 双引号字符串
	var b strings.Builder
	last := 0
	for _, loc := range jsStrRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(s[last:loc[0]])
		inner := unescapeJS(s[loc[2]:loc[3]])
		b.WriteString(`"`)
		b.WriteString(strings.ReplaceAll(inner, `"`, `\"`))
		b.WriteString(`"`)
		last = loc[1]
	}
	b.WriteString(s[last:])
	out := b.String()

	// 对象/数组尾部的逗号在 JS 里合法、在 JSON 里非法
	out = trailCom.ReplaceAllString(out, `$1`)
	return out
}

// unescapeJS 还原 JS 字符串里的常见转义。
func unescapeJS(s string) string {
	s = strings.ReplaceAll(s, `\'`, `'`)
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\t`, "\t")
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

// extractBalanced 从 src[from:] 的第一个 '{' 起做括号配平，取出完整对象文本（字符串内的括号不计）。
func extractBalanced(src string, from int) string {
	if from < 0 || from >= len(src) {
		return ""
	}
	start := strings.IndexByte(src[from:], '{')
	if start < 0 {
		return ""
	}
	start += from

	depth := 0
	var quote byte
	esc := false
	for i := start; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	return ""
}

// findQuotedVar 读取页面里 `var name = 'xxx'` 的字符串值。
func findQuotedVar(src, name string) string {
	re := regexp.MustCompile(`var\s+` + regexp.QuoteMeta(name) + `\s*=\s*['"]((?:[^'"\\]|\\.)*)['"]`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	return unescapeJS(m[1])
}

// uniq 去重并丢掉空值。
func uniq(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// jsonNum 把 JSON 数字原样转成字符串（避免 float64 变成 1.2e+06）。
func jsonNum(v interface{}) string {
	switch n := v.(type) {
	case json.Number:
		return n.String()
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case string:
		return n
	}
	return ""
}

// mustURL 解析 URL，失败返回 nil。
func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		return nil
	}
	return u
}

// encURI 等价于 JS 的 encodeURIComponent。
func encURI(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
