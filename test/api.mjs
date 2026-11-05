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

/** 拿原始响应（不走 JSON 解析），用于校验 /v/ 播放代理吐回的 m3u8 */
async function getRaw(path) {
  const res = await worker.fetch(new Request(ORIGIN + path));
  return { res, text: await res.text() };
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
    assert.strictEqual(s.type, 1, `${s.key} type 应为 1（maccms 标准）`);
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
  await step(`${c.key} 首页 ?ac=detail`, async () => {
    const { body } = await get(`/api/${c.key}?ac=detail`);
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

    // type=1 站点 TVBox 不会调用 ?play=，vod_play_url 必须直接是可播地址，
    // 且要能命中 DefaultConfig.isVideoFormat 的 .m3u8 正则（否则会退化成 WebView 嗅探）。
    const first = v.vod_play_url.split('$$$')[0].split('#')[0];
    const target = first.slice(first.indexOf('$') + 1);
    assert(/^https?:\/\//i.test(target), `播放地址不是直链: ${target}`);
    assert(/\.m3u8(\?|$)/i.test(target), `播放地址不是 m3u8: ${target}`);

    // 本机 /v/ 代理层：拉回来的清单里不能再有相对路径
    const { res, text } = await getRaw(new URL(target).pathname + new URL(target).search);
    assert.strictEqual(res.status, 200, `播放代理返回 ${res.status}`);
    assert(text.includes('#EXTM3U'), '不是合法 m3u8');
    const rel = text
      .split(/\r?\n/)
      .filter((l) => l.trim() && !l.startsWith('#'))
      .filter((l) => !/^https?:\/\//i.test(l.trim()));
    assert.strictEqual(rel.length, 0, `清单里仍有相对路径: ${rel[0]}`);
    console.log(`       播放代理 -> ${target.slice(0, 78)}  (分片已绝对化)`);
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
