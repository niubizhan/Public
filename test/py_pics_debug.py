#!/usr/bin/env python3
#coding=utf-8
"""封面图失败原因甄别：串行 + 重试 + Referer 对比，区分「并发被重置」「防盗链」「真不可达」。"""
import warnings
from urllib.parse import urlparse

import requests

warnings.filterwarnings("ignore")

UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

# 上一轮失败样本（按域名归类）
SAMPLES = [
    ("fofoys", "https://pic.youkupic.com/upload/vod/20240521-1/7eadcbac1252251e109e5935bf14bf46.jpg"),
    ("fofoys", "https://pic.youkupic.com/upload/vod/20240516-1/e7333241aeb5458c762483b86a147787.jpg"),
    ("fofoys", "https://hongniuzyimage.com/cover/8b3e7cb9ee8d50727f5d6ba08593e8dd.jpg"),
    ("fofoys", "https://hongniuzyimage.com/cover/b344e694a29c3f21a854ed9114b59fc7.jpg"),  # 上轮 OK
    ("piaoys", "https://img.picbf.com/upload/vod/20260921-1/8362432ced56b7e5d3416e9974e79e97.jpg"),
    ("piaoys", "https://shandianpic.vip/upload/vod/20231008-1/bb84c55bc2645b7eccdd77cf8d9077cb.jpg"),
    ("piaoys", "http://jkcasvcsc.com/upload/vod/20231128-1/526a47900b78318f5e42d6950fd1e83a.jpg"),
    ("piaoys", "https://suonipic.com/upload/vod/20240913-1/16fe8320fd2144427062aaf409f62a37.jpeg"),
    ("piaoys", "https://p.bfvp26.com/upload/vod/20261026-1/5cf680ed0e76e3043b91e3cd2e04159a.webp"),  # OK
    ("xcyycn", "https://cdnv1.daquanz.com/upload/vod/20261025-1/24f608d5b647ccadc70991c2ae185560.webp"),
]
SITE_HOME = {"fofoys": "https://www.fofoys.com/", "piaoys": "https://www.piaoys.com/",
             "xcyycn": "https://www.ixcyy.tv/"}


def probe(url, referer=None, timeout=20, tries=2):
    last = ""
    for i in range(tries):
        try:
            h = {"User-Agent": UA}
            if referer:
                h["Referer"] = referer
            r = requests.get(url, headers=h, timeout=timeout, verify=False, stream=True)
            ctype = (r.headers.get("Content-Type") or "").split(";")[0].strip()
            head = next(r.iter_content(8), b"") if r.ok else b""
            r.close()
            if r.ok and (ctype.startswith("image/") or head.startswith((b"\xff\xd8", b"\x89PNG", b"RIFF", b"GIF"))):
                return "OK %s" % ctype
            last = "HTTP %d ct=%s" % (r.status_code, ctype)
        except Exception as e:
            last = type(e).__name__ + ":" + str(e)[:60]
    return "FAIL " + last


print("%-8s %-46s %-28s %-28s %s" % ("site", "host", "无Referer", "带源站Referer", "URL"))
for site, url in SAMPLES:
    host = urlparse(url).netloc
    no_ref = probe(url, None)
    with_ref = probe(url, SITE_HOME[site])
    print("%-8s %-46s %-28s %-28s %s" % (site, host, no_ref[:28], with_ref[:28], url[:70]))
