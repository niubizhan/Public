/**
 * TVBox 4.4 零 JS 爬虫 · Cloudflare Worker 入口
 *
 * 部署到 Cloudflare Workers 或 Pages（functions/[[path]].js 复用本模块）。
 *
 * 路由：
 *   GET /                     -> TVBox 4.4 配置 JSON（直接填到 TVBox 的接口地址）
 *   GET /api                  -> 接口自述
 *   GET /api/{site}           -> type=4 业务接口（首页/分类/搜索/详情/播放，按参数分发）
 *
 * TVBox 4.4 type=4 请求参数（见 TVBoxOS SourceViewModel.java）：
 *   首页   ?filter=true
 *   分类   ?ac=detail&t={type_id}&pg={page}
 *   搜索   ?wd={关键词}&ac=detail&quick={true|false}
 *   详情   ?ac=detail&ids={vod_id}
 *   播放   ?play={token}&flag={线路名}
 */

import { SITES, getSite } from './sites/index.js';
import { listEnvelope, homeEnvelope, homeListEnvelope, playEnvelope, emptyList } from './maccms.js';

const JSON_HEADERS = {
  'Content-Type': 'application/json; charset=utf-8',
  'Access-Control-Allow-Origin': '*',
  'Access-Control-Allow-Methods': 'GET, POST, OPTIONS',
  'Access-Control-Allow-Headers': '*',
};

function json(data, status = 200, extra = {}) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { ...JSON_HEADERS, ...extra },
  });
}

/** 把上游失败原因写进 msg，方便在浏览器/TVBox 里直接看到为什么是空列表 */
function withError(envelope, e) {
  envelope.msg = `error: ${(e && e.message) || e}`;
  return envelope;
}

/** TVBox 4.4 完整配置（sites 全部为 type=4 的 HTTP JSON 站点） */
function buildConfig(origin) {
  return {
    spider: '',
    wallpaper: '',
    sites: SITES.map((s) => ({
      key: s.key,
      name: s.name,
      type: 1,
      api: `${origin}/api/${s.key}`,
      searchable: 1,
      quickSearch: 1,
      filterable: 0,
      changeable: 1,
      timeout: 20,
      style: '',
    })),
    // 注意：不能输出 "lives": []。TVBox 4.4 的 ApiConfig.parseJson 见到 lives 键就取
    // lives_groups.get(getLiveGroupIndex())，空数组会让它 IndexOutOfBounds，
    // 外层 catch 后直接回调 error("配置解析失败")。官方配置一律省略该键。
    parses: [],
    flags: [],
    ads: [],
  };
}

function helpDoc(origin) {
  return {
    name: 'TVBox 4.4 零 JS 爬虫',
    config: `${origin}/`,
    sites: SITES.map((s) => ({ key: s.key, name: s.name, api: `${origin}/api/${s.key}` })),
    endpoints: {
      '首页(分类导航+推荐)': 'GET {api}?ac=detail   （type=1 标准；type=4 用 ?filter=true）',
      分类: 'GET {api}?ac=detail&t={type_id}&pg={page}',
      搜索: 'GET {api}?wd={关键词}&ac=detail&quick=true',
      详情: 'GET {api}?ac=detail&ids={vod_id}',
      播放: 'GET {api}?play={vod_play_url里的token}&flag={线路名}',
    },
  };
}

/** 播放器/上游站点的 UA：取 m3u8 时代入，避免部分 CDN 拒绝空 UA */
const UA =
  'Mozilla/5.0 (Linux; Android 12; TVBox) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36';

/**
 * 列表条目索引（id -> vod）。
 *
 * TVBox 首页加载完分类/推荐后会立刻再发一次 `?ac=detail&ids=一长串`，
 * 把推荐位的每条都换成「完整详情」。对灰产源站来说这就是十几次详情页抓取，
 * 实测要 11~13 秒，首页就一直转圈，用户以为点不动。
 *
 * 其实这份批量数据只用于渲染首页卡片（片名/海报/备注），点进详情时
 * DetailActivity 会重新按单个 id 请求。所以这里把首页/分类/搜索已经拿到的
 * 列表条目缓存起来，批量请求直接命中，秒回。
 */
const VOD_INDEX = new Map();
const VOD_INDEX_MAX = 1200;

/** 存副本：rewritePics 会原地改写 vod_pic，存引用会让图片被二次套代理 */
function indexVods(list) {
  for (const v of list || []) {
    if (!v || !v.vod_id) continue;
    const id = String(v.vod_id);
    VOD_INDEX.delete(id);
    VOD_INDEX.set(id, { ...v });
  }
  while (VOD_INDEX.size > VOD_INDEX_MAX) {
    VOD_INDEX.delete(VOD_INDEX.keys().next().value);
  }
}

/**
 * 封面图内存缓存（LRU，最近 240 张）。
 * 一屏列表就有 20 多张图，滚动时同一张会被反复请求，回源太浪费。
 */
const IMG_CACHE = new Map();
const IMG_CACHE_MAX = 240;

function imgCacheGet(key) {
  const hit = IMG_CACHE.get(key);
  if (!hit) return null;
  IMG_CACHE.delete(key);
  IMG_CACHE.set(key, hit);
  return hit;
}

function imgCacheSet(key, value) {
  if (IMG_CACHE.size >= IMG_CACHE_MAX) {
    const oldest = IMG_CACHE.keys().next().value;
    IMG_CACHE.delete(oldest);
  }
  IMG_CACHE.set(key, value);
}

/**
 * 图片回源并发闸门。
 * 一屏列表会让电视同时要 20+ 张图，这些灰产图床对同一 IP 的高并发很敏感，
 * 直接并发打过去会成片超时/502（表现为部分海报空白）。限制 6 路，其余排队。
 */
const IMG_MAX_CONCURRENCY = 12;
let imgRunning = 0;
const imgQueue = [];

async function withImgSlot(fn) {
  if (imgRunning >= IMG_MAX_CONCURRENCY) {
    await new Promise((resolve) => imgQueue.push(resolve));
  }
  imgRunning += 1;
  try {
    return await fn();
  } finally {
    imgRunning -= 1;
    const next = imgQueue.shift();
    if (next) next();
  }
}

/** 带超时的 fetch：图床卡住时不能一直占着闸门 */
async function fetchWithTimeout(url, init = {}, ms = 8000) {
  const ctl = typeof AbortController !== 'undefined' ? new AbortController() : null;
  const timer = ctl ? setTimeout(() => ctl.abort(), ms) : null;
  try {
    return await fetch(url, ctl ? { ...init, signal: ctl.signal } : init);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/**
 * m3u8 里的相对地址改成绝对地址。
 *
 * 播放器拿到的清单地址是本机服务（/v/...），如果清单里还是 `/20260101/xx/seg-1.ts`
 * 这种相对路径，播放器会拼到本机服务上去 → 404。所以这里全部绝对化，
 * 之后的分片请求直接打到 CDN，本机只承担第一层清单。
 */
function absolutize(text, base) {
  const out = [];
  for (const raw of String(text).split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) {
      out.push(raw);
      continue;
    }
    if (line.startsWith('#')) {
      // 标签里的 URI="xxx" 也要绝对化（EXT-X-KEY / EXT-X-MAP / EXT-X-MEDIA 等）
      out.push(
        raw.replace(/URI="([^"]+)"/g, (m, uri) => {
          try {
            return `URI="${new URL(uri, base).toString()}"`;
          } catch {
            return m;
          }
        })
      );
      continue;
    }
    try {
      out.push(new URL(line, base).toString());
    } catch {
      out.push(line);
    }
  }
  return out.join('\n');
}

/**
 * 封面图统一走本机代理。
 *
 * 三个源用了十多个图床，其中不少是灰产 CDN：电视（Android 4.4）直连时会碰到
 * DNS 污染、证书链不兼容、防盗链 403 等各种问题，表现就是列表里一片首字色块。
 * 本机能正常取到这些图，所以把 vod_pic 换成本机 /img?u= 代理地址，
 * 由服务端代取后回传给电视，顺带把源站返回的相对路径也补成绝对地址。
 */
function picUrlFor(origin, absUrl) {
  return `${origin}/img?u=${encodeURIComponent(absUrl)}`;
}

function rewritePics(list, origin, siteBase) {
  for (const v of list || []) {
    for (const field of ['vod_pic', 'vod_pic_thumb', 'vod_pic_slide']) {
      const raw = String(v[field] || '').trim();
      if (!raw) continue;
      let abs = raw;
      if (!/^https?:\/\//i.test(raw)) {
        try {
          abs = new URL(raw, siteBase).toString();
        } catch {
          continue;
        }
      }
      v[field] = picUrlFor(origin, abs);
    }
  }
  return list;
}

/** 详情里把播放 token 包装成本机可播的 m3u8 地址（type=1 只认这种直链） */
function playUrlFor(origin, siteKey, token, flag) {
  const t = encodeURIComponent(token || '');
  const f = encodeURIComponent(flag || '');
  return `${origin}/v/${siteKey}/index.m3u8?t=${t}&f=${f}`;
}

/** 把详情里的 vod_play_from / vod_play_url 改写成 type=1 能直连的形态 */
function rewritePlayUrls(vod, origin, siteKey) {
  const froms = String(vod.vod_play_from || '').split('$$$');
  const groups = String(vod.vod_play_url || '').split('$$$');
  const newGroups = groups.map((group, i) => {
    const flag = froms[i] || '';
    return String(group)
      .split('#')
      .map((ep) => {
        const idx = ep.indexOf('$');
        if (idx < 0) return ep;
        const name = ep.slice(0, idx);
        const token = ep.slice(idx + 1);
        if (!token) return ep;
        return `${name}$${playUrlFor(origin, siteKey, token, flag)}`;
      })
      .join('#');
  });
  return { ...vod, vod_play_url: newGroups.join('$$$') };
}

/** 分发单个站点的 type=4 请求 */
async function dispatch(site, params, origin) {
  const play = params.get('play');
  const ids = params.get('ids');
  const wd = params.get('wd');
  const ac = params.get('ac') || '';
  const pg = Math.max(1, parseInt(params.get('pg') || '1', 10) || 1);

  // 1) 播放解析
  if (play) {
    try {
      const url = await site.play(play, params.get('flag') || '');
      return json(
        playEnvelope(url, {
          flag: params.get('flag') || '',
          header: {
            'User-Agent':
              'Mozilla/5.0 (Linux; Android 12; TVBox) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36',
            Referer: `${site.base}/`,
          },
        })
      );
    } catch (e) {
      return json(playEnvelope('', { flag: params.get('flag') || '', header: {} }));
    }
  }

  // 2) 详情
  // ids 可能是单个，也可能是 TVBox 预取时拼起来的一整页（逗号分隔，实测 9~20 个）。
  // 之前只取第一个，其余条目在 TVBox 侧就拿不到详情，点进去会被判成「本源没内容」，
  // 进而触发它的多源兜底搜索 —— 表现就是「点列表跳到搜索页」。所以这里必须全部返回。
  if (ids) {
    const idList = ids
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);

    // 源站偶发返回验证页/空壳，detail() 会解析出没有片名或没有播放列表的 vod。
    // 单条时重试一次；批量时为了不把响应拖长，只保留能解析出片名的。
    const fetchOne = async (id, retry) => {
      try {
        const v = await site.detail(id);
        if (v && v.vod_name && v.vod_play_url) return v;
        if (v && v.vod_name && !retry) return v;
        if (retry) {
          await new Promise((r) => setTimeout(r, 250));
          const v2 = await site.detail(id);
          return v2 && v2.vod_name ? v2 : v || v2;
        }
        return v;
      } catch (e) {
        if (retry) {
          await new Promise((r) => setTimeout(r, 250));
          try {
            return await site.detail(id);
          } catch {
            return null;
          }
        }
        return null;
      }
    };

    if (idList.length > 1) {
      const cap = idList.slice(0, 24);

      // 先用列表索引兜住：首页推荐位的 ids 就是刚才首页 list 里的那批，
      // 全部命中时这个请求从十几秒降到几毫秒（首页不再长时间转圈）。
      const out = cap.map((id) => VOD_INDEX.get(String(id)) || null);

      const missing = [];
      out.forEach((v, i) => {
        if (!v) missing.push(i);
      });

      if (missing.length) {
        let cursor = 0;
        // 源站是灰产站点，并发太高会直接 403/断连，这里限 5。
        const lanes = Array.from({ length: Math.min(5, missing.length) }, async () => {
          for (;;) {
            const k = cursor++;
            if (k >= missing.length) return;
            const i = missing[k];
            out[i] = await fetchOne(cap[i], false);
          }
        });
        await Promise.all(lanes);
      }

      // 补漏：源站偶发超时/断连，第一轮失败的再串一轮。
      // 少一条就会让 TVBox 认为该条目「本源没有详情」，点进去就会跳到它的多源搜索，
      // 所以宁可多花几百毫秒也要把条数补齐。
      const stillMissing = [];
      out.forEach((v, i) => {
        if (!v || !v.vod_name) stillMissing.push(i);
      });
      if (stillMissing.length) {
        await Promise.all(
          stillMissing.slice(0, 10).map(async (i) => {
            out[i] = await fetchOne(cap[i], true);
          })
        );
      }

      const vods = out.filter((v) => v && v.vod_name);
      const env = listEnvelope(vods, { page: 1, pagecount: 1, total: vods.length });
      env.list = env.list.map((v) => rewritePlayUrls(v, origin, site.key));
      rewritePics(env.list, origin, site.base);
      return json(env);
    }

    const vod = await fetchOne(idList[0], true);
    if (!vod || !vod.vod_name) {
      return json(withError(listEnvelope([], { page: 1, pagecount: 1, total: 0 }), '详情为空'));
    }
    const env = listEnvelope([vod], { page: 1, pagecount: 1, total: 1 });
    env.list[0] = rewritePlayUrls(env.list[0], origin, site.key);
    rewritePics(env.list, origin, site.base);
    return json(env);
  }

  // 3) 搜索（TVBox 4.4 搜索不带 pg，固定第 1 页）
  if (wd) {
    try {
      const r = await site.search(wd, 1);
      indexVods(r.list);
      return json(
        listEnvelope(rewritePics(r.list, origin, site.base), {
          page: 1,
          pagecount: r.pagecount ?? 1,
          total: r.total,
        })
      );
    } catch (e) {
      return json(withError(emptyList(1), e));
    }
  }

  // 4) 分类 / type=1 首页
  if (ac === 'detail') {
    // maccms 标准：首页也是 ac=detail，只是不带 t。此时返回 class + list。
    if (!params.get('t')) {
      try {
        const r = await site.home();
        indexVods(r.list);
        return json(homeListEnvelope(r.classes, rewritePics(r.list, origin, site.base)));
      } catch (e) {
        return json(withError(homeListEnvelope([], []), e));
      }
    }
    try {
      const r = await site.category(params.get('t') || '', pg);
      indexVods(r.list);
      return json(
        listEnvelope(rewritePics(r.list, origin, site.base), {
          page: r.page ?? pg,
          pagecount: r.pagecount ?? 1,
          total: r.total,
        })
      );
    } catch (e) {
      return json(withError(emptyList(pg), e));
    }
  }

  // 5) 首页：class = 分类导航，list = 首页推荐
  try {
    const r = await site.home();
    indexVods(r.list);
    return json(homeEnvelope(r.classes, rewritePics(r.list, origin, site.base)));
  } catch (e) {
    return json(withError(homeEnvelope([], []), e));
  }
}

export default {
  async fetch(request) {
    const url = new URL(request.url);
    const path = url.pathname.replace(/\/+$/, '') || '/';
    const params = url.searchParams;

    if (request.method === 'OPTIONS') {
      return new Response(null, { status: 204, headers: JSON_HEADERS });
    }

    // 首页：TVBox 4.4 配置
    if (path === '/' || path === '/config' || path === '/tvbox.json') {
      return json(buildConfig(url.origin));
    }

    // 接口自述
    if (path === '/api' || path === '/help') {
      return json(helpDoc(url.origin));
    }

    // 封面图代理：/img?u={原图绝对地址}
    // 电视端（Android 4.4）对部分灰产图床的 DNS 解析和证书链不友好，直连会白图；
    // 本机能取到，所以统一由服务端代取后回传。
    if (path === '/img') {
      const u = params.get('u') || '';
      if (!/^https?:\/\//i.test(u)) return new Response('bad url', { status: 400 });

      const hit = imgCacheGet(u);
      if (hit) {
        return new Response(hit.buf, {
          headers: {
            'Content-Type': hit.ct,
            'Cache-Control': 'public, max-age=604800',
            'Access-Control-Allow-Origin': '*',
          },
        });
      }

      let referer = '';
      let host = '';
      try {
        host = new URL(u).host;
      } catch {
        return new Response('bad url', { status: 400 });
      }
      // 豆瓣图床不做 Referer 会直接 418
      if (/doubanio\.com$/i.test(host) || /douban\.com$/i.test(host)) {
        referer = 'https://movie.douban.com/';
      } else {
        for (const s of SITES) {
          if (u.startsWith(s.base)) {
            referer = `${s.base}/`;
            break;
          }
        }
      }
      const headers = { 'User-Agent': UA };
      if (referer) headers.Referer = referer;

      try {
        const got = await withImgSlot(async () => {
          let lastErr = null;
          // 图床偶发 ECONNRESET/限流，重试一次基本能过
          for (let attempt = 0; attempt < 2; attempt++) {
            try {
              const up = await fetchWithTimeout(u, { headers }, 7000);
              if (!up.ok) throw new Error(`upstream ${up.status}`);
              const ct = up.headers.get('content-type') || 'image/jpeg';
              const buf = await up.arrayBuffer();
              if (!buf.byteLength) throw new Error('empty body');
              return { buf, ct };
            } catch (e) {
              lastErr = e;
              if (attempt === 0) await new Promise((r) => setTimeout(r, 150));
            }
          }
          throw lastErr;
        });
        imgCacheSet(u, got);
        return new Response(got.buf, {
          status: 200,
          headers: {
            'Content-Type': got.ct,
            'Cache-Control': 'public, max-age=604800',
            'Access-Control-Allow-Origin': '*',
          },
        });
      } catch (e) {
        // 本机也取不到（图床限流/超时）时，退化成 302 让电视自己直连试一次，
        // 总比回一个 502 让海报空着强。
        return new Response(null, {
          status: 302,
          headers: { Location: u, 'Cache-Control': 'no-store' },
        });
      }
    }

    // 播放直链：/v/{site}/index.m3u8?t={token}&f={flag}
    // type=1 站点不会调用 ?play=，只认 vod_play_url 里的直链；
    // 这里把 token 解析成真实地址：m3u8 拉回来改写成绝对路径再吐给播放器，
    // 其他格式（mp4/flv）直接 302。
    if (path.startsWith('/v/')) {
      const site = getSite(path.slice(3).split('/')[0]);
      if (!site) return new Response('unknown site', { status: 404 });
      const token = params.get('t') || '';
      const flag = params.get('f') || '';
      let real = '';
      try {
        real = await site.play(token, flag);
      } catch {
        real = '';
      }
      if (!real) return new Response('play resolve failed', { status: 404 });

      const isM3u8 = /\.m3u8(\?|$)/i.test(real) || /\.m3u8(\?|$)/i.test(path);
      if (!isM3u8) {
        return new Response(null, { status: 302, headers: { Location: real } });
      }

      try {
        const up = await fetch(real, {
          headers: { 'User-Agent': UA, Referer: `${site.base}/` },
        });
        if (!up.ok) return new Response(null, { status: 302, headers: { Location: real } });
        const text = absolutize(await up.text(), real);
        return new Response(text, {
          status: 200,
          headers: {
            'Content-Type': 'application/vnd.apple.mpegurl',
            'Access-Control-Allow-Origin': '*',
            'Cache-Control': 'no-store',
          },
        });
      } catch {
        return new Response(null, { status: 302, headers: { Location: real } });
      }
    }

    // 站点业务接口
    if (path.startsWith('/api/')) {
      const key = path.slice(5).split('/')[0];
      const site = getSite(key);
      if (!site) return json({ code: 0, msg: `unknown site: ${key}`, list: [] }, 404);
      try {
        return await dispatch(site, params, url.origin);
      } catch (e) {
        return json({ code: 0, msg: String((e && e.message) || e), list: [] }, 500);
      }
    }

    return json({ code: 0, msg: 'not found', hint: '访问 / 获取 TVBox 4.4 配置' }, 404);
  },
};
