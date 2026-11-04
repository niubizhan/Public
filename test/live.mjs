/**
 * 真实站点联调测试（会访问三个目标站，需要联网）
 *   node test/live.mjs
 *
 * 覆盖 TVBox 4.4 type=4 的全部五个动作：首页 / 分类 / 搜索 / 详情 / 播放。
 */
import assert from 'node:assert';
import { SITES } from '../src/sites/index.js';

const FIRST_TYPE = { fofoys: 'film', piaoys: '1', xcyycn: '1' };
const SEARCH_KW = '庆余年';

let pass = 0;
let fail = 0;

async function step(name, fn) {
  try {
    await fn();
    pass++;
    console.log(`  OK   ${name}`);
  } catch (e) {
    fail++;
    console.log(`  FAIL ${name} -> ${e.message}`);
  }
}

async function checkPlay(site, vod, label) {
  assert(vod.vod_play_from, `${label}: 缺少 vod_play_from`);
  assert(vod.vod_play_url, `${label}: 缺少 vod_play_url`);
  const firstGroup = vod.vod_play_url.split('$$$')[0];
  const firstEp = firstGroup.split('#')[0];
  const token = firstEp.split('$')[1];
  assert(token, `${label}: 播放 token 解析失败: ${firstEp}`);
  const url = await site.play(token);
  assert(/^https?:\/\//i.test(url), `${label}: 播放地址不是 http(s): ${url}`);
  console.log(`       播放(${label}) -> ${url.slice(0, 100)}`);
  return url;
}

for (const site of SITES) {
  console.log(`\n== ${site.key} (${site.name}) ==`);
  let catList = [];
  let searchList = [];

  await step('首页 home()', async () => {
    const r = await site.home();
    assert(Array.isArray(r.classes) && r.classes.length >= 3, `分类数异常: ${r.classes && r.classes.length}`);
    assert(r.list.length > 0, '首页推荐列表为空');
    const v = r.list[0];
    assert(v.vod_id && v.vod_name && v.vod_pic, `首页字段缺失: ${JSON.stringify(v).slice(0, 200)}`);
    console.log(`       首页 ${r.classes.length} 分类 / ${r.list.length} 条，示例：${v.vod_name}`);
  });

  await step('分类 category()', async () => {
    const r = await site.category(FIRST_TYPE[site.key], 1);
    assert(r.list.length > 0, '分类列表为空');
    catList = r.list;
    console.log(`       分类 ${r.list.length} 条，示例：${r.list[0].vod_name}`);
  });

  if (site.key === 'xcyycn') {
    await step('搜索第 2 页（源站分页可用）', async () => {
      const p1 = await site.search(SEARCH_KW, 1);
      const p2 = await site.search(SEARCH_KW, 2);
      assert(p2.list.length > 0, '搜索第 2 页为空');
      assert(p2.list[0].vod_id !== p1.list[0].vod_id, '搜索第 2 页与第 1 页相同');
      console.log(`       搜索第2页 ${p2.list.length} 条，示例：${p2.list[0].vod_name}`);
    });
  }

  await step('分类第 2 页（源站单页，应返回空）', async () => {
    const r = await site.category(FIRST_TYPE[site.key], 2);
    assert(Array.isArray(r.list), '返回结构异常');
    console.log(`       第2页返回 ${r.list.length} 条（源站无分页，TVBox pagecount=1 不会翻页）`);
  });

  await step(`搜索 search("${SEARCH_KW}")`, async () => {
    const r = await site.search(SEARCH_KW, 1);
    assert(r.list.length > 0, '搜索结果为空');
    searchList = r.list;
    console.log(`       搜索 ${r.list.length} 条，示例：${r.list[0].vod_name}`);
  });

  await step('详情 detail(分类第一条)', async () => {
    const id = catList[0].vod_id;
    const vod = await site.detail(id);
    assert(vod.vod_name, '缺少片名');
    assert(vod.vod_pic, '缺少海报');
    console.log(`       详情：${vod.vod_name} | ${vod.vod_remarks || '-'} | 线路 ${vod.vod_play_from || '无'}`);
    await checkPlay(site, vod, id);
  });

  await step('详情 detail(搜索第一条) + 播放', async () => {
    const id = searchList[0].vod_id;
    const vod = await site.detail(id);
    assert(vod.vod_name, '缺少片名');
    await checkPlay(site, vod, id);
  });
}

console.log(`\n结果：${pass} 通过，${fail} 失败`);
process.exit(fail ? 1 : 0);
