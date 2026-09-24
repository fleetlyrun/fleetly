// Console 冒烟三链路（W1 T1-V2.6）：登录 → 应用详情+部署历史 → 登出，
// 全部打真实 staging（baseURL 由 playwright.config.ts 提供）。锚点只用
// 既有 testid（app-row / deployment-row / state-badge / login-email /
// login-password / login-submit / user-menu / logout）与可访问名
//（"Applications" / "Deployments"），不新造锚点。
//
// 凭据口径（v0.3 W3-S3 改造）：v0.3 fresh staging 的 bootstrap token 随
// 首用户注册即时吊销（e2e auth.sh 断言口径），FLEETLY_SMOKE_TOKEN 已不可
// 用——CI secrets 中的同名变量自本票起作废（保留也不生效），冒烟改为
// **会话登录驱动**：founder 演练账号（runbook §13 的 staging 演练账号）
// 走登录页邮箱+口令主形态（/v1/auth/login 下发会话 cookie）。凭据经环境
// 变量 FLEETLY_SMOKE_USER / FLEETLY_SMOKE_PASS 注入（缺省
// founder@fleetly.run / founder-pass-1），本文件不入库真值。
//
// 跳过口径（现状延续）：本地无 staging 时整体跳过——FLEETLY_SMOKE_BASE 与
// FLEETLY_SMOKE_USER/PASS 均未提供即跳（与旧「TOKEN 未设置即跳」同性质：
// 无 staging 凭据/目标不跑，不报错）。

import { expect, test, type Page } from "@playwright/test";

const USER = process.env.FLEETLY_SMOKE_USER || "";
const PASS = process.env.FLEETLY_SMOKE_PASS || "";
const BASE = process.env.FLEETLY_SMOKE_BASE || "";
// 缺省 = runbook §13 的 founder 演练账号（显式注入值优先）。
const EMAIL = USER || "founder@fleetly.run";
const PASSWORD = PASS || "founder-pass-1";

// 每个用例独立 browser context（localStorage 不共享），各自完整走一遍
// 登录——登出链路也因此不依赖前序用例的落库状态。
test.describe.configure({ mode: "serial" });

test.skip(
  !BASE && !USER && !PASS,
  "FLEETLY_SMOKE_BASE / FLEETLY_SMOKE_USER 均未设置（本地无 staging），跳过冒烟",
);

/** 从 /ui/ 未登录态走邮箱+口令主形态登录，等已登录侧边栏出现。 */
async function signIn(page: Page): Promise<void> {
  await page.goto("/ui/");
  await page.getByTestId("login-email").fill(EMAIL);
  await page.getByTestId("login-password").fill(PASSWORD);
  await page.getByTestId("login-submit").click();
  // 登录成功 → Gate 换已登录路由壳；侧边栏 Applications 可点即已登录。
  // exact: true——首页还有 "View applications" 链接，子串匹配会撞严格模式。
  await expect(
    page.getByRole("link", { name: "Applications", exact: true }),
  ).toBeVisible();
}

test("登录：邮箱口令会话登录后应用列表出现两个 staging 应用", async ({ page }) => {
  await signIn(page);
  await page
    .getByRole("link", { name: "Applications", exact: true })
    .click();

  const rows = page.getByTestId("app-row");
  await expect(rows.first()).toBeVisible();
  expect(await rows.count()).toBeGreaterThanOrEqual(2);
  await expect(rows.filter({ hasText: "hello-web" }).first()).toBeVisible();
  await expect(rows.filter({ hasText: "gitpush-demo" }).first()).toBeVisible();

  await page.screenshot({ path: "smoke-artifacts/01-login-app-list.png", fullPage: true });
});

test("应用详情+部署历史：hello-web 切 Deployments 页签有历史行", async ({ page }) => {
  await signIn(page);
  await page
    .getByRole("link", { name: "Applications", exact: true })
    .click();
  const rows = page.getByTestId("app-row");
  await expect(rows.first()).toBeVisible();

  await rows.filter({ hasText: "hello-web" }).click();
  // 详情壳加载：标题旁出现派生状态徽章（既有 state-badge 锚点）。
  await expect(page.getByTestId("state-badge").first()).toBeVisible();

  await page.getByRole("tab", { name: "Deployments" }).click();
  await expect(page).toHaveURL(/\/apps\/hello-web\/deployments$/);

  const deploymentRows = page.getByTestId("deployment-row");
  await expect(deploymentRows.first()).toBeVisible();
  expect(await deploymentRows.count()).toBeGreaterThanOrEqual(1);

  await page.screenshot({ path: "smoke-artifacts/02-detail-deployments.png", fullPage: true });
});

test("登出：用户菜单 Sign out 后回到登录页", async ({ page }) => {
  await signIn(page);

  await page.getByTestId("user-menu").click();
  await page.getByTestId("logout").click();
  // 登出 → navigate("/login") → 未登录 Gate 渲染登录表单（邮箱主形态）。
  await expect(page.getByTestId("login-email")).toBeVisible();
  await expect(page.getByTestId("login-submit")).toBeVisible();
  await expect(page).toHaveURL(/\/ui\/(login)?$/);

  await page.screenshot({ path: "smoke-artifacts/03-logout-login-page.png", fullPage: true });
});
