package main

// 飘花影视 https://www.piaoys.com/
//
// 站点形态：苹果 CMS V10（stui 模板），服务端渲染 HTML
//   分类   /sort/{1|2|3|4|27}.html        （单页列表，无分页）
//   搜索   /sou/-------------.html?wd=关键词
//   详情   /html/{id}.html                播放组 <span class="pull-right1">源名</span> + 选集列表
//   播放   /play/{id}-{flag}-{ep}.html    页面内嵌 var player_data={...,"url":"...m3u8"}

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const piaoBase = "https://www.piaoys.com"

var piaoTypes = []siteType{
	{"1", "电影"},
	{"2", "电视剧"},
	{"3", "动漫"},
	{"4", "综艺"},
	{"27", "短剧"},
}

var (
	piaoHrefRe   = regexp.MustCompile(`href="(/html/\d+\.html)"`)
	piaoIDRe     = regexp.MustCompile(`/html/(\d+)\.html`)
	piaoTitleRe  = regexp.MustCompile(`title="([^"]*)"`)
	piaoPicRe    = regexp.MustCompile(`data-original="([^"]*)"`)
	piaoRemarkRe = regexp.MustCompile(`<span class="pic-text[^"]*">([\s\S]*?)</span>`)
	piaoH1Re     = regexp.MustCompile(`<h1 class="title">([^<]+)</h1>`)
	piaoThumbRe  = regexp.MustCompile(`<div class="stui-content__thumb"[\s\S]{0,600}?data-original="([^"]+)"`)
	piaoDescRe   = regexp.MustCompile(`<span class="detail-content"[^>]*>([\s\S]*?)</span>`)
	piaoULRe     = regexp.MustCompile(`<ul class="stui-content__playlist[^"]*">([\s\S]*?)</ul>`)
	piaoSrcName  = regexp.MustCompile(`<span class="pull-right1">([^<]*)</span>`)
	piaoEpRe     = regexp.MustCompile(`<a href="/play/(\d+)-(\d+)-(\d+)\.html">([^<]*)</a>`)
	piaoPlayerRe = regexp.MustCompile(`"url"\s*:\s*"([^"]+)"`)
)

// piaoParseCards 解析 stui-vodlist 卡片（首页 / 分类 / 搜索页通用结构）。
func piaoParseCards(page string) []V {
	const marker = `<div class="stui-vodlist__box"`
	parts := strings.Split(page, marker)
	out := make([]V, 0, len(parts))
	seen := map[string]bool{}

	for _, chunk := range parts[1:] {
		href := piaoHrefRe.FindStringSubmatch(chunk)
		if href == nil {
			continue
		}
		idm := piaoIDRe.FindStringSubmatch(href[1])
		if idm == nil {
			continue
		}
		id := idm[1]
		if seen[id] {
			continue
		}

		title := piaoTitleRe.FindStringSubmatch(chunk)
		pic := piaoPicRe.FindStringSubmatch(chunk)
		if title == nil || pic == nil {
			continue
		}

		seen[id] = true
		v := V{
			"vod_id":   id,
			"vod_name": stripTags(title[1]),
			"vod_pic":  pic[1],
		}
		if rm := piaoRemarkRe.FindStringSubmatch(chunk); rm != nil {
			v["vod_remarks"] = stripTags(rm[1])
		}
		out = append(out, v)
	}
	return out
}

type piaoSite struct{}

func (s *piaoSite) Key() string  { return "piaoys" }
func (s *piaoSite) Name() string { return "飘花影视" }
func (s *piaoSite) Base() string { return piaoBase }

func (s *piaoSite) Home() ([]Class, []V, error) {
	page, err := fetchText(piaoBase+"/", fetchOpts{TTL: 60})
	if err != nil {
		return nil, nil, err
	}
	return typesToClasses(piaoTypes), piaoParseCards(page), nil
}

func (s *piaoSite) Category(typeID string, page int) (Result, error) {
	tid := typeID
	known := false
	for _, t := range piaoTypes {
		if t.ID == tid {
			known = true
			break
		}
	}
	if !known {
		tid = piaoTypes[0].ID
	}
	if page > 1 {
		return Result{Page: page, PageCount: 1}, nil
	}
	html, err := fetchText(piaoBase+"/sort/"+tid+".html", fetchOpts{TTL: 120})
	if err != nil {
		return Result{}, err
	}
	list := piaoParseCards(html)
	return Result{List: list, Page: 1, PageCount: 1, Total: len(list)}, nil
}

func (s *piaoSite) Search(keyword string, page int) (Result, error) {
	if page > 1 {
		return Result{Page: page, PageCount: 1}, nil
	}
	html, err := fetchText(piaoBase+"/sou/-------------.html?wd="+encURI(keyword), fetchOpts{TTL: 60})
	if err != nil {
		return Result{}, err
	}
	list := piaoParseCards(html)
	return Result{List: list, Page: 1, PageCount: 1, Total: len(list)}, nil
}

func (s *piaoSite) Detail(id string) (V, error) {
	html, err := fetchText(piaoBase+"/html/"+id+".html", fetchOpts{TTL: 300})
	if err != nil {
		return nil, err
	}

	// 详情字段区：<p class="data"><span class="data2">年份：</span>2026</p>
	get := func(label string) string {
		re := regexp.MustCompile(`<p class="data"><span class="data2">` + regexp.QuoteMeta(label) + `：</span>([\s\S]*?)</p>`)
		if m := re.FindStringSubmatch(html); m != nil {
			return stripTags(m[1])
		}
		return ""
	}

	title := ""
	if m := piaoH1Re.FindStringSubmatch(html); m != nil {
		title = m[1]
	}
	pic := ""
	if m := piaoThumbRe.FindStringSubmatch(html); m != nil {
		pic = m[1]
	}
	desc := ""
	if m := piaoDescRe.FindStringSubmatch(html); m != nil {
		desc = stripTags(m[1])
	}

	v := V{
		"vod_id":    id,
		"vod_name":  stripTags(title),
		"vod_pic":   pic,
		"vod_class": get("类型"),
	}
	for field, label := range map[string]string{
		"type_name": "类型", "vod_remarks": "状态", "vod_year": "年份",
		"vod_area": "地区", "vod_actor": "主演", "vod_director": "导演",
		"vod_time": "更新",
	} {
		v.set(field, get(label))
	}
	if desc != "" {
		v["vod_content"] = desc
		v["vod_blurb"] = desc
	}

	// 播放组：每个选集 <ul> 之前的 pull-right1 即源名
	var froms, urls []string
	usedNames := map[string]bool{}

	for _, m := range piaoULRe.FindAllStringSubmatchIndex(html, -1) {
		beforeStart := m[0] - 1200
		if beforeStart < 0 {
			beforeStart = 0
		}
		before := html[beforeStart:m[0]]

		name := ""
		if srcs := piaoSrcName.FindAllStringSubmatch(before, -1); len(srcs) > 0 {
			name = stripTags(srcs[len(srcs)-1][1])
		}
		if name == "" {
			name = fmt.Sprintf("线路%d", len(froms)+1)
		}
		if usedNames[name] {
			name = fmt.Sprintf("%s%d", name, len(froms)+1)
		}
		usedNames[name] = true

		inner := html[m[2]:m[3]]
		eps := piaoEpRe.FindAllStringSubmatch(inner, -1)
		if len(eps) == 0 {
			continue
		}

		items := make([]string, 0, len(eps))
		for _, e := range eps {
			items = append(items, stripTags(e[4])+"$py:play/"+e[1]+"-"+e[2]+"-"+e[3]+".html")
		}
		froms = append(froms, name)
		urls = append(urls, strings.Join(items, "#"))
	}

	if len(froms) > 0 {
		v["vod_play_from"] = strings.Join(froms, "$$$")
		v["vod_play_url"] = strings.Join(urls, "$$$")
	}
	return v, nil
}

// Play token 形如 py:play/502680-2-1.html
func (s *piaoSite) Play(token, flag string) (string, error) {
	path := strings.TrimSpace(strings.TrimPrefix(token, "py:"))
	if !regexp.MustCompile(`^play/\d+-\d+-\d+\.html$`).MatchString(path) {
		return "", fmt.Errorf("bad play token")
	}

	html, err := fetchText(piaoBase+"/"+path, fetchOpts{Referer: piaoBase + "/", TTL: 60})
	if err != nil {
		return "", err
	}

	u := ""
	if idx := strings.Index(html, "var player_data"); idx >= 0 {
		if lit := extractBalanced(html, idx); lit != "" {
			var m map[string]interface{}
			if json.Unmarshal([]byte(lit), &m) == nil {
				if s, ok := m["url"].(string); ok {
					u = s
				}
			}
		}
	}
	if u == "" {
		if m := piaoPlayerRe.FindStringSubmatch(html); m != nil {
			u = m[1]
		}
	}
	u = strings.ReplaceAll(u, `\/`, "/")
	if !isHTTPURL(u) {
		return "", fmt.Errorf("play resolve failed")
	}
	return u, nil
}
