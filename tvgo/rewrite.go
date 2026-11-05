package main

import (
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"
)

var uriAttrRe = regexp.MustCompile(`URI="([^"]+)"`)

func newJSONEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	// 片名里的 & < > 不必转义成 \u0026，保持可读
	enc.SetEscapeHTML(false)
	return enc
}

// resolveURL 等价于 JS 的 new URL(u, base)。
func resolveURL(base, raw string) string {
	if raw == "" {
		return ""
	}
	if isHTTPURL(raw) {
		return raw
	}
	bu, err := url.Parse(base)
	if err != nil {
		return raw
	}
	ru, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return bu.ResolveReference(ru).String()
}

// rewritePics 把封面图换成 /img 代理地址。
//
// 三个源用了十多个图床，其中不少是灰产 CDN：电视（Android 4.4）直连会碰到
// DNS 污染、证书链不兼容、防盗链 403，表现就是列表一片首字色块。
// 服务端能取到，所以统一代取后回传给电视，顺带把相对路径补成绝对地址。
func rewritePics(list []V, origin, siteBase string) []V {
	for _, v := range list {
		for _, field := range []string{"vod_pic", "vod_pic_thumb", "vod_pic_slide"} {
			raw := strings.TrimSpace(v.s(field))
			if raw == "" {
				continue
			}
			abs := resolveURL(siteBase+"/", raw)
			if abs == "" {
				continue
			}
			v[field] = origin + "/img?u=" + encURI(abs)
		}
	}
	return list
}

// playURLFor 把播放 token 包装成本机可播的 m3u8 直链（type=1 只认这种）。
func playURLFor(origin, siteKey, token, flag string) string {
	return origin + "/v/" + siteKey + "/index.m3u8?t=" + encURI(token) + "&f=" + encURI(flag)
}

// rewritePlayURLs 把 vod_play_url 里的 token 换成 /v/{site}/index.m3u8 直链。
// 格式：线路1名$token#线路1集2$token$$$线路2名$token...
func rewritePlayURLs(v V, origin, siteKey string) {
	froms := strings.Split(v.s("vod_play_from"), "$$$")
	groups := strings.Split(v.s("vod_play_url"), "$$$")

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
			name := ep[:idx]
			token := ep[idx+1:]
			if token == "" {
				continue
			}
			eps[n] = name + "$" + playURLFor(origin, siteKey, token, flag)
		}
		out = append(out, strings.Join(eps, "#"))
	}
	v["vod_play_url"] = strings.Join(out, "$$$")
}
