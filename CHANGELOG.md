# 变更记录

TVBox 4.4 type=1 接口（fofo影视 / 飘花影视 / 星辰影院），部署形态为 Cloudflare Worker + 本机局域网 HTTP 服务（`server.mjs`，端口 8787）；自 v1.4.0 起另有 Go 版可**直接跑在电视上**（`tvgo/`），不再依赖电脑。

## v2.2.0 — 2026-11-14

### 并发预取分片 + 线路自动测速选最快

v2.1.1 解决了「卡死」，但慢 CDN 顺序下载每片 25~38s，播放器缓冲永远追不上。
本版加两项能力，`spider-go/main.go`、`spider-go/tvbox.go`。

- **并发预取缓存**：`handleProxyPlaylist` 返回子清单时登记 session，
  后台并发（6 路）预取后续 8 个分片到内存；`seg?u=...&p=<pid>` 命中即秒回，
  支持 Range/206。缓存上限 32 片、消费即释放，一次只保留一个活跃 session，
  切换片源时 `context.Cancel` 取消旧下载，避免吃光电视内存。
- **线路自动测速**：详情返回时 `rewritePlayURLs` 把「同一集在各线路下的
  token」登记成组（`registerLineOptions`）；起播时 `chooseLine` 并发探测最多
  4 条线路，解析真实地址后取首分片前 64KB 计算吞吐，8 秒预算内选最快的
  线路透明替换。结果按剧集缓存 10 分钟，换集不重复测；全部失败/超时则保持
  TVBox 原本选中的线路。日志形如 `[speed] probed 4 lines, pick 金星20 (58 KB/s)`。
- `appVersion`：`2.1.1-proxy` → `2.2.0-autospeed`。

核验：
- 本机自测：`pick 地球20 (62 KB/s)`；master 8.2s（含测速）；
  285 分片带 `p`；seg0 4.9s、seg1 1.3s、seg2 2.1s（预取命中）；Range 206 正确。
- 电视侧：`pick 金星20 (58 KB/s)`；seg0 14.6s、**seg1 0.1s**（预取命中）。
- 实机：TVBox 重启后播放第20集，`[speed] cached pick 金星20` 命中缓存，
  音频轨持续输出（`AudioFlinger` active track，UndFrmCnt 3.3万→9.3万），
  无 502、无 `Source error`、无 `[seg] fail`。**网络/源站链路已完全打通**。
- 遗留（非源站问题）：画面仍黑，`SurfaceFlinger` 报
  `[SurfaceView] bindTextureImage ... 0x0`。当前 TVBox 用的是 ExoPlayer，
  属 Android 4.4 上视频 Surface/解码渲染层问题，需在 TVBox 设置里把播放器
  改为 IJK（或换回带「系统播放器」引擎的自编包）。本版未改 APK。

部署：`C:\Go\bin\go.exe`(go1.27.4) 交叉编译，产物 8,192,160 B /
MD5 `5c8f47b6c5f4a20f41ec3a7a2868016d`；电视 `/data/local/tmp/tvbox44` 已替换，
看门狗自动拉起，health `2.2.0-autospeed`。回滚备份：
`/data/local/tmp/tvbox44.v201.bak`(v2.0.1)、`.v210`(v2.1.0)、`.v211`(v2.1.1)。
接口地址与开机钩子未改动。

## v2.1.1 — 2026-11-14

> 中间版本：全链路代理 + 流式分片修复；已被 v2.2.0 取代，记录保留供回滚参考。

### HLS 全链路本地代理 + 流式分片（修复插广告后黑屏卡死）

背景：电视装的 `TVBox_4.4_armeabi-v7a.apk` 没有 `rw_timeout`/`reconnect` 补丁，
播放器（当前为 ExoPlayer）直连灰产 CDN，某个分片 connection reset 后永久卡在读流，
表现为「插入广告后黑屏」。上游 `mixed.m3u8` 有 281 个分片、51 个
`#EXT-X-DISCONTINUITY`（每 5 段一个，属 CDN 混切，**不是广告标记**，
因此不做盲目的广告段剔除）。

改动文件：`spider-go/main.go`、`spider-go/tvbox.go`。

- 新增 `rewriteM3U8()`：master 子清单改写成 `/v/{site}/proxy.m3u8?u=`，
  媒体分片改写成 `/v/{site}/seg?u=`，AES 密钥仍走 `enc.key?u=`
  （保持 v2.0.1 行为）。播放器从此只连 `127.0.0.1:8787`。
- 新增 `handleProxyPlaylist()` 递归改写子清单；`handleSegment()` 代理 TS，
  带 UA/Referer、Range 透传、响应头阶段 3 次重试（连接重置/5xx 由 Go 吸收）。
- **关键修正（v2.1.0 → v2.1.1）**：初版先把分片全缓冲再返回并设 20s 整体超时，
  实测 CDN 单分片 1.5MB 在电视上要 25~29s，被误判超时，3 次重试堆到 61s
  返回 502，Exo 直接 `ExoPlaybackException: Source error`。
  改为流式 `io.Copy` 边下边播，超时只兜底响应头阶段，
  `http.Server.WriteTimeout` 90s → 180s。
- 新增 `-strip-disc` 排障开关（去掉 DISCONTINUITY，默认关闭，不改变清单语义）。
- 成功的 `/seg` 不打 access log（一集 281 片），仅记录失败与慢分片。
- `appVersion`：`2.0.1-goquery` → `2.1.1-proxy`（health 里可确认部署版本）。

核验：
- 本机自测：health `2.1.1-proxy`；master 无外链；281 分片全部本地化；
  `seg` 200 / 716,280 B / `video/mp2t`；Range 206 / 1,024 B / `Content-Range` 正确。
- 电视侧：8788 试跑后正式替换，8787 health `2.1.1-proxy`，
  看门狗 `boot-tvbox.sh` 自动拉起（PID 25070）；实机日志
  `[seg] slow ... 1512272 bytes 29s` 等，分片连续完整传完、无 502、无 Source error。
- 已知限制：当前 CDN 单分片 25~29s（≈52KB/s），起播缓冲慢属于带宽问题，
  代理不能创造带宽；卡死已消除，但慢速线路仍可能长时间转圈，建议换线路/站点。

部署：仍用 `C:\Go\bin\go.exe`（go1.27.4）交叉编译
`GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0` + `-ldflags="-s -w"`；
产物 8,192,160 B / MD5 `4a219461cf075d5752bff0b56bc0e0b5`。
回滚备份：本机 `tvbox44-arm.v201.bak`（v2.0.1）、电视
`/data/local/tmp/tvbox44.v201.bak`，另有初版代理 `/data/local/tmp/tvbox44.v210`。
接口地址 `http://127.0.0.1:8787/` 与开机钩子均未改动。

## v2.0.2 — 2026-11-14

### 编译工具链升级 Go 1.22.5 → Go 1.27.4，重新部署到电视（源码零改动）

用户指定改用 `C:\Go\bin\go.exe`（`go1.27.4 windows/amd64`）重新编译并部署。
**本轮 `spider-go/` 源码一行未改**，只更换编译器并替换电视上的可执行文件；
依赖仍锁 `goquery v1.9.2` / `cascadia v1.3.2` / `x/net v0.24.0`，
复用本机 `_gopath` 模块缓存，编译设 `GOPROXY=off` 全程离线。

- 构建参数：`GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 GOTOOLCHAIN=local`
  `-ldflags="-s -w"`；产物 `tvbox44-arm` 8,126,624 B（旧版 6,422,680 B），
  MD5 `42072af9251be8a02e6b26841d604148`，ELF32 ARM(e_machine=40) 静态二进制。
  构建元数据含 `DefaultGODEBUG`（Go 1.27 新引入），无需改代码即兼容。
- 旧版备份：本机 `tvbox44-arm.go1225.bak`、电视 `/data/local/tmp/tvbox44.go1225.bak`
  （MD5 `5263adfd823356c5656519dcdc0a51f3`），可随时回滚。
- 电视 IP 由 `192.168.5.19` 换成 **`192.168.5.21`**；接口地址仍是
  `http://127.0.0.1:8787/`（本机回环），不受 IP 变更影响，无需重填配置。

部署踩坑（重要）：源站由 root 运行，普通 adb shell 的 `kill` 杀不掉，
`cp` 覆盖会报 `Text file busy`；必须 `su 0 kill <pid>` 后，在开机看门狗
`boot-tvbox.sh` 的 5 秒重启窗口内用 `su 0 cp` 覆盖并 `chmod 755`，
看门狗随后自动用新二进制拉起服务（`trap "" HUP` 双 fork 进程 PPID=1 保持不变）。
另：本机 toolchain 默认 `GOMODCACHE=C:\GOPATH\pkg\mod`，需显式
`GOPATH=<工作区>/_gopath` 才能命中已有依赖缓存。

核验（电视 `192.168.5.21`，经 `adb forward` 访问 127.0.0.1:8787）：
- `/health` 200，`version=2.0.1-goquery`、`sites=2`、`proxy=""`、resolver `192.168.5.1`，
  uptime 重新计时证明是新进程（PID 26512，父进程为看门狗 897）。
- `/` 配置 2 站点；ixcyy 首页 118 条、fofoys 首页 35 条；
  搜索「光阴之外」1 条；详情 `ids=152289` 含线路；播放清单 200 且返回合法
  `#EXTM3U` master（含真实外站直链）。外网 HTTPS 抓取正常 → Go 1.27 TLS 栈在
  Android 4.4 / 内核 3.10 上工作正常。
- 电视端 `md5 /data/local/tmp/tvbox44` 与本机产物一致；8787 监听正常；
  开机钩子 `/system/etc/install-recovery.sh` 未改动，重启后仍自启新版本。
- 未做加密片源实机播放验证（HLS AES-128 密钥代理逻辑源码未改，v2.0.1 已验证过）。

## v2.0.1 — 2026-11-14

### 修复 HLS AES-128 密钥 404 导致加密片源无法播放

实机播放 fofoys 加密 m3u8 时，源站日志出现连续 `GET /v/fofoys/enc.key 404`：
`absolutize()` 原先跳过所有 `#` 开头行，清单里的
`#EXT-X-KEY:METHOD=AES-128,URI="enc.key"` 未被改写，播放器把相对 URI
拼到本地服务 `/v/{site}/enc.key` → 404 → 分片无法解密、播放黑屏。

- `absolutize()` 新增处理 `#EXT-X-KEY` / `#EXT-X-MAP` / `#EXT-X-SESSION-KEY`
  行的 `URI="..."`：按原始清单 URL 解析成绝对地址，改写为
  `enc.key?u=<绝对密钥URL>&ref=<原清单URL>`，改走本地代理。
- 新增 `handleHLSKey()`（`/v/{site}/enc.key` 路由），带播放器 UA 与 Referer
  回源取密钥，规避防盗链；用 `lastBase` 缓存「最近一次清单目录」兜底
  播放器丢掉 query 的情况。
- `/v/` 路由按子路径分发清单与密钥；`appVersion` 升到 `2.0.1-goquery`。

验证：本机与 TCL 电视端均返回 `200 / 16 字节` AES-128 key；重启 TVBox 后
源站日志出现 `GET /` 与 `GET /api/ixcyy`，配置持久化生效。
同轮电视端 TVBox 换装 `TVBox_4.4_armeabi-v7a.apk`（`1.0.0-tv44`，
证书 CN=TVBox4.4，与旧包签名不同故数据清空），已通过 9978 POST
重新写入接口地址 `http://127.0.0.1:8787/`。

## v2.0.0 — 2026-11-14

### 新增 `spider-go/`：按 fongmi 爬虫规范重写的 Go 源站（双站点，goquery 解析）

用户提供了一份 Go 草稿（`/spider/ixcyy` 路由 + `VodResponse`/`VodItem`/`Spider` 骨架），
要求修复、加入 fofoys、部署到电视。草稿有 6 处跑不起来的问题，逐条修复并落到 `spider-go/`：

1. `Category` / `PlayUrl` / `extractID` 只被引用未定义 → 编译失败。补全类型与工具函数，
   `Spider` 接口补 `Key/Name/Base/HomeContent`。
2. 草稿 URL `https://www.ixcyy.tv/vodshow/{id}--------{pg}---.html` 是通用苹果 CMS 猜测，
   实测 ixcyy 是 shoutu46 模板：分类 `/v/{1..5}.html`、搜索 `/s.html?wd=`、
   详情 `/d-{id}.html`、播放 `/p/{id}-{flag}-{ep}.html`。
3. 草稿选择器 `.module-item` / `.module-item-title` 不存在；改用 goquery 解析实测结构
   `a.public-list-exp` / `h3.slide-info-title`（ixcyy）、
   `a[href^="/{type}/"] img[alt=video]` + `span.font-bold`（fofoys）。
4. `http.Get` 无 UA / 无超时 → 电视上源站掐线时永久挂起。改为带 UA、全链路超时、
   3 次退避重试、证书失败降级的 client。
5. 草稿只有 `/spider/ixcyy` 一条路由且无配置下发。补齐 `/`（TVBox 配置 JSON，
   sites[] 两个 type=1 源）、`/api/{site}`（保留 `/spider/{site}` 兼容别名）、
   `/img` 图片代理、`/v/{site}/index.m3u8` 播放清单代理、`/health`。
6. 草稿未处理 Android 4.4 两个专属坑：无 `/etc/resolv.conf`（显式问 `net.dns1`）、
   Go 不认散装系统 CA（`//go:embed cacert.pem`）。两者补齐。

**新增 fofoys.com**：Next.js SSR，列表 `/type/{type}`、搜索 `/search?q=`、
详情内嵌 `var urlList` JS 字面量（`jsLiteralToJSON` 转换）、播放 `POST /source/ id={sid}`。

**TVBox 4.4 兼容约束**（沿用 tvgo 实机经验）：不输出 `action` 键、`group_id`/`type_id_1`
必须是数字、`limit` 为字符串 `"20"`、首页 `?ac=detail` 需同时带 `class` + `list`、
配置不能输出 `"lives": []`。另修正「单个 id 详情命中首页卡片缓存导致没有集数」：
单个 id 强制抓真实详情页，只有批量 ids 预取才走索引缓存。

**代理口径**：`127.0.0.1:7890` 只用于本机安装 Go 环境 / 下载依赖；
源站抓取默认直连（实测该代理对两站不可用：ixcyy EOF、fofoys 返 Cloudflare 403），
保留 `-proxy` 开关供必要时显式启用。goquery 锁 v1.9.2（v1.13 要求 Go>=1.25，
与本机 Go 1.22.5 不兼容）。

**部署与实机验证**：
- 交叉编译 `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0`，产物 6.4MB 静态二进制。
- 推送 `/data/local/tmp/tvbox44`，监听 `127.0.0.1:8787`，进程 PPID=1 独立存活
  （解决 adb shell 退出连带杀后台进程：启动脚本用 `trap "" HUP` + 双 fork）。
- 恢复 `/system/etc/install-recovery.sh` 开机钩子（双 fork 拉起 `boot-tvbox.sh`
  看门狗，755 权限；原文件已备份到 `install-recovery.sh.bak`），`/system` 已回只读。
- 通过应用自带 9978 推送服务 **POST** `/action?do=api&url=http://127.0.0.1:8787/`
  写入接口地址（GET 返回的 ok 是假象，`InputRequestProcess.isRequest` 只认 POST）。
- 电视端实测：`/health` 正常；ixcyy 首页 119 条 / fofoys 首页 35 条；
  分类 56 / 84 条；搜索 10 / 20 条；详情均拿到完整线路与集数；
  两站播放均解出真实 m3u8 直链并回传合法 master 清单。
- TVBox 应用日志确认已来拉配置：`GET /` 与 `GET /api/ixcyy` 均 200。

## v1.7.0 — 2026-11-06

### 新增：点播清单/分片代理 `tvgo/segproxy.go`（默认关闭，实验能力）

针对「播到广告帧后画面冻住、声音还在」继续做分片预取，完整实现并实机验证，
**结论是这条路在点播 HLS 上走不通**，因此默认仍是 v1.6.1 的完全直连。

**实现**（`tvgo/segproxy.go`）：

- 关键设计是**不改写分片行**：`/v/` 拿到的其实只是 146 字节 master，
  真正的 58KB 媒体清单是播放器直连源站取的。把 master 的变体行改成本地
  短路径 `/p/<sid>/index.m3u8` 后，**媒体清单原文原样返回**，清单里的相对
  分片路径会被播放器自然拼成 `/p/<sid>/xxx.ts`，Go 按文件名查回源站绝对
  地址。清单大小、每行长度、所有 `#EXT` 标签都不变，避免了 v1.6.1 记下的
  「58KB→117KB + 200 字符 base64 URL」问题。
- 三种模式：`off`（直连，默认）/ `list`（清单代取+缓存、分片绝对化直连）/
  `full`（清单和分片都走 localhost）。运行时可切：
  `GET /seg/off | /seg/list | /seg/on`；`GET /health` 会回报当前模式。
- `full` 的分片代理带：预取窗口（向后 4 个、向前留 2 个）、并发回源上限 3、
  上游 30 秒超时 + 失败重试、完整 `Content-Length` / `Content-Type`、
  VOD 清单缓存 2 小时、流 30 分钟 TTL 回收。响应实测 1~4ms 命中预取
  （回源要 2.5~5 秒），`Content-Length: 896572` 正确。
- 站点配置新增 `playerType` 下发与 `GET /player/exo | /ijk` 开关：
  依据 `ApiConfig.java:776 → PlayFragment.java:1382`，源配置可指定站点级
  播放器类型（1=IJK / 2=Exo / -1=跟随设置页）。

**实机结论（三条都验证过，均放弃为默认）**：

1. `full`：HTTP 层全部通过，但 IJK 起播停在 `FFP_MSG_FIND_STREAM_INFO`，
   读 3~7 个分片后不再请求、不出画面。长 URL(`/lv`) 与短 URL(`/p/`) 都一样。
   直播 `/lv` 能播是因为 live 与 vod 的 ijk option 不同
   （`max_cached_duration` / `infbuf` / `threads`）。
2. `playerType=2`（ExoPlayer 2.19.1，想用 Java HTTP 栈的读超时绕开 ffmpeg）：
   Exo 初始化正常，但 8 秒只下 70KB、一直黑屏转圈。且 `VodInfo.playerCfg`
   会记住历史里的 `pl`，配置下发对看过的剧不生效。
3. `list`：清单代取 1.3 秒（源站慢时要 20 秒）、分片绝对直连，HTTP 层验证
   通过，但实机起播仍未确认出画面。

**因此默认 `off`**：不把未验证的行为强加给电视，`/v/` 输出与 v1.6.1 逐字
一致（源站绝对地址），`playerType` 默认 -1 跟随设置页，零回归风险。
顺带独立测过源站：`v.lzcdn27.com`（线路6）1.5 秒 / 626KB、
`vip.dytt-tvs.com`（线路3）分片 4.9~8.8 秒 / 896KB，均可达，
卡住不是源站整体故障。

核验：`/health` 返回 `{"player_type":-1,"seg_mode":"off","version":"1.7.0"}`；
`/v/` 恢复输出源站绝对 https；服务单进程；TVBox 重启后首页正常。

## v1.6.1 — 2026-11-06

### 修复：`/lv` 转发分片时先整段读进内存，慢且会被 4MB 上限截断

**背景**：排查「点播播到片头广告就卡死、声音还在响」时顺手改的。
原来的 `/lv` 不看地址后缀，一律 `io.ReadAll(LimitReader(body, 4MB))`
读完再判断是不是清单，分片也要等最后一个字节才开始往播放器吐，
而且超过 4MB 的片会被截断。

**改动**（`tvgo/live.go`）：

- 先按 URL 后缀（`.m3u8` / `playlist`）和 `Content-Type` 判断清单还是分片；
- 分片走 `io.Copy` **流式转发**，`Content-Length` 原样透给播放器，
  让它知道确切边界；
- 分片上游超时由 12 秒提到 30 秒，失败**重试一次**（源站偶发掐连接 / 502）；
- 清单仍整段读取（要重写里面的地址），上限从 4MB 提到 8MB。

### 记录：两条试过但走不通的「广告后卡死」修法

同一次排查里验证并放弃的，避免以后重复踩：

1. **把点播分片也改成走 `/lv` 本地代理**（连接复用 + 超时重试）——
   IJK 在 `FFP_MSG_FIND_STREAM_INFO` 之后就停住不起播。清单从 58KB 涨到
   117KB、每个分片 URL 变成 200 多字符的 base64，这台电视的 ffmpeg 不认。
   已回退，`main.go` 里留了注释说明。
2. **渲染方式 TextureView → SurfaceView**（`play_render`）——
   `ijkmp_set_android_surface(surface=0x0)`，SurfaceView 拿不到 surface，
   更播不了。已通过删除 `Hawk2.xml` 里的 `play_render` 键恢复默认。

**实测结论**：卡死时 6 秒只下 98KB、88 秒里一条解码日志都没有、CPU 空转，
是 IJK（4.4 老 ffmpeg）在源站变慢/掐连接后 read 线程永久阻塞、不会超时退出。
服务端无法从根上修复，只能减小触发概率。

## v1.6.0 — 2026-11-05

### 新增：直播代理（`/live.txt` + `/lv`），修复电视上大量直播频道转圈

**现象**：TVBox 直播里很多频道一直转圈，同一份直播源在手机上正常。

**根因**：该直播源（`Supprise0901/TVBox_live`）里约 1/4 的频道是
「**302 跨域跳转 + 清单内相对路径**」：

```
请求 http://38.75.136.137:98/gslb/.../cctv15hd.m3u8
  → 302 到 http://204.12.241.18:82/live/cctv15md.m3u8?jsbt=...
  → 清单里是   cctv15md/segment_135107.ts     ← 相对路径
```

新版播放器按**跳转后的地址**拼分片；电视上的 TVBox 4.4 拼回**原始域名**
（`38.75.136.137:98/cctv15md/...`），那个地址不存在 → 永远转圈。
日志里能直接看到 `ijkmp_set_data_source(url=...)` 之后 TCP 已连上但无数据。

**实现**（`tvgo/live.go`，只重写清单、不转发视频字节）：

- `GET /live.txt`：现取上游列表 → 每个频道换成 `/lv?u=<base64>`
- `GET /lv?u=...`：取清单、跟随 302，按**最终地址**绝对化
  - master 的变体 → 继续走 `/lv`，下一层同样绝对化
  - 媒体清单分片：http 直连源站（不吃电视 CPU），https 走 `/lv` 透传
    （Android 4.4 的 TLS 对部分 CDN 不兼容）
  - 非 m3u8 响应原样透传，`&p=1` 可强制代理分片
- 上游镜像按实测速度排序 jsdelivr(2s) > gh-proxy(15s) > raw；启动后每 50s 后台
  预热，请求命中缓存（3–30ms），避免播放器的列表超时
- 同一频道多个地址原样保留，TVBox 会合并成多源可切换

**两个关键细节**：

- **不能带 Referer**：hndt（河南卫视）等 CDN 一带 Referer 就 403，不带 200。
  最初统一补 Referer 反而让这些台挂了，已去掉。
- **咪咕 token 会过期**：141 个咪咕频道带 `timestamp=` 签名，旧 token 返回
  **HTTP 605**。走本机代理后每次都取上游最新列表，不再用陈旧 token。

**部署**：电视上把直播源改为 `http://127.0.0.1:8787/live.txt`。设置存在 Hawk 里，
压缩推送接口可下发（`POST :9978/action`，表单 `do=liveApi` + `url=...`；
注意必须是 **POST**，GET 走的是 `/action` 的另一个分支，只处理 `refresh`，返回 ok 但什么都不做）。

**核验**（抽样 40 个频道，通过本机服务跑完整链路「清单→变体→首分片」）：

| 分组 | 通过 |
|---|---|
| 河南地方 | 8/8 |
| 组播线路 | 8/8 |
| IPV4线路 | 6/8 |
| 影音剧场 | 3/8 |
| 电影在线 | 0/8 |

三个主要电视分组 22/24（92%）。`电影在线` 全组 27 个频道是海外灰产源，服务端
也连不上（TCP 超时），属于源本身失效，非播放器问题。
电视实机：CCTV-10、CCTV-13 正常出画面，连续切台正常，服务端可见
`/lv` 请求 200（含 https 分片经 `p=1` 透传 1038ms）。

## v1.5.0 — 2026-11-05

### 移除：DNAT 地址兜底规则（TVBox 已内填 `127.0.0.1:8787`）

v1.4.0 因为改不动 TVBox 的 Hawk 加密配置，在电视内部加了一条 NAT 规则，把旧地址
`192.168.5.12:8787` 重定向到 `127.0.0.1:8787`。用户在应用内把接口地址改成
`http://127.0.0.1:8787/` 并验证可用后，这条兜底规则成为多余的一层，删掉：

- 设备侧删除 `iptables -t nat -A OUTPUT -d 192.168.5.12/32 -p tcp --dport 8787
  -j DNAT --to-destination 127.0.0.1:8787`，`nat OUTPUT` 链已清空
- `route_localnet` 还原为内核默认 `0`（该参数只服务 DNAT 到回环地址这一用途）
- `boot-tvbox.sh` 去掉「开机装规则」，改为开机**清理**可能残留的同名规则，
  避免旧规则在升级/恢复出厂后被重新装回
- `tvgo/README.md` 把「端口重定向」一节改写成「TVBox 内直填 127.0.0.1」的正确做法，
  并保留历史说明与手动删除命令

**核验**：删除后重启 TVBox，`HomeActivity` 正常出数据（首页 35 条、批量详情 23ms 命中索引），
详情页「播放地址」显示 `http://127.0.0.1:8787/v/piaoys/index.m3u8?...`，
`iptables -t nat -L OUTPUT -n` 输出为空。全程无新增 NAT 层，链路少一跳。

## v1.4.0 — 2026-11-05

### 新增：Go 版源站服务，部署到电视本机运行（`tvgo/`）

**动机**：原方案服务跑在电脑 8787 端口，电脑关机/睡眠电视就没数据。用户明确说「电脑无法当源服务器」。

**选型**：电视是 Android 4.4.3 / armv7。CPython 没有 4.4 的构建（交叉编译要 NDK + bionic 补丁，`ssl`/`socket`/`ctypes` 全是坑，体积 40MB 起），Termux 要求 Android 5.0+。Go 一条命令产出 5.9MB 静态二进制（`CGO_ENABLED=0` 不依赖 libc），且自带 TLS 栈，反而绕开了 Android 4.4 只支持老密码套件的问题。

**实现**：把 `src/` 下全部 JS 逻辑逐函数移植到 Go（标准库，零外部依赖），保持接口契约完全一致：

- `main.go` 路由、配置、`/img` 封面代理、`/v/` 清单代理、VOD_INDEX
- `maccms.go` 信封（`action` 必须缺席、`group_id`/`type_id_1` 必须是数字）
- `fofoys.go` / `piaoys.go` / `xcyycn.go` 三个站点解析
- JS 正则用了后行断言（`(?<![\w-])`、`(?<!data-)`），Go 的 RE2 不支持，
  改成「属性前必须是空白或标签开头」，效果等价

**部署**：

- 二进制 `/data/local/tmp/tvbox44`，脚本 `/data/local/tmp/boot-tvbox.sh`
- 开机自启挂 `/system/etc/install-recovery.sh`（init.rc 的 `class main` oneshot）。
  该文件原本是 **644 没有执行位**，init 一直 `execve: Permission denied`，
  补 `chmod 755` 后才真正生效；原工厂看门狗逻辑完整保留
- TVBox 的接口地址存在 Hawk 里（AES 加密），推送服务 `do=api` 在源码里
  只处理 `do=refresh&type=danmaku`，其余直接返回 ok 什么都不做——
  这条改配置的路是假象。改用 DNAT 在电视内部把旧地址
  `192.168.5.12:8787` 导到 `127.0.0.1:8787`，**TVBox 配置一个字不用改**

**踩坑**（桌面调试完全看不出来，只有交叉编译到 ARM 才暴露）：

1. Android 没有 `/etc/resolv.conf`，Go 解析器直接失败 → 启动时 `getprop net.dns1` 取 DNS，塞进自定义 `net.Resolver`
2. Go 的 `crypto/x509` 只认桌面 CA 路径，Android 的根证书在 `/system/etc/security/cacerts/*.0` → 内嵌 Mozilla CA 包 + 叠加系统目录；另加「证书校验失败的请求降级重试」兜底
3. 4.4 没有 `killall`/`awk`/`sed`/`head`/`tail`/`wc`/`which`/`printf`/`nohup`/`setsid` → 脚本只用 `ps | grep` + shell 分词
4. 内核 netfilter 不支持 `REDIRECT` 目标 → 只能用 `DNAT`，且必须开 `route_localnet`

**核验**：

- 首页/分类/搜索/详情/播放清单，与 Node 版输出**逐字一致**（连源站那两个粘连的片名都一样）
- 三个源首页：fofoys 35 条、piaoys 65 条、xcyycn 122 条
- 批量详情（首页 35 个 id）命中索引 17-22ms
- 封面 `/img?u=` 正常回 JPEG；播放清单 200 且相对路径已绝对化
- **关掉电脑 8787 服务后电视重启，仍完整可用**
- 电视重启后进程自动拉起（root，PID 稳定）、iptables 规则自动恢复

## v1.3.0 — 2026-11-05

### 修复：三个源的列表/首页海报点确定键完全没反应

**现象**：遥控确定键点海报毫无反应，详情页打不开；连点两下反而跳到搜索页。三个源（fofo影视、飘花影视、星辰影院）的首页推荐位和分类列表全部如此。

**根因**：`normalizeVod()` 给每条数据都输出了 `action: ""`。TVBox 的列表点击回调第一句是

```java
if (video.action != null) {          // "" != null → 成立
    sourceViewModel.action(sourceKey, video.action);
    return;                          // 直接 return，详情页压根不打开
}
```

空字符串不是 `null`，于是每次点击都被当成「执行动作」，执行完就返回：既不发详情请求，也不跳转，表现为「按了没反应」。用户以为没点上再按一次，被 TVBox 识别成列表长按，长按走的是 `FastSearchActivity`，于是「连点两下跳到搜索页」。

**修复**：`VOD_DEFAULTS` 移除 `action`，并新增 `OMIT_FIELDS` 在 `normalizeVod()` 末尾 `delete` 该键——JSON 里完全没有这个键，Gson 才会给出 `null`，点击才走 `jumpActivity(DetailActivity)`。

**核验**：电视实测按确定键 → `DetailActivity` 打开 → 服务端收到单个 `?ac=detail&ids=tvbox...` → 详情页 9 条线路、17 集全部列出 → 自动预览触发 `GET /v/fofoys/index.m3u8`。

### 优化：首页加载从 20 秒降到 10 秒内

TVBox 首页会紧跟着发一次 `?ac=detail&ids=一长串` 把推荐位换成完整详情。之前每次都真去抓十几个源站详情页，fofo 实测 11.5 秒，首页长时间转圈，用户以为点不动。

新增 `VOD_INDEX`（id → vod 浅拷贝，上限 1200 条 LRU）：首页/分类/搜索拿到列表时就登记，批量详情请求先查索引，命中直接返回（只用于渲染卡片，点进详情时 TVBox 会重新按单个 id 请求）。

- fofo：11521ms → 3ms
- 飘花：1039ms → 2ms
- 星辰：2305ms → 2ms

注意存的是副本：`rewritePics()` 会原地改写 `vod_pic`，存引用会导致图片被二次套上 `/img?u=` 代理。

### 测试

`node test/api.mjs` → 19 项通过，0 失败。

## v1.2.0 — 2026-11-05

- 封面图统一走本机 `/img?u=` 代理（豆瓣补 Referer、站点补 Referer、12 路并发闸门、7s 超时重试、240 张 LRU、失败 302 回原图）。
- 详情支持批量 ids（最多 24，5 路并发、保持顺序、第二轮补漏），避免 TVBox 判「本源无详情」而走多源搜索。

## v1.1.0 — 2026-11-05

- type=1 播放改为 `.m3u8` 直链 + `/v/{site}/index.m3u8` 清单代理（TVBox 对 type=1 不请求 `?play=`，只认 `isVideoFormat()` 命中的直链）。
- 新增 `server.mjs` 局域网 HTTP 服务：电视（Android 4.4）无法与 `pages.dev`/`workers.dev` 完成 TLS 握手，改由本机 8787 端口提供。

## v1.0.0

- 三站点 type=4 接口（首页/分类/搜索/详情/播放）。
