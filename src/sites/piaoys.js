/**
 * 飘花影视 https://www.piaoys.com/
 *
 * 站点形态：苹果 CMS V10（stui 模板），服务端渲染 HTML
 *   分类   /sort/{1|2|3|4|27}.html        （单页列表，无分页）
 *   搜索   /sou/------------- .html?wd=关键词
 *   详情   /html/{id}.html                播放组 <span class="pull-right1">源名</span> + 选集列表
 *   播放   /play/{id}-{flag}-{ep}.html    页面内嵌 var player_data={...,"url":"...m3u8"}
 */

import { fetchText, stripTags, extractBalanced } from '../util.js';

const BASE = 'https://www.piaoys.com';

export const TYPES = [
  { id: '1', name: '电影' },
  { id: '2', name: '电视剧' },
  { id: '3', name: '动漫' },
  { id: '4', name: '综艺' },
  { id: '27', name: '短剧' },
];

const TYPE_NAME = Object.fromEntries(TYPES.map((t) => [t.id, t.name]));

/** 解析 stui-vodlist 卡片（首页 / 分类 / 搜索页通用结构） */
function parseCards(html) {
  const out = [];
  const seen = new Set();
  const chunks = html.split(/<div class="stui-vodlist__box"/).slice(1);

  for (const chunk of chunks) {
    const href = /href="(\/html\/\d+\.html)"/.exec(chunk);
    if (!href) continue;
    const id = /\/html\/(\d+)\.html/.exec(href[1])[1];
    if (seen.has(id)) continue;

    const title = /title="([^"]*)"/.exec(chunk);
    const pic = /data-original="([^"]*)"/.exec(chunk);
    const remark = /<span class="pic-text[^"]*">([\s\S]*?)<\/span>/.exec(chunk);
    if (!title || !pic) continue;

    seen.add(id);
    out.push({
      vod_id: id,
      type_id: '',
      type_name: '',
      vod_name: stripTags(title[1]),
      vod_pic: pic[1],
      vod_remarks: remark ? stripTags(remark[1]) : '',
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
  const type = TYPES.find((t) => t.id === String(typeId)) || TYPES[0];
  if (page > 1) return { list: [], page, pagecount: 1, total: 0 };
  const html = await fetchText(`${BASE}/sort/${type.id}.html`, { ttl: 120 });
  const list = parseCards(html);
  return { list, page: 1, pagecount: 1, total: list.length };
}

async function search(keyword, page = 1) {
  if (page > 1) return { list: [], page, pagecount: 1, total: 0 };
  const url = `${BASE}/sou/-------------.html?wd=${encodeURIComponent(keyword)}`;
  const html = await fetchText(url, { ttl: 60 });
  const list = parseCards(html);
  return { list, page: 1, pagecount: 1, total: list.length };
}

/** 详情页：字段区 + 播放组（选集） */
async function detail(id) {
  const html = await fetchText(`${BASE}/html/${id}.html`, { ttl: 300 });

  const get = (label) => {
    const m = new RegExp(
      `<p class="data"><span class="data2">${label}：</span>([\\s\\S]*?)</p>`
    ).exec(html);
    return m ? stripTags(m[1]) : '';
  };

  const title = (/<h1 class="title">([^<]+)<\/h1>/.exec(html) || [])[1] || '';
  const pic = (/<div class="stui-content__thumb"[\s\S]{0,600}?data-original="([^"]+)"/.exec(html) || [])[1] || '';
  const desc = (/<span class="detail-content"[^>]*>([\s\S]*?)<\/span>/.exec(html) || [])[1] || '';

  const vod = {
    vod_id: id,
    type_id: '',
    type_name: get('类型'),
    vod_name: stripTags(title),
    vod_pic: pic,
    vod_remarks: get('状态'),
    vod_year: get('年份'),
    vod_area: get('地区'),
    vod_class: get('类型'),
    vod_actor: get('主演'),
    vod_director: get('导演'),
    vod_time: get('更新'),
    vod_content: stripTags(desc),
    vod_blurb: stripTags(desc),
  };

  // 播放组：每个选集 <ul> 之前的 pull-right1 即源名
  const froms = [];
  const urls = [];
  const usedNames = new Set();

  for (const m of html.matchAll(/<ul class="stui-content__playlist[^"]*">([\s\S]*?)<\/ul>/g)) {
    const before = html.slice(Math.max(0, m.index - 1200), m.index);
    const srcMatches = [...before.matchAll(/<span class="pull-right1">([^<]*)<\/span>/g)];
    let name = srcMatches.length ? stripTags(srcMatches[srcMatches.length - 1][1]) : '';
    if (!name) name = `线路${froms.length + 1}`;
    if (usedNames.has(name)) name = `${name}${froms.length + 1}`;
    usedNames.add(name);

    const eps = [...m[1].matchAll(/<a href="\/play\/(\d+)-(\d+)-(\d+)\.html">([^<]*)<\/a>/g)];
    if (!eps.length) continue;

    froms.push(name);
    urls.push(eps.map((e) => `${stripTags(e[4])}$py:play/${e[1]}-${e[2]}-${e[3]}.html`).join('#'));
  }

  if (froms.length) {
    vod.vod_play_from = froms.join('$$$');
    vod.vod_play_url = urls.join('$$$');
  }

  return vod;
}

/** 播放：token 形如 py:play/502680-2-1.html */
async function play(token) {
  const path = String(token).replace(/^py:/, '').trim();
  if (!/^play\/\d+-\d+-\d+\.html$/.test(path)) throw new Error('bad play token');

  const html = await fetchText(`${BASE}/${path}`, {
    referer: `${BASE}/`,
    ttl: 60,
  });

  let url = '';
  const idx = html.indexOf('var player_data');
  if (idx >= 0) {
    const lit = extractBalanced(html, idx);
    if (lit) {
      try {
        url = JSON.parse(lit).url || '';
      } catch (_) {
        url = '';
      }
    }
  }
  if (!url) {
    url = (/"url"\s*:\s*"([^"]+)"/.exec(html) || [])[1] || '';
  }
  url = url.replace(/\\\//g, '/');
  if (!/^https?:\/\//i.test(url)) throw new Error('play resolve failed');
  return url;
}

export default {
  key: 'piaoys',
  name: '飘花影视',
  base: BASE,
  home,
  category,
  search,
  detail,
  play,
};
