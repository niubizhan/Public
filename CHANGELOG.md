# 变更记录

TVBox 4.4 type=1 接口（fofo影视 / 飘花影视 / 星辰影院），部署形态为 Cloudflare Worker + 本机局域网 HTTP 服务（`server.mjs`，端口 8787）；自 v1.4.0 起另有 Go 版可**直接跑在电视上**（`tvgo/`），不再依赖电脑。

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
