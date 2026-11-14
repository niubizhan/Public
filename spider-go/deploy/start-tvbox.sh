#!/system/bin/sh
# 启动 TVBox 本地源站服务（Android 4.4 电视）
# 关键：adb shell 退出会向会话进程组发 SIGHUP，普通 `( cmd & )` 起的子进程会被连带杀掉。
# 用 `trap "" HUP` 让子进程继承 SIG_IGN，再双 fork，进程会被 init 收养（PPID=1）独立存活。
# 4.4 的 toolbox 没有 killall / awk / sed，只能靠 shell 分词从 ps 里取 PID。
cd /data/local/tmp || exit 1
LINE=$(ps | grep tvbox44 | grep -v grep)
if [ -n "$LINE" ]; then
  set -- $LINE
  kill "$2" 2>/dev/null
  sleep 1
fi
trap "" HUP
( ./tvbox44 -port 8787 -addr 127.0.0.1 >> tvbox.log 2>&1 < /dev/null & )
exit 0
