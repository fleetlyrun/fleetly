import { defineConfig, devices } from "@playwright/test";

// Console 视觉/可访问性基线（2026-11-16 全面收口）：mock /v1 后跑真实
// Vite 壳，避免依赖 fleetlyd；snapshot 基线随仓提交，CI 可加
// `pnpm visual` 作为可选 job。
export default defineConfig({
  testDir: "./visual",
  timeout: 30_000,
  expect: { timeout: 10_000, toHaveScreenshot: { animations: "disabled", caret: "hide" } },
  fullyParallel: true,
  reporter: [["list"]],
  use: {
    baseURL: "http://127.0.0.1:4179",
    viewport: { width: 1600, height: 1000 },
    colorScheme: "dark",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "pnpm dev --host 127.0.0.1 --port 4179",
    url: "http://127.0.0.1:4179/overview",
    reuseExistingServer: true,
    timeout: 120_000,
  },
});
