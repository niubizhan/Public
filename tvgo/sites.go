package main

// 站点适配器接口。三个源站的 HTML 结构各不相同，各自实现同一组方法。

// Result 列表类接口的统一返回。
type Result struct {
	List      []V
	Page      int
	PageCount int
	Total     int
}

// Site 一个可接入 TVBox 的源站。
type Site interface {
	Key() string
	Name() string
	Base() string
	Home() ([]Class, []V, error)
	Category(typeID string, page int) (Result, error)
	Search(keyword string, page int) (Result, error)
	Detail(id string) (V, error)
	Play(token, flag string) (string, error)
}

var allSites = []Site{
	&fofoSite{},
	&piaoSite{},
	&xcSite{},
}

func getSite(key string) Site {
	for _, s := range allSites {
		if s.Key() == key {
			return s
		}
	}
	return nil
}

// siteType 站点内置分类（固定几个大类，源站不提供可筛选的二级分类）。
type siteType struct {
	ID   string
	Name string
}

func typesToClasses(ts []siteType) []Class {
	out := make([]Class, 0, len(ts))
	for _, t := range ts {
		out = append(out, Class{ID: t.ID, Name: t.Name})
	}
	return out
}
