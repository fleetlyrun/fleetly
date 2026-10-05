import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// 产物直出 internal/console/dist（go:embed 落点，ADR-0044）；outDir 在
// 工程根之外时 vite 要求显式 emptyOutDir。dev 代理 /v1 到 fleetlyd 的
// REST gateway（默认 :9081）——开发面不进 fleetlyd。
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
});
