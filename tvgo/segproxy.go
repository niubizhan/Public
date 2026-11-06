package main

// 点播分片代理（HLS 本地 serverside 预取）。
//
// 为什么要它：这台 Android 4.4 电视上的 IJK 用的是老 ffmpeg，读 https 分片
// 没有读超时。源站一旦变慢或把空闲连接掐掉，av_read_frame 就永久阻塞——
// 不报错、不重试、不退出，画面停在最后一帧（通常是刚播完的广告帧），
// 声音只是早就解码好的缓冲在放。实测卡住时 88 秒零解码日志、6 秒只下 98KB。
//
// 做法的关键：**不改分片行**。
//   /v/ 拿到的其实是一份 146 字节的 master，里面只有一行
//   https://.../mixed.m3u8；真正的 58KB 媒体清单是播放器直连源站取的。
//   上次把媒体清单里每个分片改写成 `/lv?u=<base64>`，清单涨到 117KB、
//   每行 200 多字符，IJK 直接卡在 FIND_STREAM_INFO 不起播。
//
//   这次把 master 的变体行改成本地短路径 `/p/<sid>/index.m3u8`，媒体清单
//   **原样返回**。清单里的分片是相对路径（xxx.ts?hash=...），播放器会自然
//   拼成 `http://127.0.0.1:8787/p/<sid>/xxx.ts?hash=...`，Go 按文件名查回
//   源站绝对地址。清单大小、每行长度、所有 #EXT 标签完全不变，播放器看到的
//   仍是一份标准 HLS，只是字节由本机代取。
//
// 播放器全程只读 loopback：源站的抖动、掐连接、慢响应都被 Go 的超时/重试/
// 预取吸收掉。返回清单时后台并发预取前几个分片，请求第 n 个时再补 n+1..n+4，
// 保证换分片（尤其是 #EXT-X-DISCONTINUITY 广告段之后）时是内存秒回。
//
// 【实测结论 2026-11-06】full 对**直播**有效，对**点播 HLS 不通**：
// HTTP 层全部验证通过（清单 58KB 原样、Content-Length 完整、预取命中 1~4ms），
// 但 IJK 起播停在 FFP_MSG_FIND_STREAM_INFO，读 3~7 个分片后不再请求，
// 也不出画面。list（清单由 Go 代取+缓存，分片仍直连源站）在 HTTP 层验证
// 通过但实机起播未确认，因此三种模式默认 off，全部作为可选实验能力保留。
//   GET /seg/list | /seg/on(full) | /seg/off(默认，直连)
//   touch /data/local/tmp/segproxy.list 或 .full 可改重启后的默认模式

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	segPrefetchAhead = 4                // 向后预取几个分片
	segKeepBehind    = 2                // 向前保留几个分片（seek 回头用）
	segFetchTimeout  = 30 * time.Second // 单个分片上游超时
	segFetchRetry    = 2                // 上游失败重试次数（含首次）
	segWaitTimeout   = 40 * time.Second // 播放器等一个分片的最长时间
	segPlaylistTTL   = 2 * time.Hour    // VOD 清单内容固定，可以缓存
	segStreamTTL     = 30 * time.Minute
	segMaxStreams    = 4
	segMaxConcurrent = 3 // 并发回源上限，别把电视 WiFi 打满
)

// ---------------------------------------------------------------- 开关

// 三种模式：
//   off  —— 完全直连（v1.6.1 行为）
//   list —— 清单由 Go 代取+缓存，分片绝对化后仍由播放器直连源站（默认）
//   full —— 清单和分片都走 localhost（预取版，实测点播起播不通）
const (
	segModeOff  = "off"
	segModeList = "list"
	segModeFull = "full"
)

var segCtl struct {
	sync.RWMutex
	mode string
}

func init() {
	// full 实测不通：点播 HLS 分片改走 localhost 后 IJK 停在
	// FFP_MSG_FIND_STREAM_INFO，读 3~7 个分片就不再请求也不出画面；
	// 长 URL(/lv) 与短 URL(/p/) 两种写法都一样。直播 /lv 能播，是因为
	// live 与 vod 的 ijk option 不同（max_cached_duration / infbuf / threads）。
	//
	// list 在 HTTP 层全部验证通过（清单 44KB、分片绝对直连、代取 1.3 秒），
	// 但实机起播未能确认出画面（播放器停在黑屏转圈，源站经 curl 验证正常）。
	// 因此**默认 off = v1.6.1 的完全直连**，绝不把未验证的行为强加给电视；
	// 想试清单缓存时 GET /seg/list 即时开启，GET /seg/off 随时退回。
	segCtl.mode = segModeOff
	if _, err := os.Stat("/data/local/tmp/segproxy.list"); err == nil {
		segCtl.mode = segModeList
	}
	if _, err := os.Stat("/data/local/tmp/segproxy.full"); err == nil {
		segCtl.mode = segModeFull
	}
	// Exo 在这台电视上也没能成功起播（ExoPlayer 2.19.1 初始化正常，
	// 但 8 秒只下 70KB、一直转圈），所以播放器类型默认跟随设置页。
	playerTypeCtl.v = -1
	if _, err := os.Stat("/data/local/tmp/player.exo"); err == nil {
		playerTypeCtl.v = 2
	}
}

func segMode() string {
	segCtl.RLock()
	defer segCtl.RUnlock()
	return segCtl.mode
}

// segEnabled 控制 /v/ 是否把 master 改走本地 /p/。
func segEnabled() bool { return segMode() != segModeOff }

func segSet(mode string) {
	segCtl.Lock()
	segCtl.mode = mode
	segCtl.Unlock()
	log.Printf("[seg] 模式 -> %s", mode)
}

func handleSegCtl(w http.ResponseWriter, r *http.Request, p string) {
	switch {
	case strings.HasSuffix(p, "/on"), p == "/seg=on":
		segSet(segModeFull)
	case strings.HasSuffix(p, "/list"):
		segSet(segModeList)
	case strings.HasSuffix(p, "/off"), p == "/seg=off":
		segSet(segModeOff)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "seg_mode=%s\n", segMode())
}

// ---------------------------------------------------------------- 播放器类型
//
// TVBox 的源配置支持站点级 "playerType"
// （ApiConfig.java:776 -> PlayFragment.java:1382）：
//   1=IJK  2=ExoPlayer  10=MXPlayer  -1=跟随设置页
// 试过下发 2 想用 Exo 的 Java HTTP 超时/重试绕开 ffmpeg 的死结，
// 但 Exo 在这台 4.4 电视上同样起不了播，故默认 -1。
// GET /player/exo 可临时再试。
var playerTypeCtl struct {
	sync.RWMutex
	v int
}

func cfgPlayerType() int {
	playerTypeCtl.RLock()
	defer playerTypeCtl.RUnlock()
	return playerTypeCtl.v
}

func handlePlayerCtl(w http.ResponseWriter, r *http.Request, p string) {
	v := cfgPlayerType()
	switch {
	case strings.HasSuffix(p, "/ijk"):
		v = -1
	case strings.HasSuffix(p, "/exo"):
		v = 2
	}
	playerTypeCtl.Lock()
	playerTypeCtl.v = v
	playerTypeCtl.Unlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "playerType=%d (1=IJK 2=Exo -1=跟随设置页)\n", v)
}

// ---------------------------------------------------------------- 流注册表

type segResult struct {
	ready chan struct{}
	data  []byte
	ct    string
	err   error
}

type hlsStream struct {
	id       string
	playlist string // 源站媒体清单绝对地址

	mu     sync.Mutex
	plBody []byte
	plAt   time.Time
	segs   map[string]string // 清单里的文件名 -> 源站绝对地址（含 ?hash=）
	order  []string          // 分片顺序，用于预取窗口与缓存裁剪
	cache  map[string]*segResult
	sem    chan struct{}
	touched time.Time
}

var segReg = struct {
	sync.Mutex
	m map[string]*hlsStream
}{m: map[string]*hlsStream{}}

func streamID(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:10]
}

func newStream(id, playlist string) *hlsStream {
	segReg.Lock()
	defer segReg.Unlock()
	if old, ok := segReg.m[id]; ok {
		old.mu.Lock()
		old.playlist, old.touched = playlist, time.Now()
		old.mu.Unlock()
		return old
	}
	s := &hlsStream{
		id:       id,
		playlist: playlist,
		segs:     map[string]string{},
		cache:    map[string]*segResult{},
		sem:      make(chan struct{}, segMaxConcurrent),
		touched:  time.Now(),
	}
	segReg.m[id] = s
	sweepStreamsLocked()
	return s
}

func getStream(id string) *hlsStream {
	segReg.Lock()
	defer segReg.Unlock()
	return segReg.m[id]
}

// sweepStreamsLocked 必须在 segReg 持锁时调用。
func sweepStreamsLocked() {
	now := time.Now()
	for k, s := range segReg.m {
		s.mu.Lock()
		t := s.touched
		s.mu.Unlock()
		if now.Sub(t) > segStreamTTL {
			delete(segReg.m, k)
		}
	}
	for len(segReg.m) > segMaxStreams {
		oldest, ot := "", time.Now()
		for k, s := range segReg.m {
			s.mu.Lock()
			t := s.touched
			s.mu.Unlock()
			if t.Before(ot) {
				oldest, ot = k, t
			}
		}
		if oldest == "" {
			break
		}
		delete(segReg.m, oldest)
	}
}

func (s *hlsStream) touch() {
	s.mu.Lock()
	s.touched = time.Now()
	s.mu.Unlock()
}

// ---------------------------------------------------------------- master 本地化

// localizeMaster 把 master 里的每一行变体地址改成本地短路径。
// 清单原文的其他部分（#EXT-X-STREAM-INF 等）一字不动。
func localizeMaster(txt, baseURL, origin string) (string, int) {
	lines := strings.Split(txt, "\n")
	n := 0
	for i, ln := range lines {
		line := strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		abs := resolveURL(baseURL, line)
		if abs == "" || !isHTTPURL(abs) {
			continue
		}
		id := streamID(baseURL) + "-" + strconv.Itoa(n)
		newStream(id, abs)
		lines[i] = origin + "/p/" + id + "/index.m3u8"
		n++
	}
	return strings.Join(lines, "\n"), n
}

// ---------------------------------------------------------------- 媒体清单

// playlistBody 取源站媒体清单并缓存。VOD 有 #EXT-X-ENDLIST，内容不会变，
// 而实测源站返回这份 58KB 清单要 20 秒以上，缓存能显著缩短起播等待。
func (s *hlsStream) playlistBody() ([]byte, error) {
	s.mu.Lock()
	cached := len(s.plBody) > 0 && time.Since(s.plAt) < segPlaylistTTL
	body := s.plBody
	s.mu.Unlock()
	if cached {
		return body, nil
	}

	var resp *http.Response
	var err error
	for i := 0; i < segFetchRetry; i++ {
		resp, err = liveGet(s.playlist, 40*time.Second)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream %d", resp.StatusCode)
	}
	final := s.playlist
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(b), "#EXTM3U") {
		return nil, fmt.Errorf("upstream is not m3u8")
	}

	s.mu.Lock()
	s.playlist, s.plBody, s.plAt = final, b, time.Now()
	s.segs, s.order, s.cache = map[string]string{}, nil, map[string]*segResult{}
	s.mu.Unlock()
	return b, nil
}

// parsePlaylist 建立「清单里的文件名 -> 源站绝对地址」映射和分片顺序。
// 只做一次；清单缓存过期重新拉取时会清空重来。
func (s *hlsStream) parsePlaylist(body []byte) {
	s.mu.Lock()
	need := len(s.order) == 0
	s.mu.Unlock()
	if !need {
		return
	}

	segs := map[string]string{}
	order := []string{}
	for _, ln := range strings.Split(string(body), "\n") {
		t := strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		abs := resolveURL(s.playlist, t)
		if abs == "" || !isHTTPURL(abs) {
			continue
		}
		key := t
		if j := strings.IndexByte(key, '?'); j >= 0 {
			key = key[:j]
		}
		key = path.Base(key)
		if key == "" || key == "." || key == "/" {
			continue
		}
		if _, dup := segs[key]; dup {
			continue
		}
		segs[key] = abs
		order = append(order, key)
	}
	s.mu.Lock()
	if len(s.order) == 0 {
		s.segs, s.order = segs, order
	}
	s.mu.Unlock()
	log.Printf("[seg] %s 清单解析 %d 个分片", s.id, len(order))
}

func handleSegPlaylist(w http.ResponseWriter, r *http.Request, s *hlsStream) {
	body, err := s.playlistBody()
	if err != nil {
		log.Printf("[seg] %s 取清单失败: %v", s.id, err)
		http.Error(w, "playlist fetch failed", http.StatusBadGateway)
		return
	}
	out := body
	mode := segMode()
	if mode == segModeList {
		// list 模式：只把分片地址绝对化，播放器仍直连源站取字节。
		// 清单本身由 Go 代取并缓存，省掉源站那 20 秒和 4.4 的 TLS 兼容问题。
		out = []byte(absolutize(string(body), s.playlist))
	} else {
		s.parsePlaylist(body)
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(out)
	}
	if mode == segModeFull {
		// 清单发出去的同时开始预取开头几个分片，等播放器来要时已在内存里。
		go s.prefetchFrom(0)
	}
}

// ---------------------------------------------------------------- 分片代理

// lookup 找到播放器请求的文件名对应的源站地址，并给出它在清单里的序号。
// 查不到时按清单地址 + 原始 query 兜底拼一个，保证不因映射缺失直接 404。
func (s *hlsStream) lookup(key, query string) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if abs, ok := s.segs[key]; ok {
		idx := -1
		for i, k := range s.order {
			if k == key {
				idx = i
				break
			}
		}
		return abs, idx
	}
	raw := key
	if query != "" {
		raw += "?" + query
	}
	abs := resolveURL(s.playlist, raw)
	if !isHTTPURL(abs) {
		return "", -1
	}
	return abs, -1
}

// ensure 命中缓存就直接返回；否则登记一个「下载中」的条目，避免并发重复回源。
func (s *hlsStream) ensure(abs, key string) *segResult {
	s.mu.Lock()
	if e, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return e
	}
	e := &segResult{ready: make(chan struct{})}
	s.cache[key] = e
	s.mu.Unlock()

	go func() {
		data, ct, err := s.download(abs)
		e.data, e.ct, e.err = data, ct, err
		close(e.ready)
		if err != nil {
			log.Printf("[seg] %s 分片失败 %s: %v", s.id, key, err)
		}
	}()
	return e
}

func (s *hlsStream) download(abs string) ([]byte, string, error) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	var lastErr error
	for i := 0; i < segFetchRetry; i++ {
		resp, err := liveGet(abs, segFetchTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("upstream %d", resp.StatusCode)
			continue
		}
		ct := resp.Header.Get("Content-Type")
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if len(data) == 0 {
			lastErr = fmt.Errorf("empty segment")
			continue
		}
		if ct == "" || strings.Contains(strings.ToLower(ct), "text") {
			ct = "video/mp2t"
		}
		return data, ct, nil
	}
	return nil, "", lastErr
}

func (s *hlsStream) drop(key string) {
	s.mu.Lock()
	delete(s.cache, key)
	s.mu.Unlock()
}

func (s *hlsStream) prefetchFrom(from int) {
	s.mu.Lock()
	order := append([]string(nil), s.order...)
	segs := s.segs
	s.mu.Unlock()
	if from < 0 {
		from = 0
	}
	for i := from; i < from+segPrefetchAhead && i < len(order); i++ {
		k := order[i]
		if abs := segs[k]; abs != "" {
			s.ensure(abs, k)
		}
	}
}

// trim 只保留当前分片附近的小窗口，防止整部片子（469 个分片）吃光电视内存。
func (s *hlsStream) trim(current int) {
	if current < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := make(map[string]bool)
	for i := current - segKeepBehind; i <= current+segPrefetchAhead+2 && i < len(s.order); i++ {
		if i >= 0 {
			keep[s.order[i]] = true
		}
	}
	for k := range s.cache {
		if !keep[k] {
			delete(s.cache, k)
		}
	}
}

func handleSegSegment(w http.ResponseWriter, r *http.Request, s *hlsStream, rel string) {
	key := rel
	if j := strings.IndexByte(key, '?'); j >= 0 {
		key = key[:j]
	}
	key = path.Base(key)
	abs, idx := s.lookup(key, r.URL.RawQuery)
	if abs == "" {
		http.Error(w, "unknown segment", http.StatusNotFound)
		return
	}

	e := s.ensure(abs, key)
	select {
	case <-e.ready:
	case <-time.After(segWaitTimeout):
		log.Printf("[seg] %s 等待分片超时 %s", s.id, key)
		http.Error(w, "segment timeout", http.StatusGatewayTimeout)
		return
	}
	if e.err != nil || len(e.data) == 0 {
		// 清掉失败条目，播放器重连时会重新回源，而不是一直拿到同一个 502。
		s.drop(key)
		http.Error(w, "segment fetch failed", http.StatusBadGateway)
		return
	}

	// 明确给出 Content-Length：播放器知道确切边界，才不会在半死的连接上干等。
	w.Header().Set("Content-Type", e.ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(e.data)))
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.data)
	}
	if idx >= 0 {
		go s.prefetchFrom(idx + 1)
		go s.trim(idx)
	}
}

// ---------------------------------------------------------------- 路由入口

func handleSeg(w http.ResponseWriter, r *http.Request, p string) {
	rest := strings.TrimPrefix(p, "/p/")
	parts := strings.SplitN(rest, "/", 2)
	if parts[0] == "" {
		http.Error(w, "bad stream", http.StatusNotFound)
		return
	}
	s := getStream(parts[0])
	if s == nil {
		http.Error(w, "stream expired", http.StatusNotFound)
		return
	}
	s.touch()
	if len(parts) < 2 || parts[1] == "" || strings.HasSuffix(strings.ToLower(parts[1]), ".m3u8") {
		handleSegPlaylist(w, r, s)
		return
	}
	handleSegSegment(w, r, s, parts[1])
}
