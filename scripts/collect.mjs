/**
 * 定时采集脚本（GitHub Actions / 本地均可运行）
 *   node scripts/collect.mjs                       # 直连模式：本地直接抓源站
 *   TVBOX_API_BASE=https://xxx.pages.dev node ...   # 远程模式：走已部署的 Worker 接口
 *
 * 产出 data 目录：
 *   data/summary.json、data/{站点}/home.json、category-*.json、search-*.json、
 *   detail-sample.json、play-sample.json（含 m3u8 直链）
 *
 * 为什么 Actions 上要用远程模式：fofoys 对境外 IP 返回 403，GitHub runner 在美国
 * 会被源站直接拒绝；已部署的 Worker 出口在 Cloudflare，fofoys/piaoys 都能访问。
 * xcyycn 的源站对境外时通时断（实测 55 个境外节点里约 4 成超时），因此本脚本
 * 采用「分步容错」：任何一步失败只记录错误，不影响已采到的数据。
 */
import { mkdir, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { SITES } from '../src/sites/index.js';

const KEYWORDS = ['庆余年', '斗罗大陆'];
const DATA_DIR = fileURLToPath(new URL('../data/', import.meta.url));
const API_BASE = (process.env.TVBOX_API_BASE || '').replace(/\/+$/, '');

async function save(dir, name, data) {
  await writeFile(`${dir}/${name}`, JSON.stringify(data, null, 2), 'utf8');
}

function firstToken(vod) {
  const group = (vod.vod_play_url || '').split('$$$')[0] || '';
  const ep = group.split('#')[0] || '';
  return ep.split('$')[1] || '';
}

/** 远程模式：所有请求打到已部署的 Worker（TVBox 4.4 type=4 接口）
 *  超时给 100s——Worker 侧对上游最多重试 3 次 × 30s，客户端要能等完整条重试链。
 *  不在客户端再重试：Worker 已经重试过上游，避免故障站点拖太久。 */
function remoteSource(key) {
  const call = async (qs) => {
    const url = `${API_BASE}/api/${key}${qs}`;
    const res = await fetch(url, {
      headers: { Accept: 'application/json', 'User-Agent': 'tvbox44-collect/1.0' },
      signal: AbortSignal.timeout(100000),
    });
    if (!res.ok) throw new Error(`HTTP ${res.status} ${url}`);
    const j = await res.json();
    if (j.code === 0) throw new Error(`api error: ${j.msg || 'unknown'}`);
    if (j.msg && String(j.msg).startsWith('error:')) throw new Error(String(j.msg).slice(7));
    return j;
  };
  return {
    mode: 'remote',
    home: async () => {
      const j = await call('?filter=true');
      return { classes: j.class || [], list: j.list || [] };
    },
    category: async (tid) => (await call(`?ac=detail&t=${encodeURIComponent(tid)}&pg=1`)).list || [],
    search: async (kw) => (await call(`?wd=${encodeURIComponent(kw)}&ac=detail&quick=true`)).list || [],
    detail: async (id) => {
      const j = await call(`?ac=detail&ids=${encodeURIComponent(id)}`);
      const vod = (j.list || [])[0];
      if (!vod) throw new Error(`empty detail: ${id}`);
      return vod;
    },
    play: async (token) => {
      const j = await call(`?play=${encodeURIComponent(token)}&flag=collect`);
      if (!j.url) throw new Error('play returned empty url');
      return j.url;
    },
  };
}

/** 直连模式：直接调用与 Worker 相同的零 JS 适配器 */
function localSource(site) {
  return {
    mode: 'local',
    home: () => site.home(),
    category: async (tid) => (await site.category(tid, 1)).list || [],
    search: async (kw) => (await site.search(kw, 1)).list || [],
    detail: (id) => site.detail(id),
    play: (token) => site.play(token, 'collect'),
  };
}

const summary = [];
const started = Date.now();

for (const site of SITES) {
  const src = API_BASE ? remoteSource(site.key) : localSource(site);
  const row = {
    site: site.key,
    name: site.name,
    mode: src.mode,
    home: 0,
    categories: 0,
    search: 0,
    detail: 0,
    play: '',
    errors: [],
  };
  const dir = `${DATA_DIR}/${site.key}`;
  await mkdir(dir, { recursive: true });
  // 连续失败 3 步就判定该站当前不可达，立刻止损（境外访问受限的站点不会拖垮整体）
  let broken = false;
  const note = (label, e) => {
    row.errors.push(`${label}: ${(e && e.message) || e}`);
    if (row.errors.length >= 3) broken = true;
  };

  // 1) 首页：分类导航 + 推荐位（失败则退回适配器内置的静态分类）
  let classes = [];
  if (!broken) {
    try {
      const home = await src.home();
      row.home = home.list.length;
      await save(dir, 'home.json', home);
      classes = home.classes || [];
    } catch (e) {
      note('home', e);
    }
  }
  if (!classes.length) {
    classes = (site.types || []).map((t) => ({ type_id: t.id, type_name: t.name }));
  }

  // 2) 每个分类独立采集，互不影响
  for (const cls of classes) {
    if (broken) break;
    const tid = cls.type_id ?? cls.id;
    try {
      const list = await src.category(tid);
      row.categories += list.length;
      await save(dir, `category-${tid}.json`, list);
    } catch (e) {
      note(`category-${tid}`, e);
    }
  }

  // 3) 搜索独立采集
  for (const kw of KEYWORDS) {
    if (broken) break;
    try {
      const list = await src.search(kw);
      row.search += list.length;
      await save(dir, `search-${kw}.json`, list);
    } catch (e) {
      note(`search-${kw}`, e);
    }
  }

  // 4) 详情抽样 + 5) 播放直链抽样（懒解析链路验证）
  if (!broken) {
    try {
      const cat = await src.category(classes[0].type_id ?? classes[0].id);
      if (!cat.length) throw new Error('first category is empty');
      const vod = await src.detail(cat[0].vod_id);
      row.detail = 1;
      await save(dir, 'detail-sample.json', vod);
      const token = firstToken(vod);
      if (token) {
        const url = await src.play(token);
        row.play = url;
        await save(dir, 'play-sample.json', { token, url, vod_name: vod.vod_name });
      }
    } catch (e) {
      note('sample', e);
    }
  }

  if (broken) row.errors.push('判定：该站点当前不可达（连续失败 3 步），已跳过剩余步骤');

  row.error = row.errors.join(' | ');
  summary.push(row);
  console.log(
    `[${site.key}] mode=${row.mode} home=${row.home} cat=${row.categories} search=${row.search} detail=${row.detail} play=${row.play ? 'OK' : '-'}${row.error ? `  ERR: ${row.error}` : ''}`
  );
}

const withData = summary.filter((s) => s.home > 0 || s.categories > 0 || s.search > 0).length;
const result = {
  generatedAt: new Date().toISOString(),
  mode: API_BASE ? `remote:${API_BASE}` : 'local',
  durationMs: Date.now() - started,
  sites: summary,
};
await save(DATA_DIR.replace(/\/$/, ''), 'summary.json', result);

console.log(
  `\n完成：${withData}/${summary.length} 个站点采到数据，耗时 ${Math.round(result.durationMs / 1000)}s（模式：${result.mode}）`
);
// 有数据就算成功（境外访问受限时保留部分结果），全部失败才返回非 0
process.exit(withData > 0 ? 0 : 1);
