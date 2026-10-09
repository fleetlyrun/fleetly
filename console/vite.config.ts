import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

const here = path.dirname(fileURLToPath(import.meta.url));

// 产物直出 internal/console/dist（go:embed 落点，ADR-0044）；outDir 在
// 工程根之外时 vite 要求显式 emptyOutDir。dev 代理 /v1 到 fleetlyd 的
// REST gateway（默认 :9081）——开发面不进 fleetlyd。
// test 面（F3.1）：jsdom 环境（TanStack Query renderHook 消费面），
// `pnpm test` = console:verify 的组成步（与 CI console job 同口径）。
// UI v2 重构（ADR-0057）：@ alias 对齐 components.json（shadcn 体系）。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(here, "src"),
    },
  },
  build: {
    outDir: "../internal/console/dist",
    emptyOutDir: true,
    target: "es2022",
    rollupOptions: {
      output: {
        // vendor 拆分（走查性能批）：react 全家桶变更频率远低于业务代码——
        // 独立 chunk 吃满 immutable 缓存；xterm 随 Terminal 路由 lazy 自动
        // 分包，不进首屏。
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          if (id.includes("@xterm")) return "xterm";
          return "vendor";
        },
      },
    },
  },
  server: {
    proxy: {
      "/v1": "http://localhost:9081",
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
