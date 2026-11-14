package main

// TVBox 4.4 本地源站（Go + goquery），跑在电视本机 127.0.0.1:8787。
//
// 路由：
//   GET /                        -> TVBox 配置 JSON（接口地址填这个根路径）
//   GET /api/{site}              -> 业务接口（配置里下发的 api）
//   GET /spider/{site}           -> 同上（保留原稿里的 /spider/ 路径，兼容自定义调用）
//   GET /img?u=                  -> 封面图代理
//   GET /v/{site}/index.m3u8     -> 播放清单代理（命中 isVideoFormat）
//   GET /health                  -> 健康检查
//
// TVBox 4.4 type=1 请求参数（见 SourceViewModel.java）：
//   首页  ?ac=detail
//   分类  ?ac=detail&t={type_id}&pg={page}
//   搜索  ?wd={关键词}&ac=detail&quick=true
//   详情  ?ac=detail&ids={vod_id}   （可能逗号分隔一长串）
//   播放  ?play={token}&flag={线路名}

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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

	playerType = -1 // -1 跟随设置页；2 = ExoPlayer
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

// absolutize m3u8 里的相对分片改成绝对地址，否则播放器会拼到本机服务上 404。
func absolutize(text, base string) string {
	bu, err := url.Parse(base)
	if err != nil {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if ref, err := url.Parse(line); err == nil {
			lines[i] = bu.ResolveReference(ref).String()
		}
	}
	return strings.Join(lines, "\n")
}

func handlePlaylist(w http.ResponseWriter, r *http.Request, site Spider) {
	token := r.URL.Query().Get("t")
	flag := r.URL.Query().Get("f")
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
	_, _ = io.WriteString(w, absolutize(body, real))
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
			key := strings.SplitN(strings.TrimPrefix(path, "/v/"), "/", 2)[0]
			if site := getSpider(key); site != nil {
				handlePlaylist(lw, r, site)
			} else {
				http.Error(lw, "unknown site", http.StatusNotFound)
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
		log.Printf("%s %s %d %dms", r.Method, r.URL.RequestURI(), lw.status, time.Since(t0).Milliseconds())
	})

	srv := &http.Server{
		Addr: net.JoinHostPort(*addr, strconv.Itoa(*port)), Handler: mux,
		ReadTimeout: 20 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 120 * time.Second,
	}
	log.Printf("tvbox-spider-go %s listening on %s", appVersion, srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
