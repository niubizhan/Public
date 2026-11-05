# tvgo — TVBox 源站服务（Go 版，跑在电视本机）

把原来跑在电脑上的 Node 服务（`server.mjs`）改写成一个**单文件静态二进制**，
直接部署到 TCL 电视（Android 4.4.3 / armv7）上运行。电视开机即自启，
取数据、转封面图、代理 m3u8 全部由电视自己完成，**不再需要电脑常开**。

## 为什么用 Go，不用 Python

| | Python | Go |
|---|---|---|
| Android 4.4 运行时 | 官方无构建，要 NDK + bionic 补丁，`ssl`/`socket`/`ctypes` 全是坑 | 不需要运行时，静态二进制直接跑 |
| 体积 | 40MB 起（含 stdlib） | 5.9MB（含内嵌 CA） |
| 交叉编译 | 基本不可行 | `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build` 一条命令 |
| TLS | 依赖系统 OpenSSL（4.4 只支持老套件，这也是 pages.dev 连不上的原因） | 自带 TLS 栈，绕开系统限制 |

Termux 也不行——它要求 Android 5.0+。

## 构建

需要 Go 1.22+（本机用的是 go1.22.12 windows-amd64）。

```bash
# 电视（armeabi-v7a）
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -ldflags="-s -w" -o tvbox44-arm .

# 本机调试
go build -o tvbox44.exe .
./tvbox44.exe -port 8788
```

`cacert.pem` 是内嵌的 Mozilla CA 包，必须和源码放在一起（`//go:embed`）。

## 部署到电视

电视：MT5882 / Android 4.4.3 / SDK19 / armv7 / 已 root。
下面的 `ADB` 指 `adb.exe`，设备 `192.168.5.19:5555`。

```bash
adb connect 192.168.5.19:5555
adb push tvbox44-arm  /data/local/tmp/tvbox44
adb push deploy/boot-tvbox.sh /data/local/tmp/boot-tvbox.sh
adb shell "chmod 755 /data/local/tmp/tvbox44 /data/local/tmp/boot-tvbox.sh"
```

### 开机自启

Android 4.4 没有 `init.d`，这台机器唯一可靠的开机钩子是
`/system/etc/install-recovery.sh`（init.rc 里是 `service flash_recovery ... class main / oneshot`）。

原始文件权限是 **644（没有执行位）**，所以 init 一直执行失败、
dmesg 里能看到 `init: cannot execve('/system/etc/install-recovery.sh'): Permission denied`。
也就是说 TCL 注释写的「this func is not necessary for TV」之外，它还压根没跑起来过。

做法：在 shebang 之后插入一行双 fork 调用，**原有工厂看门狗逻辑一字不动**，然后补上执行位。

```bash
adb shell "su 0 mount -o remount,rw /system"
adb shell "su 0 cp /system/etc/install-recovery.sh /data/local/tmp/install-recovery.sh.bak"
adb push deploy/install-recovery.sh.new /data/local/tmp/ir.new
adb shell "su 0 cp /data/local/tmp/ir.new /system/etc/install-recovery.sh"
adb shell "su 0 chmod 755 /system/etc/install-recovery.sh"   # 关键：644 → 755
adb shell "su 0 mount -o remount,ro /system"
```

回滚：

```bash
adb shell "su 0 mount -o remount,rw /system"
adb shell "su 0 cp /data/local/tmp/install-recovery.sh.bak /system/etc/install-recovery.sh"
adb shell "su 0 chmod 644 /system/etc/install-recovery.sh"
adb shell "su 0 mount -o remount,ro /system"
```

### 接口地址（当前方案：TVBox 内直接填 127.0.0.1）

**正确做法**：在 TVBox 的「设置 → 数据源 / 接口」里把接口地址改成
`http://127.0.0.1:8787/`，直连本机服务。这是唯一干净的方式，也是当前线上状态。

> v1.4.0 曾经用 DNAT 兜底：因为 TVBox 的接口地址存在 Hawk2.xml（AES 加密）里，
> 改不动文件，而推送服务 `POST :9978/action?do=api&url=...` 在源码里只处理
> `do=refresh&type=danmaku`，其他一律返回 `ok` 却什么都不做（当时以为改成功了，
> 核对 Hawk2.xml 的 mtime 才发现是假象）。于是在电视内部加了一条
> `iptables -t nat -A OUTPUT -d 192.168.5.12/32 -p tcp --dport 8787 -j DNAT
> --to-destination 127.0.0.1:8787`，让旧地址指向本机。
>
> **v1.5.0 起这条规则已删除**：用户在应用内把地址改成 `127.0.0.1:8787` 后不再需要，
> `boot-tvbox.sh` 也改成开机顺手清理残留规则。若旧规则还在，手动删：
>
> ```bash
> adb shell "su 0 iptables -t nat -D OUTPUT -d 192.168.5.12/32 -p tcp --dport 8787 -j DNAT --to-destination 127.0.0.1:8787"
> adb shell "su 0 sh -c 'echo 0 > /proc/sys/net/ipv4/conf/all/route_localnet; echo 0 > /proc/sys/net/ipv4/conf/lo/route_localnet'"
> ```
>
> 删完用 `su 0 iptables -t nat -L OUTPUT -n` 确认链为空即可，不需要重启服务。

## 运行时行为

- 监听 `0.0.0.0:8787`
- 日志 `/data/local/tmp/tvbox.log`（每次启动覆盖，避免无限增长）
- `boot-tvbox.sh` 带看门狗：进程退出 5 秒后自动拉起
- 手动重启：`adb shell "kill \$(ps | grep tvbox44 | grep -v grep | ...)"` →
  实际上直接用 `adb shell "sh /data/local/tmp/start-tvbox.sh"`，
  或者最省事：`adb shell "su 0 killall"` 不存在，用 `adb shell "kill <PID>"`

看日志（4.4 没有 `tail`）：

```bash
adb shell "cat /data/local/tmp/tvbox.log"
```

## Android 4.4 上踩到的坑

1. **没有 `/etc/resolv.conf`** —— Go 的纯 Go 解析器直接失败。
   启动时用 `getprop net.dns1` 取路由器 DNS，塞进自定义 `net.Resolver`。
2. **没有系统根证书路径** —— Go 的 `crypto/x509` 只认 `/etc/ssl/certs/ca-certificates.crt`
   这类桌面路径，Android 的根证书是 `/system/etc/security/cacerts/*.0` 散装 PEM。
   表现是 `x509: certificate signed by unknown authority`，三个源全部抓不到。
   解法：内嵌 `cacert.pem`，再叠加系统证书目录；同时保留「证书校验失败时对该请求
   降级重试」的兜底（灰产源站经常缺中间证书）。
3. **没有 `killall` / `awk` / `sed` / `head` / `tail` / `wc` / `which` / `printf`**
   —— 脚本里只能用 `ps | grep` + shell 分词（`set -- $LINE`）取 PID。
4. **没有 `nohup` / `setsid`** —— `adb shell` 退出会带走同会话子进程，
   所以必须在 init 的 service 里双 fork（`( script & )`）才活得下来。
5. **内核对 netfilter 支持不全** —— `REDIRECT` 目标不可用，只能 `DNAT`
   （v1.4.0 的地址兜底方案用过，v1.5.0 已改为 TVBox 内直填 `127.0.0.1`，不再需要）。
6. **时间戳**：Go 日志输出的时区与系统 `date` 不一致（进程拿的是 UTC），
   对比日志和截图时间时要注意差 8 小时。

## 已验证

- 三个源首页、分类、搜索、详情、播放清单，数据与电脑端 Node 版**逐字一致**
  （连源站那两个粘连在一起的片名都一样）
- 电视端抓取：fofoys 35 条 / piaoys 65 条 / xcyycn 122 条
- 批量详情（首页 35 个 id）命中索引 **17-22ms**
- 播放清单 `/v/{site}/index.m3u8` 返回 200，相对路径已绝对化
- 封面 `/img?u=` 正常返回 JPEG（fofo 的 `pic.youkupic.com` 偶发连接重置，
  会退化成 302，那几张海报显示色块——网络问题，非代码问题）
- **关掉电脑上的 8787 服务后，电视重启仍完整可用**
- 电视重启后 `tvbox44` 以 root 身份自动拉起，iptables 规则自动恢复
