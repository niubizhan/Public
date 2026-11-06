package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 直播代理。
//
// 为什么需要它：这份直播源里大量频道是「302 跨域跳转 + 清单内相对路径」
// （形如 `cctv15md/segment_135107.ts`）。新版播放器会按跳转后的最终地址
// 拼接，老版（这台电视上的 TVBox 4.4）会拼回原始域名，那个地址根本不存在，
// 表现就是永远转圈。
//
// 做法只重写清单，不代理视频字节：
//   1. GET /live.txt  —— 现取上游列表，把每个频道地址换成 /lv?u=<base64>
//   2. GET /lv?u=...  —— 取清单、跟随跳转，把里面的相对地址按「最终地址」绝对化
// 分片仍然由播放器直连源站，电视这颗弱 CPU 不承担转发带宽。
// 需要强制转发分片时加 &p=1（个别源站校验 Referer 时用得上）。

const (
	// 上游镜像，按实测速度排序：jsdelivr（约 2s）> gh-proxy（约 15s）> raw。
	// 这份列表每十几分钟会更新咪咕 token，所以缓存只留 60 秒。
	liveUpstreamFastly = "https://fastly.jsdelivr.net/gh/Supprise0901/TVBox_live@main/live.txt"
	liveUpstreamCDN    = "https://cdn.jsdelivr.net/gh/Supprise0901/TVBox_live@main/live.txt"
	liveUpstreamProxy  = "https://gh-proxy.com/https://raw.githubusercontent.com/Supprise0901/TVBox_live/main/live.txt"
	liveUpstreamRaw    = "https://raw.githubusercontent.com/Supprise0901/TVBox_live/main/live.txt"
	liveCacheTTL       = 60 * time.Second
)

var liveListCache struct {
	mu   sync.Mutex
	body string
	at   time.Time
}

func b64u(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func b64uDecode(s string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func reqOrigin(r *http.Request) string {
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

// ---------------------------------------------------------------- 列表

func upstreams(src string) []string {
	if src != "" {
		return []string{src}
	}
	return []string{liveUpstreamFastly, liveUpstreamCDN, liveUpstreamProxy, liveUpstreamRaw}
}

// warmLiveList 后台定时刷新列表缓存。
// 电视加载直播列表有自己的超时，上游最慢要十几秒，靠请求时现取容易被判超时，
// 所以启动后先拉一次，之后每 50 秒刷一次（缓存 TTL 是 60 秒）。
func warmLiveList() {
	for {
		if _, err := loadLiveList(""); err != nil {
			log.Printf("[live] 预热失败: %v", err)
		}
		time.Sleep(50 * time.Second)
	}
}

func loadLiveList(src string) (string, error) {
	cacheable := src == ""
	if cacheable {
		liveListCache.mu.Lock()
		if liveListCache.body != "" && time.Since(liveListCache.at) < liveCacheTTL {
			b := liveListCache.body
			liveListCache.mu.Unlock()
			return b, nil
		}
		liveListCache.mu.Unlock()
	}

	var lastErr error
	for _, u := range upstreams(src) {
		b, err := fetchText(u, fetchOpts{
			TTL:     0,
			Timeout: 25 * time.Second,
			Headers: map[string]string{"User-Agent": playerUA, "Accept": "*/*"},
		})
		if err != nil {
			lastErr = err
			continue
		}
		if !strings.Contains(b, "#genre#") && !strings.Contains(b, "#EXTM3U") {
			lastErr = fmt.Errorf("%s 返回的不是直播列表", shortURL(u))
			continue
		}
		if cacheable {
			liveListCache.mu.Lock()
			liveListCache.body, liveListCache.at = b, time.Now()
			liveListCache.mu.Unlock()
		}
		return b, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的上游地址")
	}
	return "", lastErr
}

// rewriteLiveList 把每行的 `名称,http地址` 换成 `名称,{origin}/lv?u=<base64>`。
// 分组行（以 #genre# 结尾）、注释、空行原样保留。
func rewriteLiveList(body, origin string) string {
	lines := strings.Split(body, "\n")
	total, rewrote := 0, 0
	for i, ln := range lines {
		s := strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if s == "" || strings.HasPrefix(s, "#") || strings.HasSuffix(s, "#genre#") {
			continue
		}
		idx := strings.Index(s, "http")
		if idx <= 0 {
			continue
		}
		total++
		name := strings.TrimRight(s[:idx], ",")
		name = strings.TrimRight(name, " \t")
		rawurl := strings.TrimSpace(s[idx:])
		if name == "" || rawurl == "" {
			continue
		}
		lines[i] = name + "," + origin + "/lv?u=" + b64u(rawurl)
		rewrote++
	}
	log.Printf("[live] 列表重写 %d/%d 个频道", rewrote, total)
	return strings.Join(lines, "\n")
}

func handleLiveList(w http.ResponseWriter, r *http.Request) {
	body, err := loadLiveList(strings.TrimSpace(r.URL.Query().Get("src")))
	if err != nil {
		log.Printf("[live] 上游失败: %v", err)
		http.Error(w, "upstream fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	out := rewriteLiveList(body, reqOrigin(r))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, out)
}

// ---------------------------------------------------------------- 清单

// liveGet 取一个地址，返回最终地址（响应里的 resp.Request.URL）。
//
// 注意：这里**不能**带 Referer。实测 hndt（河南卫视）等 CDN 一旦带上
// Referer 就直接 403，不带反而 200。
func liveGet(rawurl string, timeout time.Duration) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", playerUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	cli := *getClient()
	cli.Timeout = timeout
	return cli.Do(req)
}

// rewriteLivePlaylist 把清单里的地址绝对化。
//
//	master（含 #EXT-X-STREAM-INF）：变体清单改走 /lv，好让下一层也被绝对化
//	media（分片清单）：分片直接用绝对地址，播放器直连源站
func rewriteLivePlaylist(txt, finalURL, origin string, forceProxy bool) string {
	isMaster := strings.Contains(txt, "#EXT-X-STREAM-INF")
	lines := strings.Split(txt, "\n")
	out := make([]string, 0, len(lines))

	for _, ln := range lines {
		s := strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if s == "" {
			out = append(out, "")
			continue
		}
		if strings.HasPrefix(s, "#") {
			out = append(out, rewriteURIAttrs(ln, finalURL, origin, forceProxy))
			continue
		}
		abs := resolveURL(finalURL, s)
		if abs == "" {
			out = append(out, ln)
			continue
		}
		out = append(out, liveRewrite(abs, origin, isMaster, forceProxy))
	}
	return strings.Join(out, "\n")
}

// liveRewrite 决定一个清单内的地址怎么给播放器：
//
//	master 的变体清单 / 任何 .m3u8 → 走 /lv，下一层继续绝对化
//	http 分片                       → 直接给绝对地址（播放器直连，不占电视 CPU）
//	https 分片                      → 走 /lv 透传（电视 Android 4.4 的 TLS 对部分
//	                                  CDN 不兼容，交给 Go 的代取更稳）
func liveRewrite(abs, origin string, isMaster, force bool) string {
	lower := strings.ToLower(abs)
	isPlaylist := strings.Contains(lower, ".m3u8")
	isHTTPS := strings.HasPrefix(lower, "https://")
	needProxy := force || isMaster || isPlaylist || isHTTPS
	if !needProxy {
		return abs
	}
	return origin + "/lv?u=" + b64u(abs) + proxyFlag(force || isHTTPS)
}

func proxyFlag(force bool) string {
	if force {
		return "&p=1"
	}
	return ""
}

// rewriteURIAttrs 处理 #EXT-X-KEY / #EXT-X-MAP / #EXT-X-MEDIA 里的 URI="..."。
// 这几个标签的 URI 若是相对路径，同样会被老播放器拼错，必须绝对化。
func rewriteURIAttrs(line, finalURL, origin string, forceProxy bool) string {
	const marker = `URI="`
	idx := strings.Index(line, marker)
	if idx < 0 {
		return line
	}
	start := idx + len(marker)
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return line
	}
	raw := line[start : start+end]
	if raw == "" {
		return line
	}
	abs := resolveURL(finalURL, raw)
	if abs == "" {
		return line
	}
	val := liveRewrite(abs, origin, false, forceProxy)
	return line[:start] + val + line[start+end:]
}

func handleLivePlaylist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw, err := b64uDecode(q.Get("u"))
	if err != nil || raw == "" {
		// 兼容直接传明文地址（调试方便）
		raw = q.Get("u")
	}
	if !isHTTPURL(raw) {
		http.Error(w, "bad u", http.StatusBadRequest)
		return
	}
	forceProxy := q.Get("p") == "1"

	// 先按地址后缀判断是清单还是分片：分片不能整段读进内存再吐，
	// 那样既慢（要等最后一个字节）又会被 4MB 上限截断。
	lower := strings.ToLower(raw)
	looksList := strings.Contains(lower, ".m3u8") || strings.Contains(lower, "playlist")

	timeout := 12 * time.Second
	if !looksList {
		timeout = 30 * time.Second
	}

	resp, err := liveGet(raw, timeout)
	if err != nil && !looksList {
		// 分片允许重来一次：源站偶发掐连接 / 502
		resp, err = liveGet(raw, timeout)
	}
	if err != nil {
		log.Printf("[lv] 取上游失败 %s: %v", shortURL(raw), err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	finalURL := raw
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))

	if looksList || strings.Contains(ct, "mpegurl") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		txt := string(body)
		if strings.Contains(txt, "#EXTM3U") {
			if resp.StatusCode != http.StatusOK {
				http.Error(w, "upstream status "+resp.Status, http.StatusBadGateway)
				return
			}
			out := rewriteLivePlaylist(txt, finalURL, reqOrigin(r), forceProxy)
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, out)
			return
		}
		// 声称是清单、内容却不是：按普通文件透传
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	// 分片：流式转发，边收边发；Content-Length 原样透给播放器。
	// 播放器知道确切边界，才不会因为一条半死的连接一直等下去。
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("[lv] 分片转发中断 %s: %v", shortURL(raw), err)
	}
}
