#!/system/bin/sh
# 启动 TVBox 源站服务（Android 4.4 电视）
# 4.4 的 toolbox 没有 killall / awk / sed，只能靠 shell 分词从 ps 里取 PID。
cd /data/local/tmp || exit 1
LINE=$(ps | grep tvbox44 | grep -v grep)
if [ -n "$LINE" ]; then
  set -- $LINE
  kill "$2" 2>/dev/null
  sleep 1
fi
( ./tvbox44 -port 8787 >> tvbox.log 2>&1 < /dev/null & )
exit 0
