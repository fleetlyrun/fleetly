import { fileURLToPath, URL } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

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
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
  },
});
