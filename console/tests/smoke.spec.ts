// Console 冒烟三链路（W1 T1-V2.6）：登录 → 应用详情+部署历史 → 登出，
// 全部打真实 staging（baseURL 由 playwright.config.ts 提供）。锚点只用
// 既有 testid（app-row / deployment-row / state-badge）与可访问名
// （"API token" / "Sign in" / "Sign out" / "Deployments"），不新造锚点。
// token 从 FLEETLY_SMOKE_TOKEN 读取（config 已并入 .env.smoke 缺省），
// 本文件不入库真值。

import { expect, test, type Page } from "@playwright/test";

const TOKEN = process.env.FLEETLY_SMOKE_TOKEN ?? "";

// 每个用例独立 browser context（localStorage 不共享），各自完整走一遍
// 登录——登出链路也因此不依赖前序用例的落库状态。
test.describe.configure({ mode: "serial" });

test.skip(TOKEN === "", "FLEETLY_SMOKE_TOKEN 未设置，跳过 staging 冒烟");

/** 从 /ui/ 未登录态粘贴 token 走真实登录，等已登录侧边栏出现。 */
async function signIn(page: Page): Promise<void> {
  await page.goto("/ui/");
  const tokenInput = page.getByLabel("API token");
  await expect(tokenInput).toBeVisible();
  await tokenInput.fill(TOKEN);
  await page.getByRole("button", { name: "Sign in" }).click();
  // 登录成功 → Gate 换已登录路由壳；侧边栏 Applications 可点即已登录。
  // exact: true——首页还有 "View applications" 链接，子串匹配会撞严格模式。
  await expect(
    page.getByRole("link", { name: "Applications", exact: true }),
  ).toBeVisible();
}

test("登录：粘贴 token 后应用列表出现两个 staging 应用", async ({ page }) => {
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

test("登出：Sign out 后回到登录页", async ({ page }) => {
  await signIn(page);

  await page.getByRole("button", { name: "Sign out" }).click();
  // 登出 → navigate("/login") → 未登录 Gate 渲染登录表单。
  await expect(page.getByLabel("API token")).toBeVisible();
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
  await expect(page).toHaveURL(/\/ui\/(login)?$/);

  await page.screenshot({ path: "smoke-artifacts/03-logout-login-page.png", fullPage: true });
});
