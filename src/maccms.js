/**
 * TVBox 4.4 (type=4) HTTP JSON 接口的数据封装
 *
 * 规范来源：TVBoxOS-main 源码
 *   - app/src/main/java/com/github/tvbox/osc/viewmodel/SourceViewModel.java
 *   - app/src/main/java/com/github/tvbox/osc/bean/AbsJson.java
 *   - app/src/main/java/com/github/tvbox/osc/bean/AbsSortJson.java
 *
 * 首页:   GET {api}?filter=true                 -> {"class":[...],"list":[...]}
 * 分类:   GET {api}?ac=detail&t=..&pg=..        -> {"code":1,"page":..,"list":[...]}
 * 搜索:   GET {api}?wd=..&ac=detail&quick=..    -> 同上
 * 详情:   GET {api}?ac=detail&ids=..            -> 同上（list 单条，含播放字段）
 * 播放:   GET {api}?play=..&flag=..             -> {"url":"..","parse":0,"header":{...}}
 */

const VOD_DEFAULTS = {
  vod_id: '',
  type_id: '',
  type_id_1: '',
  type_name: '',
  group_id: '0',
  vod_name: '',
  vod_sub: '',
  vod_en: '',
  vod_pic: '',
  vod_pic_thumb: '',
  vod_pic_slide: '',
  vod_pic_screenshot: '',
  vod_actor: '',
  vod_director: '',
  vod_writer: '',
  vod_behind: '',
  vod_blurb: '',
  vod_content: '',
  vod_remarks: '',
  vod_pubdate: '',
  vod_total: '',
  vod_serial: '',
  vod_area: '',
  vod_lang: '',
  vod_year: '',
  vod_version: '',
  vod_state: '',
  vod_tag: '',
  vod_class: '',
  vod_score: '',
  vod_time: '',
  vod_hits: '',
  vod_duration: '',
  vod_isend: '0',
  vod_lock: '0',
  vod_level: '0',
  vod_copyright: '0',
  vod_points: '0',
  vod_points_play: '0',
  vod_points_down: '0',
  vod_up: '0',
  vod_down: '0',
  vod_trysee: '0',
  vod_douban_score: '',
  vod_pwd: '',
  vod_pwd_url: '',
  vod_pwd_play: '',
  vod_pwd_play_url: '',
  vod_pwd_down: '',
  vod_pwd_down_url: '',
  vod_jumpurl: '',
  vod_play_from: '',
  vod_play_server: 'no',
  vod_play_note: '',
  vod_play_url: '',
  vod_down_from: '',
  vod_down_server: '',
  vod_down_note: '',
  vod_down_url: '',
  vod_plot: '0',
  vod_plot_name: '',
  vod_plot_detail: '',
  action: '',
};

/** 把适配器返回的 vod 对象补全为 TVBox 可解析的完整字段（全部转成字符串） */
export function normalizeVod(vod) {
  const out = { ...VOD_DEFAULTS };
  for (const [k, v] of Object.entries(vod || {})) {
    if (v === undefined || v === null) continue;
    out[k] = typeof v === 'string' ? v : String(v);
  }
  return out;
}

/** 分类/搜索/详情的响应信封（AbsJson） */
export function listEnvelope(list, { page = 1, pagecount = 1, total, limit = 20 } = {}) {
  const vods = (list || []).map(normalizeVod);
  return {
    code: 1,
    msg: '数据列表',
    page: Number(page) || 1,
    pagecount: Number(pagecount) || 1,
    limit: String(limit),
    total: Number.isFinite(Number(total)) ? Number(total) : vods.length,
    list: vods,
  };
}

/** 首页响应信封（AbsSortJson：class + list，list 用作首页推荐位） */
export function homeEnvelope(classes, list) {
  return {
    class: (classes || []).map((c) => ({
      type_id: String(c.type_id ?? c.id ?? ''),
      type_name: String(c.type_name ?? c.name ?? ''),
      type_flag: c.type_flag || '',
    })),
    list: (list || []).map(normalizeVod),
  };
}

/**
 * type=1（maccms 标准）首页信封
 *
 * TVBox 的 type=1 首页请求是 `{api}?ac=detail`（不带 t/ids/wd），
 * 解析器 AbsJson 同时读 class（分类导航）和 list（推荐位）；
 * 缺 class 时首页只剩「主页」一个标签，分类点不进去。
 * 真正的 maccms API 也是在 detail 响应里带 class，这里对齐该行为。
 */
export function homeListEnvelope(classes, list, { page = 1, pagecount = 1 } = {}) {
  const env = listEnvelope(list, { page, pagecount, total: (list || []).length });
  env.class = (classes || []).map((c) => ({
    type_id: String(c.type_id ?? c.id ?? ''),
    type_name: String(c.type_name ?? c.name ?? ''),
    type_flag: c.type_flag || '',
  }));
  return env;
}

/** 播放解析响应（normalizePlayerResult 会读取 url / parse / header / playUrl） */
export function playEnvelope(url, { header = {}, flag = '', playUrl = '' } = {}) {
  return {
    url: url || '',
    parse: 0,
    jx: 0,
    playUrl,
    header,
    flag,
  };
}

/** 空结果 */
export function emptyList(page = 1) {
  return listEnvelope([], { page, pagecount: 1, total: 0 });
}
