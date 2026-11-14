#!/system/bin/sh
# 开机自启：等网络就绪 → 启动/守护源站服务
#
# 由 /system/etc/install-recovery.sh 调用（init 的 class main oneshot，root 身份）。
# Android 4.4 没有 init.d，install-recovery.sh 是这台机器唯一可靠的开机钩子。
#
# 注：TVBox 的接口地址已在应用内改为 http://127.0.0.1:8787/，直连本机，
# 不再需要 DNAT 把旧地址 192.168.5.12:8787 重定向过来。为避免旧规则在
# 升级/恢复后残留，这里顺手清理一次（不存在时报错可忽略）。

TAG=tvbox-boot
log -t $TAG "boot hook start"

# 1) 等 DHCP 拿到 DNS（WiFi 连接较慢，最多等 90 秒）
n=0
while [ -z "$(getprop net.dns1)" ]; do
  n=$((n + 1))
  if [ $n -gt 90 ]; then break; fi
  sleep 1
done
log -t $TAG "network ready: dns=$(getprop net.dns1) waited=${n}s"
sleep 2

# 2) 清理历史遗留的端口重定向规则（v1.4.0 曾用 DNAT 兜底，v1.5.0 起不再需要）
for i in 1 2 3; do
  iptables -t nat -D OUTPUT -d 192.168.5.12/32 -p tcp --dport 8787 -j DNAT --to-destination 127.0.0.1:8787 2>/dev/null
done

cd /data/local/tmp || exit 1
if [ ! -x ./tvbox44 ]; then
  log -t $TAG "ERROR: /data/local/tmp/tvbox44 missing"
  exit 1
fi

# 3) 看门狗：进程退出 5 秒后拉起（源站在内存吃紧时可能被系统杀掉）
# 监听 127.0.0.1 而非 0.0.0.0：只给本机 TVBox 用，不暴露到局域网。
while true; do
  ./tvbox44 -port 8787 -addr 127.0.0.1 > tvbox.log 2>&1
  log -t $TAG "service exited (rc=$?), restart in 5s"
  sleep 5
done
