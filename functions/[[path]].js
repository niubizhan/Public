/**
 * Cloudflare Pages Functions 入口（通配路由）
 * Pages 会用 esbuild 打包 ../src 下的模块，与 Workers 共用同一份代码。
 */
import worker from '../src/index.js';

export const onRequest = (context) => worker.fetch(context.request, context.env, context);
