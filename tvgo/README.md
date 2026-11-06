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

## 直播代理（v1.1.0 起）

电视上的直播源改成

```
http://127.0.0.1:8787/live.txt
```

即可。本服务会把上游直播列表现取下来，把每个频道地址换成 `/lv?u=<base64>` 再吐给 TVBox。

### 为什么要绕这一层

这份直播源里约 1/4 的频道是「**302 跨域跳转 + 清单内相对路径**」：

```
请求 http://38.75.136.137:98/gslb/.../cctv15hd.m3u8
  → 302 到 http://204.12.241.18:82/live/cctv15md.m3u8?jsbt=...
  → 清单里写的是   cctv15md/segment_135107.ts      ← 相对路径
```

新版播放器按**跳转后的地址**拼分片，能播；这台电视上的 TVBox 4.4 会拼回**原始域名**
（`38.75.136.137:98/cctv15md/...`），那个地址根本不存在，表现就是永远转圈。

代理只重写清单、**不转发视频字节**：

- `GET /live.txt` — 现取上游列表 → 每个频道地址换成 `/lv?u=<base64>`
- `GET /lv?u=...` — 取清单、跟随 302，把相对地址按最终地址绝对化
- master 清单里的变体 → 继续走 `/lv`，让下一层也被绝对化
- 媒体清单里的分片：**http 直连源站**（不吃电视 CPU）；**https 走 `/lv` 透传**
  （Android 4.4 的 TLS 对部分 CDN 不兼容，交给 Go 的代取更稳）
- 不是清单的响应（.ts 等）原样透传，所以 `&p=1` 也能强制代理分片

### 实现细节（踩过的坑）

- **不能带 Referer**：hndt（河南卫视）等 CDN 一带 Referer 就 403，不带反而 200。
- **列表镜像排序**：`fastly.jsdelivr.net`（约 2s）> `cdn.jsdelivr` > `gh-proxy`（约 15s）
  > raw。上游慢会在播放器那边判超时，所以启动后后台每 50 秒预热一次（缓存 TTL 60s），
  请求永远命中缓存（实测 3–30ms）。
- **token 会过期**：咪咕那 141 个频道带 `timestamp=` 签名，列表里 `IPV4线路` 组的
  token 是旧的（用旧 token 请求返回 **HTTP 605**）。走本机代理后每次拿的是上游最新列表，
  重启/重进直播即可拿到新 token。
- 同一频道有多个地址时（如北京卫视 3 条），上游列表里的多行会保留，TVBox 会合并成
  一个频道多个源，可在界面上切换。

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

## 点播清单 / 分片代理（v1.7.0，默认关闭）

针对「播到广告帧后画面冻住、声音还在」做的实验能力，**默认 `off`**，
`/v/` 输出与 v1.6.1 逐字一致（源站绝对地址），不影响正常看电视。

| 模式 | 行为 | 状态 |
|---|---|---|
| `off` | 完全直连（默认） | v1.6.1 行为，零回归 |
| `list` | 清单由 Go 代取+缓存（1.3 秒），分片绝对化后仍直连源站 | HTTP 层验证通过，起播未确认 |
| `full` | 清单与分片都走 localhost，带预取/超时/重试 | **实机起播不通** |

运行时切换（无需重新编译 / 重启服务）：

```bash
curl "http://127.0.0.1:8787/seg/list"   # 开清单代取
curl "http://127.0.0.1:8787/seg/off"    # 退回直连
curl "http://127.0.0.1:8787/health"     # 看 seg_mode / player_type
```

`full` 的设计要点是**不改写分片行**：把 master 的变体行改成本地短路径
`/p/<sid>/index.m3u8`，媒体清单原文原样返回，清单里的相对分片路径会被
播放器自然拼到 `/p/<sid>/` 上，Go 按文件名查回源站地址。这样清单大小、
每行长度和所有 `#EXT` 标签都不变。分片带完整 `Content-Length`、预取窗口
（后 4 前 2）、并发回源上限 3、上游 30 秒超时 + 重试；实测预取命中 1~4ms
（回源要 2.5~5 秒）。

**为什么没做成默认**：三种模式在实机上都没能确认出画面——
`full` 下 IJK 停在 `FFP_MSG_FIND_STREAM_INFO`、读 3~7 个分片后不再请求；
`playerType=2`（ExoPlayer 2.19.1，想用 Java HTTP 栈的读超时绕开 ffmpeg）
初始化正常但 8 秒只下 70KB；`list` 同样停在黑屏转圈。独立测过源站可达
（`v.lzcdn27.com` 1.5 秒 / 626KB），不是源站整体故障。详见 `CHANGELOG.md`
v1.7.0。

站点配置另支持下发 `playerType`（1=IJK / 2=Exo / -1=跟随设置页，见
`ApiConfig.java:776 → PlayFragment.java:1382`），默认 -1。
`GET /player/exo | /ijk` 可临时切换。注意 `VodInfo.playerCfg` 会记住历史
里的 `pl`，配置下发对已看过的剧不生效，要在播放页的「播放器」按钮切。

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
