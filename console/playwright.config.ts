// 冒烟测试配置（W1 T1-V2.6）：三链路打真实 staging（daemon 同源托管
// /ui + /v1），不走 dev server、不打 mock。token 只经环境变量注入
// （CI = secrets；本机 = 命令行内联或 console/.env.smoke，后者不入库），
// 任何提交文件里不得出现真值。

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { defineConfig, devices } from "@playwright/test";

// 本机便利：console/.env.smoke（.gitignore 排除）提供 KEY=VALUE 缺省；
// 真实环境变量优先，绝不覆盖 CI/命令行注入值。
try {
  const envFile = fileURLToPath(new URL("./.env.smoke", import.meta.url));
  for (const line of readFileSync(envFile, "utf-8").split(/\r?\n/)) {
    const match = /^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/.exec(line.trim());
    if (match && process.env[match[1]] === undefined) {
      process.env[match[1]] = match[2];
    }
  }
} catch {
  // 无 .env.smoke：仅靠 process.env（CI 形态），正常路径。
}

export default defineConfig({
  testDir: "tests",
  // 单文件内按序执行：三链路共享 staging 活环境，节奏放缓、互不并发施压。
  fullyParallel: false,
  timeout: 60_000,
  expect: {
    // staging 网络往返 + react-query 首拉，放宽到 15s。
    timeout: 15_000,
  },
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI
    ? [["list"], ["html", { open: "never" }]]
    : [["list"], ["html", { open: "on-failure" }]],
  outputDir: "test-results",
  use: {
    baseURL: process.env.FLEETLY_SMOKE_BASE ?? "http://dev.fleetly.run:8420",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
