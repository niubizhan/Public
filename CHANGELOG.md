# 变更记录

TVBox 4.4 type=1 接口（fofo影视 / 飘花影视 / 星辰影院），部署形态为 Cloudflare Worker + 本机局域网 HTTP 服务（`server.mjs`，端口 8787）。

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
