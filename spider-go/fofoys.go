package main

// fofo影视 https://www.fofoys.com/ （Next.js 服务端渲染，HTML 已含列表与详情数据，无需执行 JS）
//
// 实测 URL 规则：
//   首页   /
//   分类   /type/{tvseries|film|varietyshow|anime|shortplay}
//   搜索   /search?q=关键词
//   详情   /{type}/{id}   内嵌 var title/pic/note/... 与 var urlList = {...}
//   播放   POST /source/  id={sid} -> 返回 m3u8/mp4 直链（纯文本）

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

const fofoBase = "https://www.fofoys.com"

var fofoCategories = []Category{
	{"tvseries", "电视剧"}, {"film", "电影"}, {"varietyshow", "综艺"},
	{"anime", "动漫"}, {"shortplay", "短剧"},
}

var fofoLinkRe = regexp.MustCompile(`^/(tvseries|film|varietyshow|anime|shortplay)/(\d+)`)

func fofoTypeName(id string) string {
	for _, c := range fofoCategories {
		if c.TypeID == id {
			return c.TypeName
		}
	}
	return ""
}

// FofoysSpider 针对 fofoys.com 的实现（本次新增）。
type FofoysSpider struct{}

func (s *FofoysSpider) Key() string  { return "fofoys" }
func (s *FofoysSpider) Name() string { return "fofo影视" }
func (s *FofoysSpider) Base() string { return fofoBase }

func (s *FofoysSpider) Home() ([]Category, error) {
	return fofoCategories, nil
}

// fofoParseCards 只认带 alt="video"/alt="vod" 图片的卡片，自动跳过轮播横幅。
func fofoParseCards(doc *goquery.Document) []VodItem {
	out := make([]VodItem, 0, 40)
	seen := map[string]bool{}
	doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		m := fofoLinkRe.FindStringSubmatch(href)
		if m == nil {
			return
		}
		img := a.Find("img").FilterFunction(func(_ int, im *goquery.Selection) bool {
			alt, _ := im.Attr("alt")
			return alt == "video" || alt == "vod"
		}).First()
		if img.Length() == 0 {
			return
		}
		pic := pickAttr(img, "src", "data-src")
		if pic == "" {
			return
		}

		// 搜索卡片用 .topic-details-title；网格卡片用带 font-bold 的 span
		title := stripTags(a.Find(".topic-details-title").First().Text())
		if title == "" {
			a.Find("span[class]").EachWithBreak(func(_ int, sp *goquery.Selection) bool {
				cl, _ := sp.Attr("class")
				if strings.Contains(cl, "font-bold") {
					title = stripTags(sp.Text())
					return false
				}
				return true
			})
		}
		if title == "" {
			return
		}

		id := m[1] + "/" + m[2]
		if seen[id] {
			return
		}
		seen[id] = true
		sub := stripTags(a.Find(".topic-details-title-sub").First().Text())
		out = append(out, VodItem{
			VodID:      id,
			VodName:    title,
			VodPic:     absURL(fofoBase, pic),
			VodRemarks: sub,
			TypeID:     m[1],
			TypeName:   fofoTypeName(m[1]),
		})
	})
	return out
}

func (s *FofoysSpider) HomeContent() ([]VodItem, error) {
	doc, err := fetchDoc(fofoBase+"/", fetchOpts{TTL: 60})
	if err != nil {
		return nil, err
	}
	return fofoParseCards(doc), nil
}

func (s *FofoysSpider) Category(tid string, page int) (*VodResponse, error) {
	known := false
	for _, c := range fofoCategories {
		if c.TypeID == tid {
			known = true
			break
		}
	}
	if !known {
		tid = fofoCategories[0].TypeID
	}
	if page > 1 {
		return &VodResponse{Code: 1, Msg: "数据列表", Page: page, PageCount: 1, Limit: 20, List: []VodItem{}}, nil
	}
	doc, err := fetchDoc(fofoBase+"/type/"+tid, fetchOpts{TTL: 120})
	if err != nil {
		return nil, err
	}
	return newResponse(fofoParseCards(doc)), nil
}

func (s *FofoysSpider) Search(keyword string, page int) (*VodResponse, error) {
	if page > 1 {
		return &VodResponse{Code: 1, Msg: "数据列表", Page: page, PageCount: 1, Limit: 20, List: []VodItem{}}, nil
	}
	doc, err := fetchDoc(fofoBase+"/search?q="+encURI(keyword), fetchOpts{TTL: 60})
	if err != nil {
		return nil, err
	}
	list := fofoParseCards(doc)
	resp := newResponse(list)
	if m := regexp.MustCompile(`\d+`).FindString(doc.Find(".search-count").First().Text()); m != "" {
		resp.Total = mustAtoi(m)
	}
	return resp, nil
}

type fofoEpisode struct {
	Title   string       `json:"title"`
	SID     json.Number  `json:"sid"`
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

func (s *FofoysSpider) Detail(id string) (*VodItem, error) {
	html, err := fetchText(fofoBase+"/"+id, fetchOpts{TTL: 300})
	if err != nil {
		return nil, err
	}
	typeID := findQuotedVar(html, "mtype")
	item := &VodItem{
		VodID:       id,
		TypeID:      typeID,
		TypeName:    fofoTypeName(typeID),
		VodName:     findQuotedVar(html, "title"),
		VodPic:      findQuotedVar(html, "pic"),
		VodRemarks:  findQuotedVar(html, "note"),
		VodYear:     findQuotedVar(html, "year"),
		VodArea:     findQuotedVar(html, "area"),
		VodClass:    findQuotedVar(html, "label"),
		VodActor:    findQuotedVar(html, "actor"),
		VodDirector: findQuotedVar(html, "director"),
		VodContent:  findQuotedVar(html, "description"),
	}
	item.VodBlurb = item.VodContent

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
						if k, e := it.Episode.Int64(); e == nil {
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
			item.VodPlayFrom = strings.Join(froms, "$$$")
			item.VodPlayURL = strings.Join(urls, "$$$")
		}
	}
	return item, nil
}

// Play token 形如 ff:9879220
func (s *FofoysSpider) Play(flag, id string) ([]PlayUrl, error) {
	sid := strings.TrimSpace(strings.TrimPrefix(id, "ff:"))
	if sid == "" || !isDigits(sid) {
		return nil, fmt.Errorf("bad play token")
	}
	body, err := fetchText(fofoBase+"/source/", fetchOpts{
		Method:  "POST",
		Body:    "id=" + encURI(sid),
		Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=UTF-8"},
		Referer: fofoBase + "/",
		TTL:     -1,
	})
	if err != nil {
		return nil, err
	}
	u := strings.Trim(strings.TrimSpace(body), `"'`)
	if !isHTTPURL(u) {
		return nil, fmt.Errorf("play resolve failed")
	}
	return []PlayUrl{{Name: "播放", URL: u, Flag: flag}}, nil
}
