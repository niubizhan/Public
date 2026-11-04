/**
 * Worker 入口集成测试：TVBox 4.4 配置 + 五类请求分发
 *   node test/api.mjs
 */
import assert from 'node:assert';
import worker from '../src/index.js';

const ORIGIN = 'https://tvbox44-crawler.example.workers.dev';

async function get(path) {
  const res = await worker.fetch(new Request(ORIGIN + path));
  const text = await res.text();
  let body = null;
  try {
    body = JSON.parse(text);
  } catch (_) {
    body = text;
  }
  return { status: res.status, body, raw: text };
}

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

await step('GET / -> TVBox 4.4 配置', async () => {
  const { status, body } = await get('/');
  assert.strictEqual(status, 200);
  assert(Array.isArray(body.sites) && body.sites.length === 3, 'sites 数量不对');
  for (const s of body.sites) {
    assert.strictEqual(s.type, 4, `${s.key} type 应为 4`);
    assert(s.api.startsWith(`${ORIGIN}/api/`), `${s.key} api 地址不对: ${s.api}`);
    assert.strictEqual(s.searchable, 1);
    assert.strictEqual(s.quickSearch, 1);
  }
  console.log('       sites:', body.sites.map((s) => `${s.name}(${s.key})`).join(' / '));
});

await step('GET /api -> 接口自述', async () => {
  const { body } = await get('/api');
  assert(body.sites.length === 3 && body.endpoints);
});

const CASES = [
  { key: 'fofoys', tid: 'film', ids: null },
  { key: 'piaoys', tid: '1', ids: null },
  { key: 'xcyycn', tid: '1', ids: null },
];

for (const c of CASES) {
  await step(`${c.key} 首页 ?filter=true`, async () => {
    const { body } = await get(`/api/${c.key}?filter=true`);
    assert(Array.isArray(body.class) && body.class.length >= 3, 'class 缺失');
    for (const c of body.class) {
      assert(c.type_id && c.type_id !== 'undefined', `class.type_id 异常: ${JSON.stringify(c)}`);
      assert(c.type_name && c.type_name !== 'undefined', `class.type_name 异常: ${JSON.stringify(c)}`);
    }
    assert(Array.isArray(body.list) && body.list.length > 0, 'list 缺失');
    assert(body.list[0].vod_name && body.list[0].vod_pic, 'vod 字段缺失');
  });

  await step(`${c.key} 分类 ?ac=detail&t=&pg=1`, async () => {
    const { body } = await get(`/api/${c.key}?ac=detail&filter=true&t=${c.tid}&pg=1`);
    assert.strictEqual(body.code, 1);
    assert(body.list.length > 0, '分类为空');
    c.firstId = body.list[0].vod_id;
  });

  await step(`${c.key} 搜索 ?wd=庆余年&ac=detail&quick=true`, async () => {
    const { body } = await get(`/api/${c.key}?wd=${encodeURIComponent('庆余年')}&ac=detail&quick=true`);
    assert.strictEqual(body.code, 1);
    assert(body.list.length > 0, '搜索为空');
    c.searchId = body.list[0].vod_id;
  });

  await step(`${c.key} 详情 ?ac=detail&ids=`, async () => {
    const { body } = await get(`/api/${c.key}?ac=detail&ids=${encodeURIComponent(c.firstId)}`);
    assert.strictEqual(body.code, 1);
    assert.strictEqual(body.list.length, 1);
    const v = body.list[0];
    assert(v.vod_name, '缺片名');
    assert(v.vod_play_from && v.vod_play_url, '缺播放字段');
    const token = v.vod_play_url.split('$$$')[0].split('#')[0].split('$')[1];
    assert(token, 'token 解析失败');
    const { body: play } = await get(`/api/${c.key}?play=${encodeURIComponent(token)}&flag=test`);
    assert(play.url && /^https?:\/\//i.test(play.url), `播放失败: ${JSON.stringify(play)}`);
    assert.strictEqual(play.parse, 0);
    console.log(`       播放 -> ${play.url.slice(0, 90)}`);
  });

  await step(`${c.key} 搜索结果也能进详情`, async () => {
    const { body } = await get(`/api/${c.key}?ac=detail&ids=${encodeURIComponent(c.searchId)}`);
    assert.strictEqual(body.code, 1);
    assert(body.list[0].vod_name, '搜索结果详情缺片名');
  });
}

await step('未知站点返回 404', async () => {
  const { status } = await get('/api/nope?filter=true');
  assert.strictEqual(status, 404);
});

await step('OPTIONS 预检带 CORS', async () => {
  const res = await worker.fetch(new Request(`${ORIGIN}/api/fofoys`, { method: 'OPTIONS' }));
  assert.strictEqual(res.status, 204);
  assert(res.headers.get('Access-Control-Allow-Origin') === '*');
});

console.log(`\n结果：${pass} 通过，${fail} 失败`);
process.exit(fail ? 1 : 0);
