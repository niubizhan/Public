/**
 * fofo影视 https://www.fofoys.com/
 *
 * 站点形态：Next.js 服务端渲染（HTML 里已包含列表与详情数据，无需执行 JS）
 *   列表   /type/{tvseries|film|varietyshow|anime|shortplay}
 *   搜索   /search?q=关键词
 *   详情   /{type}/{id}          页面内嵌 var title/pic/note/... 与 var urlList = {...}
 *   播放   POST /source/  id={sid}  -> 返回 m3u8/mp4 直链（纯文本）
 */

import {
  fetchText,
  stripTags,
  attr,
  jsLiteralToJson,
  extractBalanced,
  findQuotedVar,
} from '../util.js';

const BASE = 'https://www.fofoys.com';

export const TYPES = [
  { id: 'tvseries', name: '电视剧' },
  { id: 'film', name: '电影' },
  { id: 'varietyshow', name: '综艺' },
  { id: 'anime', name: '动漫' },
  { id: 'shortplay', name: '短剧' },
];

const TYPE_NAME = Object.fromEntries(TYPES.map((t) => [t.id, t.name]));
const TYPE_RE = /<a[^>]*href="\/(tvseries|film|anime|varietyshow|shortplay)\/(\d+)"[^>]*>/g;

/** 从列表页 HTML 解析卡片（只认 alt="video"/alt="vod" 的图，自动跳过轮播/横幅） */
function parseCards(html) {
  const matches = [...html.matchAll(TYPE_RE)];
  const out = [];
  const seen = new Set();

  for (let i = 0; i < matches.length; i++) {
    const m = matches[i];
    const start = m.index + m[0].length;
    const end = i + 1 < matches.length ? matches[i + 1].index : Math.min(html.length, start + 6000);
    const block = html.slice(start, end);

    const imgTag = /<img[^>]*alt="(?:video|vod)"[^>]*>/.exec(block);
    if (!imgTag) continue;
    const pic = attr(imgTag[0], 'src');
    if (!pic) continue;

    // 标题：搜索卡片是 topic-details-title；网格卡片是带 font-bold 的 span
    //（源站会把 class 属性换行输出，所以不能写死 "<span class=" 直连）
    const boldSpan = [...block.matchAll(/<span[^>]*class="([^"]*)"[^>]*>([\s\S]*?)<\/span>/g)].find(
      (m) => m[1].includes('font-bold')
    );
    const title =
      (/<div class="topic-details-title">([\s\S]*?)<\/div>/.exec(block) || [])[1] ||
      (boldSpan ? boldSpan[2] : '') ||
      '';
    if (!title) continue;

    const sub = (/<div class="topic-details-title-sub[^"]*">([\s\S]*?)<\/div>/.exec(block) || [])[1];

    const id = `${m[1]}/${m[2]}`;
    if (seen.has(id)) continue;
    seen.add(id);

    out.push({
      vod_id: id,
      type_id: m[1],
      type_name: TYPE_NAME[m[1]] || '',
      vod_name: stripTags(title),
      vod_pic: pic,
      vod_remarks: sub ? stripTags(sub) : '',
      vod_blurb: '',
      vod_content: '',
    });
  }
  return out;
}

async function home() {
  const html = await fetchText(`${BASE}/`, { ttl: 60 });
  return { classes: TYPES, list: parseCards(html) };
}

async function category(typeId, page = 1) {
  const type = TYPES.find((t) => t.id === typeId) || TYPES[0];
  if (page > 1) return { list: [], page, pagecount: 1, total: 0 };
  const html = await fetchText(`${BASE}/type/${type.id}`, { ttl: 120 });
  const list = parseCards(html);
  return { list, page: 1, pagecount: 1, total: list.length };
}

async function search(keyword, page = 1) {
  if (page > 1) return { list: [], page, pagecount: 1, total: 0 };
  const html = await fetchText(`${BASE}/search?q=${encodeURIComponent(keyword)}`, { ttl: 60 });
  const list = parseCards(html);
  const countHit = /class="search-count[^"]*">\s*<span>\s*(\d+)\s*<\/span>/.exec(html);
  const total = countHit ? parseInt(countHit[1], 10) : list.length;
  return { list, page: 1, pagecount: list.length ? 1 : 1, total };
}

/** 详情页内嵌的 var urlList = {'source':[...], 'url_list':[[...]]} */
function parseUrlList(html) {
  const idx = html.indexOf('var urlList');
  if (idx < 0) return null;
  const lit = extractBalanced(html, idx);
  if (!lit) return null;
  try {
    return JSON.parse(jsLiteralToJson(lit));
  } catch (_) {
    return null;
  }
}

async function detail(id) {
  const html = await fetchText(`${BASE}/${id}`, { ttl: 300 });

  const v = (name) => findQuotedVar(html, name);
  const typeId = v('mtype');
  const vod = {
    vod_id: id,
    type_id: typeId,
    type_name: TYPE_NAME[typeId] || '',
    vod_name: v('title'),
    vod_pic: v('pic'),
    vod_remarks: v('note'),
    vod_year: v('year'),
    vod_area: v('area'),
    vod_class: v('label'),
    vod_actor: v('actor'),
    vod_director: v('director'),
    vod_content: v('description'),
    vod_blurb: v('description'),
    vod_lang: '',
    vod_score: '',
    vod_time: '',
  };

  const data = parseUrlList(html);
  if (data && Array.isArray(data.source) && Array.isArray(data.url_list)) {
    const froms = [];
    const urls = [];
    for (let i = 0; i < data.source.length && i < data.url_list.length; i++) {
      const items = data.url_list[i];
      if (!Array.isArray(items) || items.length === 0) continue;
      froms.push(String(data.source[i]));
      urls.push(
        items
          .map((it, n) => {
            const name = it.title || `第${(it.episode ?? n) + 1}集`;
            return `${name}$ff:${it.sid}`;
          })
          .join('#')
      );
    }
    if (froms.length) {
      vod.vod_play_from = froms.join('$$$');
      vod.vod_play_url = urls.join('$$$');
    }
  }

  return vod;
}

/** 播放：token 形如 ff:9879220 */
async function play(token) {
  const sid = String(token).replace(/^ff:/, '').trim();
  if (!/^\d+$/.test(sid)) throw new Error('bad play token');
  const body = await fetchText(`${BASE}/source/`, {
    method: 'POST',
    body: `id=${encodeURIComponent(sid)}`,
    headers: { 'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8' },
    referer: `${BASE}/`,
    ttl: 0,
  });
  const url = body.trim().replace(/^["']|["']$/g, '');
  if (!/^https?:\/\//i.test(url)) throw new Error('play resolve failed');
  return url;
}

export default {
  key: 'fofoys',
  name: 'fofo影视',
  base: BASE,
  types: TYPES,
  home,
  category,
  search,
  detail,
  play,
};
