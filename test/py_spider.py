#!/usr/bin/env python3
#coding=utf-8
"""PC 端联调 TVBox 内置 Python 爬虫（真实访问三个源站）。

    python test/py_spider.py

覆盖 TVBox 4.4 type=3 的全部动作：homeContent / categoryContent /
searchContent / detailContent / playerContent。
"""
import os
import sys
import json
import warnings

warnings.filterwarnings("ignore")  # verify=False 的证书告警不刷屏

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "test", "pyenv"))  # 提供 base.spider 替身

import importlib.util  # noqa: E402

SITES = ["fofoys", "piaoys", "xcyycn"]
FIRST_TYPE = {"fofoys": "film", "piaoys": "1", "xcyycn": "1"}
KEYWORD = "庆余年"

passed = failed = 0


def step(name, fn):
    global passed, failed
    try:
        fn()
        passed += 1
        print("  OK   %s" % name)
    except Exception as e:
        failed += 1
        print("  FAIL %s -> %s" % (name, e))


def load(site):
    path = os.path.join(ROOT, "tvbox44_%s.py" % site)
    if not os.path.exists(path):
        raise SystemExit("缺少 %s，请先运行 python tools/build_pys.py" % path)
    name = "tvbox44_%s" % site
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod.Spider()


def first_token(vod):
    group = (vod.get("vod_play_url") or "").split("$$$")[0]
    ep = group.split("#")[0]
    return ep.split("$")[1] if "$" in ep else ""


for site in SITES:
    print("\n== %s ==" % site)
    sp = load(site)
    sp.init("")

    cat_items = []

    def _home():
        global cat_items
        r = sp.homeContent(True)
        assert isinstance(r.get("class"), list) and len(r["class"]) >= 3, "class 异常: %r" % r.get("class")
        for c in r["class"]:
            assert c.get("type_id") and c["type_id"] != "None", "type_id 异常: %r" % c
        assert r.get("list"), "首页列表为空"
        v = r["list"][0]
        assert v.get("vod_id") and v.get("vod_name") and v.get("vod_pic"), "字段缺失: %r" % v
        print("       首页 %d 分类 / %d 条，示例：%s" % (len(r["class"]), len(r["list"]), v["vod_name"]))

    step("%s homeContent" % site, _home)

    def _cat():
        global cat_items
        r = sp.categoryContent(FIRST_TYPE[site], "1", True, {})
        assert r.get("list"), "分类为空"
        cat_items = r["list"]
        print("       分类 %d 条，示例：%s" % (len(r["list"]), r["list"][0]["vod_name"]))

    step("%s categoryContent" % site, _cat)

    def _search():
        r = sp.searchContent(KEYWORD, True)
        assert r.get("list"), "搜索为空"
        print("       搜索 %d 条，示例：%s" % (len(r["list"]), r["list"][0]["vod_name"]))
        return r["list"]

    search_items = []
    def _search_wrap():
        global search_items
        search_items = _search()
    step("%s searchContent(%s)" % (site, KEYWORD), _search_wrap)

    def _detail():
        assert cat_items, "没有可用的分类数据"
        vid = cat_items[0]["vod_id"]
        r = sp.detailContent([vid])
        assert r.get("list"), "详情为空"
        vod = r["list"][0]
        assert vod.get("vod_name"), "缺片名"
        assert vod.get("vod_play_from") and vod.get("vod_play_url"), "缺播放字段"
        token = first_token(vod)
        assert token, "token 解析失败: %r" % vod.get("vod_play_url")
        p = sp.playerContent("test", token, [])
        assert p.get("url", "").startswith("http"), "播放失败: %r" % p
        print("       详情：%s | 线路 %s" % (vod["vod_name"], vod["vod_play_from"][:40]))
        print("       播放 -> %s" % p["url"][:100])

    step("%s detailContent + playerContent" % site, _detail)

    def _search_detail():
        if not search_items:
            return
        vid = search_items[0]["vod_id"]
        r = sp.detailContent([vid])
        assert r.get("list") and r["list"][0].get("vod_name"), "搜索结果详情失败"

    step("%s 搜索结果可进详情" % site, _search_detail)

print("\n结果：%d 通过，%d 失败" % (passed, failed))
sys.exit(1 if failed else 0)
