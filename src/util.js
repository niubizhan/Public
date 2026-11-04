/**
 * 零 JS 爬虫工具箱
 *
 * 所谓“零 JS”：不启动浏览器、不执行目标站的 JS（无 headless/无 eval/无 new Function），
 * 只用 HTTP 抓取 + 正则/字符串解析。目标站内嵌的 JS 字面量（如 var urlList = {...}）
 * 也是按“纯文本”转成 JSON 后读取，而不是解释执行。
 */

export const UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36';

const CACHE_TTL_DEFAULT = 120;

/**
 * 抓取文本（GET/POST）。GET 默认走 Cloudflare Cache API，降低对源站压力。
 */
export async function fetchText(url, opts = {}) {
  const {
    method = 'GET',
    body,
    headers = {},
    referer,
    ttl = CACHE_TTL_DEFAULT,
    timeout = 20000,
  } = opts;

  const finalHeaders = {
    'User-Agent': UA,
    // 注意：Accept 里不能带 application/json —— 部分源站（如飘花）会因此返回
    // JSON 转义过的 HTML（\uXXXX + \"），导致正则全部失配。
    Accept: 'text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8',
    'Accept-Language': 'zh-CN,zh;q=0.9,en;q=0.8',
    ...headers,
  };
  if (referer) finalHeaders.Referer = referer;

  const useCache = method === 'GET' && ttl > 0 && typeof caches !== 'undefined' && !!caches.default;
  const cacheKey = useCache ? new Request(url, { method: 'GET', headers: { 'User-Agent': UA } }) : null;

  try {
    if (useCache) {
      const hit = await caches.default.match(cacheKey);
      if (hit) return await hit.text();
    }
  } catch (_) {
    /* 缓存不可用时忽略 */
  }

  const ctrl = typeof AbortController !== 'undefined' ? new AbortController() : null;
  const timer = ctrl ? setTimeout(() => ctrl.abort(), timeout) : null;

  let res;
  try {
    res = await fetch(url, {
      method,
      headers: finalHeaders,
      body,
      redirect: 'follow',
      signal: ctrl ? ctrl.signal : undefined,
    });
  } finally {
    if (timer) clearTimeout(timer);
  }

  if (!res.ok) throw new Error(`HTTP ${res.status} ${url}`);

  const text = await res.text();

  if (useCache) {
    try {
      const ct = res.headers.get('Content-Type') || 'text/html; charset=utf-8';
      await caches.default.put(
        cacheKey,
        new Response(text, {
          status: 200,
          headers: { 'Content-Type': ct, 'Cache-Control': `public, max-age=${ttl}` },
        })
      );
    } catch (_) {
      /* 含 Set-Cookie 等不可缓存响应，忽略 */
    }
  }
  return text;
}

/** HTML 实体解码 */
export function decodeEntities(s) {
  return String(s)
    .replace(/&#x([0-9a-fA-F]+);/g, (_, h) => safeFromCode(parseInt(h, 16)))
    .replace(/&#(\d+);/g, (_, d) => safeFromCode(parseInt(d, 10)))
    .replace(/&nbsp;/g, ' ')
    .replace(/&amp;/g, '&')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&#39;/g, "'")
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>');
}

function safeFromCode(cp) {
  try {
    return String.fromCodePoint(cp);
  } catch (_) {
    return '';
  }
}

/** 去标签 + 实体解码 + 压缩空白 */
export function stripTags(s) {
  if (!s) return '';
  return decodeEntities(String(s).replace(/<[^>]*>/g, ''))
    .replace(/\s+/g, ' ')
    .trim();
}

/** 从标签串里取属性值（避免匹配到 data-src 这类同名后缀） */
export function attr(tag, name) {
  if (!tag) return '';
  const m = new RegExp(`(?<![\\w-])${name}\\s*=\\s*"([^"]*)"`).exec(tag);
  return m ? m[1] : '';
}

/** 相对地址转绝对地址 */
export function absUrl(base, u) {
  if (!u) return '';
  if (/^https?:\/\//i.test(u)) return u;
  return base.replace(/\/+$/, '') + (u.startsWith('/') ? u : '/' + u);
}

/** 把 JS 单引号对象字面量转成 JSON 文本（纯文本替换，不执行代码） */
export function jsLiteralToJson(text) {
  let s = String(text);
  // 1) 给未加引号的对象键补引号： {key: ...} 、 ,key: ...
  s = s.replace(/([{,]\s*)([A-Za-z_$][A-Za-z0-9_$]*)(\s*:)/g, '$1"$2"$3');
  // 2) 单引号字符串 -> 双引号字符串
  s = s.replace(/'((?:[^'\\]|\\.)*)'/g, (_, p1) => {
    const inner = p1
      .replace(/\\'/g, "'")
      .replace(/\\"/g, '"')
      .replace(/\\n/g, '\n')
      .replace(/\\t/g, '\t')
      .replace(/\\\\/g, '\\');
    return '"' + inner.replace(/"/g, '\\"') + '"';
  });
  return s;
}

/** 从 src[fromIndex] 的 '{' 起做括号配平，取出完整对象文本（字符串内的括号不计） */
export function extractBalanced(src, fromIndex) {
  const start = src.indexOf('{', fromIndex);
  if (start < 0) return null;
  let depth = 0;
  let quote = null;
  let esc = false;
  for (let i = start; i < src.length; i++) {
    const c = src[i];
    if (quote) {
      if (esc) {
        esc = false;
        continue;
      }
      if (c === '\\') {
        esc = true;
        continue;
      }
      if (c === quote) quote = null;
      continue;
    }
    if (c === '"' || c === "'") {
      quote = c;
      continue;
    }
    if (c === '{') depth++;
    else if (c === '}') {
      depth--;
      if (depth === 0) return src.slice(start, i + 1);
    }
  }
  return null;
}

/** 读取页面里 `var name = 'xxx'` 的字符串值 */
export function findQuotedVar(html, name) {
  const m = new RegExp(`var\\s+${name}\\s*=\\s*['"]((?:[^'"\\\\]|\\\\.)*)['"]`).exec(html);
  if (!m) return '';
  return m[1]
    .replace(/\\'/g, "'")
    .replace(/\\"/g, '"')
    .replace(/\\n/g, '\n')
    .replace(/\\t/g, '\t')
    .replace(/\\\\/g, '\\');
}

/** 按分隔符切分但忽略分隔符两侧重复（用于 $$$ 去重场景的辅助） */
export function uniq(arr) {
  return [...new Set(arr.filter((x) => x !== undefined && x !== null && x !== ''))];
}
