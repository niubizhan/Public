package main

// TVBox 4.4 本地源站（Go + goquery），跑在电视本机 127.0.0.1:8787。
//
// 路由：
//   GET /                        -> TVBox 配置 JSON（接口地址填这个根路径）
//   GET /api/{site}              -> 业务接口（配置里下发的 api）
//   GET /spider/{site}           -> 同上（保留原稿里的 /spider/ 路径，兼容自定义调用）
//   GET /img?u=                  -> 封面图代理
//   GET /v/{site}/index.m3u8     -> 播放清单代理（命中 isVideoFormat）
//   GET /v/{site}/proxy.m3u8     -> 上级 master 里的子播放列表代理（递归改写）
//   GET /v/{site}/seg?u=...      -> TS 分片代理（带 UA/Referer、超时、重试、Range）
//   GET /v/{site}/enc.key?u=...  -> HLS AES-128 密钥代理（带 UA/Referer，防 404/防盗链）
//   GET /health                  -> 健康检查
//
// 播放器只连 127.0.0.1:8787；外网请求全部由 Go 客户端承担。
// 老播放器直连灰产 CDN 时，某个分片 connection reset 会让它永久卡在读流上
// （表现为「插广告后黑屏」），代理后由 Go 侧超时重试吸收，不再拖死解码线程。
// 另加：并发预取后续分片（慢 CDN 也能连播）+ 起播时自动测速选最快线路。
//   /v/{site}/seg?u=...&p=<pid>   带预取 session 的分片（命中内存缓存秒回）
//
// TVBox 4.4 type=1 请求参数（见 SourceViewModel.java）：
//   首页  ?ac=detail
//   分类  ?ac=detail&t={type_id}&pg={page}
//   搜索  ?wd={关键词}&ac=detail&quick=true
//   详情  ?ac=detail&ids={vod_id}   （可能逗号分隔一长串）
//   播放  ?play={token}&flag={线路名}

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const playerUA = "Mozilla/5.0 (Android TV; TVBox) AppleWebKit/537.36 Chrome/124.0.0.0 Mobile Safari/537.36"

var (
	startAt = time.Now()

	// 首页/分类/搜索拿到列表时登记缓存：TVBox 首页会紧跟一次
	// ?ac=detail&ids=<一长串>，逐个抓详情要十几秒，命中缓存可秒回。
	vodIndexMu sync.Mutex
	vodIndex   = map[string]VodItem{}
	vodOrder   []string

	imgCacheMu sync.Mutex
	imgCache   = map[string]*imgEntry{}
	imgOrder   []string
	imgSem     = make(chan struct{}, 12)

	// 每个站点最近一次播放清单所在目录：播放器丢掉 enc.key 的 query 时兜底用
	lastBaseMu sync.Mutex
	lastBase   = map[string]string{}

	playerType = -1 // -1 跟随设置页；2 = ExoPlayer

	// 老 IJK 每次 #EXT-X-DISCONTINUITY 都会重置解码器；混切源多达 50+ 次，
	// 排障时可加 -strip-disc 去掉该标记（默认保留，不改变原清单语义）。
	stripDiscontinuity bool
)

const vodIndexMax = 1200
const imgCacheMax = 240

type imgEntry struct {
	data []byte
	ct   string
}

func cfgPlayerType() int {
	playerTypeMu.Lock()
	defer playerTypeMu.Unlock()
	return playerType
}

var playerTypeMu sync.Mutex

func indexVods(list []VodItem) {
	vodIndexMu.Lock()
	defer vodIndexMu.Unlock()
	for _, v := range list {
		if v.VodID == "" {
			continue
		}
		vodIndex[v.VodID] = v
		vodOrder = append(vodOrder, v.VodID)
	}
	for len(vodOrder) > vodIndexMax {
		delete(vodIndex, vodOrder[0])
		vodOrder = vodOrder[1:]
	}
}

func vodIndexGet(id string) (VodItem, bool) {
	vodIndexMu.Lock()
	defer vodIndexMu.Unlock()
	v, ok := vodIndex[id]
	return v, ok
}

func imgCacheGet(k string) *imgEntry {
	imgCacheMu.Lock()
	defer imgCacheMu.Unlock()
	return imgCache[k]
}

func imgCacheSet(k string, e *imgEntry) {
	imgCacheMu.Lock()
	defer imgCacheMu.Unlock()
	imgCache[k] = e
	imgOrder = append(imgOrder, k)
	for len(imgOrder) > imgCacheMax {
		delete(imgCache, imgOrder[0])
		imgOrder = imgOrder[1:]
	}
}

type logWriter struct {
	http.ResponseWriter
	status int
}

func (l *logWriter) WriteHeader(code int) {
	l.status = code
	l.ResponseWriter.WriteHeader(code)
}

func sendJSON(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.WriteHeader(status)
	writeJSON(w, data)
}

// ---------------------------------------------------------------- 图片代理

func handleImage(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("u")
	if !isHTTPURL(raw) {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	if hit := imgCacheGet(raw); hit != nil {
		w.Header().Set("Content-Type", hit.ct)
		w.Header().Set("Cache-Control", "public, max-age=604800")
		_, _ = w.Write(hit.data)
		return
	}
	imgSem <- struct{}{}
	defer func() { <-imgSem }()

	var got *imgEntry
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(150 * time.Millisecond)
		}
		data, ct, err := fetchBinary(raw, map[string]string{"User-Agent": UA}, 7*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		got = &imgEntry{data: data, ct: ct}
		break
	}
	if got == nil {
		log.Printf("[img] fail %v -> 302", lastErr)
		w.Header().Set("Location", raw)
		w.WriteHeader(http.StatusFound)
		return
	}
	imgCacheSet(raw, got)
	w.Header().Set("Content-Type", got.ct)
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(got.data)
}

// ---------------------------------------------------------------- 播放清单

// #EXT-X-KEY / #EXT-X-MAP / #EXT-X-MEDIA 等标签里的 URI="..."
var extKeyURI = regexp.MustCompile(`URI="([^"]*)"`)

// rewriteAttrURI 把标签内的 URI 换成本地代理路径，ref 为回源时使用的 Referer。
func rewriteAttrURI(line string, bu *url.URL, ref, target string) string {
	return extKeyURI.ReplaceAllStringFunc(line, func(m string) string {
		inner := m[len(`URI="`) : len(m)-1]
		if inner == "" {
			return m
		}
		u, e := url.Parse(inner)
		if e != nil {
			return m
		}
		abs := bu.ResolveReference(u).String()
		return `URI="` + target + `?u=` + encURI(abs) + `&ref=` + encURI(ref) + `"`
	})
}

// rewriteM3U8 把清单里所有外链改成本地代理路径，播放器从此只连 127.0.0.1:8787。
// master 的子清单走 proxy.m3u8（可递归），媒体分片走 seg，AES 密钥走 enc.key。
// 这样每个外网请求都经过带超时/重试/UA/Referer 的 Go 客户端。
func rewriteM3U8(text, base, ref string, pid int) string {
	bu, err := url.Parse(base)
	if err != nil {
		return text
	}
	isMaster := strings.Contains(text, "#EXT-X-STREAM-INF") ||
		strings.Contains(text, "#EXT-X-I-FRAME-STREAM-INF")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			out = append(out, raw)
			continue
		}
		if stripDiscontinuity && strings.HasPrefix(line, "#EXT-X-DISCONTINUITY") {
			continue
		}
		if strings.HasPrefix(line, "#") {
			switch {
			case strings.HasPrefix(line, "#EXT-X-KEY"),
				strings.HasPrefix(line, "#EXT-X-MAP"),
				strings.HasPrefix(line, "#EXT-X-SESSION-KEY"):
				out = append(out, rewriteAttrURI(line, bu, ref, "enc.key"))
			case strings.HasPrefix(line, "#EXT-X-MEDIA"),
				strings.HasPrefix(line, "#EXT-X-I-FRAME-STREAM-INF"):
				out = append(out, rewriteAttrURI(line, bu, ref, "proxy.m3u8"))
			default:
				out = append(out, raw)
			}
			continue
		}
		u, e := url.Parse(line)
		if e != nil {
			out = append(out, raw)
			continue
		}
		abs := bu.ResolveReference(u).String()
		target := "seg"
		if isMaster {
			target = "proxy.m3u8"
		}
		rewritten := target + "?u=" + encURI(abs) + "&ref=" + encURI(ref)
		if pid > 0 && target == "seg" {
			rewritten += "&p=" + strconv.Itoa(pid)
		}
		out = append(out, rewritten)
	}
	return strings.Join(out, "\n")
}

// ---------------------------------------------------------------- 分片并发预取
//
// 慢 CDN 上顺序下载每片要 25~38s，播放器缓冲永远追不上（一直转圈）。
// 源站在返回子播放清单时后台并发预取后续分片，播放器请求时直接从内存秒回。
// 一次只保留一个活跃 session，缓存上限 32 片，避免吃光电视内存。

type segFetch struct {
	done chan struct{}
	data []byte
	ct   string
	err  error
}

type plSession struct {
	id     int
	segs   []string
	ref    string
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	cache  map[string]*segFetch
	order  []string
	sem    chan struct{}
}

var (
	plMu      sync.Mutex
	plSeq     int
	plCurrent *plSession
	plByID    = map[int]*plSession{}
	plByURL   = map[string]*plSession{}
	rangeRe   = regexp.MustCompile(`bytes=(\d+)-(\d*)`)
)

const prefetchAhead = 8
const segCacheMax = 32

func isMasterPlaylist(text string) bool {
	return strings.Contains(text, "#EXT-X-STREAM-INF") ||
		strings.Contains(text, "#EXT-X-I-FRAME-STREAM-INF")
}

func parseSegments(text, base string) []string {
	bu, err := url.Parse(base)
	if err != nil {
		return nil
	}
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, e := url.Parse(line)
		if e != nil {
			continue
		}
		out = append(out, bu.ResolveReference(u).String())
	}
	return out
}

func getSession(id int) *plSession {
	plMu.Lock()
	defer plMu.Unlock()
	return plByID[id]
}

func getOrCreateSession(key string, segs []string, ref string) *plSession {
	plMu.Lock()
	defer plMu.Unlock()
	if s, ok := plByURL[key]; ok && s.ctx.Err() == nil {
		return s
	}
	if plCurrent != nil {
		plCurrent.cancel()
		delete(plByID, plCurrent.id)
		delete(plByURL, key)
	}
	plSeq++
	ctx, cancel := context.WithCancel(context.Background())
	s := &plSession{
		id: plSeq, segs: segs, ref: ref, ctx: ctx, cancel: cancel,
		cache: map[string]*segFetch{}, sem: make(chan struct{}, 6),
	}
	plCurrent = s
	plByID[s.id] = s
	plByURL[key] = s
	for k, v := range plByURL {
		if v != s && v.ctx.Err() != nil {
			delete(plByURL, k)
		}
	}
	return s
}

func downloadSeg(ctx context.Context, u, ref string) ([]byte, string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 400 * time.Millisecond)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("User-Agent", playerUA)
		req.Header.Set("Referer", ref)
		req.Header.Set("Accept", "*/*")
		cli := *getClient()
		cli.Timeout = 120 * time.Second
		resp, err := cli.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		ct := resp.Header.Get("Content-Type")
		_ = resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 400 || len(b) == 0 {
			lastErr = fmt.Errorf("upstream %d / %d bytes", resp.StatusCode, len(b))
			continue
		}
		if ct == "" {
			ct = "video/mp2t"
		}
		return b, ct, nil
	}
	return nil, "", lastErr
}

func (s *plSession) start(u string) *segFetch {
	s.mu.Lock()
	if e, ok := s.cache[u]; ok {
		s.mu.Unlock()
		return e
	}
	e := &segFetch{done: make(chan struct{})}
	s.cache[u] = e
	s.order = append(s.order, u)
	for len(s.order) > segCacheMax {
		old := s.order[0]
		s.order = s.order[1:]
		if old != u {
			delete(s.cache, old)
		}
	}
	s.mu.Unlock()

	go func() {
		defer close(e.done)
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-s.ctx.Done():
			e.err = s.ctx.Err()
			return
		}
		data, ct, err := downloadSeg(s.ctx, u, s.ref)
		e.data, e.ct, e.err = data, ct, err
		if err != nil && s.ctx.Err() == nil {
			log.Printf("[prefetch] fail %s %v", shortURL(u), err)
		}
	}()
	return e
}

func (s *plSession) prefetch(from, count int) {
	end := from + count
	if end > len(s.segs) {
		end = len(s.segs)
	}
	for i := from; i < end; i++ {
		if s.ctx.Err() != nil {
			return
		}
		s.start(s.segs[i])
	}
}

func (s *plSession) consume(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.cache[u]; ok {
		select {
		case <-e.done:
			delete(s.cache, u)
		default:
		}
	}
}

// writeSegBytes 从内存缓存回分片，支持 Range（Exo 会发 bytes=...）。
func writeSegBytes(w http.ResponseWriter, r *http.Request, data []byte, ct string) {
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Accept-Ranges", "bytes")
	if m := rangeRe.FindStringSubmatch(r.Header.Get("Range")); m != nil {
		start, e1 := strconv.ParseInt(m[1], 10, 64)
		end := int64(len(data)) - 1
		if m[2] != "" {
			if v, e := strconv.ParseInt(m[2], 10, 64); e == nil && v < end {
				end = v
			}
		}
		if e1 == nil && start >= 0 && start < int64(len(data)) && end >= start {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.Header().Set("Content-Length", strconv.Itoa(int(end-start+1)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start : end+1])
			return
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleProxyPlaylist 代理 master 指向的子播放列表，递归改写成同样的本地路径。
func handleProxyPlaylist(w http.ResponseWriter, r *http.Request, site Spider) {
	q := r.URL.Query()
	u := q.Get("u")
	ref := q.Get("ref")
	if ref == "" {
		ref = site.Base() + "/"
	}
	if !isHTTPURL(u) {
		http.Error(w, "playlist url missing", http.StatusBadRequest)
		return
	}
	body, err := fetchText(u, fetchOpts{
		TTL: -1, Timeout: 20 * time.Second,
		Headers: map[string]string{"User-Agent": playerUA, "Referer": ref, "Accept": "*/*"},
	})
	if err != nil {
		log.Printf("[m3u8] fail %s %v", shortURL(u), err)
		http.Error(w, "playlist fetch failed", http.StatusBadGateway)
		return
	}
	pid := 0
	if !isMasterPlaylist(body) {
		segs := parseSegments(body, u)
		if len(segs) > 0 {
			s := getOrCreateSession(u, segs, ref)
			pid = s.id
			go s.prefetch(0, prefetchAhead)
		}
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, rewriteM3U8(body, u, ref, pid))
}

// handleSegment 代理 TS 分片：流式边下边播。
//
// 不能先全缓冲再返回：灰产 CDN 单个 1.5MB 分片在本机就要 8~15s、电视更慢，
// 整体超时会把「慢但正常」的分片判死，播放器反而直接 Source error。
// 因此重试只覆盖「拿到响应头之前」（连接重置/5xx/首字节超时），
// 一旦开始吐数据就交给 io.Copy 流式转发，客户端持续收数据不会判定超时。
func handleSegment(w http.ResponseWriter, r *http.Request, site Spider) {
	q := r.URL.Query()
	u := q.Get("u")
	ref := q.Get("ref")
	if ref == "" {
		ref = site.Base() + "/"
	}
	if !isHTTPURL(u) {
		http.Error(w, "segment url missing", http.StatusBadRequest)
		return
	}
	rangeHdr := r.Header.Get("Range")

	// 命中预取 session：从内存缓存秒回，并把预取窗口往前推进。
	if pid, e := strconv.Atoi(q.Get("p")); e == nil && pid > 0 {
		if s := getSession(pid); s != nil {
			idx := -1
			for i, su := range s.segs {
				if su == u {
					idx = i
					break
				}
			}
			if idx >= 0 {
				go s.prefetch(idx+1, prefetchAhead)
			}
			entry := s.start(u)
			select {
			case <-entry.done:
			case <-time.After(150 * time.Second):
				log.Printf("[seg] wait timeout %s", shortURL(u))
				http.Error(w, "segment timeout", http.StatusGatewayTimeout)
				return
			}
			if entry.err != nil {
				log.Printf("[seg] fail %s %v", shortURL(u), entry.err)
				http.Error(w, "segment fetch failed", http.StatusBadGateway)
				return
			}
			defer s.consume(u)
			writeSegBytes(w, r, entry.data, entry.ct)
			return
		}
	}

	// 无 session（旧播放器请求 / session 已过期）：保持流式兜底。
	var resp *http.Response
	var lastErr error
	for attempt := 0; attempt < 3 && resp == nil; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 400 * time.Millisecond)
		}
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", playerUA)
		req.Header.Set("Referer", ref)
		req.Header.Set("Accept", "*/*")
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}
		cli := *getClient()
		cli.Timeout = 120 * time.Second // 仅兜底；响应头阶段由 Transport 的 25s 约束
		rp, err := cli.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if rp.StatusCode < 200 || rp.StatusCode >= 400 {
			lastErr = fmt.Errorf("upstream %d", rp.StatusCode)
			_ = rp.Body.Close()
			continue
		}
		resp, lastErr = rp, nil
	}
	if resp == nil {
		log.Printf("[seg] fail %s %v", shortURL(u), lastErr)
		http.Error(w, "segment fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Cache-Control"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "video/mp2t")
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(resp.StatusCode)
	t0 := time.Now()
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		log.Printf("[seg] copy %s %d bytes %v", shortURL(u), n, err)
	} else if n == 0 {
		log.Printf("[seg] empty %s", shortURL(u))
	} else if d := time.Since(t0); d > 20*time.Second {
		log.Printf("[seg] slow %s %d bytes %s", shortURL(u), n, d.Round(time.Second))
	}
}

func lastBaseSet(k, v string) {
	lastBaseMu.Lock()
	defer lastBaseMu.Unlock()
	lastBase[k] = v
}

func lastBaseGet(k string) string {
	lastBaseMu.Lock()
	defer lastBaseMu.Unlock()
	return lastBase[k]
}

// handleHLSKey 代理 HLS AES-128 密钥（16 字节二进制），带站点 UA/Referer 防防盗链。
func handleHLSKey(w http.ResponseWriter, r *http.Request, site Spider) {
	q := r.URL.Query()
	u := q.Get("u")
	ref := q.Get("ref")
	if ref == "" {
		ref = site.Base() + "/"
	}
	if !isHTTPURL(u) {
		// 兜底：播放器丢掉 query 时，用最近一次清单目录拼出密钥地址
		if b := lastBaseGet(site.Key()); b != "" {
			if bu, e := url.Parse(b); e == nil {
				name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				if u2 := bu.ResolveReference(&url.URL{Path: name}).String(); isHTTPURL(u2) {
					u = u2
				}
			}
		}
	}
	if !isHTTPURL(u) {
		http.Error(w, "key url missing", http.StatusNotFound)
		return
	}
	data, _, err := fetchBinary(u, map[string]string{
		"User-Agent": playerUA,
		"Referer":    ref,
	}, 12*time.Second)
	if err != nil {
		log.Printf("[key] fail %v", err)
		http.Error(w, "key fetch failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(data)
}

func handlePlaylist(w http.ResponseWriter, r *http.Request, site Spider) {
	token := r.URL.Query().Get("t")
	flag := r.URL.Query().Get("f")
	flag, token = chooseLine(site, flag, token)
	urls, err := site.Play(flag, token)
	if err != nil || len(urls) == 0 {
		http.Error(w, "play resolve failed", http.StatusNotFound)
		return
	}
	real := urls[0].URL
	if !strings.Contains(strings.ToLower(real), ".m3u8") {
		w.Header().Set("Location", real)
		w.WriteHeader(http.StatusFound)
		return
	}
	// 记下清单所在目录，供 enc.key 兜底解析
	if bu, e := url.Parse(real); e == nil {
		if idx := strings.LastIndex(bu.Path, "/"); idx >= 0 {
			bu.Path = bu.Path[:idx+1]
		}
		bu.RawQuery = ""
		lastBaseSet(site.Key(), bu.String())
	}
	body, err := fetchText(real, fetchOpts{
		TTL: -1, Timeout: 20 * time.Second,
		Headers: map[string]string{"User-Agent": playerUA, "Referer": site.Base() + "/"},
	})
	if err != nil {
		w.Header().Set("Location", real)
		w.WriteHeader(http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, rewriteM3U8(body, real, site.Base()+"/", 0))
}

// ---------------------------------------------------------------- 线路自动测速
//
// 一部剧有多条线路（天王/水星/金星…），CDN 快慢差异极大。
// 详情返回时把「同一集的所有线路」登记成组；起播时并发探测首片吞吐，
// 选最快的线路透明替换，播放器与 TVBox 配置都不用改。
// 结果按剧集缓存 10 分钟，换集不重复测速。

type lineOpt struct {
	flag  string
	token string
}

type lineGroup struct {
	vodID string
	opts  []lineOpt
}

type speedPick struct {
	opt lineOpt
	at  time.Time
}

type probeResult struct {
	opt   lineOpt
	score float64 // bytes/s
}

var (
	lineMu         sync.Mutex
	lineOpts       = map[string]lineGroup{}
	speedMu        sync.Mutex
	speedPickCache = map[string]speedPick{}
)

const speedTTL = 10 * time.Minute
const speedProbeMax = 4
const speedProbeBudget = 8 * time.Second

func registerLineOptions(siteKey, vodID string, byEpisode map[int][]lineOpt) {
	lineMu.Lock()
	defer lineMu.Unlock()
	if len(lineOpts) > 4000 {
		lineOpts = map[string]lineGroup{}
	}
	for _, opts := range byEpisode {
		if len(opts) < 2 {
			continue
		}
		g := lineGroup{vodID: vodID, opts: opts}
		for _, o := range opts {
			lineOpts[siteKey+"|"+o.token] = g
		}
	}
}

func getLineGroup(siteKey, token string) (lineGroup, bool) {
	lineMu.Lock()
	defer lineMu.Unlock()
	g, ok := lineOpts[siteKey+"|"+token]
	return g, ok
}

func getSpeedPick(k string) (lineOpt, bool) {
	speedMu.Lock()
	defer speedMu.Unlock()
	p, ok := speedPickCache[k]
	return p.opt, ok && time.Since(p.at) < speedTTL
}

func setSpeedPick(k string, o lineOpt) {
	speedMu.Lock()
	defer speedMu.Unlock()
	if len(speedPickCache) > 300 {
		speedPickCache = map[string]speedPick{}
	}
	speedPickCache[k] = speedPick{opt: o, at: time.Now()}
}

// probeGet 只取前 limit 字节，整体耗时由 ctx 约束。
func probeGet(ctx context.Context, u, ref string, limit int64, useRange bool) ([]byte, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", playerUA)
	req.Header.Set("Referer", ref)
	req.Header.Set("Accept", "*/*")
	if useRange {
		req.Header.Set("Range", "bytes=0-65535")
	}
	cli := *getClient()
	cli.Timeout = 0
	t0 := time.Now()
	resp, err := cli.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, 0, fmt.Errorf("upstream %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return b, time.Since(t0), err
}

// probeLine 解析线路真实地址，取首个分片前 64KB 计算吞吐。
func probeLine(site Spider, o lineOpt) float64 {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	urls, err := site.Play(o.flag, o.token)
	if err != nil || len(urls) == 0 || urls[0].URL == "" {
		return 0
	}
	ref := site.Base() + "/"
	u := urls[0].URL
	for depth := 0; depth < 2 && strings.Contains(strings.ToLower(u), ".m3u8"); depth++ {
		body, _, err := probeGet(ctx, u, ref, 256<<10, false)
		if err != nil {
			return 0
		}
		bu, e := url.Parse(u)
		if e != nil {
			return 0
		}
		next := ""
		for _, raw := range strings.Split(string(body), "\n") {
			l := strings.TrimSpace(raw)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			ru, e2 := url.Parse(l)
			if e2 != nil {
				continue
			}
			next = bu.ResolveReference(ru).String()
			break
		}
		if next == "" {
			return 0
		}
		u = next
	}
	b, d, err := probeGet(ctx, u, ref, 64<<10, true)
	if err != nil || len(b) == 0 || d <= 0 {
		return 0
	}
	return float64(len(b)) / d.Seconds()
}

// chooseLine 并发探测，8 秒预算内取吞吐最高的线路；全失败则保持原选择。
func chooseLine(site Spider, flag, token string) (string, string) {
	g, ok := getLineGroup(site.Key(), token)
	if !ok || len(g.opts) < 2 {
		return flag, token
	}
	key := site.Key() + "|" + g.vodID
	if p, ok := getSpeedPick(key); ok {
		log.Printf("[speed] cached pick %s", p.flag)
		return p.flag, p.token
	}
	cands := g.opts
	if len(cands) > speedProbeMax {
		cands = cands[:speedProbeMax]
	}
	ch := make(chan probeResult, len(cands))
	for _, o := range cands {
		go func(o lineOpt) {
			ch <- probeResult{opt: o, score: probeLine(site, o)}
		}(o)
	}
	deadline := time.NewTimer(speedProbeBudget)
	defer deadline.Stop()
	best := probeResult{}
	got := 0
	for got < len(cands) {
		select {
		case r := <-ch:
			got++
			if r.score > best.score {
				best = r
			}
		case <-deadline.C:
			got = len(cands)
		}
	}
	if best.score <= 0 {
		log.Printf("[speed] %d lines all failed/timeout, keep %s", len(cands), flag)
		return flag, token
	}
	log.Printf("[speed] probed %d lines, pick %s (%.0f KB/s)", len(cands), best.opt.flag, best.score/1024)
	setSpeedPick(key, best.opt)
	return best.opt.flag, best.opt.token
}

// ---------------------------------------------------------------- 业务分发

func dispatch(w http.ResponseWriter, r *http.Request, site Spider, origin string) {
	q := r.URL.Query()
	playToken := q.Get("play")
	ids := q.Get("ids")
	wd := q.Get("wd")
	ac := q.Get("ac")
	pg, _ := strconv.Atoi(q.Get("pg"))
	if pg < 1 {
		pg = 1
	}

	// 1) 播放解析
	if playToken != "" {
		urls, err := site.Play(q.Get("flag"), playToken)
		if err != nil || len(urls) == 0 {
			sendJSON(w, playEnvelope("", q.Get("flag")), 200)
			return
		}
		env := playEnvelope(urls[0].URL, q.Get("flag"))
		env["header"] = map[string]string{"User-Agent": playerUA, "Referer": site.Base() + "/"}
		sendJSON(w, env, 200)
		return
	}

	// 2) 详情（ids 可能是一整页逗号分隔）
	if ids != "" {
		var idList []string
		for _, s := range strings.Split(ids, ",") {
			if s = strings.TrimSpace(s); s != "" {
				idList = append(idList, s)
			}
		}

		// 单个 id = 用户真正点进详情页，必须抓真实页面拿播放列表；
		// 不能命中首页卡片缓存（卡片没有 vod_play_url，会表现为「没有集数」）。
		if len(idList) == 1 {
			v, err := site.Detail(idList[0])
			if err != nil || v == nil || v.VodName == "" {
				env := listEnvelope(nil, 1, 1, 0)
				env["msg"] = "error: 详情为空"
				if err != nil {
					env["msg"] = "error: " + err.Error()
				}
				sendJSON(w, env, 200)
				return
			}
			if v.VodPlayURL != "" {
				rewritePlayURLs(v, origin, site.Key())
			}
			list := []VodItem{*v}
			rewritePics(list, origin, site.Base())
			sendJSON(w, listEnvelope(list, 1, 1, 1), 200)
			return
		}

		// 多个 id = TVBox 首页批量预取，用缓存避免十几次详情页抓取。
		if len(idList) > 24 {
			idList = idList[:24]
		}
		out := make([]VodItem, 0, len(idList))
		var missing []string
		for _, id := range idList {
			if v, ok := vodIndexGet(id); ok {
				out = append(out, v)
			} else {
				missing = append(missing, id)
			}
		}
		// 源站并发太高会 403，限 5 路
		if len(missing) > 0 {
			res := make([]*VodItem, len(missing))
			sem := make(chan struct{}, 5)
			var wg sync.WaitGroup
			for i, id := range missing {
				wg.Add(1)
				go func(i int, id string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					if v, err := site.Detail(id); err == nil && v != nil {
						res[i] = v
					}
				}(i, id)
			}
			wg.Wait()
			for _, v := range res {
				if v != nil && v.VodName != "" {
					out = append(out, *v)
				}
			}
		}
		for i := range out {
			if out[i].VodPlayURL != "" {
				rewritePlayURLs(&out[i], origin, site.Key())
			}
		}
		rewritePics(out, origin, site.Base())
		sendJSON(w, listEnvelope(out, 1, 1, len(out)), 200)
		return
	}

	// 3) 搜索（TVBox 4.4 搜索不带 pg）
	if wd != "" {
		resp, err := site.Search(wd, 1)
		if err != nil {
			sendJSON(w, withError(listEnvelope(nil, 1, 1, 0), err), 200)
			return
		}
		indexVods(resp.List)
		rewritePics(resp.List, origin, site.Base())
		sendJSON(w, listEnvelope(resp.List, 1, resp.PageCount, resp.Total), 200)
		return
	}

	// 4) 分类 / type=1 首页
	if ac == "detail" {
		if q.Get("t") == "" {
			cs, err := site.Home()
			if err != nil {
				sendJSON(w, withError(homeListEnvelope(nil, nil), err), 200)
				return
			}
			list, err := site.HomeContent()
			if err != nil {
				sendJSON(w, withError(homeListEnvelope(cs, nil), err), 200)
				return
			}
			indexVods(list)
			rewritePics(list, origin, site.Base())
			sendJSON(w, homeListEnvelope(cs, list), 200)
			return
		}
		resp, err := site.Category(q.Get("t"), pg)
		if err != nil {
			sendJSON(w, withError(listEnvelope(nil, pg, 1, 0), err), 200)
			return
		}
		indexVods(resp.List)
		rewritePics(resp.List, origin, site.Base())
		sendJSON(w, listEnvelope(resp.List, pg, resp.PageCount, resp.Total), 200)
		return
	}

	// 5) 兜底首页
	cs, _ := site.Home()
	list, err := site.HomeContent()
	if err != nil {
		sendJSON(w, withError(homeListEnvelope(cs, nil), err), 200)
		return
	}
	indexVods(list)
	rewritePics(list, origin, site.Base())
	sendJSON(w, homeListEnvelope(cs, list), 200)
}

// ---------------------------------------------------------------- 入口

func main() {
	port := flag.Int("port", 8787, "监听端口")
	addr := flag.String("addr", "0.0.0.0", "监听地址")
	showVer := flag.Bool("version", false, "打印版本")
	flag.StringVar(&proxyAddr, "proxy", "", "可选上游代理（默认直连；抓取不走代理）")
	flag.BoolVar(&insecureTLS, "insecure", false, "全局跳过 TLS 证书校验")
	flag.BoolVar(&stripDiscontinuity, "strip-disc", false, "去掉 m3u8 的 #EXT-X-DISCONTINUITY（老 IJK 频繁重置解码器时启用）")
	flag.Parse()

	if *showVer {
		fmt.Println("tvbox-spider-go", appVersion)
		return
	}

	log.SetFlags(log.Ltime)
	dns := setupResolver()
	log.Printf("resolver -> %s", dns)
	if proxyAddr != "" {
		log.Printf("proxy -> %s (explicit)", proxyAddr)
	} else {
		log.Printf("proxy -> disabled (direct)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		lw := &logWriter{ResponseWriter: w, status: 200}
		if r.Method == http.MethodOptions {
			lw.Header().Set("Access-Control-Allow-Origin", "*")
			lw.WriteHeader(http.StatusNoContent)
			return
		}
		path := strings.TrimRight(r.URL.Path, "/")
		if path == "" {
			path = "/"
		}
		origin := "http://" + r.Host
		if r.TLS != nil {
			origin = "https://" + r.Host
		}

		switch {
		case path == "/" || path == "/config" || path == "/tvbox.json":
			sendJSON(lw, buildConfig(origin), 200)
		case path == "/health":
			sendJSON(lw, map[string]interface{}{
				"ok": true, "version": appVersion, "sites": len(allSpiders),
				"uptime": time.Since(startAt).String(), "player_type": cfgPlayerType(),
				"proxy": proxyAddr,
			}, 200)
		case path == "/api" || path == "/help":
			sites := make([]map[string]string, 0, len(allSpiders))
			for _, s := range allSpiders {
				sites = append(sites, map[string]string{
					"key": s.Key(), "name": s.Name(), "api": origin + "/api/" + s.Key(),
				})
			}
			sendJSON(lw, map[string]interface{}{"name": "TVBox 4.4 源站 (Go/goquery)", "version": appVersion, "sites": sites}, 200)
		case path == "/img":
			handleImage(lw, r)
		case strings.HasPrefix(path, "/v/"):
			rest := strings.TrimPrefix(path, "/v/")
			parts := strings.SplitN(rest, "/", 2)
			key := parts[0]
			sub := ""
			if len(parts) > 1 {
				sub = parts[1]
			}
			site := getSpider(key)
			if site == nil {
				http.Error(lw, "unknown site", http.StatusNotFound)
			} else if sub == "seg" {
				handleSegment(lw, r, site)
			} else if sub == "proxy.m3u8" || sub == "list.m3u8" {
				handleProxyPlaylist(lw, r, site)
			} else if sub == "key" || sub == "enc.key" || strings.HasSuffix(sub, ".key") {
				handleHLSKey(lw, r, site)
			} else {
				handlePlaylist(lw, r, site)
			}
		case strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/spider/"):
			rest := strings.TrimPrefix(path, "/api/")
			if !strings.HasPrefix(path, "/api/") {
				rest = strings.TrimPrefix(path, "/spider/")
			}
			key := strings.SplitN(rest, "/", 2)[0]
			site := getSpider(key)
			if site == nil {
				sendJSON(lw, map[string]interface{}{"code": 0, "msg": "unknown site: " + key, "list": []interface{}{}}, 404)
				return
			}
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("[%s] panic %v", key, rec)
					sendJSON(lw, map[string]interface{}{"code": 0, "msg": fmt.Sprint(rec), "list": []interface{}{}}, 500)
				}
			}()
			dispatch(lw, r, site, origin)
		default:
			sendJSON(lw, map[string]interface{}{"code": 0, "msg": "not found", "hint": "访问 / 获取配置"}, 404)
		}
		// 一集 280+ 分片，seg 成功请求不打 access log；失败已在 handler 内记录。
		if !strings.HasSuffix(path, "/seg") || lw.status >= 400 {
			log.Printf("%s %s %d %dms", r.Method, r.URL.RequestURI(), lw.status, time.Since(t0).Milliseconds())
		}
	})

	srv := &http.Server{
		Addr: net.JoinHostPort(*addr, strconv.Itoa(*port)), Handler: mux,
		ReadTimeout: 20 * time.Second, WriteTimeout: 180 * time.Second, IdleTimeout: 120 * time.Second,
	}
	log.Printf("tvbox-spider-go %s listening on %s", appVersion, srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
