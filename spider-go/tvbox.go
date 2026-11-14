package main

// TVBox 4.4 (type=1 / maccms) JSON 信封。
// 规范来源：TVBoxOS-main / bean/AbsJson.java / viewmodel/SourceViewModel.java
//
// 三条硬性约束（旧项目实机踩过的坑）：
//  1. 绝对不能输出 action 键（哪怕空串）——GridFragment 点击回调
//     `if (video.action != null) { action(); return; }` 会命中，详情页打不开；
//  2. group_id / type_id_1 必须是数字，空串会让 Gson 抛 NumberFormatException，
//     整个首页 JSON 报废，只剩「主页」标签；
//  3. AbsJson.limit 是 String，输出 "20" 而不是 20。

import (
	"encoding/json"
	"io"
	"strings"
)

const appVersion = "2.0.0-goquery"

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

// normalizeVod 把 VodItem 补全成 TVBox 要求的完整字段集，并剔除 action。
func normalizeVod(item VodItem) map[string]interface{} {
	out := make(map[string]interface{}, len(vodDefaults)+4)
	for k, def := range vodDefaults {
		out[k] = def
	}
	// 用 json 往返把结构体字段摊平，omitempty 的空字段不覆盖默认值
	b, _ := json.Marshal(item)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	for k, v := range m {
		if k == "action" || v == nil {
			continue
		}
		out[k] = v
	}
	out["group_id"] = 0
	out["type_id_1"] = 0
	delete(out, "action")
	return out
}

func normalizeList(list []VodItem) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for _, v := range list {
		out = append(out, normalizeVod(v))
	}
	return out
}

func classList(cs []Category) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(cs))
	for _, c := range cs {
		out = append(out, map[string]interface{}{
			"type_id": c.TypeID, "type_name": c.TypeName, "type_flag": "",
		})
	}
	return out
}

// listEnvelope 分类 / 搜索 / 详情响应（AbsJson）。
func listEnvelope(list []VodItem, page, pagecount, total int) map[string]interface{} {
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
		"code": 1, "msg": "数据列表", "page": page, "pagecount": pagecount,
		"limit": "20", "total": total, "list": vods,
	}
}

// homeListEnvelope type=1 首页：detail 响应里同时带 class 与 list。
func homeListEnvelope(cs []Category, list []VodItem) map[string]interface{} {
	env := listEnvelope(list, 1, 1, len(list))
	env["class"] = classList(cs)
	return env
}

func playEnvelope(u, flag string) map[string]interface{} {
	return map[string]interface{}{
		"url": u, "parse": 0, "jx": 0, "playUrl": "",
		"header": map[string]string{"User-Agent": UA, "Referer": ""},
		"flag":   flag,
	}
}

func withError(env map[string]interface{}, err error) map[string]interface{} {
	if err != nil {
		env["msg"] = "error: " + err.Error()
	}
	return env
}

func writeJSON(w io.Writer, data interface{}) {
	// 用 Encoder 而非 html.Escape：中文片名里的 & < > 不该变成 \u0026
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(data)
}

// rewritePics 封面图统一走 /img 代理（电视直连灰产图床常遇证书/防盗链失败）。
func rewritePics(list []VodItem, origin, siteBase string) []VodItem {
	for i := range list {
		it := &list[i]
		for _, p := range []*string{&it.VodPic} {
			raw := strings.TrimSpace(*p)
			if raw == "" {
				continue
			}
			abs := resolveURL(siteBase+"/", raw)
			if abs != "" {
				*p = origin + "/img?u=" + encURI(abs)
			}
		}
	}
	return list
}

// playURLFor 把播放 token 包装成本机 m3u8 直链（带 .m3u8 才命中 isVideoFormat）。
func playURLFor(origin, siteKey, token, flag string) string {
	return origin + "/v/" + siteKey + "/index.m3u8?t=" + encURI(token) + "&f=" + encURI(flag)
}

// rewritePlayURLs 把 vod_play_url 里的 token 换成本机可播直链。
func rewritePlayURLs(item *VodItem, origin, siteKey string) {
	froms := strings.Split(item.VodPlayFrom, "$$$")
	groups := strings.Split(item.VodPlayURL, "$$$")
	out := make([]string, 0, len(groups))
	for i, group := range groups {
		flag := ""
		if i < len(froms) {
			flag = froms[i]
		}
		eps := strings.Split(group, "#")
		for n, ep := range eps {
			idx := strings.Index(ep, "$")
			if idx < 0 {
				continue
			}
			name, token := ep[:idx], ep[idx+1:]
			if token == "" {
				continue
			}
			eps[n] = name + "$" + playURLFor(origin, siteKey, token, flag)
		}
		out = append(out, strings.Join(eps, "#"))
	}
	item.VodPlayURL = strings.Join(out, "$$$")
}

// buildConfig TVBox 接口地址填 http://127.0.0.1:8787/ 时拿到的配置。
// 不能输出 "lives": [] —— ApiConfig.parseJson 见到空 lives 会 IndexOutOfBounds。
func buildConfig(origin string) map[string]interface{} {
	sites := make([]map[string]interface{}, 0, len(allSpiders))
	for _, s := range allSpiders {
		sites = append(sites, map[string]interface{}{
			"key": s.Key(), "name": s.Name(), "type": 1,
			"api": origin + "/api/" + s.Key(),
			"searchable": 1, "quickSearch": 1, "filterable": 0, "changeable": 1,
			"timeout": 20, "playerType": cfgPlayerType(), "style": "",
		})
	}
	return map[string]interface{}{
		"spider": "", "wallpaper": "", "sites": sites,
		"parses": []interface{}{}, "flags": []interface{}{}, "ads": []interface{}{},
	}
}
