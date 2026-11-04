/**
 * 星辰影院 https://www.xcyycn.cc/
 *
 * 站点形态：苹果 CMS V10（shoutu46 模板），服务端渲染 HTML
 *   分类   /v/{1..5}.html  第 N 页为 /v/{type}-{N}.html
 *   搜索   /s.html?wd=关键词  第 N 页为 /s{关键词}/page/{N}.html
 *   详情   /d-{id}.html     资源列表 .anthology-tab（线路名） + .anthology-list-box（选集 /p/...）
 *   播放   /p/{id}-{flag}-{ep}.html   页面内嵌 var player_aaaa={...,"url":"...m3u8"}
 */

import { fetchText, stripTags, extractBalanced, absUrl } from '../util.js';

const BASE = 'https://www.xcyycn.cc';

export const TYPES = [
  { id: '1', name: '电影' },
  { id: '2', name: '电视剧' },
  { id: '3', name: '综艺' },
  { id: '4', name: '动漫' },
  { id: '5', name: '短剧' },
];

const TYPE_NAME = Object.fromEntries(TYPES.map((t) => [t.id, t.name]));

/** 网格卡片：public-list-exp */
function parseGrid(html) {
  const out = [];
  const re = /<a([^>]*class="public-list-exp"[^>]*)>([\s\S]*?)<\/a>/g;
  for (const m of html.matchAll(re)) {
    const attrs = m[1];
    const inner = m[2];
    const href = /href="(\/d-\d+\.html)"/.exec(attrs);
    if (!href) continue;
    const id = /\/d-(\d+)\.html/.exec(href[1])[1];
    const pic = /data-src="([^"]+)"/.exec(inner) || /(?<!data-)src="([^"]+)"/.exec(inner);
    const title = /title="([^"]*)"/.exec(attrs);
    // 搜索页的 a 标签没有 title 属性：退化到图片 alt（去掉“封面图”后缀），
    // 再退化到 a 标签之后的 .thumb-txt
    let name = title ? stripTags(title[1]) : '';
    if (!name) {
      const alt = /alt="([^"]*)"/.exec(inner);
      if (alt) name = stripTags(alt[1]).replace(/封面图$/, '');
    }
    if (!name) {
      const after = html.slice(m.index + m[0].length, m.index + m[0].length + 500);
      const tt = /<div class="thumb-txt[^"]*">([\s\S]*?)<\/div>/.exec(after);
      if (tt) name = stripTags(tt[1]);
    }
    const remark = /<span class="public-list-prb[^"]*">([\s\S]*?)<\/span>/.exec(inner);
    const typeName = /<span class="public-prt[^"]*">([\s\S]*?)<\/span>/.exec(inner);
    out.push({
      vod_id: id,
      type_id: '',
      type_name: typeName ? stripTags(typeName[1]) : '',
      vod_name: name,
      vod_pic: pic ? absUrl(BASE, pic[1]) : '',
      vod_remarks: remark ? stripTags(remark[1]) : '',
      vod_blurb: '',
      vod_content: '',
    });
  }
  return out;
}

/** 首页轮播卡片：slide-time-img3 */
function parseHero(html) {
  const out = [];
  const re =
    /<a href="(\/d-\d+\.html)"[\s\S]{0,600}?background-image:\s*url\('([^']+)'\)[\s\S]{0,900}?<h3 class="slide-info-title[^"]*">([\s\S]*?)<\/h3>/g;
  for (const m of html.matchAll(re)) {
    const id = /\/d-(\d+)\.html/.exec(m[1])[1];
    const tail = html.slice(m.index, m.index + m[0].length + 400);
    const remark = /<span class="slide-info-remarks">([\s\S]*?)<\/span>/.exec(tail);
    out.push({
      vod_id: id,
      type_id: '',
      type_name: '',
      vod_name: stripTags(m[3]),
      vod_pic: absUrl(BASE, m[2]),
      vod_remarks: remark ? stripTags(remark[1]) : '',
      vod_blurb: '',
      vod_content: '',
    });
  }
  return out;
}

function merge(...lists) {
  const seen = new Set();
  const out = [];
  for (const item of lists.flat()) {
    if (!item.vod_id || !item.vod_name) continue;
    if (seen.has(item.vod_id)) continue;
    seen.add(item.vod_id);
    out.push(item);
  }
  return out;
}

function parsePageCount(html) {
  const m = /共(\d+)条数据,当前(\d+)\/(\d+)页/.exec(html);
  return m ? parseInt(m[3], 10) : 0;
}

async function home() {
  const html = await fetchText(`${BASE}/`, { ttl: 60 });
  return { classes: TYPES, list: merge(parseGrid(html), parseHero(html)) };
}

async function category(typeId, page = 1) {
  const type = TYPES.find((t) => t.id === String(typeId)) || TYPES[0];
  // 源站分类页没有分页器：/v/1-2.html 与 ?page=2 返回的都是同一批数据，
  // 因此按单页处理（pagecount=1，TVBox 不会再翻页）。
  if (page > 1) return { list: [], page, pagecount: 1, total: 0 };
  const html = await fetchText(`${BASE}/v/${type.id}.html`, { ttl: 120 });
  const list = parseGrid(html);
  return { list, page: 1, pagecount: 1, total: list.length };
}

async function search(keyword, page = 1) {
  const kw = encodeURIComponent(keyword);
  const url = page > 1 ? `${BASE}/s${kw}/page/${page}.html` : `${BASE}/s.html?wd=${kw}`;
  const html = await fetchText(url, { ttl: 60 });
  const list = merge(parseGrid(html), parseHero(html));
  const pc = parsePageCount(html);
  return {
    list,
    page: Number(page) || 1,
    pagecount: pc || (list.length ? 1 : 1),
    total: pc ? pc * 20 : list.length,
  };
}

async function detail(id) {
  const html = await fetchText(`${BASE}/d-${id}.html`, { ttl: 300 });

  const title =
    (/<h3 class="slide-info-title[^"]*">([\s\S]*?)<\/h3>/.exec(html) || [])[1] ||
    (/<title>[^《]*《([^》]+)》/.exec(html) || [])[1] ||
    '';
  const picM = /(?:data-src|src)="(https?:\/\/[^"]*\/upload\/vod\/[^"]+)"/.exec(html);
  const descM = /id="height_limit"[^>]*>([\s\S]*?)<\/div>/.exec(html);
  const remarkM = /<span class="slide-info-remarks">([\s\S]*?)<\/span>/.exec(html);

  const fields = {};
  for (const m of html.matchAll(/<li><em>([^<]+)：<\/em>([\s\S]*?)<\/li>/g)) {
    fields[stripTags(m[1])] = stripTags(m[2]);
  }

  const desc = descM ? stripTags(descM[1]) : fields['简介'] || '';

  const vod = {
    vod_id: id,
    type_id: '',
    type_name: fields['类型'] || '',
    vod_name: stripTags(title),
    vod_pic: picM ? picM[1] : '',
    vod_remarks: remarkM ? stripTags(remarkM[1]) : fields['更新'] || '',
    vod_year: /\/s\/year\/(\d{4})\.html/.exec(html)?.[1] || '',
    vod_area: '',
    vod_class: fields['类型'] || '',
    vod_actor: '',
    vod_director: '',
    vod_lang: fields['语言'] || '',
    vod_time: fields['更新'] || '',
    vod_content: desc,
    vod_blurb: desc,
  };

  // 线路名（.anthology-tab 内的 swiper-slide）
  const tabM = /<div class="anthology-tab[^"]*">([\s\S]*?)<\/div>/.exec(html);
  const tabs = [];
  if (tabM) {
    for (const m of tabM[1].matchAll(/<a class="swiper-slide"><i class="fa[^"]*"><\/i>&nbsp;([^<]+)<\/a>/g)) {
      const name = stripTags(m[1]);
      if (name && !/下载/.test(name)) tabs.push(name);
    }
  }

  // 选集块
  const froms = [];
  const urls = [];
  const usedNames = new Set();
  const boxes = [...html.matchAll(/<div class=['"]anthology-list-box[^'"]*['"]>([\s\S]*?)<\/div>/g)];

  boxes.forEach((box, i) => {
    const eps = [
      ...box[1].matchAll(/<a[^>]*href="(\/p\/\d+-\d+-\d+\.html)"[^>]*>([^<]*)<\/a>/g),
    ];
    if (!eps.length) return;

    let name = tabs[i] || `线路${froms.length + 1}`;
    if (usedNames.has(name)) name = `${name}${froms.length + 1}`;
    usedNames.add(name);

    froms.push(name);
    urls.push(
      eps
        .map((e) => {
          const path = e[1].replace(/^\//, '');
          return `${stripTags(e[2])}$xc:${path}`;
        })
        .join('#')
    );
  });

  if (froms.length) {
    vod.vod_play_from = froms.join('$$$');
    vod.vod_play_url = urls.join('$$$');
  }

  return vod;
}

/** 播放：token 形如 xc:p/179223-2-1.html */
async function play(token) {
  const path = String(token).replace(/^xc:/, '').trim();
  if (!/^p\/\d+-\d+-\d+\.html$/.test(path)) throw new Error('bad play token');

  const html = await fetchText(`${BASE}/${path}`, { referer: `${BASE}/`, ttl: 60 });

  let url = '';
  const idx = html.indexOf('var player_aaaa');
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
  if (!url) url = (/"url"\s*:\s*"([^"]+)"/.exec(html) || [])[1] || '';
  url = url.replace(/\\\//g, '/');
  if (!/^https?:\/\//i.test(url)) throw new Error('play resolve failed');
  return url;
}

export default {
  key: 'xcyycn',
  name: '星辰影院',
  base: BASE,
  home,
  category,
  search,
  detail,
  play,
};
