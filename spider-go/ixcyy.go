package main

// 星辰影院 https://www.ixcyy.tv/ （苹果 CMS V10 / shoutu46 模板，服务端渲染 HTML）
//
// 实测 URL 规则（原稿里猜的 /vodshow/{id}--------{pg}---.html 是错的）：
//   首页   /
//   分类   /v/{1..5}.html        （源站分类页无分页器，按单页处理）
//   搜索   /s.html?wd=关键词      第 N 页 /s{关键词}/page/{N}.html
//   详情   /d-{id}.html
//   播放   /p/{id}-{flag}-{ep}.html  页面内嵌 var player_aaaa={...,"url":"...m3u8"}

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

const xcBase = "https://www.ixcyy.tv"

var xcCategories = []Category{
	{"1", "电影"}, {"2", "电视剧"}, {"3", "综艺"}, {"4", "动漫"}, {"5", "短剧"},
}

var (
	xcPlayerRe = regexp.MustCompile(`"url"\s*:\s*"([^"]+)"`)
	xcYearRe   = regexp.MustCompile(`/s/year/(\d{4})\.html`)
	xcPlayPath = regexp.MustCompile(`^p/\d+-\d+-\d+\.html$`)
	xcTitleTag = regexp.MustCompile(`<title>[^《]*《([^》]+)》`)
)

// IxcyySpider 针对 ixcyy.tv 的实现（保留原稿类名，解析改用 goquery）。
type IxcyySpider struct{}

func (s *IxcyySpider) Key() string  { return "ixcyy" }
func (s *IxcyySpider) Name() string { return "星辰影院" }
func (s *IxcyySpider) Base() string { return xcBase }

func (s *IxcyySpider) Home() ([]Category, error) {
	return xcCategories, nil
}

// xcParseGrid 解析 a.public-list-exp 网格卡片。
func xcParseGrid(doc *goquery.Document) []VodItem {
	out := make([]VodItem, 0, 24)
	doc.Find("a.public-list-exp").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		id := extractID(href)
		if id == "" {
			return
		}
		img := a.Find("img").First()
		pic := pickAttr(img, "data-src", "src")
		// 模板占位图要跳过
		if strings.Contains(pic, "img-bj-k") {
			pic = ""
		}
		name := strings.TrimSpace(pickAttr(a, "title"))
		if name == "" {
			name = strings.TrimSuffix(strings.TrimSpace(pickAttr(img, "alt")), "封面图")
		}
		if name == "" {
			name = stripTags(a.Find(".thumb-txt").First().Text())
		}
		if name == "" {
			return
		}
		item := VodItem{
			VodID:      id,
			VodName:    name,
			VodPic:     absURL(xcBase, pic),
			VodRemarks: stripTags(a.Find(".public-list-prb").First().Text()),
			TypeName:   stripTags(a.Find(".public-prt").First().Text()),
		}
		out = append(out, item)
	})
	return out
}

// xcParseHero 解析首页轮播（slide-time-img3 + slide-info-title）。
func xcParseHero(doc *goquery.Document) []VodItem {
	out := make([]VodItem, 0, 8)
	doc.Find("a").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		id := extractID(href)
		if id == "" {
			return
		}
		title := stripTags(a.Find("h3.slide-info-title").First().Text())
		if title == "" {
			return
		}
		style, _ := a.Find(".slide-time-img3").First().Attr("style")
		remarks := ""
		if sp := a.Find("span.slide-info-remarks"); sp.Length() > 0 {
			remarks = stripTags(sp.Last().Text())
		}
		out = append(out, VodItem{
			VodID:      id,
			VodName:    title,
			VodPic:     absURL(xcBase, cssURL(style)),
			VodRemarks: remarks,
		})
	})
	return out
}

func xcMerge(lists ...[]VodItem) []VodItem {
	seen := map[string]bool{}
	out := make([]VodItem, 0, 32)
	for _, l := range lists {
		for _, it := range l {
			if it.VodID == "" || it.VodName == "" || seen[it.VodID] {
				continue
			}
			seen[it.VodID] = true
			out = append(out, it)
		}
	}
	return out
}

func (s *IxcyySpider) HomeContent() ([]VodItem, error) {
	doc, err := fetchDoc(xcBase+"/", fetchOpts{TTL: 60})
	if err != nil {
		return nil, err
	}
	return xcMerge(xcParseGrid(doc), xcParseHero(doc)), nil
}

func (s *IxcyySpider) Category(tid string, page int) (*VodResponse, error) {
	known := false
	for _, c := range xcCategories {
		if c.TypeID == tid {
			known = true
			break
		}
	}
	if !known {
		tid = xcCategories[0].TypeID
	}
	if page > 1 {
		return &VodResponse{Code: 1, Msg: "数据列表", Page: page, PageCount: 1, Limit: 20, List: []VodItem{}}, nil
	}
	doc, err := fetchDoc(xcBase+"/v/"+tid+".html", fetchOpts{TTL: 120})
	if err != nil {
		return nil, err
	}
	return newResponse(xcParseGrid(doc)), nil
}

func (s *IxcyySpider) Search(keyword string, page int) (*VodResponse, error) {
	kw := encURI(keyword)
	u := xcBase + "/s.html?wd=" + kw
	if page > 1 {
		u = fmt.Sprintf("%s/s%s/page/%d.html", xcBase, kw, page)
	}
	doc, err := fetchDoc(u, fetchOpts{TTL: 60})
	if err != nil {
		return nil, err
	}
	list := xcMerge(xcParseGrid(doc), xcParseHero(doc))
	resp := newResponse(list)
	resp.Page = page
	// 搜索页带「共N条数据,当前a/b页」
	pc := 1
	if m := regexp.MustCompile(`共(\d+)条数据,当前(\d+)/(\d+)页`).FindStringSubmatch(doc.Text()); m != nil {
		pc = mustAtoi(m[3])
	}
	resp.PageCount = pc
	if pc > 1 {
		resp.Total = pc * 20
	}
	return resp, nil
}

func (s *IxcyySpider) Detail(id string) (*VodItem, error) {
	doc, err := fetchDoc(xcBase+"/d-"+id+".html", fetchOpts{TTL: 300})
	if err != nil {
		return nil, err
	}
	html, _ := doc.Html()

	title := stripTags(doc.Find("h3.slide-info-title").First().Text())
	if title == "" {
		if m := xcTitleTag.FindStringSubmatch(html); m != nil {
			title = stripTags(m[1])
		}
	}

	pic := ""
	doc.Find("img").EachWithBreak(func(_ int, im *goquery.Selection) bool {
		p := pickAttr(im, "data-src", "src")
		if strings.Contains(p, "/upload/vod/") {
			pic = p
			return false
		}
		return true
	})

	fields := map[string]string{}
	doc.Find("li").Each(func(_ int, li *goquery.Selection) {
		k := stripTags(li.Find("em").First().Text())
		k = strings.TrimSuffix(strings.TrimSuffix(k, "："), ":")
		if k != "" {
			fields[k] = stripTags(li.Text())
		}
	})
	desc := stripTags(doc.Find("#height_limit").First().Text())
	if desc == "" {
		desc = fields["简介"]
	}
	remark := stripTags(doc.Find("span.slide-info-remarks").Last().Text())
	if remark == "" {
		remark = fields["更新"]
	}

	item := &VodItem{
		VodID:       id,
		VodName:     title,
		VodPic:      absURL(xcBase, pic),
		VodRemarks:  remark,
		VodClass:    fields["类型"],
		VodLang:     fields["语言"],
		VodTime:     fields["更新"],
		VodContent:  desc,
		VodBlurb:    desc,
		VodActor:    fields["主演"],
		VodDirector: fields["导演"],
		VodArea:     fields["地区"],
	}
	if m := xcYearRe.FindStringSubmatch(html); m != nil {
		item.VodYear = m[1]
	}
	if fields["类型"] != "" {
		item.TypeName = fields["类型"]
	}

	// 线路名 + 选集分组
	var tabs []string
	doc.Find(".anthology-tab a.swiper-slide").Each(func(_ int, a *goquery.Selection) {
		n := stripTags(a.Text())
		if n != "" && !strings.Contains(n, "下载") {
			tabs = append(tabs, n)
		}
	})

	var froms, urls []string
	used := map[string]bool{}
	doc.Find(".anthology-list-box").Each(func(i int, box *goquery.Selection) {
		var eps []string
		box.Find("a").Each(func(_ int, a *goquery.Selection) {
			href, _ := a.Attr("href")
			if !strings.HasPrefix(href, "/p/") {
				return
			}
			name := stripTags(a.Text())
			if name == "" {
				name = "播放"
			}
			eps = append(eps, name+"$xc:"+strings.TrimPrefix(href, "/"))
		})
		if len(eps) == 0 {
			return
		}
		name := fmt.Sprintf("线路%d", len(froms)+1)
		if i < len(tabs) && tabs[i] != "" {
			name = tabs[i]
		}
		if used[name] {
			name = fmt.Sprintf("%s%d", name, len(froms)+1)
		}
		used[name] = true
		froms = append(froms, name)
		urls = append(urls, strings.Join(eps, "#"))
	})
	if len(froms) > 0 {
		item.VodPlayFrom = strings.Join(froms, "$$$")
		item.VodPlayURL = strings.Join(urls, "$$$")
	}
	return item, nil
}

// Play token 形如 xc:p/179223-2-1.html
func (s *IxcyySpider) Play(flag, id string) ([]PlayUrl, error) {
	path := strings.TrimSpace(strings.TrimPrefix(id, "xc:"))
	if !xcPlayPath.MatchString(path) {
		return nil, fmt.Errorf("bad play token")
	}
	html, err := fetchText(xcBase+"/"+path, fetchOpts{Referer: xcBase + "/", TTL: 60})
	if err != nil {
		return nil, err
	}
	u := ""
	if idx := strings.Index(html, "var player_aaaa"); idx >= 0 {
		if lit := extractBalanced(html, idx); lit != "" {
			var m map[string]interface{}
			if json.Unmarshal([]byte(lit), &m) == nil {
				if v, ok := m["url"].(string); ok {
					u = v
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
		return nil, fmt.Errorf("play resolve failed")
	}
	return []PlayUrl{{Name: "播放", URL: u, Flag: flag}}, nil
}
