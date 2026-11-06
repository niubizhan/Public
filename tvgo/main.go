package main

// TVBox 4.4 源站服务（单文件、零依赖，可在 Android 4.4 电视上直接跑）
//
// 路由：
//   GET /                     -> TVBox 4.4 配置 JSON
//   GET /api                  -> 接口自述
//   GET /api/{site}           -> 业务接口（首页/分类/搜索/详情/播放）
//   GET /img?u=               -> 封面图代理
//   GET /v/{site}/index.m3u8  -> 播放清单代理（master 改走本地 /p/）
//   GET /p/{sid}/index.m3u8   -> 媒体清单原样返回（分片相对路径自动落到 /p/）
//   GET /p/{sid}/{file}.ts    -> 分片代理：超时重试 + 预取 + 完整 Content-Length
//   GET /seg/list | /on | /off -> 切换清单代理模式（默认 off：完全直连）
//   GET /player/exo | /ijk      -> 切换下发的播放器类型（默认 -1 跟随设置页）
//   GET /health               -> 健康检查
//
// TVBox 4.4 type=1 请求参数（见 TVBoxOS SourceViewModel.java）：
//   首页 ?ac=detail
//   分类 ?ac=detail&t={type_id}&pg={page}
//   搜索 ?wd={关键词}&ac=detail&quick={true|false}
//   详情 ?ac=detail&ids={vod_id}
//   播放 ?play={token}&flag={线路名}

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const appVersion = "1.7.0"

// 播放器/上游 UA：取 m3u8 时代入，避免部分 CDN 拒绝空 UA。
const playerUA = "Mozilla/5.0 (Linux; Android 12; TVBox) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36"

// ---------------------------------------------------------------- 列表索引

// TVBox 首页加载完分类/推荐后会立刻再发一次 ?ac=detail&ids=一长串，
// 把推荐位每条都换成「完整详情」。对灰产源站来说这是十几次详情页抓取，
// 实测 11~13 秒，首页一直转圈，用户以为点不动。
// 首页/分类/搜索拿到列表时就登记，批量请求直接命中。
//
// 必须存副本：rewritePics 会原地改写 vod_pic，存引用会让图片被二次套上代理。
var (
	vodIndexMu sync.Mutex
	vodIndex   = map[string]V{}
	vodOrder   []string
)

const vodIndexMax = 1200

func indexVods(list []V) {
	vodIndexMu.Lock()
	defer vodIndexMu.Unlock()
	for _, v := range list {
		id := v.s("vod_id")
		if id == "" {
			continue
		}
		cp := make(V, len(v))
		for k, val := range v {
			cp[k] = val
		}
		if _, ok := vodIndex[id]; ok {
			for i, k := range vodOrder {
				if k == id {
					vodOrder = append(vodOrder[:i], vodOrder[i+1:]...)
					break
				}
			}
		}
		vodIndex[id] = cp
		vodOrder = append(vodOrder, id)
	}
	for len(vodOrder) > vodIndexMax {
		old := vodOrder[0]
		vodOrder = vodOrder[1:]
		delete(vodIndex, old)
	}
}

func vodIndexGet(id string) (V, bool) {
	vodIndexMu.Lock()
	defer vodIndexMu.Unlock()
	v, ok := vodIndex[id]
	return v, ok
}

// ---------------------------------------------------------------- 图片缓存

type imgEntry struct {
	data []byte
	ct   string
}

var (
	imgCacheMu sync.Mutex
	imgCache   = map[string]*imgEntry{}
	imgOrder   []string
)

const imgCacheMax = 240

func imgCacheGet(key string) *imgEntry {
	imgCacheMu.Lock()
	defer imgCacheMu.Unlock()
	e := imgCache[key]
	if e == nil {
		return nil
	}
	for i, k := range imgOrder {
		if k == key {
			imgOrder = append(imgOrder[:i], imgOrder[i+1:]...)
			break
		}
	}
	imgOrder = append(imgOrder, key)
	return e
}

func imgCacheSet(key string, e *imgEntry) {
	imgCacheMu.Lock()
	defer imgCacheMu.Unlock()
	if _, ok := imgCache[key]; ok {
		for i, k := range imgOrder {
			if k == key {
				imgOrder = append(imgOrder[:i], imgOrder[i+1:]...)
				break
			}
		}
	}
	imgCache[key] = e
	imgOrder = append(imgOrder, key)
	for len(imgOrder) > imgCacheMax {
		old := imgOrder[0]
		imgOrder = imgOrder[1:]
		delete(imgCache, old)
	}
}

// 一屏列表会让电视同时要 20+ 张图，这些灰产图床对同一 IP 的高并发很敏感，
// 直接打过去会成片超时（表现为部分海报空白）。限 12 路，其余排队。
var imgSem = make(chan struct{}, 12)

// ---------------------------------------------------------------- HTTP 工具

type logWriter struct {
	http.ResponseWriter
	status int
}

func (l *logWriter) WriteHeader(code int) {
	l.status = code
	l.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.WriteHeader(status)
	enc := newJSONEncoder(w)
	_ = enc.Encode(data)
}

// ---------------------------------------------------------------- 封面图代理

func pickImageReferer(u string) string {
	host := ""
	if pu := mustURL(u); pu != nil {
		host = pu.Host
	}
	// 豆瓣图床不做 Referer 会直接 418
	if strings.HasSuffix(host, "doubanio.com") || strings.HasSuffix(host, "douban.com") {
		return "https://movie.douban.com/"
	}
	for _, s := range allSites {
		if strings.HasPrefix(u, s.Base()) {
			return s.Base() + "/"
		}
	}
	return ""
}

func handleImage(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("u")
	if !isHTTPURL(raw) {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}

	if hit := imgCacheGet(raw); hit != nil {
		w.Header().Set("Content-Type", hit.ct)
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(hit.data)
		return
	}

	referer := pickImageReferer(raw)
	headers := map[string]string{"User-Agent": playerUA}
	if referer != "" {
		headers["Referer"] = referer
	}

	imgSem <- struct{}{}
	defer func() { <-imgSem }()

	var got *imgEntry
	var lastErr error
	// 图床偶发 ECONNRESET/限流，重试一次基本能过
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(150 * time.Millisecond)
		}
		data, ct, err := fetchBinary(raw, headers, 7*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		got = &imgEntry{data: data, ct: ct}
		break
	}

	if got == nil {
		// 本机也取不到（图床限流/被墙）时退化成 302 让电视自己直连试一次，
		// 总比回 502 让海报空着强。
		log.Printf("[img] fail %v -> 302 %s", lastErr, shortURL(raw))
		w.Header().Set("Location", raw)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusFound)
		return
	}

	imgCacheSet(raw, got)
	w.Header().Set("Content-Type", got.ct)
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(got.data)
}

func fetchBinary(raw string, headers map[string]string, timeout time.Duration) ([]byte, string, error) {
	req, err := http.NewRequest("GET", raw, nil)
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

// ---------------------------------------------------------------- 播放清单

// absolutize m3u8 里的相对地址改成绝对地址。
// 播放器拿到的清单地址是本机服务，若清单里还是 /20260101/xx/seg-1.ts 这种相对路径，
// 播放器会拼到本机服务上 → 404。所以全部绝对化，分片请求直接打到 CDN。
func absolutize(text, base string) string {
	bu := mustURL(base)
	if bu == nil {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			lines[i] = uriAttrRe.ReplaceAllStringFunc(raw, func(m string) string {
				sub := uriAttrRe.FindStringSubmatch(m)
				if sub == nil {
					return m
				}
				if ref := mustURL(sub[1]); ref != nil {
					return `URI="` + bu.ResolveReference(ref).String() + `"`
				}
				return m
			})
			continue
		}
		if ref := mustURL(line); ref != nil {
			lines[i] = bu.ResolveReference(ref).String()
		}
	}
	return strings.Join(lines, "\n")
}

func handlePlaylist(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v/")
	siteKey := strings.SplitN(rest, "/", 2)[0]
	site := getSite(siteKey)
	if site == nil {
		http.Error(w, "unknown site", http.StatusNotFound)
		return
	}

	token := r.URL.Query().Get("t")
	flag := r.URL.Query().Get("f")

	real, err := site.Play(token, flag)
	if err != nil || real == "" {
		http.Error(w, "play resolve failed", http.StatusNotFound)
		return
	}

	isM3u8 := strings.Contains(strings.ToLower(real), ".m3u8")
	if !isM3u8 {
		w.Header().Set("Location", real)
		w.WriteHeader(http.StatusFound)
		return
	}

	html, err := fetchText(real, fetchOpts{
		TTL:     0,
		Timeout: 20 * time.Second,
		Headers: map[string]string{"User-Agent": playerUA, "Referer": site.Base() + "/"},
	})
	if err != nil {
		w.Header().Set("Location", real)
		w.WriteHeader(http.StatusFound)
		return
	}

	// master 清单只有一行变体地址，真正的分片清单是播放器直连源站取的。
	// 非 off 模式时把这一行改成本地短路径：清单由 Go 代取并缓存（源站慢时
	// 这份 58KB 要 20 秒），同时不重演上次「清单 58KB -> 117KB、每行 200
	// 字符 base64」把 IJK 卡在 FIND_STREAM_INFO 的问题。详见 segproxy.go。
	if segEnabled() && strings.Contains(html, "#EXT-X-STREAM-INF") {
		out, n := localizeMaster(html, real, reqOrigin(r))
		if n > 0 {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, out)
			return
		}
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, absolutize(html, real))
}

// ---------------------------------------------------------------- 业务分发

func fetchOne(site Site, id string, retry bool) V {
	v, err := site.Detail(id)
	if err == nil && v != nil && v.s("vod_name") != "" {
		if v.s("vod_play_url") != "" || !retry {
			return v
		}
	}
	if retry {
		time.Sleep(250 * time.Millisecond)
		v2, err2 := site.Detail(id)
		if err2 == nil && v2 != nil && v2.s("vod_name") != "" {
			return v2
		}
		if v != nil {
			return v
		}
		return v2
	}
	if v != nil {
		return v
	}
	return nil
}

func dispatch(w http.ResponseWriter, r *http.Request, site Site, origin string) {
	q := r.URL.Query()
	playToken := q.Get("play")
	ids := q.Get("ids")
	wd := q.Get("wd")
	ac := q.Get("ac")
	pg, _ := strconv.Atoi(q.Get("pg"))
	if pg < 1 {
		pg = 1
	}

	// 1) 播放解析（type=1 实际不走这里，保留给直接调接口的场景）
	if playToken != "" {
		flag := q.Get("flag")
		real, err := site.Play(playToken, flag)
		if err != nil {
			writeJSON(w, playEnvelope("", nil, flag), 200)
			return
		}
		header := map[string]string{"User-Agent": playerUA, "Referer": site.Base() + "/"}
		writeJSON(w, playEnvelope(real, header, flag), 200)
		return
	}

	// 2) 详情
	// ids 可能单个，也可能是 TVBox 预取时拼起来的一整页（逗号分隔，实测 9~20 个）。
	// 只取第一个会让其余条目拿不到详情，点进去被判成「本源没内容」，
	// 进而触发多源兜底搜索 —— 表现就是「点列表跳到搜索页」。所以必须全部返回。
	if ids != "" {
		var idList []string
		for _, s := range strings.Split(ids, ",") {
			if s = strings.TrimSpace(s); s != "" {
				idList = append(idList, s)
			}
		}

		if len(idList) > 1 {
			cap := idList
			if len(cap) > 24 {
				cap = cap[:24]
			}
			out := make([]V, len(cap))
			var missing []int
			for i, id := range cap {
				if v, ok := vodIndexGet(id); ok {
					out[i] = v
				} else {
					missing = append(missing, i)
				}
			}

			if len(missing) > 0 {
				// 源站是灰产站点，并发太高会直接 403/断连，这里限 5。
				sem := make(chan struct{}, 5)
				var wg sync.WaitGroup
				for _, idx := range missing {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						sem <- struct{}{}
						defer func() { <-sem }()
						out[i] = fetchOne(site, cap[i], false)
					}(idx)
				}
				wg.Wait()
			}

			// 补漏：源站偶发超时/断连，第一轮失败的再串一轮。
			var still []int
			for i, v := range out {
				if v == nil || v.s("vod_name") == "" {
					still = append(still, i)
				}
			}
			if len(still) > 0 {
				if len(still) > 10 {
					still = still[:10]
				}
				var wg sync.WaitGroup
				for _, idx := range still {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						out[i] = fetchOne(site, cap[i], true)
					}(idx)
				}
				wg.Wait()
			}

			vods := make([]V, 0, len(out))
			for _, v := range out {
				if v != nil && v.s("vod_name") != "" {
					vods = append(vods, v)
				}
			}
			for i := range vods {
				rewritePlayURLs(vods[i], origin, site.Key())
			}
			rewritePics(vods, origin, site.Base())
			writeJSON(w, listEnvelope(vods, 1, 1, len(vods)), 200)
			return
		}

		v := fetchOne(site, idList[0], true)
		if v == nil || v.s("vod_name") == "" {
			env := listEnvelope(nil, 1, 1, 0)
			env["msg"] = "error: 详情为空"
			writeJSON(w, env, 200)
			return
		}
		rewritePlayURLs(v, origin, site.Key())
		list := []V{v}
		rewritePics(list, origin, site.Base())
		writeJSON(w, listEnvelope(list, 1, 1, 1), 200)
		return
	}

	// 3) 搜索（TVBox 4.4 搜索不带 pg，固定第 1 页）
	if wd != "" {
		res, err := site.Search(wd, 1)
		if err != nil {
			log.Printf("[%s] search %q err %v", site.Key(), wd, err)
			writeJSON(w, withError(emptyList(1), err), 200)
			return
		}
		indexVods(res.List)
		pc := res.PageCount
		if pc < 1 {
			pc = 1
		}
		writeJSON(w, listEnvelope(rewritePics(res.List, origin, site.Base()), 1, pc, res.Total), 200)
		return
	}

	// 4) 分类 / type=1 首页
	if ac == "detail" {
		// maccms 标准：首页也是 ac=detail，只是不带 t。此时返回 class + list。
		if q.Get("t") == "" {
			classes, list, err := site.Home()
			if err != nil {
				log.Printf("[%s] home err %v", site.Key(), err)
				writeJSON(w, withError(homeListEnvelope(nil, nil), err), 200)
				return
			}
			indexVods(list)
			writeJSON(w, homeListEnvelope(classes, rewritePics(list, origin, site.Base())), 200)
			return
		}
		res, err := site.Category(q.Get("t"), pg)
		if err != nil {
			log.Printf("[%s] category %s err %v", site.Key(), q.Get("t"), err)
			writeJSON(w, withError(emptyList(pg), err), 200)
			return
		}
		indexVods(res.List)
		pc := res.PageCount
		if pc < 1 {
			pc = 1
		}
		p := res.Page
		if p < 1 {
			p = pg
		}
		writeJSON(w, listEnvelope(rewritePics(res.List, origin, site.Base()), p, pc, res.Total), 200)
		return
	}

	// 5) 首页：class = 分类导航，list = 首页推荐
	classes, list, err := site.Home()
	if err != nil {
		log.Printf("[%s] home err %v", site.Key(), err)
		writeJSON(w, withError(homeEnvelope(nil, nil), err), 200)
		return
	}
	indexVods(list)
	writeJSON(w, homeEnvelope(classes, rewritePics(list, origin, site.Base())), 200)
}

// ---------------------------------------------------------------- 配置

func buildConfig(origin string) map[string]interface{} {
	sites := make([]map[string]interface{}, 0, len(allSites))
	for _, s := range allSites {
		sites = append(sites, map[string]interface{}{
			"key":         s.Key(),
			"name":        s.Name(),
			"type":        1,
			"api":         origin + "/api/" + s.Key(),
			"searchable":  1,
			"quickSearch": 1,
			"filterable":  0,
			"changeable":  1,
			"timeout":     20,
			// 站点级播放器类型：2=ExoPlayer。IJK 在这台 Android 4.4 电视上
			// 读 https 分片无读超时，源站掐连接就永久卡在广告帧；Exo 走 Java
			// HTTP 栈有超时和重试。GET /player/ijk 可立刻改回跟随设置页。
			"playerType":  cfgPlayerType(),
			"style":       "",
		})
	}
	// 不能输出 "lives": []。ApiConfig.parseJson 见到 lives 键就取
	// lives_groups.get(0)，空数组会 IndexOutOfBounds，配置解析直接失败。
	return map[string]interface{}{
		"spider":    "",
		"wallpaper": "",
		"sites":     sites,
		"parses":    []interface{}{},
		"flags":     []interface{}{},
		"ads":       []interface{}{},
	}
}

func helpDoc(origin string) map[string]interface{} {
	sites := make([]map[string]interface{}, 0, len(allSites))
	for _, s := range allSites {
		sites = append(sites, map[string]interface{}{
			"key": s.Key(),
			"name": s.Name(),
			"api":  origin + "/api/" + s.Key(),
		})
	}
	return map[string]interface{}{
		"name":    "TVBox 4.4 源站服务（Go）",
		"version": appVersion,
		"config":  origin + "/",
		"sites":   sites,
		"endpoints": map[string]string{
			"首页(分类导航+推荐)": "GET {api}?ac=detail",
			"分类":           "GET {api}?ac=detail&t={type_id}&pg={page}",
			"搜索":           "GET {api}?wd={关键词}&ac=detail&quick=true",
			"详情":           "GET {api}?ac=detail&ids={vod_id}",
			"播放":           "GET {api}?play={token}&flag={线路名}",
			"直播列表":         "GET /live.txt（TVBox 直播源填这个地址）",
			"直播清单代理":       "GET /lv?u={base64}",
		},
	}
}

// ---------------------------------------------------------------- 入口

func main() {
	port := flag.Int("port", 8787, "监听端口")
	addr := flag.String("addr", "0.0.0.0", "监听地址")
	showVer := flag.Bool("version", false, "打印版本")
	insecure := flag.Bool("insecure", false, "全局跳过 TLS 证书校验（仅调试用；默认只对证书校验失败的请求降级）")
	flag.Parse()
	insecureTLS = *insecure

	if *showVer {
		fmt.Println("tvbox44-go", appVersion)
		return
	}

	log.SetFlags(log.Ltime)
	dns := setupResolver()
	if dns != "" {
		log.Printf("resolver -> %s", dns)
	} else {
		log.Printf("resolver -> system default")
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

		path := r.URL.Path
		if len(path) > 1 && strings.HasSuffix(path, "/") {
			path = strings.TrimRight(path, "/")
		}
		origin := "http://" + r.Host
		if r.TLS != nil {
			origin = "https://" + r.Host
		}

		switch {
		case path == "/" || path == "/config" || path == "/tvbox.json":
			writeJSON(lw, buildConfig(origin), 200)

		case path == "/api" || path == "/help":
			writeJSON(lw, helpDoc(origin), 200)

		case path == "/health":
			writeJSON(lw, map[string]interface{}{
				"ok": true, "version": appVersion, "uptime": time.Since(startAt).String(),
				"player_type": cfgPlayerType(), "seg_mode": segMode(),
			}, 200)

		case path == "/img":
			handleImage(lw, r)

		case path == "/live.txt" || path == "/live" || path == "/lives.txt":
			handleLiveList(lw, r)

		case path == "/lv":
			handleLivePlaylist(lw, r)

		case strings.HasPrefix(path, "/seg"):
			handleSegCtl(lw, r, path)

		case strings.HasPrefix(path, "/player/"):
			handlePlayerCtl(lw, r, path)

		case strings.HasPrefix(path, "/p/"):
			handleSeg(lw, r, path)

		case strings.HasPrefix(path, "/v/"):
			handlePlaylist(lw, r)

		case strings.HasPrefix(path, "/api/"):
			key := strings.SplitN(strings.TrimPrefix(path, "/api/"), "/", 2)[0]
			site := getSite(key)
			if site == nil {
				writeJSON(lw, map[string]interface{}{"code": 0, "msg": "unknown site: " + key, "list": []interface{}{}}, 404)
				return
			}
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("[%s] panic %v", key, rec)
					writeJSON(lw, map[string]interface{}{"code": 0, "msg": fmt.Sprint(rec), "list": []interface{}{}}, 500)
				}
			}()
			dispatch(lw, r, site, origin)

		default:
			writeJSON(lw, map[string]interface{}{"code": 0, "msg": "not found", "hint": "访问 / 获取 TVBox 4.4 配置"}, 404)
		}

		log.Printf("%s %s %d %dms", r.Method, r.URL.RequestURI(), lw.status, time.Since(t0).Milliseconds())
	})

	srv := &http.Server{
		Addr:         net.JoinHostPort(*addr, strconv.Itoa(*port)),
		Handler:      mux,
		ReadTimeout:  20 * time.Second,
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("tvbox44-go %s listening on %s", appVersion, srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// 直播列表后台预热（首次要十几秒，别让电视等）
	go warmLiveList()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down")
	_ = srv.Close()
}

var startAt = time.Now()

// runGetprop 只在 Android 上有效；桌面调试时返回错误，调用方退回系统 DNS。
func runGetprop(name string) (string, error) {
	out, err := exec.Command("/system/bin/getprop", name).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func shortURL(u string) string {
	if len(u) > 80 {
		return u[:80] + "..."
	}
	return u
}
