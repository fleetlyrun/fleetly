import { fileURLToPath, URL } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { configDefaults, defineConfig } from "vitest/config";

// 开发态：dev server 在 /ui/ 前缀下服务（与 daemon 静态托管的 URL 空间
// 一致，深链形态两边等价）；REST /v1 经 dev proxy 转发到本机 fleetlyd
// ——网关不提供 CORS 头，代理是开发态跨源规避的唯一手段。
// 生产态：`pnpm build` 产物为相对 base（/ui/assets/...），由 daemon
// console.static_dir 托管，数据面同源 /v1（或 VITE_API_BASE 覆盖）。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "/ui/",
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/v1": {
        target: "http://127.0.0.1:8420",
        changeOrigin: true,
      },
    },
  },
  build: {
    rolldownOptions: {
      output: {
        // vendor 稳定分包（2026-09-25 加载优化）：react 栈/查询/无障碍组件
        // 各自成 chunk——应用代码每版全变，vendor 哈希稳定（浏览器跨版本
        // 缓存不失效）；xterm/uplot 只被所属路由引用，随路由 chunk 自动
        // 分离，不进首屏。路由级 lazy 在 App.tsx（登录/邀请/壳 eager）。
        advancedChunks: {
          groups: [
            {
              name: "vendor-react",
              test: /[\\/]node_modules[\\/](react|react-dom|react-router|react-router-dom|scheduler|@remix-run)[\\/]/,
              priority: 10,
            },
            {
              name: "vendor-query",
              test: /[\\/]node_modules[\\/]@tanstack[\\/]/,
            },
            {
              name: "vendor-radix",
              test: /[\\/]node_modules[\\/]@radix-ui[\\/]/,
            },
          ],
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    // vitest 与 Playwright 的分工边界（W1 T1-V2.6）：tests/ 是 Playwright
    // 冒烟（真浏览器打 staging），vitest 只收 src/** 的 jsdom 单测——
    // 否则默认 include `**/*.spec.ts` 会把 Playwright spec 当单测加载。
    exclude: [...configDefaults.exclude, "tests/**"],
  },
});
