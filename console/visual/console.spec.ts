import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { mockConsole } from "./mock-api";

test.describe("console visual", () => {
  test.beforeEach(async ({ page }) => {
    await mockConsole(page);
  });

  test("overview", async ({ page }) => {
    await page.goto("/overview");
    await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
    await expect(page).toHaveScreenshot("overview.png", { fullPage: true });
  });

  test("app detail", async ({ page }) => {
    await page.goto("/p/proj-atlas/apps/app-atlas-api");
    await expect(page.getByRole("heading", { name: "api" })).toBeVisible();
    await expect(page).toHaveScreenshot("app-detail.png", { fullPage: true });
  });

  test("databases list", async ({ page }) => {
    await page.goto("/p/proj-atlas/databases");
    await expect(page.getByRole("heading", { name: "Databases" })).toBeVisible();
    await expect(page.getByRole("row", { name: /pg/ })).toBeVisible();
    await expect(page).toHaveScreenshot("databases.png", { fullPage: true });
  });

  test("settings", async ({ page }) => {
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
    await expect(page).toHaveScreenshot("settings.png", { fullPage: true });
  });

  test("workbench logs", async ({ page }) => {
    await page.goto("/logs");
    await expect(page.getByRole("heading", { name: "Logs" })).toBeVisible();
    await expect(page).toHaveScreenshot("logs.png", { fullPage: true });
  });

  test("axe critical/serious on overview and app detail", async ({ page }) => {
    for (const path of ["/overview", "/p/proj-atlas/apps/app-atlas-api"]) {
      await page.goto(path);
      const result = await new AxeBuilder({ page }).analyze();
      const blocking = result.violations.filter((violation) => violation.impact === "critical" || violation.impact === "serious");
      expect(blocking, `${path}: ${blocking.map((violation) => `${violation.id}: ${violation.help}`).join("\n")}`).toEqual([]);
    }
  });
});

test.describe("login", () => {
  test("login card", async ({ page }) => {
    await page.goto("/login");
    await expect(page.getByRole("heading", { name: "fleetly console" })).toBeVisible();
    await expect(page).toHaveScreenshot("login.png", { fullPage: true });
  });
});

test.describe("console light + mobile", () => {
  test("app detail light", async ({ page }) => {
    await mockConsole(page, "light");
    await page.goto("/p/proj-atlas/apps/app-atlas-api");
    await expect(page.getByRole("heading", { name: "api" })).toBeVisible();
    await expect(page).toHaveScreenshot("app-detail-light.png", { fullPage: true });
  });

  test("app detail mobile", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await mockConsole(page);
    await page.goto("/p/proj-atlas/apps/app-atlas-api");
    await expect(page.getByRole("heading", { name: "api" })).toBeVisible();
    await expect(page).toHaveScreenshot("app-detail-mobile.png", { fullPage: true });
  });
});
