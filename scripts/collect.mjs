/**
 * 定时采集脚本（GitHub Actions / 本地均可运行）
 *   node scripts/collect.mjs
 *
 * 作用：用与 Worker 完全相同的零 JS 适配器，把三个站点的
 * 首页 / 分类 / 搜索 / 详情 / 播放直链 采集下来，落盘到 data 目录的 json 文件。
 * GitHub Actions 会定时执行本脚本并把结果提交回仓库。
 */
import { mkdir, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { SITES } from '../src/sites/index.js';

const KEYWORDS = ['庆余年', '斗罗大陆'];
const DATA_DIR = fileURLToPath(new URL('../data/', import.meta.url));

async function save(dir, name, data) {
  const path = `${dir}/${name}`;
  await writeFile(path, JSON.stringify(data, null, 2), 'utf8');
  return path;
}

function firstToken(vod) {
  const group = (vod.vod_play_url || '').split('$$$')[0] || '';
  const ep = group.split('#')[0] || '';
  return ep.split('$')[1] || '';
}

const summary = [];
const started = Date.now();

for (const site of SITES) {
  const row = { site: site.key, name: site.name, home: 0, categories: 0, search: 0, detail: 0, play: '', error: '' };
  const dir = `${DATA_DIR}/${site.key}`;
  await mkdir(dir, { recursive: true });

  try {
    // 1) 首页：分类导航 + 推荐位
    const home = await site.home();
    row.home = home.list.length;
    await save(dir, 'home.json', home);

    // 2) 每个分类的第 1 页
    for (const cls of home.classes) {
      const tid = cls.id ?? cls.type_id;
      const list = await site.category(tid, 1);
      row.categories += list.list.length;
      await save(dir, `category-${tid}.json`, list.list);
    }

    // 3) 搜索
    for (const kw of KEYWORDS) {
      const r = await site.search(kw, 1);
      row.search += r.list.length;
      await save(dir, `search-${kw}.json`, r.list);
    }

    // 4) 详情抽样（取第一个分类的第一条）
    const firstClass = home.classes[0];
    const cat = await site.category(firstClass.id ?? firstClass.type_id, 1);
    if (cat.list.length) {
      const vod = await site.detail(cat.list[0].vod_id);
      row.detail = 1;
      await save(dir, 'detail-sample.json', vod);

      // 5) 播放直链抽样（懒解析链路验证）
      const token = firstToken(vod);
      if (token) {
        const url = await site.play(token);
        row.play = url;
        await save(dir, 'play-sample.json', { token, url, vod_name: vod.vod_name });
      }
    }
  } catch (e) {
    row.error = String((e && e.message) || e);
  }

  summary.push(row);
  console.log(
    `[${site.key}] home=${row.home} cat=${row.categories} search=${row.search} detail=${row.detail} play=${row.play ? 'OK' : '-'} ${row.error}`
  );
}

const result = {
  generatedAt: new Date().toISOString(),
  durationMs: Date.now() - started,
  sites: summary,
};
await save(DATA_DIR.replace(/\/$/, ''), 'summary.json', result);

const failed = summary.filter((s) => s.error);
console.log(`\n完成：${summary.length} 个站点，耗时 ${Math.round(result.durationMs / 1000)}s${failed.length ? `，${failed.length} 个出错` : ''}`);
process.exit(failed.length ? 1 : 0);
