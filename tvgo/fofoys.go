package main

// fofo影视 https://www.fofoys.com/
//
// 站点形态：Next.js 服务端渲染（HTML 里已含列表与详情数据，无需执行 JS）
//   列表   /type/{tvseries|film|varietyshow|anime|shortplay}
//   搜索   /search?q=关键词
//   详情   /{type}/{id}          页面内嵌 var title/pic/note/... 与 var urlList = {...}
//   播放   POST /source/  id={sid}  -> 返回 m3u8/mp4 直链（纯文本）

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const fofoBase = "https://www.fofoys.com"

var fofoTypes = []siteType{
	{"tvseries", "电视剧"},
	{"film", "电影"},
	{"varietyshow", "综艺"},
	{"anime", "动漫"},
	{"shortplay", "短剧"},
}

func fofoTypeName(id string) string {
	for _, t := range fofoTypes {
		if t.ID == id {
			return t.Name
		}
	}
	return ""
}

var (
	fofoTypeRe    = regexp.MustCompile(`<a[^>]*href="/(tvseries|film|anime|varietyshow|shortplay)/(\d+)"[^>]*>`)
	fofoImgRe     = regexp.MustCompile(`<img[^>]*alt="(?:video|vod)"[^>]*>`)
	fofoSpanRe    = regexp.MustCompile(`<span[^>]*class="([^"]*)"[^>]*>([\s\S]*?)</span>`)
	fofoTitleRe   = regexp.MustCompile(`<div class="topic-details-title">([\s\S]*?)</div>`)
	fofoSubRe     = regexp.MustCompile(`<div class="topic-details-title-sub[^"]*">([\s\S]*?)</div>`)
	fofoCountRe   = regexp.MustCompile(`class="search-count[^"]*">\s*<span>\s*(\d+)\s*</span>`)
)

// fofoParseCards 从列表页 HTML 解析卡片。
// 只认 alt="video"/alt="vod" 的图，自动跳过轮播/横幅。
func fofoParseCards(page string) []V {
	matches := fofoTypeRe.FindAllStringSubmatchIndex(page, -1)
	out := make([]V, 0, len(matches))
	seen := map[string]bool{}

	for i, m := range matches {
		start := m[1]
		end := len(page)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		} else if start+6000 < end {
			end = start + 6000
		}
		if start >= end || start >= len(page) {
			continue
		}
		block := page[start:end]

		imgTag := fofoImgRe.FindString(block)
		if imgTag == "" {
			continue
		}
		pic := attr(imgTag, "src")
		if pic == "" {
			continue
		}

		// 标题：搜索卡片是 topic-details-title；网格卡片是带 font-bold 的 span
		//（源站会把 class 属性换行输出，所以不能写死 "<span class=" 直连）
		title := ""
		if mm := fofoTitleRe.FindStringSubmatch(block); mm != nil {
			title = mm[1]
		}
		if strings.TrimSpace(title) == "" {
			for _, sm := range fofoSpanRe.FindAllStringSubmatch(block, -1) {
				if strings.Contains(sm[1], "font-bold") {
					title = sm[2]
					break
				}
			}
		}
		if strings.TrimSpace(title) == "" {
			continue
		}

		sub := ""
		if mm := fofoSubRe.FindStringSubmatch(block); mm != nil {
			sub = mm[1]
		}

		typeID := page[m[2]:m[3]]
		num := page[m[4]:m[5]]
		id := typeID + "/" + num
		if seen[id] {
			continue
		}
		seen[id] = true

		v := V{
			"vod_id":    id,
			"type_id":   typeID,
			"type_name": fofoTypeName(typeID),
			"vod_name":  stripTags(title),
			"vod_pic":   pic,
		}
		if s := stripTags(sub); s != "" {
			v["vod_remarks"] = s
		}
		out = append(out, v)
	}
	return out
}

type fofoSite struct{}

func (s *fofoSite) Key() string  { return "fofoys" }
func (s *fofoSite) Name() string { return "fofo影视" }
func (s *fofoSite) Base() string { return fofoBase }

func (s *fofoSite) Home() ([]Class, []V, error) {
	page, err := fetchText(fofoBase+"/", fetchOpts{TTL: 60})
	if err != nil {
		return nil, nil, err
	}
	return typesToClasses(fofoTypes), fofoParseCards(page), nil
}

func (s *fofoSite) Category(typeID string, page int) (Result, error) {
	tid := typeID
	known := false
	for _, t := range fofoTypes {
		if t.ID == tid {
			known = true
			break
		}
	}
	if !known {
		tid = fofoTypes[0].ID
	}
	if page > 1 {
		return Result{Page: page, PageCount: 1}, nil
	}
	html, err := fetchText(fofoBase+"/type/"+tid, fetchOpts{TTL: 120})
	if err != nil {
		return Result{}, err
	}
	list := fofoParseCards(html)
	return Result{List: list, Page: 1, PageCount: 1, Total: len(list)}, nil
}

func (s *fofoSite) Search(keyword string, page int) (Result, error) {
	if page > 1 {
		return Result{Page: page, PageCount: 1}, nil
	}
	html, err := fetchText(fofoBase+"/search?q="+encURI(keyword), fetchOpts{TTL: 60})
	if err != nil {
		return Result{}, err
	}
	list := fofoParseCards(html)
	total := len(list)
	if m := fofoCountRe.FindStringSubmatch(html); m != nil {
		fmt.Sscanf(m[1], "%d", &total)
	}
	return Result{List: list, Page: 1, PageCount: 1, Total: total}, nil
}

// fofoUrlList 详情页内嵌的 var urlList = {'source':[...], 'url_list':[[...]]}
type fofoEpisode struct {
	Title   string      `json:"title"`
	SID     json.Number `json:"sid"`
	Episode *json.Number `json:"episode"`
}

type fofoUrlList struct {
	Source  []interface{}   `json:"source"`
	URLList [][]fofoEpisode `json:"url_list"`
}

func fofoParseUrlList(page string) *fofoUrlList {
	idx := strings.Index(page, "var urlList")
	if idx < 0 {
		return nil
	}
	lit := extractBalanced(page, idx)
	if lit == "" {
		return nil
	}
	var out fofoUrlList
	dec := json.NewDecoder(strings.NewReader(jsLiteralToJSON(lit)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil
	}
	return &out
}

func (s *fofoSite) Detail(id string) (V, error) {
	html, err := fetchText(fofoBase+"/"+id, fetchOpts{TTL: 300})
	if err != nil {
		return nil, err
	}

	typeID := findQuotedVar(html, "mtype")
	v := V{
		"vod_id":    id,
		"type_id":   typeID,
		"type_name": fofoTypeName(typeID),
		"vod_name":  findQuotedVar(html, "title"),
		"vod_pic":   findQuotedVar(html, "pic"),
	}
	for field, varName := range map[string]string{
		"vod_remarks": "note", "vod_year": "year", "vod_area": "area",
		"vod_class": "label", "vod_actor": "actor", "vod_director": "director",
	} {
		v.set(field, findQuotedVar(html, varName))
	}
	if d := findQuotedVar(html, "description"); d != "" {
		v["vod_content"] = d
		v["vod_blurb"] = d
	}

	data := fofoParseUrlList(html)
	if data != nil {
		var froms, urls []string
		for i := 0; i < len(data.Source) && i < len(data.URLList); i++ {
			items := data.URLList[i]
			if len(items) == 0 {
				continue
			}
			var eps []string
			for n, it := range items {
				name := it.Title
				if name == "" {
					ep := n
					if it.Episode != nil {
						if k, err := it.Episode.Int64(); err == nil {
							ep = int(k)
						}
					}
					name = fmt.Sprintf("第%d集", ep+1)
				}
				eps = append(eps, name+"$ff:"+it.SID.String())
			}
			froms = append(froms, fmt.Sprintf("%v", data.Source[i]))
			urls = append(urls, strings.Join(eps, "#"))
		}
		if len(froms) > 0 {
			v["vod_play_from"] = strings.Join(froms, "$$$")
			v["vod_play_url"] = strings.Join(urls, "$$$")
		}
	}
	return v, nil
}

// Play token 形如 ff:9879220
func (s *fofoSite) Play(token, flag string) (string, error) {
	sid := strings.TrimSpace(strings.TrimPrefix(token, "ff:"))
	if sid == "" || !isDigits(sid) {
		return "", fmt.Errorf("bad play token")
	}
	body, err := fetchText(fofoBase+"/source/", fetchOpts{
		Method:  "POST",
		Body:    "id=" + encURI(sid),
		Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=UTF-8"},
		Referer: fofoBase + "/",
		TTL:     0,
	})
	if err != nil {
		return "", err
	}
	u := strings.Trim(strings.TrimSpace(body), `"'`)
	if !isHTTPURL(u) {
		return "", fmt.Errorf("play resolve failed")
	}
	return u, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
