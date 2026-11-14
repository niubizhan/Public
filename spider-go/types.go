package main

// 修复说明（对应你给的原稿）：
//  1. 原稿引用了 Category / PlayUrl / extractID 但从未定义 -> 这里补全，否则编译失败。
//  2. 原稿 VodItem 只有 4 个字段，TVBox 4.4 详情页需要 vod_play_from / vod_play_url
//     等字段，缺字段会导致「点进去没有播放列表」-> 补全常用字段。
//  3. 原稿 VodResponse.limit 用 int，但 TVBox AbsJson.limit 是 String
//     （bean 里 Integer.parseInt），输出层统一转成 "20" 字符串。
//  4. 原稿 Spider 接口只有 5 个方法，缺站点元信息与首页推荐 -> 补 Key/Name/Base/HomeContent。

// Category 首页分类导航项。
type Category struct {
	TypeID   string `json:"type_id"`
	TypeName string `json:"type_name"`
}

// PlayUrl 一条可播放地址。
type PlayUrl struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Flag string `json:"flag,omitempty"`
}

// VodItem 一部影片的卡片 / 详情数据。
type VodItem struct {
	VodID       string `json:"vod_id"`
	VodName     string `json:"vod_name"`
	VodPic      string `json:"vod_pic"`
	VodRemarks  string `json:"vod_remarks"`
	TypeID      string `json:"type_id,omitempty"`
	TypeName    string `json:"type_name,omitempty"`
	VodYear     string `json:"vod_year,omitempty"`
	VodArea     string `json:"vod_area,omitempty"`
	VodLang     string `json:"vod_lang,omitempty"`
	VodClass    string `json:"vod_class,omitempty"`
	VodActor    string `json:"vod_actor,omitempty"`
	VodDirector string `json:"vod_director,omitempty"`
	VodContent  string `json:"vod_content,omitempty"`
	VodBlurb    string `json:"vod_blurb,omitempty"`
	VodTime     string `json:"vod_time,omitempty"`
	VodPlayFrom string `json:"vod_play_from,omitempty"`
	VodPlayURL  string `json:"vod_play_url,omitempty"`
}

// VodResponse TVBox 通用列表响应（原稿结构，保留）。
type VodResponse struct {
	Code      int        `json:"code"`
	Msg       string     `json:"msg"`
	Page      int        `json:"page"`
	PageCount int        `json:"pagecount"`
	Limit     int        `json:"limit"`
	Total     int        `json:"total"`
	List      []VodItem  `json:"list"`
	Class     []Category `json:"class,omitempty"`
}

// Class 是 Category 的别名，方便接口里写 []Category。
type Class = Category

// Spider 核心爬虫接口（保留原稿 5 个方法签名，补 4 个站点方法）。
type Spider interface {
	Key() string
	Name() string
	Base() string

	// Home 分类导航；HomeContent 首页推荐位（TVBox 首页两样都要）
	Home() ([]Category, error)
	HomeContent() ([]VodItem, error)

	// Category 分类页 / 换页
	Category(tid string, page int) (*VodResponse, error)
	// Detail 详情（含播放分组）
	Detail(id string) (*VodItem, error)
	// Search 搜索
	Search(keyword string, page int) (*VodResponse, error)
	// Play 返回最终可播地址列表；id 是详情页里下发的 token
	Play(flag, id string) ([]PlayUrl, error)
}

// 站点注册表：新增站点只需实现 Spider 并加到这里。
var allSpiders = []Spider{
	&IxcyySpider{},
	&FofoysSpider{},
}

func getSpider(key string) Spider {
	for _, s := range allSpiders {
		if s.Key() == key {
			return s
		}
	}
	return nil
}

func newResponse(list []VodItem) *VodResponse {
	return &VodResponse{Code: 1, Msg: "数据列表", Page: 1, PageCount: 1, Limit: 20, Total: len(list), List: list}
}
