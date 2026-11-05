package main

// TVBox 4.4 (type=1 / maccms) JSON 信封。
//
// 规范来源：TVBoxOS-main
//   bean/AbsJson.java（AbsJsonVod 只有 group_id、type_id_1 两个 int 字段）
//   viewmodel/SourceViewModel.java
//
// 两条硬性约束（血泪教训，别改）：
//   1. 绝对不能输出 action 键（哪怕空串）—— GridFragment 的点击回调
//      `if (video.action != null) { action(); return; }` 会命中，
//      详情页打不开，表现为「按确定没反应」。
//   2. group_id / type_id_1 必须是数字 —— 空串会让 Gson 抛
//      NumberFormatException，整个首页 JSON 报废，只剩「主页」标签。

// V 是站点适配器返回的中间结构（字段不固定，各站不同）。
type V map[string]interface{}

func (v V) s(key string) string {
	if x, ok := v[key]; ok {
		switch t := x.(type) {
		case string:
			return t
		case nil:
			return ""
		default:
			return jsonNum(t)
		}
	}
	return ""
}

func (v V) set(key, val string) {
	if val != "" {
		v[key] = val
	}
}

// vodDefaults 对应 JS 版 VOD_DEFAULTS，保证输出字段完整（TVBox 对缺字段的容错不一致）。
var vodDefaults = map[string]string{
	"vod_id": "", "type_id": "", "type_name": "", "vod_name": "", "vod_sub": "",
	"vod_en": "", "vod_pic": "", "vod_pic_thumb": "", "vod_pic_slide": "",
	"vod_pic_screenshot": "", "vod_actor": "", "vod_director": "", "vod_writer": "",
	"vod_behind": "", "vod_blurb": "", "vod_content": "", "vod_remarks": "",
	"vod_pubdate": "", "vod_total": "", "vod_serial": "", "vod_area": "", "vod_lang": "",
	"vod_year": "", "vod_version": "", "vod_state": "", "vod_tag": "", "vod_class": "",
	"vod_score": "", "vod_time": "", "vod_hits": "", "vod_duration": "",
	"vod_isend": "0", "vod_lock": "0", "vod_level": "0", "vod_copyright": "0",
	"vod_points": "0", "vod_points_play": "0", "vod_points_down": "0",
	"vod_up": "0", "vod_down": "0", "vod_trysee": "0", "vod_douban_score": "",
	"vod_pwd": "", "vod_pwd_url": "", "vod_pwd_play": "", "vod_pwd_play_url": "",
	"vod_pwd_down": "", "vod_pwd_down_url": "", "vod_jumpurl": "",
	"vod_play_from": "", "vod_play_server": "no", "vod_play_note": "", "vod_play_url": "",
	"vod_down_from": "", "vod_down_server": "", "vod_down_note": "", "vod_down_url": "",
	"vod_plot": "0", "vod_plot_name": "", "vod_plot_detail": "",
}

// normalizeVod 补全字段；int 字段转数字；确保 action 缺席。
func normalizeVod(v V) map[string]interface{} {
	out := make(map[string]interface{}, len(vodDefaults)+4)
	for k, def := range vodDefaults {
		out[k] = def
	}
	for k, val := range v {
		if val == nil || k == "action" {
			continue
		}
		if s, ok := val.(string); ok {
			out[k] = s
		} else {
			out[k] = jsonNum(val)
		}
	}
	out["type_id_1"] = toInt(out["type_id_1"])
	out["group_id"] = toInt(out["group_id"])
	delete(out, "action")
	return out
}

func toInt(v interface{}) int {
	s, _ := v.(string)
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && (c == '-' || c == '+') {
			neg = c == '-'
			continue
		}
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}

func normalizeList(list []V) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for _, v := range list {
		out = append(out, normalizeVod(v))
	}
	return out
}

func classList(classes []Class) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(classes))
	for _, c := range classes {
		out = append(out, map[string]interface{}{
			"type_id":   c.ID,
			"type_name": c.Name,
			"type_flag": "",
		})
	}
	return out
}

// listEnvelope 分类 / 搜索 / 详情的响应体（AbsJson）。
func listEnvelope(list []V, page, pagecount, total int) map[string]interface{} {
	if page < 1 {
		page = 1
	}
	if pagecount < 1 {
		pagecount = 1
	}
	vods := normalizeList(list)
	if total < 0 {
		total = len(vods)
	}
	return map[string]interface{}{
		"code":      1,
		"msg":       "数据列表",
		"page":      page,
		"pagecount": pagecount,
		"limit":     "20",
		"total":     total,
		"list":      vods,
	}
}

// homeListEnvelope type=1 首页：detail 响应里同时带 class（分类导航）和 list（推荐位）。
// 缺 class 时首页只剩「主页」一个标签，分类点不进去。
func homeListEnvelope(classes []Class, list []V) map[string]interface{} {
	env := listEnvelope(list, 1, 1, len(list))
	env["class"] = classList(classes)
	return env
}

// homeEnvelope AbsSortJson 形态（class + list，无 code/page）。
func homeEnvelope(classes []Class, list []V) map[string]interface{} {
	return map[string]interface{}{
		"class": classList(classes),
		"list":  normalizeList(list),
	}
}

// playEnvelope 播放解析响应。
func playEnvelope(u string, header map[string]string, flag string) map[string]interface{} {
	if header == nil {
		header = map[string]string{}
	}
	return map[string]interface{}{
		"url":     u,
		"parse":   0,
		"jx":      0,
		"playUrl": "",
		"header":  header,
		"flag":    flag,
	}
}

func emptyList(page int) map[string]interface{} {
	return listEnvelope(nil, page, 1, 0)
}

func withError(env map[string]interface{}, err error) map[string]interface{} {
	if err != nil {
		env["msg"] = "error: " + err.Error()
	}
	return env
}

// Class 分类导航项。
type Class struct {
	ID   string
	Name string
}
