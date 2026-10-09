import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

const here = path.dirname(fileURLToPath(import.meta.url));

// 产物直出 internal/console/dist（go:embed 落点，ADR-0044）；outDir 在
// 工程根之外时 vite 要求显式 emptyOutDir。dev 代理 /v1 到 fleetlyd 的
// REST gateway（默认 :9081）——开发面不进 fleetlyd。
// test 面（F3.1）：jsdom 环境（TanStack Query renderHook 消费面），
// `pnpm test` = console:verify 的组成步（与 CI console job 同口径）。
// UI v2 重构（ADR-0057）：@ alias 对齐 components.json（shadcn 体系）；
// TanStack Router 文件式路由（router 插件须先于 react 插件），autoCodeSplitting
// 延续走查性能批的路由级分包口径。
export default defineConfig({
  plugins: [
    tanstackRouter({
      routesDirectory: "src/routes",
      generatedRouteTree: "src/routeTree.gen.ts",
      autoCodeSplitting: true,
    }),
    react(),
    tailwindcss(),
  ],
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
        // vendor 拆分（走查性能批口径的延续）：壳层依赖（react 全家桶/
        // router/query/radix/lucide）吃 immutable 缓存；重库按消费路由
        // 独立分包——charts 随指标页、table/forms 随资源页、virtual 随
        // 日志页、xterm 随终端页，首屏不背。
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          if (id.includes("@xterm")) return "xterm";
          if (id.includes("recharts") || id.includes("d3-") || id.includes("victory") || id.includes("internmap")) {
            return "charts";
          }
          if (id.includes("@tanstack/react-table") || id.includes("@tanstack/table-core")) return "table";
          if (id.includes("@tanstack/react-virtual")) return "virtual";
          if (id.includes("react-hook-form") || id.includes("zod") || id.includes("@hookform")) return "forms";
          if (id.includes("cmdk")) return "cmdk";
          return "vendor";
        },
      },
    },
  },
  server: {
    proxy: {
      // dev API 目标可环境覆盖（FLEETLY_DEV_API，staging 走查指隧道口）
      "/v1": process.env.FLEETLY_DEV_API ?? "http://localhost:9081",
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
