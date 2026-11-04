#!/usr/bin/env python3
#coding=utf-8
"""从 tools/tvbox44_template.py 生成三个独立站点文件。

必须分文件：TVBox 的 base.spider.Spider 是单例，同一个 .py 被多个站点共用时
self.extend / 站点状态会互相覆盖。

用法：python tools/build_pys.py
"""
import os
import re

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TEMPLATE = os.path.join(ROOT, "tools", "tvbox44_template.py")
SITES = ["fofoys", "piaoys", "xcyycn"]


def main():
    with open(TEMPLATE, "r", encoding="utf-8") as f:
        tpl = f.read()
    if '__SITE__' not in tpl:
        raise SystemExit("模板缺少 __SITE__ 占位符")
    for site in SITES:
        code = tpl.replace("__SITE__", site)
        # 文件头注明生成来源，避免有人直接改生成文件
        code = code.replace(
            "#coding=utf-8\n",
            "#coding=utf-8\n# 本文件由 tools/build_pys.py 生成（源：tools/tvbox44_template.py），请勿直接修改。\n",
            1,
        )
        out = os.path.join(ROOT, "tvbox44_%s.py" % site)
        with open(out, "w", encoding="utf-8", newline="\n") as f:
            f.write(code)
        # 自检：占位符必须被替换干净，且语法合法
        assert "__SITE__" not in code
        compile(code, out, "exec")
        print("generated %s (%d bytes)" % (os.path.relpath(out, ROOT), len(code.encode("utf-8"))))


if __name__ == "__main__":
    main()
