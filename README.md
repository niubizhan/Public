# TVBox 4.4 零 JS 爬虫（Cloudflare Workers / Pages）

把 **fofo影视（fofoys.com）**、**飘花影视（piaoys.com）**、**星辰影院（ixcyy.tv，原 xcyycn.cc）**
三个网站改造成 **TVBox 4.4 的 `type=4` HTTP JSON 接口**，可直接部署到
Cloudflare Workers 或 Cloudflare Pages，全程 **零 JS 执行**（不跑浏览器、不执行目标站 JS、
无 headless、无 `eval`/`new Function`），只做「HTTP 抓取 + 正则/字符串解析」。

> 接口规范不是猜的，是按仓库自带的 `TVBoxOS-main` 源码逐条对齐的：
> - `app/src/main/java/com/github/tvbox/osc/viewmodel/SourceViewModel.java`（首页/分类/搜索/详情/播放五类请求的入参）
> - `.../bean/AbsJson.java`、`.../bean/AbsSortJson.java`（响应 JSON 字段）
> - `.../api/ApiConfig.java`（配置 JSON 里 `sites` 的字段要求）
> - `.../bean/SourceBean.java`（`type`、`api`、`ext`、`searchable`…）

---

## 一、能力总览

| 站点 | 源站形态 | 首页 | 分类 | 搜索 | 详情 | 播放直链 |
|---|---|---|---|---|---|---|
| fofo影视 `fofoys` | Next.js SSR | ✅ 5 分类 / 35 条 | ✅ `/type/{slug}` | ✅ `/search?q=` | ✅ `var urlList` 内嵌播放组 | ✅ `POST /source/` |
| 飘花影视 `piaoys` | 苹果 CMS V10 (stui) | ✅ 5 分类 / 69 条 | ✅ `/sort/{id}.html` | ✅ `/sou/----.html?wd=` | ✅ `stui-content__playlist` | ✅ `var player_data.url` |
| 星辰影院 `xcyycn` | 苹果 CMS V10 (shoutu46) | ✅ 5 分类 / 120 条 | ✅ `/v/{id}.html` | ✅ `/s.html?wd=`（含翻页） | ✅ `.anthology-tab` 播放组 | ✅ `var player_aaaa.url` |

三个站全部走同一套 Worker 代码，站点差异全部收敛在 `src/sites/*.js` 适配器里。

---

## 二、接口规范（TVBox 4.4 `type=4`）

部署后有一个统一入口 `https://<你的域名>/api/<site>`，TVBox 按参数自动区分五种请求：

| 动作 | TVBox 发出的请求 | 本服务返回 |
|---|---|---|
| 首页（分类导航 + 推荐位） | `GET {api}?filter=true` | `{"class":[{type_id,type_name}],"list":[vod…]}` |
| 分类列表 | `GET {api}?ac=detail&t={type_id}&pg={page}` | `{"code":1,"page":N,"pagecount":N,"total":N,"list":[vod…]}` |
| **搜索** | `GET {api}?wd={关键词}&ac=detail&quick=true` | 同上（`list` 为搜索结果） |
| 详情 | `GET {api}?ac=detail&ids={vod_id}` | 同上（单条，含 `vod_play_from` / `vod_play_url`） |
| 播放 | `GET {api}?play={token}&flag={线路名}` | `{"url":"…m3u8","parse":0,"header":{…}}` |

`vod` 字段完全按 `AbsJson.AbsJsonVod` 输出（`vod_id/type_id/type_name/vod_name/vod_pic/
vod_remarks/vod_year/vod_area/vod_actor/vod_director/vod_content/vod_play_from/vod_play_url…`，全部为字符串）。

**播放采用“懒解析”**：详情接口只回传站点播放页 token（`ff:9879220`、`py:play/502680-2-1.html`、
`xc:p/179223-2-1.html`），TVBox 真正点播时才请求 `?play=`，由 Worker 现场去源站解出 m3u8 直链。
这样一部 40 集 × 10 条线路的剧，详情接口也只需要 1 次上游请求，稳稳落在 Cloudflare 免费版
每次请求 50 个子请求的限制之内。

### 配置接口

`GET https://<你的域名>/` 直接返回一份可用的 TVBox 4.4 配置：

```json
{
  "spider": "",
  "wallpaper": "",
  "sites": [
    { "key": "fofoys", "name": "fofo影视", "type": 4,
      "api": "https://xxx.workers.dev/api/fofoys",
      "searchable": 1, "quickSearch": 1, "filterable": 0, "changeable": 1, "timeout": 20 },
    { "key": "piaoys", "name": "飘花影视", "type": 4, "api": "https://xxx.workers.dev/api/piaoys", "...": "..." },
    { "key": "xcyycn", "name": "星辰影院", "type": 4, "api": "https://xxx.workers.dev/api/xcyycn", "...": "..." }
  ],
  "lives": [], "parses": [], "flags": [], "ads": []
}
```

把 `https://<你的域名>/` 填进 TVBox 4.4 的「配置地址 / 接口地址」即可；
也可以只把其中某一个 `api` 填到自定义站点里（`type` 选 `4`）。

`GET /api` 返回接口自述（站点清单 + 五类端点说明），方便自查。

---

## 三、部署到 Cloudflare

> **已部署示例（本项目实测可用）**
>
> | 入口 | 地址 | 状态 |
> |---|---|---|
> | **Cloudflare Pages（推荐）** | `https://tvbox44-crawler.pages.dev/` | ✅ 首页/分类/搜索/详情/播放全通 |
> | Cloudflare Workers | `https://tvbox44-crawler.562347955.workers.dev/` | ✅ 部署成功（`*.workers.dev` 在大陆被 DNS 污染，需代理或绑自有域名才能访问） |
>
> TVBox 里直接填 **`https://tvbox44-crawler.pages.dev/`** 即可。
> 想换成自己的域名：Workers → Settings → Domains & Routes → Add Custom Domain。

### 方式 A：Cloudflare Workers（推荐）

**A1. 命令行（wrangler）**

```bash
npm install
npx wrangler login        # 首次需要授权
npm run deploy            # = wrangler deploy，输出 https://tvbox44-crawler.<子域>.workers.dev
```

**A2. Dashboard 手动上传**

Cloudflare 控制台 → Workers & Pages → Create → **Upload**，把本仓库打包成 zip 上传，
入口文件选 `src/index.js`（`wrangler.toml` 已配置 `main = "src/index.js"`）。

### 方式 B：Cloudflare Pages

**B1. Dashboard 用 GitHub 仓库部署（推荐）**

1. 把本仓库推到 GitHub（见第四节）；
2. Cloudflare 控制台 → Workers & Pages → Create → Pages → **Connect to Git** → 选中仓库；
3. 构建设置：Framework preset = **None**，Build command 留空，**Output directory = `public`**；
4. Cloudflare 会自动识别 `functions/[[path]].js`（Pages Functions），它直接复用 `src/` 下的同一份代码；
5. 部署完成后访问 `https://tvbox44-crawler.pages.dev/`。

**B2. 命令行**

```bash
npx wrangler pages deploy public --project-name=tvbox44-crawler
```

> Workers 与 Pages 用的是**同一份** `src/index.js`：Workers 通过 `wrangler.toml` 的 `main`
> 引入，Pages 通过 `functions/[[path]].js` 的 `import worker from '../src/index.js'` 引入，
> 两边行为完全一致。

### 方式 C：GitHub Actions 自动部署

`.github/workflows/deploy.yml` 已写好：推送到 `main`/`master` → 自动跑测试 → 部署 Workers。

用到两个 Secrets（仓库 Settings → Secrets and variables → Actions）：

| Secret | 获取方式 |
|---|---|
| `CLOUDFLARE_API_TOKEN` | Cloudflare 控制台 → My Profile → API Tokens → Create Token → 模板「Edit Cloudflare Workers」 |
| `CLOUDFLARE_ACCOUNT_ID` | Workers & Pages 首页右侧栏复制 |

需要同时发 Pages 的话，把 workflow 里 `deploy-pages` 那段注释解开即可
（先在 Dashboard 建好同名 Pages 项目）。

---

## 四、推送到 GitHub

本仓库已经 `git init` 并提交，推送只需（`<你的仓库地址>` 换成自己的）：

```bash
git remote add origin https://github.com/<你的用户名>/tvbox44-crawler.git
git branch -M main
git push -u origin main
```

没有装 `gh` CLI 的话，先在 GitHub 网页新建一个空仓库（**不要**勾选 README/.gitignore），
再执行上面三条即可。

### 在 GitHub 上直接运行采集（Actions 定时任务）

**GitHub 不托管 Worker**——接口运行时在 Cloudflare，GitHub 负责存代码 + 跑流水线。
要让 GitHub「自己跑采集」，用仓库里现成的两个文件：

| 文件 | 作用 |
|---|---|
| `scripts/collect.mjs` | 零依赖采集脚本：用与 Worker 相同的适配器抓 首页/分类/搜索/详情/播放直链，落盘 `data/**.json` |
| `.github/workflows/collect.yml` | 每天 **UTC 18:00（北京时间 02:00）** 自动执行采集，并把结果 commit 回仓库；也支持在 Actions 页手动 Run |

推上去之后：

1. 仓库 **Settings → Actions → General → Workflow permissions** 选 **Read and write permissions**（否则 bot 无权 push）；
2. 打开 **Actions** 标签 → 选 `定时采集` → **Run workflow** 手动跑一次验证；
3. 之后每天自动跑，产物在仓库 `data/` 目录：

```
data/summary.json                    # 本次采集概览（耗时、条数、播放直链）
data/fofoys/home.json                # 首页推荐
data/fofoys/category-film.json       # 各分类列表
data/fofoys/search-庆余年.json        # 搜索结果
data/fofoys/detail-sample.json       # 详情样例（含播放组）
data/fofoys/play-sample.json         # 解析出的 m3u8 直链
```

本地立即跑一次：

```bash
npm run collect     # = node scripts/collect.mjs，3 个站点约 1 分钟
```

> 想把采集结果做成网页看，再开一个 **GitHub Pages**（Source 选 `main` 分支 `/docs` 或用 Actions 发布）即可；
> 接口本身仍然部署在 Cloudflare，两边互不影响。

#### 境外可达性实测（决定采集该走哪条路）

用 check-host 的 55 个境外节点 + Cloudflare Worker 出口实测三个源站：

| 源站 | 境外直连 | 经 Cloudflare Worker | 结论 |
|---|---|---|---|
| **fofoys.com** | 全部 **403**（Cloudflare 地区封锁） | ✅ 正常 | 只能走 Worker（Cloudflare→Cloudflare 放行） |
| **piaoys.com** | **55/55 全通** | ✅ 正常 | 直连、走 Worker 都行 |
| **ixcyy.tv**（原 `xcyycn.cc`） | 2026-11 起 `xcyycn.cc` 整站 301 到 `ixcyy.tv`；旧域名对 Cloudflare 直接 444 | ✅ 新域名 200 正常 | 云端可直接采集 |

因此：

- **GitHub Actions 采集**用 `TVBOX_API_BASE`（远程模式）→ fofoys + piaoys 稳定拿到数据，
  xcyycn 会记录 `HTTP 522` 错误并跳过（**分步容错 + 连续失败 3 步止损**，不会卡死整个任务）；
  上次成功的 `data/xcyycn/*.json` 会保留，不会被清空。
- **要拿到 xcyycn 的完整数据**，在能访问它的网络（国内）跑一次直连采集再推上去：
  ```bash
  npm run collect     # 直连模式，三站全量
  git add data && git commit -m "chore: 本地全量采集" && git push
  ```
- 源站风控是动态的，某天 xcyycn 恢复可达时，Actions 会自动把它采回来。

---

## 五、本地测试

```bash
npm run test:api     # Worker 入口集成测试：配置 + 五类请求分发（19 项，真实访问源站）
npm run test:live    # 三个站点端到端：首页/分类/搜索/详情/播放全部打真实 m3u8（19 项）
npm test             # 上面两个都跑
```

本地开发：

```bash
npm run dev          # wrangler dev -> http://127.0.0.1:8787
```

直接试：

```bash
curl "http://127.0.0.1:8787/"                                   # TVBox 配置
curl "http://127.0.0.1:8787/api/piaoys?filter=true"              # 首页
curl "http://127.0.0.1:8787/api/piaoys?wd=庆余年&ac=detail&quick=true"   # 搜索
curl "http://127.0.0.1:8787/api/xcyycn?ac=detail&ids=179223"    # 详情
```

---

## 六、目录结构

```
tvbox44-crawler/
├── src/
│   ├── index.js              # Worker 入口：路由 + TVBox 4.4 配置 + type=4 请求分发
│   ├── maccms.js             # AbsJson / AbsSortJson 响应封装与字段补全
│   ├── util.js               # 零 JS 工具：HTTP 抓取(带缓存)、实体解码、JS 字面量→JSON、括号配平
│   └── sites/
│       ├── index.js          # 站点注册表
│       ├── fofoys.js         # fofo影视（Next.js SSR，var urlList + POST /source/）
│       ├── piaoys.js         # 飘花影视（苹果CMS stui，player_data）
│       └── xcyycn.js         # 星辰影院（苹果CMS shoutu46，player_aaaa）
├── functions/[[path]].js     # Cloudflare Pages Functions 入口（复用 src/index.js）
├── test/
│   ├── api.mjs               # Worker 入口集成测试
│   └── live.mjs              # 三站点真实抓取联调测试
├── public/.gitkeep           # Pages 输出目录占位
├── .github/workflows/deploy.yml   # GitHub Actions 自动测试 + 部署
├── wrangler.toml             # Workers 配置
├── package.json
└── README.md
```

---

## 七、踩过的坑（写在代码注释里了，这里再强调一次）

1. **请求头不能带 `application/json` 的 Accept**：飘花影视检测到后会返回
   `{"…":"\u98d8\u82b1…"}` 这种 JSON 转义过的 HTML，正则会全部失配。
2. **`class` 属性可能换行**：fofo影视标题 span 的 class 是跨行输出的，
   不能写死 `"<span class="` 直连匹配。
3. **演员标签和标题长得像**：fofo 卡片里演员 chip 也是 `antialiased`，标题要用 `font-bold` 区分。
4. **源站分类页普遍没有分页器**（fofo / 飘花 / 星辰的分类都只有第 1 页数据，
   `pagecount=1` 让 TVBox 不会去翻空页）；星辰的**搜索**分页是好的，
   已按 `/s{关键词}/page/{N}.html` 支持。

---

## 九、零服务器方案：TVBox 内置 Python 爬虫（推荐国内用户）

如果你的设备访问 `pages.dev` 报 **SSL 握手失败**（`sslv3 alert handshake failure`），
或网络不稳，可以直接用 TVBox 自带的 Python 运行时（Chaquopy）在**设备本机**跑爬虫——
不需要 Cloudflare、不需要任何服务器，三个源站在国内 IP 下都能直连。

### 文件

| 文件 | 作用 |
|---|---|
| `tools/tvbox44_template.py` | 单一源码模板（`SITE` 占位符） |
| `tools/build_pys.py` | 生成三个独立站点文件（**必须分文件**：TVBox 的 `Spider` 是单例，共用一个 .py 会站点串台） |
| `tvbox44_fofoys.py` / `tvbox44_piaoys.py` / `tvbox44_xcyycn.py` | 生成产物，TVBox 自动下载执行 |
| `tvbox-py.json` | TVBox 4.4 配置（`type: 3` + `api` 指向 .py） |
| `test/pyenv/base/spider.py` | PC 联调用的 `base.spider` 替身 |
| `test/py_spider.py` | 真实站点联调：`python test/py_spider.py`（15 项全过） |
| `test/py_pics.py` | 封面图可达性检查：`python test/py_pics.py` |

### TVBox 配置地址（py 版）

```
https://gh-proxy.com/https://raw.githubusercontent.com/niubizhan/Public/main/tvbox-py.json
```

或手动加站点（设置 → 自定义源 / 接口）：

```json
{ "key": "py_fofoys", "name": "fofo影视", "type": 3,
  "api": "https://gh-proxy.com/https://raw.githubusercontent.com/niubizhan/Public/main/tvbox44_fofoys.py",
  "searchable": 1, "quickSearch": 1, "filterable": 1 }
```

> `.py` 直连地址（能直连 GitHub 的网络用）：
> `https://raw.githubusercontent.com/niubizhan/Public/main/tvbox44_fofoys.py`

### 改代码的流程

```bash
vim tools/tvbox44_template.py    # 只改模板
python tools/build_pys.py        # 重新生成三个站点文件
python test/py_spider.py         # 真实站点联调（15 项）
git add -A && git commit -m "fix: py 爬虫解析" && git push
```

### 关于 `SSL handshake aborted / sslv3 alert handshake failure`

实测：`*.pages.dev` **只接受 TLS 1.2 + SNI**，TLS 1.1 或不带 SNI 的握手会被直接回
`handshake_failure`——老安卓（≤5.x，不支持 TLS1.2 或不发 SNI）就会报这个错。
排查顺序：

1. 设备 **设置 → 关于 → Android 版本**（7.1+ 一般没问题）；
2. TVBox **设置里是否开了代理**、系统是否挂了 VPN/代理（代理做 TLS 拦截同样报这个）；
3. 把配置地址换成上面的 **gh-proxy 链接**再试——能打开说明只是 Cloudflare 的 SNI 要求；
4. 若所有 HTTPS 都报 SSL 错，说明设备 TLS 整体不可用，只能用 **HTTP 明文**入口
   （自建反代，例如 `http://你的服务器/`）。

---

## 八、已知限制与免责

- 三个源站的分类页均为单页数据（源站本身不提供分类翻页），分类最多展示源站给出的几十条。
- 播放直链由源站播放页决定，源站换域名/加防盗链时，只需改对应 `src/sites/*.js` 里的解析正则。
- 本项目仅用于学习 HTTP 抓取与 TVBox 接口协议；请勿用于商业用途，
  版权归各影视站点所有，如有侵权请提交 Issue 立即移除。
