#coding=utf-8
"""base.spider 的本地替身，用于在 PC 上联调 tvbox44_*.py。

只实现模板用到的接口：fetch/post（requests）、html/xpText（lxml）、
str2json/json2str、regStr、removeHtmlTags、log、getProxyUrl、缓存桩。
真正跑在 TVBox 上时用的是 pyramid 自带的 base/spider.py。
"""
import json
import re

import requests
from lxml import etree


class _Proxy:
    @staticmethod
    def getUrl(spider, local=True):
        return "http://127.0.0.1:19999"

    @staticmethod
    def getPort(spider):
        return 19999


class Spider:
    _instance = None

    def __init__(self):
        self.extend = ""

    def __new__(cls, *args, **kwargs):
        if cls._instance:
            return cls._instance
        cls._instance = super().__new__(cls)
        return cls._instance

    def init(self, extend=""):
        pass

    def getName(self):
        return ""

    def getDependence(self):
        return []

    def setExtendInfo(self, extend):
        self.extend = extend

    def fetch(self, url, headers=None, cookies=None, params=None, timeout=15, verify=True,
              stream=False, allow_redirects=True):
        rsp = requests.get(url, params=params, cookies=cookies, headers=headers, timeout=timeout,
                           verify=verify, stream=stream, allow_redirects=allow_redirects)
        rsp.encoding = "utf-8"
        return rsp

    def post(self, url, data=None, headers=None, cookies=None, params=None, json=None, timeout=15,
             verify=True, stream=False, allow_redirects=True):
        rsp = requests.post(url, params=params, data=data, json=json, cookies=cookies, headers=headers,
                            timeout=timeout, verify=verify, stream=stream, allow_redirects=allow_redirects)
        rsp.encoding = "utf-8"
        return rsp

    def html(self, content):
        return etree.HTML(content)

    def xpText(self, root, expr):
        ele = root.xpath(expr)
        return ele[0] if ele else ""

    def regStr(self, src, reg=None, group=1):
        if reg is None:
            return ""
        m = re.search(reg, src)
        return m.group(group) if m else ""

    def removeHtmlTags(self, src):
        return re.sub("<.*?>", "", src)

    def str2json(self, content):
        return json.loads(content)

    def json2str(self, content):
        return json.dumps(content, ensure_ascii=False)

    def getProxyUrl(self, local=True):
        return "http://127.0.0.1:19999/?do=py"

    def log(self, msg):
        if isinstance(msg, (dict, list)):
            print(json.dumps(msg, ensure_ascii=False))
        else:
            print(msg)

    def getCache(self, key):
        return None

    def setCache(self, key, value):
        return "succeed"

    def delCache(self, key):
        return "succeed"

    def isVideoFormat(self, url):
        return bool(re.search(r"\.mp4|\.m3u8|\.flv|\.mkv", url or ""))

    def manualVideoCheck(self):
        return False

    def action(self, action):
        return ""

    def destroy(self):
        pass
