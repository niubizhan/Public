package main

// 星辰影院 https://www.ixcyy.tv/（原 www.xcyycn.cc，2026-11 起 301 到新域名）
//
// 站点形态：苹果 CMS V10（shoutu46 模板），服务端渲染 HTML
//   分类   /v/{1..5}.html  第 N 页为 /v/{type}-{N}.html
//   搜索   /s.html?wd=关键词  第 N 页为 /s{关键词}/page/{N}.html
//   详情   /d-{id}.html     资源列表 .anthology-tab（线路名） + .anthology-list-box（选集 /p/...）
//   播放   /p/{id}-{flag}-{ep}.html   页面内嵌 var player_aaaa={...,"url":"...m3u8"}

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const xcBase = "https://www.ixcyy.tv"

var xcTypes = []siteType{
	{"1", "电影"},
	{"2", "电视剧"},
	{"3", "综艺"},
	{"4", "动漫"},
	{"5", "短剧"},
}

var (
	xcGridRe    = regexp.MustCompile(`<a([^>]*class="public-list-exp"[^>]*)>([\s\S]*?)</a>`)
	xcGridHref  = regexp.MustCompile(`href="(/d-\d+\.html)"`)
	xcGridID    = regexp.MustCompile(`/d-(\d+)\.html`)
	xcDataSrc   = regexp.MustCompile(`data-src="([^"]+)"`)
	xcSrcRe     = regexp.MustCompile(`(?:^|[\s"'])src="([^"]+)"`)
	xcTitleAttr = regexp.MustCompile(`title="([^"]*)"`)
	xcAltRe     = regexp.MustCompile(`alt="([^"]*)"`)
	xcThumbTxt  = regexp.MustCompile(`<div class="thumb-txt[^"]*">([\s\S]*?)</div>`)
	xcRemarkRe  = regexp.MustCompile(`<span class="public-list-prb[^"]*">([\s\S]*?)</span>`)
	xcTypeRe    = regexp.MustCompile(`<span class="public-prt[^"]*">([\s\S]*?)</span>`)

	xcHeroRe    = regexp.MustCompile(`<a href="(/d-\d+\.html)"[\s\S]{0,600}?background-image:\s*url\('([^']+)'\)[\s\S]{0,900}?<h3 class="slide-info-title[^"]*">([\s\S]*?)</h3>`)
	xcHeroRem   = regexp.MustCompile(`<span class="slide-info-remarks">([\s\S]*?)</span>`)
	xcPageCount = regexp.MustCompile(`共(\d+)条数据,当前(\d+)/(\d+)页`)

	xcH3Re     = regexp.MustCompile(`<h3 class="slide-info-title[^"]*">([\s\S]*?)</h3>`)
	xcTitleTag = regexp.MustCompile(`<title>[^《]*《([^》]+)》`)
	xcPicRe    = regexp.MustCompile(`(?:data-src|src)="(https?://[^"]*/upload/vod/[^"]+)"`)
	xcDescRe   = regexp.MustCompile(`id="height_limit"[^>]*>([\s\S]*?)</div>`)
	xcLiRe     = regexp.MustCompile(`<li><em>([^<]+)：</em>([\s\S]*?)</li>`)
	xcYearRe   = regexp.MustCompile(`/s/year/(\d{4})\.html`)
	xcTabRe    = regexp.MustCompile(`<div class="anthology-tab[^"]*">([\s\S]*?)</div>`)
	xcTabItem  = regexp.MustCompile(`<a class="swiper-slide"><i class="fa[^"]*"></i>&nbsp;([^<]+)</a>`)
	xcBoxRe    = regexp.MustCompile(`<div class=['"]anthology-list-box[^'"]*['"]>([\s\S]*?)</div>`)
	xcEpRe     = regexp.MustCompile(`<a[^>]*href="(/p/\d+-\d+-\d+\.html)"[^>]*>([^<]*)</a>`)
	xcPlayerRe = regexp.MustCompile(`"url"\s*:\s*"([^"]+)"`)
)

// xcParseGrid 网格卡片：public-list-exp
func xcParseGrid(page string) []V {
	out := make([]V, 0, 24)
	for _, m := range xcGridRe.FindAllStringSubmatchIndex(page, -1) {
		attrs := page[m[2]:m[3]]
		inner := page[m[4]:m[5]]

		href := xcGridHref.FindStringSubmatch(attrs)
		if href == nil {
			continue
		}
		idm := xcGridID.FindStringSubmatch(href[1])
		if idm == nil {
			continue
		}

		pic := ""
		if pm := xcDataSrc.FindStringSubmatch(inner); pm != nil {
			pic = pm[1]
		} else if pm := xcSrcRe.FindStringSubmatch(inner); pm != nil {
			pic = pm[1]
		}

		// 搜索页的 a 标签没有 title 属性：退化到图片 alt（去掉“封面图”后缀），
		// 再退化到 a 标签之后的 .thumb-txt
		name := ""
		if tm := xcTitleAttr.FindStringSubmatch(attrs); tm != nil {
			name = stripTags(tm[1])
		}
		if name == "" {
			if am := xcAltRe.FindStringSubmatch(inner); am != nil {
				name = strings.TrimSuffix(stripTags(am[1]), "封面图")
			}
		}
		if name == "" {
			after := page[m[1]:]
			if len(after) > 500 {
				after = after[:500]
			}
			if tt := xcThumbTxt.FindStringSubmatch(after); tt != nil {
				name = stripTags(tt[1])
			}
		}

		v := V{"vod_id": idm[1], "vod_name": name}
		if pic != "" {
			v["vod_pic"] = absURL(xcBase, pic)
		}
		if rm := xcRemarkRe.FindStringSubmatch(inner); rm != nil {
			v["vod_remarks"] = stripTags(rm[1])
		}
		if tn := xcTypeRe.FindStringSubmatch(inner); tn != nil {
			v["type_name"] = stripTags(tn[1])
		}
		out = append(out, v)
	}
	return out
}

// xcParseHero 首页轮播卡片：slide-time-img3
func xcParseHero(page string) []V {
	out := make([]V, 0, 8)
	for _, m := range xcHeroRe.FindAllStringSubmatchIndex(page, -1) {
		idm := xcGridID.FindStringSubmatch(page[m[2]:m[3]])
		if idm == nil {
			continue
		}
		tail := page[m[0]:]
		if len(tail) > 1200 {
			tail = tail[:1200]
		}
		v := V{
			"vod_id":   idm[1],
			"vod_name": stripTags(page[m[6]:m[7]]),
			"vod_pic":  absURL(xcBase, page[m[4]:m[5]]),
		}
		if rm := xcHeroRem.FindStringSubmatch(tail); rm != nil {
			v["vod_remarks"] = stripTags(rm[1])
		}
		out = append(out, v)
	}
	return out
}

// xcMerge 去重合并，丢掉没有 id 或片名的条目。
func xcMerge(lists ...[]V) []V {
	seen := map[string]bool{}
	out := make([]V, 0, 32)
	for _, l := range lists {
		for _, item := range l {
			id := item.s("vod_id")
			if id == "" || item.s("vod_name") == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, item)
		}
	}
	return out
}

func xcParsePageCount(page string) int {
	if m := xcPageCount.FindStringSubmatch(page); m != nil {
		n, _ := strconv.Atoi(m[3])
		return n
	}
	return 0
}

type xcSite struct{}

func (s *xcSite) Key() string  { return "xcyycn" }
func (s *xcSite) Name() string { return "星辰影院" }
func (s *xcSite) Base() string { return xcBase }

func (s *xcSite) Home() ([]Class, []V, error) {
	page, err := fetchText(xcBase+"/", fetchOpts{TTL: 60})
	if err != nil {
		return nil, nil, err
	}
	return typesToClasses(xcTypes), xcMerge(xcParseGrid(page), xcParseHero(page)), nil
}

func (s *xcSite) Category(typeID string, page int) (Result, error) {
	tid := typeID
	known := false
	for _, t := range xcTypes {
		if t.ID == tid {
			known = true
			break
		}
	}
	if !known {
		tid = xcTypes[0].ID
	}
	// 源站分类页没有分页器：/v/1-2.html 与 ?page=2 返回同一批数据，按单页处理。
	if page > 1 {
		return Result{Page: page, PageCount: 1}, nil
	}
	html, err := fetchText(xcBase+"/v/"+tid+".html", fetchOpts{TTL: 120})
	if err != nil {
		return Result{}, err
	}
	list := xcParseGrid(html)
	return Result{List: list, Page: 1, PageCount: 1, Total: len(list)}, nil
}

func (s *xcSite) Search(keyword string, page int) (Result, error) {
	kw := encURI(keyword)
	u := xcBase + "/s.html?wd=" + kw
	if page > 1 {
		u = fmt.Sprintf("%s/s%s/page/%d.html", xcBase, kw, page)
	}
	html, err := fetchText(u, fetchOpts{TTL: 60})
	if err != nil {
		return Result{}, err
	}
	list := xcMerge(xcParseGrid(html), xcParseHero(html))
	pc := xcParsePageCount(html)
	if pc == 0 {
		pc = 1
	}
	total := len(list)
	if pc > 1 {
		total = pc * 20
	}
	p := page
	if p < 1 {
		p = 1
	}
	return Result{List: list, Page: p, PageCount: pc, Total: total}, nil
}

func (s *xcSite) Detail(id string) (V, error) {
	html, err := fetchText(xcBase+"/d-"+id+".html", fetchOpts{TTL: 300})
	if err != nil {
		return nil, err
	}

	title := ""
	if m := xcH3Re.FindStringSubmatch(html); m != nil {
		title = m[1]
	}
	if strings.TrimSpace(stripTags(title)) == "" {
		if m := xcTitleTag.FindStringSubmatch(html); m != nil {
			title = m[1]
		}
	}

	pic := ""
	if m := xcPicRe.FindStringSubmatch(html); m != nil {
		pic = m[1]
	}

	fields := map[string]string{}
	for _, m := range xcLiRe.FindAllStringSubmatch(html, -1) {
		fields[stripTags(m[1])] = stripTags(m[2])
	}

	desc := fields["简介"]
	if m := xcDescRe.FindStringSubmatch(html); m != nil {
		if s := stripTags(m[1]); s != "" {
			desc = s
		}
	}

	remark := fields["更新"]
	if m := xcHeroRem.FindStringSubmatch(html); m != nil {
		remark = stripTags(m[1])
	}

	v := V{
		"vod_id":      id,
		"vod_name":    stripTags(title),
		"vod_pic":     pic,
		"vod_remarks": remark,
		"vod_class":   fields["类型"],
		"vod_lang":    fields["语言"],
		"vod_time":    fields["更新"],
	}
	if tn := fields["类型"]; tn != "" {
		v["type_name"] = tn
	}
	if m := xcYearRe.FindStringSubmatch(html); m != nil {
		v["vod_year"] = m[1]
	}
	if desc != "" {
		v["vod_content"] = desc
		v["vod_blurb"] = desc
	}

	// 线路名（.anthology-tab 内的 swiper-slide）
	var tabs []string
	if tm := xcTabRe.FindStringSubmatch(html); tm != nil {
		for _, m := range xcTabItem.FindAllStringSubmatch(tm[1], -1) {
			name := stripTags(m[1])
			if name != "" && !strings.Contains(name, "下载") {
				tabs = append(tabs, name)
			}
		}
	}

	var froms, urls []string
	usedNames := map[string]bool{}
	for i, box := range xcBoxRe.FindAllStringSubmatch(html, -1) {
		eps := xcEpRe.FindAllStringSubmatch(box[1], -1)
		if len(eps) == 0 {
			continue
		}

		name := fmt.Sprintf("线路%d", len(froms)+1)
		if i < len(tabs) && tabs[i] != "" {
			name = tabs[i]
		}
		if usedNames[name] {
			name = fmt.Sprintf("%s%d", name, len(froms)+1)
		}
		usedNames[name] = true

		items := make([]string, 0, len(eps))
		for _, e := range eps {
			items = append(items, stripTags(e[2])+"$xc:"+strings.TrimPrefix(e[1], "/"))
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

// Play token 形如 xc:p/179223-2-1.html
func (s *xcSite) Play(token, flag string) (string, error) {
	path := strings.TrimSpace(strings.TrimPrefix(token, "xc:"))
	if !regexp.MustCompile(`^p/\d+-\d+-\d+\.html$`).MatchString(path) {
		return "", fmt.Errorf("bad play token")
	}

	html, err := fetchText(xcBase+"/"+path, fetchOpts{Referer: xcBase + "/", TTL: 60})
	if err != nil {
		return "", err
	}

	u := ""
	if idx := strings.Index(html, "var player_aaaa"); idx >= 0 {
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
		if m := xcPlayerRe.FindStringSubmatch(html); m != nil {
			u = m[1]
		}
	}
	u = strings.ReplaceAll(u, `\/`, "/")
	if !isHTTPURL(u) {
		return "", fmt.Errorf("play resolve failed")
	}
	return u, nil
}
