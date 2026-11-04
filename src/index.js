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
import { listEnvelope, homeEnvelope, playEnvelope, emptyList } from './maccms.js';

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

/** TVBox 4.4 完整配置（sites 全部为 type=4 的 HTTP JSON 站点） */
function buildConfig(origin) {
  return {
    spider: '',
    wallpaper: '',
    sites: SITES.map((s) => ({
      key: s.key,
      name: s.name,
      type: 4,
      api: `${origin}/api/${s.key}`,
      searchable: 1,
      quickSearch: 1,
      filterable: 0,
      changeable: 1,
      timeout: 20,
      style: '',
    })),
    lives: [],
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
      '首页(分类导航+推荐)': 'GET {api}?filter=true',
      分类: 'GET {api}?ac=detail&t={type_id}&pg={page}',
      搜索: 'GET {api}?wd={关键词}&ac=detail&quick=true',
      详情: 'GET {api}?ac=detail&ids={vod_id}',
      播放: 'GET {api}?play={vod_play_url里的token}&flag={线路名}',
    },
  };
}

/** 分发单个站点的 type=4 请求 */
async function dispatch(site, params) {
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
      return json(listEnvelope([vod], { page: 1, pagecount: 1, total: 1 }));
    } catch (e) {
      return json(listEnvelope([], { page: 1, pagecount: 1, total: 0 }));
    }
  }

  // 3) 搜索（TVBox 4.4 搜索不带 pg，固定第 1 页）
  if (wd) {
    try {
      const r = await site.search(wd, 1);
      return json(listEnvelope(r.list, { page: 1, pagecount: r.pagecount ?? 1, total: r.total }));
    } catch (e) {
      return json(emptyList(1));
    }
  }

  // 4) 分类
  if (ac === 'detail') {
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
      return json(emptyList(pg));
    }
  }

  // 5) 首页：class = 分类导航，list = 首页推荐
  try {
    const r = await site.home();
    return json(homeEnvelope(r.classes, r.list));
  } catch (e) {
    return json(homeEnvelope([], []));
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

    // 站点业务接口
    if (path.startsWith('/api/')) {
      const key = path.slice(5).split('/')[0];
      const site = getSite(key);
      if (!site) return json({ code: 0, msg: `unknown site: ${key}`, list: [] }, 404);
      try {
        return await dispatch(site, params);
      } catch (e) {
        return json({ code: 0, msg: String((e && e.message) || e), list: [] }, 500);
      }
    }

    return json({ code: 0, msg: 'not found', hint: '访问 / 获取 TVBox 4.4 配置' }, 404);
  },
};
