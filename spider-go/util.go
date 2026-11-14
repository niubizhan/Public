package main

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
)

var (
	tagRe    = regexp.MustCompile(`<[^>]*>`)
	wsRe     = regexp.MustCompile(`[\s\x{00A0}\x{3000}]+`)
	jsKeyRe  = regexp.MustCompile(`([{,]\s*)([A-Za-z_$][A-Za-z0-9_$]*)(\s*:)`)
	jsStrRe  = regexp.MustCompile(`'((?:[^'\\]|\\.)*)'`)
	trailCom = regexp.MustCompile(`,\s*([}\]])`)
)

// stripTags 去标签 + 实体解码 + 压缩空白。
func stripTags(s string) string {
	if s == "" {
		return ""
	}
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// extractID 从详情页链接里取 id（原稿引用但未定义）。
//
//	ixcyy: /d-173732.html -> 173732
//	fofoys: /tvseries/120662 -> tvseries/120662
func extractID(link string) string {
	link = strings.TrimSpace(link)
	if m := regexp.MustCompile(`/d-(\d+)\.html`).FindStringSubmatch(link); m != nil {
		return m[1]
	}
	if m := regexp.MustCompile(`/(tvseries|film|varietyshow|anime|shortplay)/(\d+)`).FindStringSubmatch(link); m != nil {
		return m[1] + "/" + m[2]
	}
	return ""
}

func absURL(base, u string) string {
	if u == "" {
		return ""
	}
	if isHTTPURL(u) {
		return u
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(u, "/")
}

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

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
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

func encURI(s string) string {
	return url.QueryEscape(s)
}

// pickAttr 依次尝试多个属性，返回第一个非空值。
func pickAttr(s interface{ Attr(string) (string, bool) }, names ...string) string {
	for _, n := range names {
		if v, ok := s.Attr(n); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// cssURL 从 style="background-image:url('...')" 里取地址。
var cssURLRe = regexp.MustCompile(`url\(\s*['"]?([^'")]+)['"]?\s*\)`)

func cssURL(style string) string {
	if m := cssURLRe.FindStringSubmatch(style); m != nil {
		return m[1]
	}
	return ""
}

// extractBalanced 从 src[from:] 第一个 '{' 起做括号配平（字符串内的括号不计）。
func extractBalanced(src string, from int) string {
	if from < 0 || from >= len(src) {
		return ""
	}
	start := strings.IndexByte(src[from:], '{')
	if start < 0 {
		return ""
	}
	start += from
	depth := 0
	var quote byte
	esc := false
	for i := start; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	return ""
}

// jsLiteralToJSON 把 JS 单引号对象字面量转成 JSON 文本（纯文本替换，不执行代码）。
func jsLiteralToJSON(text string) string {
	s := jsKeyRe.ReplaceAllString(text, `$1"$2"$3`)
	var b strings.Builder
	last := 0
	for _, loc := range jsStrRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(s[last:loc[0]])
		inner := unescapeJS(s[loc[2]:loc[3]])
		b.WriteString(`"`)
		b.WriteString(strings.ReplaceAll(inner, `"`, `\"`))
		b.WriteString(`"`)
		last = loc[1]
	}
	b.WriteString(s[last:])
	return trailCom.ReplaceAllString(b.String(), `$1`)
}

func unescapeJS(s string) string {
	s = strings.ReplaceAll(s, `\'`, `'`)
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\t`, "\t")
	return strings.ReplaceAll(s, `\\`, `\`)
}

// findQuotedVar 从 "var title='xxx';" 之类里取字符串值。
func findQuotedVar(page, name string) string {
	re := regexp.MustCompile(`(?:var|let|const)\s+` + regexp.QuoteMeta(name) + `\s*=\s*['"]([^'"]*)['"]`)
	if m := re.FindStringSubmatch(page); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func mustAtoi(s string) int {
	n := 0
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}
