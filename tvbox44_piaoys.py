#coding=utf-8
# 本文件由 tools/build_pys.py 生成（源：tools/tvbox44_template.py），请勿直接修改。
"""
TVBox 4.4 内置 Python 爬虫模板 —— 零服务器、设备本机抓取。

本文件是模板：SITE 常量为占位符，由 tools/build_pys.py 生成三个独立站点文件
（tvbox44_fofoys.py / tvbox44_piaoys.py / tvbox44_xcyycn.py）。
必须分文件的原因：TVBox 的 base.spider.Spider 是单例（__new__ 复用实例），
同一个 .py 被三个站点共用时 self.extend 会互相覆盖。

依赖：仅用框架自带的 base.spider（requests + lxml）与标准库 re/json。
"""
from base.spider import Spider as BaseSpider
import re
import json
import time
from urllib.parse import quote as _urlquote

SITE = "piaoys"

UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
# 注意：Accept 里不能带 application/json —— 飘花影视会因此返回 JSON 转义过的 HTML
ACCEPT = ("text/html,application/xhtml+xml,application/xml;q=0.9,"
          "image/avif,image/webp,*/*;q=0.8")

FOFOYS_TYPES = [("tvseries", "电视剧"), ("film", "电影"), ("varietyshow", "综艺"),
                ("anime", "动漫"), ("shortplay", "短剧")]
PIAOYS_TYPES = [("1", "电影"), ("2", "电视剧"), ("3", "动漫"), ("4", "综艺"), ("27", "短剧")]
XCYYCN_TYPES = [("1", "电影"), ("2", "电视剧"), ("3", "综艺"), ("4", "动漫"), ("5", "短剧")]

FF_BASE = "https://www.fofoys.com"
PY_BASE = "https://www.piaoys.com"
XY_BASE = "https://www.xcyycn.cc"

FF_RE = re.compile(r'<a[^>]*href="/(tvseries|film|anime|varietyshow|shortplay)/(\d+)"[^>]*>')


# ---------------- 通用工具（与 src/util.js 同一套逻辑） ----------------

def rx(pat, s, group=1, default=""):
    m = re.search(pat, s)
    return m.group(group) if m else default


def decode_entities(s):
    def _hex(m):
        try:
            return chr(int(m.group(1), 16))
        except Exception:
            return ""
    def _dec(m):
        try:
            return chr(int(m.group(1)))
        except Exception:
            return ""
    s = re.sub(r"&#x([0-9a-fA-F]+);", _hex, s)
    s = re.sub(r"&#(\d+);", _dec, s)
    for k, v in (("&nbsp;", " "), ("&amp;", "&"), ("&quot;", '"'), ("&apos;", "'"),
                 ("&#39;", "'"), ("&lt;", "<"), ("&gt;", ">")):
        s = s.replace(k, v)
    return s


def strip_tags(s):
    if not s:
        return ""
    s = re.sub(r"<[^>]*>", "", str(s))
    return re.sub(r"\s+", " ", decode_entities(s)).strip()


def attr(tag, name):
    if not tag:
        return ""
    return rx(r"(?<![\w-])" + re.escape(name) + r'\s*=\s*"([^"]*)"', tag)


def js_literal_to_json(text):
    """把 JS 单引号对象字面量转成 JSON 文本（纯文本替换，不执行代码）"""
    s = re.sub(r"([{,]\s*)([A-Za-z_$][A-Za-z0-9_$]*)(\s*:)", r'\1"\2"\3', text)

    def _q(m):
        inner = (m.group(1).replace("\\'", "'").replace('\\"', '"')
                 .replace("\\n", "\n").replace("\\t", "\t").replace("\\\\", "\\"))
        return '"' + inner.replace('"', '\\"') + '"'

    return re.sub(r"'((?:[^'\\]|\\.)*)'", _q, s)


def extract_balanced(src, from_index):
    """从 from_index 起找 '{'，做字符串感知的括号配平"""
    start = src.find("{", from_index)
    if start < 0:
        return None
    depth, quote, esc = 0, None, False
    for i in range(start, len(src)):
        c = src[i]
        if quote:
            if esc:
                esc = False
                continue
            if c == "\\":
                esc = True
                continue
            if c == quote:
                quote = None
            continue
        if c in ('"', "'"):
            quote = c
            continue
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return src[start:i + 1]
    return None


def find_quoted_var(html, name):
    m = re.search(r"var\s+" + re.escape(name) + r"\s*=\s*['\"]((?:[^'\"\\]|\\.)*)['\"]", html)
    if not m:
        return ""
    s = m.group(1)
    for a, b in (("\\'", "'"), ('\\"', '"'), ("\\n", "\n"), ("\\t", "\t"), ("\\\\", "\\")):
        s = s.replace(a, b)
    return s


def empty_list(page=1):
    return {"code": 1, "msg": "数据列表", "page": page, "pagecount": 1,
            "limit": "20", "total": 0, "list": []}


class Spider(BaseSpider):

    # ---------------- 框架契约 ----------------

    def init(self, extend=""):
        self.site = SITE if SITE in ("fofoys", "piaoys", "xcyycn") else "fofoys"

    def getName(self):
        return {"fofoys": "fofo影视", "piaoys": "飘花影视", "xcyycn": "星辰影院"}[self.site]

    def getDependence(self):
        return []

    def action(self, action):
        return ""

    def destroy(self):
        pass

    # ---------------- HTTP ----------------

    def _headers(self, referer=None):
        h = {"User-Agent": UA, "Accept": ACCEPT, "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8"}
        if referer:
            h["Referer"] = referer
        return h

    def _get(self, url, referer=None, timeout=25, retries=3):
        # verify=False：老设备证书库过期/不全时仍能抓取
        # retries：源站偶发读超时（尤其被高频访问时）自动重试
        last = None
        for i in range(max(1, retries)):
            if i:
                time.sleep(1.2 * i)
            try:
                rsp = self.fetch(url, headers=self._headers(referer), timeout=timeout, verify=False)
                return rsp.text or ""
            except Exception as e:
                last = e
        raise last

    def _post(self, url, data, referer=None, timeout=25, retries=3):
        last = None
        for i in range(max(1, retries)):
            if i:
                time.sleep(1.2 * i)
            try:
                rsp = self.post(url, data=data, headers=self._headers(referer), timeout=timeout, verify=False)
                return rsp.text or ""
            except Exception as e:
                last = e
        raise last

    # ---------------- 首页 / 分类 / 搜索 ----------------

    def homeContent(self, filter):
        try:
            if self.site == "fofoys":
                html = self._get(FF_BASE + "/", timeout=20)
                items = self._ff_cards(html)
                classes = FOFOYS_TYPES
            elif self.site == "piaoys":
                html = self._get(PY_BASE + "/", timeout=20)
                items = self._py_cards(html)
                classes = PIAOYS_TYPES
            else:
                html = self._get(XY_BASE + "/", timeout=20)
                items = self._xy_grid(html) + self._xy_hero(html)
                classes = XCYYCN_TYPES
            return {"class": [{"type_id": t, "type_name": n, "type_flag": ""} for t, n in classes],
                    "list": items}
        except Exception as e:
            self.log("homeContent error: %s" % e)
            return {"class": [{"type_id": t, "type_name": n, "type_flag": ""} for t, n in self._types()],
                    "list": []}

    def homeVideoContent(self):
        return {"list": self.homeContent(False).get("list", [])}

    def categoryContent(self, tid, pg, filter, extend):
        page = int(pg) if str(pg).isdigit() and int(pg) > 0 else 1
        try:
            if self.site == "fofoys":
                if page > 1:
                    return empty_list(page)
                types = dict(FOFOYS_TYPES)
                html = self._get("%s/type/%s" % (FF_BASE, tid), timeout=20)
                items = self._ff_cards(html)
            elif self.site == "piaoys":
                if page > 1:
                    return empty_list(page)
                html = self._get("%s/sort/%s.html" % (PY_BASE, tid), timeout=20)
                items = self._py_cards(html)
            else:
                url = ("%s/v/%s.html" % (XY_BASE, tid)) if page == 1 else ("%s/v/%s-%d.html" % (XY_BASE, tid, page))
                html = self._get(url, timeout=20)
                items = self._xy_grid(html)
            return {"code": 1, "msg": "数据列表", "page": page, "pagecount": 1,
                    "limit": "20", "total": len(items), "list": items}
        except Exception as e:
            self.log("categoryContent error: %s" % e)
            return empty_list(page)

    def searchContent(self, key, quick):
        try:
            kw = str(key or "").strip()
            if not kw:
                return empty_list(1)
            if self.site == "fofoys":
                html = self._get("%s/search?q=%s" % (FF_BASE, self._uq(kw)), timeout=20)
                items = self._ff_cards(html)
            elif self.site == "piaoys":
                html = self._get("%s/sou/-------------.html?wd=%s" % (PY_BASE, self._uq(kw)), timeout=20)
                items = self._py_cards(html)
            else:
                html = self._get("%s/s.html?wd=%s" % (XY_BASE, self._uq(kw)), timeout=20)
                items = self._xy_grid(html) + self._xy_hero(html)
            # 去重（网格 + 轮播可能重复）
            seen, out = set(), []
            for it in items:
                if it.get("vod_id") in seen:
                    continue
                seen.add(it.get("vod_id"))
                out.append(it)
            return {"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
                    "limit": "20", "total": len(out), "list": out}
        except Exception as e:
            self.log("searchContent error: %s" % e)
            return empty_list(1)

    def detailContent(self, ids):
        vid = ids[0] if isinstance(ids, list) and ids else str(ids or "")
        try:
            if self.site == "fofoys":
                vod = self._ff_detail(vid)
            elif self.site == "piaoys":
                vod = self._py_detail(vid)
            else:
                vod = self._xy_detail(vid)
            return {"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
                    "limit": "20", "total": 1, "list": [vod]}
        except Exception as e:
            self.log("detailContent error: %s" % e)
            return empty_list(1)

    def playerContent(self, flag, id, vipFlags):
        token = str(id or "")
        try:
            if token.startswith("ff:"):
                sid = token[3:]
                if not sid.isdigit():
                    raise ValueError("bad token")
                url = self._post(FF_BASE + "/source/", {"id": sid},
                                 referer=FF_BASE + "/", timeout=20).strip().strip('"\'')
            elif token.startswith("py:"):
                path = token[3:]
                if not re.match(r"^play/\d+-\d+-\d+\.html$", path):
                    raise ValueError("bad token")
                html = self._get(PY_BASE + "/" + path, referer=PY_BASE + "/", timeout=25)
                url = self._player_url(html, "var player_data")
            elif token.startswith("xc:"):
                path = token[3:]
                if not re.match(r"^p/\d+-\d+-\d+\.html$", path):
                    raise ValueError("bad token")
                html = self._get(XY_BASE + "/" + path, referer=XY_BASE + "/", timeout=25)
                url = self._player_url(html, "var player_aaaa")
            else:
                raise ValueError("unknown token")
            url = url.replace("\\/", "/")
            if not url.startswith("http"):
                raise ValueError("play resolve failed")
            base = {"fofoys": FF_BASE, "piaoys": PY_BASE, "xcyycn": XY_BASE}[self.site]
            return {"url": url, "parse": 0, "jx": 0, "flag": flag,
                    "header": {"User-Agent": UA, "Referer": base + "/"}}
        except Exception as e:
            self.log("playerContent error: %s" % e)
            return {"url": "", "parse": 0, "flag": flag, "header": {}}

    # ---------------- 站点：fofo影视 ----------------

    def _ff_cards(self, html):
        matches = list(FF_RE.finditer(html))
        out, seen = [], set()
        for i, m in enumerate(matches):
            start = m.end()
            end = matches[i + 1].start() if i + 1 < len(matches) else min(len(html), start + 6000)
            block = html[start:end]
            img = re.search(r'<img[^>]*alt="(?:video|vod)"[^>]*>', block)
            if not img:
                continue
            pic = attr(img.group(0), "src")
            if not pic:
                continue
            title = rx(r'<div class="topic-details-title">([\s\S]*?)</div>', block)
            if not title:
                for sm in re.finditer(r'<span[^>]*class="([^"]*)"[^>]*>([\s\S]*?)</span>', block):
                    if "font-bold" in sm.group(1):
                        title = sm.group(2)
                        break
            if not title:
                continue
            sub = rx(r'<div class="topic-details-title-sub[^"]*">([\s\S]*?)</div>', block)
            vid = "%s/%s" % (m.group(1), m.group(2))
            if vid in seen:
                continue
            seen.add(vid)
            out.append({"vod_id": vid, "type_id": m.group(1),
                        "type_name": dict(FOFOYS_TYPES).get(m.group(1), ""),
                        "vod_name": strip_tags(title), "vod_pic": pic,
                        "vod_remarks": strip_tags(sub), "vod_blurb": "", "vod_content": ""})
        return out

    def _ff_detail(self, vid):
        html = self._get("%s/%s" % (FF_BASE, vid), referer=FF_BASE + "/", timeout=25)
        v = lambda n: find_quoted_var(html, n)
        mtype = v("mtype")
        vod = {
            "vod_id": vid, "type_id": mtype, "type_name": dict(FOFOYS_TYPES).get(mtype, ""),
            "vod_name": v("title"), "vod_pic": v("pic"), "vod_remarks": v("note"),
            "vod_year": v("year"), "vod_area": v("area"), "vod_class": v("label"),
            "vod_actor": v("actor"), "vod_director": v("director"),
            "vod_content": v("description"), "vod_blurb": v("description"),
            "vod_lang": "", "vod_score": "", "vod_time": "",
        }
        idx = html.find("var urlList")
        data = None
        if idx >= 0:
            lit = extract_balanced(html, idx)
            if lit:
                try:
                    data = json.loads(js_literal_to_json(lit))
                except Exception:
                    data = None
        if isinstance(data, dict) and isinstance(data.get("source"), list) and isinstance(data.get("url_list"), list):
            froms, urls = [], []
            for i, name in enumerate(data["source"]):
                if i >= len(data["url_list"]):
                    break
                items = data["url_list"][i] or []
                if not items:
                    continue
                parts = []
                for n, it in enumerate(items):
                    sid = it.get("sid")
                    if sid is None:
                        continue
                    nm = it.get("title") or ("第%d集" % (int(it.get("episode", n)) + 1))
                    parts.append("%s$ff:%s" % (nm, sid))
                if not parts:
                    continue
                froms.append(str(name))
                urls.append("#".join(parts))
            if froms:
                vod["vod_play_from"] = "$$$".join(froms)
                vod["vod_play_url"] = "$$$".join(urls)
        return vod

    # ---------------- 站点：飘花影视 ----------------

    def _py_cards(self, html):
        out, seen = [], set()
        for chunk in re.split(r'<div class="stui-vodlist__box"', html)[1:]:
            href = rx(r'href="(/html/\d+\.html)"', chunk)
            if not href:
                continue
            vid = rx(r"/html/(\d+)\.html", href)
            if vid in seen:
                continue
            title = rx(r'title="([^"]*)"', chunk)
            pic = rx(r'data-original="([^"]*)"', chunk)
            if not title or not pic:
                continue
            seen.add(vid)
            remark = rx(r'<span class="pic-text[^"]*">([\s\S]*?)</span>', chunk)
            out.append({"vod_id": vid, "type_id": "", "type_name": "",
                        "vod_name": strip_tags(title), "vod_pic": pic,
                        "vod_remarks": strip_tags(remark), "vod_blurb": "", "vod_content": ""})
        return out

    def _py_detail(self, vid):
        html = self._get("%s/html/%s.html" % (PY_BASE, vid), referer=PY_BASE + "/", timeout=25)

        def field(label):
            return strip_tags(rx(r'<p class="data"><span class="data2">%s：</span>([\s\S]*?)</p>' % label, html))

        title = rx(r'<h1 class="title">([^<]+)</h1>', html)
        pic = rx(r'<div class="stui-content__thumb"[\s\S]{0,600}?data-original="([^"]+)"', html)
        desc = strip_tags(rx(r'<span class="detail-content"[^>]*>([\s\S]*?)</span>', html))
        vod = {
            "vod_id": vid, "type_id": "", "type_name": field("类型"),
            "vod_name": strip_tags(title), "vod_pic": pic, "vod_remarks": field("状态"),
            "vod_year": field("年份"), "vod_area": field("地区"), "vod_class": field("类型"),
            "vod_actor": field("主演"), "vod_director": field("导演"), "vod_time": field("更新"),
            "vod_content": desc, "vod_blurb": desc,
        }
        froms, urls, used = [], [], set()
        for m in re.finditer(r'<ul class="stui-content__playlist[^"]*">([\s\S]*?)</ul>', html):
            before = html[max(0, m.start() - 1200):m.start()]
            srcs = re.findall(r'<span class="pull-right1">([^<]*)</span>', before)
            name = strip_tags(srcs[-1]) if srcs else ""
            if not name:
                name = "线路%d" % (len(froms) + 1)
            if name in used:
                name = "%s%d" % (name, len(froms) + 1)
            used.add(name)
            eps = re.findall(r'<a href="/play/(\d+)-(\d+)-(\d+)\.html">([^<]*)</a>', m.group(1))
            if not eps:
                continue
            froms.append(name)
            urls.append("#".join("%s$py:play/%s-%s-%s.html" % (strip_tags(e[3]), e[0], e[1], e[2]) for e in eps))
        if froms:
            vod["vod_play_from"] = "$$$".join(froms)
            vod["vod_play_url"] = "$$$".join(urls)
        return vod

    # ---------------- 站点：星辰影院 ----------------

    def _xy_grid(self, html):
        out = []
        for m in re.finditer(r'<a([^>]*class="public-list-exp"[^>]*)>([\s\S]*?)</a>', html):
            attrs, inner = m.group(1), m.group(2)
            href = rx(r'href="(/d-\d+\.html)"', attrs)
            if not href:
                continue
            vid = rx(r"/d-(\d+)\.html", href)
            pic = rx(r'data-src="([^"]+)"', inner) or rx(r'(?<!data-)src="([^"]+)"', inner)
            name = rx(r'title="([^"]*)"', attrs)
            if not name:
                alt = rx(r'alt="([^"]*)"', inner)
                if alt:
                    name = alt.replace("封面图", "")
            if not name:
                tail = html[m.end():m.end() + 500]
                name = rx(r'<div class="thumb-txt[^"]*">([\s\S]*?)</div>', tail)
            remark = rx(r'<span class="public-list-prb[^"]*">([\s\S]*?)</span>', inner)
            tname = rx(r'<span class="public-prt[^"]*">([\s\S]*?)</span>', inner)
            out.append({"vod_id": vid, "type_id": "", "type_name": strip_tags(tname),
                        "vod_name": strip_tags(name),
                        "vod_pic": (XY_BASE + pic) if pic.startswith("/") else pic,
                        "vod_remarks": strip_tags(remark), "vod_blurb": "", "vod_content": ""})
        return out

    def _xy_hero(self, html):
        out = []
        pat = (r'<a href="(/d-\d+\.html)"[\s\S]{0,600}?background-image:\s*url\(\'([^\']+)\'\)'
               r'[\s\S]{0,900}?<h3 class="slide-info-title[^"]*">([\s\S]*?)</h3>')
        for m in re.finditer(pat, html):
            vid = rx(r"/d-(\d+)\.html", m.group(1))
            tail = html[m.start():m.start() + len(m.group(0)) + 400]
            remark = rx(r'<span class="slide-info-remarks">([\s\S]*?)</span>', tail)
            pic = m.group(2)
            out.append({"vod_id": vid, "type_id": "", "type_name": "",
                        "vod_name": strip_tags(m.group(3)),
                        "vod_pic": (XY_BASE + pic) if pic.startswith("/") else pic,
                        "vod_remarks": strip_tags(remark), "vod_blurb": "", "vod_content": ""})
        return out

    def _xy_detail(self, vid):
        html = self._get("%s/d-%s.html" % (XY_BASE, vid), referer=XY_BASE + "/", timeout=25)
        title = rx(r'<h3 class="slide-info-title[^"]*">([\s\S]*?)</h3>', html) or \
            rx(r"<title>[^《]*《([^》]+)》", html)
        pic = rx(r'(?:data-src|src)="(https?://[^"]*/upload/vod/[^"]+)"', html)
        desc = strip_tags(rx(r'id="height_limit"[^>]*>([\s\S]*?)</div>', html))
        remark = rx(r'<span class="slide-info-remarks">([\s\S]*?)</span>', html)
        fields = {}
        for m in re.finditer(r"<li><em>([^<]+)：</em>([\s\S]*?)</li>", html):
            fields[strip_tags(m.group(1))] = strip_tags(m.group(2))
        if not desc:
            desc = fields.get("简介", "")
        vod = {
            "vod_id": vid, "type_id": "", "type_name": fields.get("类型", ""),
            "vod_name": strip_tags(title), "vod_pic": pic,
            "vod_remarks": strip_tags(remark) or fields.get("更新", ""),
            "vod_year": rx(r"/s/year/(\d{4})\.html", html), "vod_area": "",
            "vod_class": fields.get("类型", ""), "vod_actor": "", "vod_director": "",
            "vod_lang": fields.get("语言", ""), "vod_time": fields.get("更新", ""),
            "vod_content": desc, "vod_blurb": desc,
        }
        tabs = []
        tab = rx(r'<div class="anthology-tab[^"]*">([\s\S]*?)</div>', html)
        if tab:
            for m in re.finditer(r'<a class="swiper-slide"><i class="fa[^"]*"></i>&nbsp;([^<]+)</a>', tab):
                nm = strip_tags(m.group(1))
                if nm and "下载" not in nm:
                    tabs.append(nm)
        froms, urls, used = [], [], set()
        boxes = list(re.finditer(r"<div class=['\"]anthology-list-box[^'\"]*['\"]>([\s\S]*?)</div>", html))
        for i, box in enumerate(boxes):
            eps = re.findall(r'<a[^>]*href="(/p/\d+-\d+-\d+\.html)"[^>]*>([^<]*)</a>', box.group(1))
            if not eps:
                continue
            name = tabs[i] if i < len(tabs) else "线路%d" % (len(froms) + 1)
            if name in used:
                name = "%s%d" % (name, len(froms) + 1)
            used.add(name)
            froms.append(name)
            urls.append("#".join("%s$xc:%s" % (strip_tags(e[1]), e[0].lstrip("/")) for e in eps))
        if froms:
            vod["vod_play_from"] = "$$$".join(froms)
            vod["vod_play_url"] = "$$$".join(urls)
        return vod

    # ---------------- 小工具 ----------------

    @staticmethod
    def _uq(kw):
        return _urlquote(str(kw), safe="")

    @staticmethod
    def _player_url(html, marker):
        idx = html.find(marker)
        if idx >= 0:
            lit = extract_balanced(html, idx)
            if lit:
                try:
                    u = json.loads(lit).get("url", "")
                    if u:
                        return u
                except Exception:
                    pass
        return rx(r'"url"\s*:\s*"([^"]+)"', html)

    def _types(self):
        return {"fofoys": FOFOYS_TYPES, "piaoys": PIAOYS_TYPES, "xcyycn": XCYYCN_TYPES}[self.site]
