import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// 产物直出 internal/console/dist（go:embed 落点，ADR-0044）；outDir 在
// 工程根之外时 vite 要求显式 emptyOutDir。dev 代理 /v1 到 fleetlyd 的
// REST gateway（默认 :9081）——开发面不进 fleetlyd。
// test 面（F3.1）：jsdom 环境（TanStack Query renderHook 消费面），
// `pnpm test` = console:verify 的组成步（与 CI console job 同口径）。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/console/dist",
    emptyOutDir: true,
    target: "es2022",
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
