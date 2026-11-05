#!/system/bin/sh
# 开机自启：等网络就绪 → 恢复端口重定向 → 启动/守护源站服务
#
# 由 /system/etc/install-recovery.sh 调用（init 的 class main oneshot，root 身份）。
# Android 4.4 没有 init.d，install-recovery.sh 是这台机器唯一可靠的开机钩子。

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

# 2) 端口重定向
# TVBox 的配置地址存在 Hawk 里（AES 加密，改不了文件），仍是旧的
# http://192.168.5.12:8787/。在电视内部用 DNAT 把它导到本机 8787，
# 这样 TVBox 配置一个字都不用动，也不再依赖那台电脑。
# route_localnet 必须打开，否则内核会丢弃 DNAT 到 127.0.0.1 的包。
echo 1 > /proc/sys/net/ipv4/conf/all/route_localnet 2>/dev/null
echo 1 > /proc/sys/net/ipv4/conf/lo/route_localnet 2>/dev/null
# 先删可能残留的同名规则，避免重复叠加
for i in 1 2 3; do
  iptables -t nat -D OUTPUT -d 192.168.5.12/32 -p tcp --dport 8787 -j DNAT --to-destination 127.0.0.1:8787 2>/dev/null
done
iptables -t nat -A OUTPUT -d 192.168.5.12/32 -p tcp --dport 8787 -j DNAT --to-destination 127.0.0.1:8787
log -t $TAG "redirect 192.168.5.12:8787 -> 127.0.0.1:8787"

cd /data/local/tmp || exit 1
if [ ! -x ./tvbox44 ]; then
  log -t $TAG "ERROR: /data/local/tmp/tvbox44 missing"
  exit 1
fi

# 3) 看门狗：进程退出 5 秒后拉起（源站在内存吃紧时可能被系统杀掉）
while true; do
  ./tvbox44 -port 8787 > tvbox.log 2>&1
  log -t $TAG "service exited (rc=$?), restart in 5s"
  sleep 5
done
