#!/usr/bin/env python3
#coding=utf-8
"""检查三个 Python 爬虫抓到的封面图能否正常访问。

模拟 TVBox 的取图方式：只带浏览器 UA、**不带 Referer**（很多图床有防盗链，
TVBox 的图片加载器通常不发 Referer，所以这是最关键的场景）。

    python test/py_pics.py
"""
import os
import sys
import warnings
from concurrent.futures import ThreadPoolExecutor

import requests

warnings.filterwarnings("ignore")

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "test", "pyenv"))  # base.spider 替身

import importlib.util  # noqa: E402

UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
SITES = ["fofoys", "piaoys", "xcyycn"]
FIRST_TYPE = {"fofoys": "film", "piaoys": "1", "xcyycn": "1"}
KEYWORD = "庆余年"
LIMIT_PER_SOURCE = 10


def load(site):
    path = os.path.join(ROOT, "tvbox44_%s.py" % site)
    spec = importlib.util.spec_from_file_location("tvbox44_%s" % site, path)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod.Spider()


def collect(site):
    """从首页/分类/搜索/详情里收集封面地址"""
    sp = load(site)
    sp.init("")
    urls = []

    def add(items, n=LIMIT_PER_SOURCE):
        for it in (items or [])[:n]:
            p = (it or {}).get("vod_pic") or ""
            if p and p.startswith("http"):
                urls.append((p, it.get("vod_name", "")))

    try:
        add(sp.homeContent(True).get("list", []))
    except Exception as e:
        print("  home 采集失败: %s" % e)
    try:
        add(sp.categoryContent(FIRST_TYPE[site], "1", True, {}).get("list", []))
    except Exception as e:
        print("  category 采集失败: %s" % e)
    try:
        add(sp.searchContent(KEYWORD, True).get("list", []))
    except Exception as e:
        print("  search 采集失败: %s" % e)
    try:
        d = sp.detailContent([sp.categoryContent(FIRST_TYPE[site], "1", True, {})["list"][0]["vod_id"]])
        add(d.get("list", []))
    except Exception as e:
        print("  detail 采集失败: %s" % e)

    # 去重，保留顺序
    seen, out = set(), []
    for u in urls:
        if u[0] in seen:
            continue
        seen.add(u[0])
        out.append(u)
    return out


def check(item):
    url, name = item
    try:
        r = requests.get(url, headers={"User-Agent": UA}, timeout=15, verify=False, stream=True)
        ctype = (r.headers.get("Content-Type") or "").split(";")[0].strip()
        size = int(r.headers.get("Content-Length") or 0)
        head = next(r.iter_content(16), b"") if r.ok else b""
        r.close()
        if not r.ok:
            return (url, name, "HTTP %d" % r.status_code, False)
        if not ctype.startswith("image/") and not head.startswith((b"\xff\xd8", b"\x89PNG", b"RIFF", b"GIF")):
            return (url, name, "非图片 content-type=%s" % ctype, False)
        if size == 0 and len(head) < 4:
            return (url, name, "空响应", False)
        return (url, name, "%s %d+" % (ctype, size), True)
    except Exception as e:
        return (url, name, str(e)[:80], False)


def main():
    total_ok = total = 0
    for site in SITES:
        print("\n== %s ==" % site)
        pics = collect(site)
        print("  采集到 %d 个封面地址" % len(pics))
        if not pics:
            print("  FAIL 没有采集到任何封面")
            continue
        with ThreadPoolExecutor(max_workers=8) as ex:
            results = list(ex.map(check, pics))
        ok = [r for r in results if r[3]]
        bad = [r for r in results if not r[3]]
        total_ok += len(ok)
        total += len(results)
        print("  可访问 %d/%d" % (len(ok), len(results)))
        for r in ok[:3]:
            print("    OK  %s  %s" % (r[2], r[0][:90]))
        for r in bad:
            print("    BAD %s | %s | %s" % (r[1][:20], r[2], r[0][:90]))
    print("\n总结果：%d/%d 张封面可访问" % (total_ok, total))
    return 0 if total_ok == total and total > 0 else 1


if __name__ == "__main__":
    sys.exit(main())
