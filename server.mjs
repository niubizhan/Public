/**
 * TVBox 4.4 接口 · 局域网 HTTP 服务
 *
 * 为什么需要它：
 *   这台电视是 Android 4.4（SDK19），其 OpenSSL 与 Cloudflare 的 pages.dev / workers.dev
 *   无法商定出共同的 TLS 套件，握手阶段就被服务器以 alert 40（handshake_failure）拒绝：
 *     javax.net.ssl.SSLProtocolException: SSL handshake aborted:
 *     ssl3_get_server_hello:SSLv6 alert handshake failure
 *   而 Cloudflare 全站证书为 ECDSA，且已禁用 TLS 1.0 / 老式 RSA 套件，
 *   4.4 的 TLS 栈既升不上去也降不下来，所以这台电视**永远**访问不了 *.pages.dev。
 *
 * 做法：
 *   接口逻辑不变（仍复用 src/index.js 的 Worker fetch 处理函数），
 *   只是把出口从 Cloudflare 换成这台常开电脑的局域网明文 HTTP。
 *   电视 -> 192.168.5.x:8787（HTTP，无 TLS，4.4 完全没问题）
 *   电脑 -> fofoys / piaoys / ixcyy（HTTPS 由电脑的现代 TLS 栈完成）
 *
 * 用法：
 *   node server.mjs              # 默认 0.0.0.0:8787
 *   PORT=9000 node server.mjs
 *
 * 电视端配置地址填： http://<本机局域网IP>:8787/
 */

import http from 'node:http';
import os from 'node:os';
import worker from './src/index.js';

const PORT = Number(process.env.PORT || 8787);
const HOST = process.env.HOST || '0.0.0.0';

/** 缓存秒数：首页/分类这类列表页短缓存，详情与播放不缓存 */
const TTL_LIST = 120;
const TTL_HOME = 300;

/** 极简 LRU：key -> { body, status, headers, exp } */
const cache = new Map();
const CACHE_MAX = 200;

function cacheKey(path, search) {
  if (!path.startsWith('/api/')) return null;
  const p = new URLSearchParams(search);
  // 详情（ids）与播放（play）结果带时效性，不缓存
  if (p.get('ids') || p.get('play')) return null;
  return path + '?' + search;
}

function ttlFor(search) {
  const p = new URLSearchParams(search);
  if (p.get('ac') === 'detail' && !p.get('t') && !p.get('wd')) return TTL_HOME;
  return TTL_LIST;
}

function lanAddresses() {
  const out = [];
  const nets = os.networkInterfaces();
  for (const name of Object.keys(nets)) {
    for (const ni of nets[name] || []) {
      if (ni.family === 'IPv4' && !ni.internal) out.push({ name, address: ni.address });
    }
  }
  return out;
}

const server = http.createServer(async (req, res) => {
  const started = Date.now();
  let path = '/';
  let search = '';
  try {
    const u = new URL(req.url, `http://${req.headers.host || `127.0.0.1:${PORT}`}`);
    path = u.pathname.replace(/\/+$/, '') || '/';
    search = u.search.replace(/^\?/, '');

    const key = req.method === 'GET' ? cacheKey(path, search) : null;
    if (key) {
      const hit = cache.get(key);
      if (hit && hit.exp > Date.now()) {
        res.writeHead(hit.status, hit.headers);
        res.end(hit.body);
        log(req, path, search, 200, Date.now() - started, 'HIT');
        return;
      }
      if (hit) cache.delete(key);
    }

    const request = new Request(u.toString(), {
      method: req.method,
      headers: req.headers,
    });

    const r = await worker.fetch(request, {}, {});
    const buf = Buffer.from(await r.arrayBuffer());
    const headers = {};
    for (const [k, v] of r.headers) headers[k] = v;

    if (key && r.status === 200) {
      if (cache.size >= CACHE_MAX) cache.delete(cache.keys().next().value);
      cache.set(key, {
        body: buf,
        status: r.status,
        headers,
        exp: Date.now() + ttlFor(search) * 1000,
      });
    }

    res.writeHead(r.status, headers);
    res.end(buf);
    log(req, path, search, r.status, Date.now() - started, key ? 'MISS' : '-');
  } catch (e) {
    const msg = JSON.stringify({ code: 0, msg: String((e && e.stack) || e), list: [] });
    res.writeHead(500, { 'Content-Type': 'application/json; charset=utf-8' });
    res.end(msg);
    log(req, path, search, 500, Date.now() - started, 'ERR');
    console.error('[error]', (e && e.stack) || e);
  }
});

function log(req, path, search, status, ms, tag) {
  const q = search ? '?' + search.slice(0, 90) : '';
  console.log(
    `${new Date().toLocaleTimeString('zh-CN', { hour12: false })} ${status} ${ms}ms [${tag}] ${path}${q}`
  );
}

server.on('error', (e) => {
  if (e.code === 'EADDRINUSE') {
    console.error(`端口 ${PORT} 已被占用。换个端口：PORT=8788 node server.mjs`);
  } else {
    console.error(e);
  }
  process.exit(1);
});

server.listen(PORT, HOST, () => {
  console.log('TVBox 4.4 局域网接口已启动');
  console.log(`  本机:   http://127.0.0.1:${PORT}/`);
  for (const a of lanAddresses()) {
    console.log(`  局域网: http://${a.address}:${PORT}/   (${a.name})  <- 电视端填这个`);
  }
  console.log('  停止: Ctrl+C');
});
