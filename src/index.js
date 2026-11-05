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
  if (ids) {
    try {
      const vod = await site.detail(ids.split(',')[0]);
      const env = listEnvelope([vod], { page: 1, pagecount: 1, total: 1 });
      env.list[0] = rewritePlayUrls(env.list[0], origin, site.key);
      return json(env);
    } catch (e) {
      return json(withError(listEnvelope([], { page: 1, pagecount: 1, total: 0 }), e));
    }
  }

  // 3) 搜索（TVBox 4.4 搜索不带 pg，固定第 1 页）
  if (wd) {
    try {
      const r = await site.search(wd, 1);
      return json(listEnvelope(r.list, { page: 1, pagecount: r.pagecount ?? 1, total: r.total }));
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
        return json(homeListEnvelope(r.classes, r.list));
      } catch (e) {
        return json(withError(homeListEnvelope([], []), e));
      }
    }
    try {
      const r = await site.category(params.get('t') || '', pg);
      return json(
        listEnvelope(r.list, {
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
    return json(homeEnvelope(r.classes, r.list));
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
