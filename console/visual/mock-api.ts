import type { Page } from "@playwright/test";

const now = "2026-11-15T12:00:00.000Z";

// 固定 fixture：只覆盖视觉测试需要的最少目录/详情数据；其它读面返回空
// envelope，页面进入诚实的空/错误态而非依赖 fleetlyd。
function bodyFor(path: string): object {
  if (path === "/v1/whoami") {
    return { token_name: "visual", role_name: "Platform Owner", user_name: "Ada Lovelace", platform_admin: true };
  }
  if (path === "/v1/projects") {
    return { projects: [{ id: "proj-atlas", name: "Atlas Platform", created_at: now }, { id: "proj-nova", name: "Nova Commerce", created_at: now }] };
  }
  if (path === "/v1/apps") {
    return { apps: [{ id: "app-atlas-api", project_id: "proj-atlas", name: "api", created_at: now }, { id: "app-atlas-web", project_id: "proj-atlas", name: "web", created_at: now }] };
  }
  if (path === "/v1/deployments") {
    return { deployments: [{ id: "dpl-1", app_id: "app-atlas-api", state: "succeeded", created_at: now, updated_at: now }] };
  }
  if (path === "/v1/nodes") {
    return { nodes: [{ platform_id: "node-1", hostname: "fra-prod-1", available: true, role: "worker", relay_online: true, relay_version: "1", first_seen_at: now, last_seen_at: now }] };
  }
  if (path === "/v1/databases") {
    return { databases: [{ id: "db-atlas", name: "pg", engine: "postgres", status: "running", project_id: "proj-atlas", host: "db-atlas.internal", port: 5432, created_at: now, last_backup_at: null }] };
  }
  if (path === "/v1/pubsubs") {
    return { pubsubs: [{ id: "pub-atlas", name: "bus", engine: "nats", status: "running", project_id: "proj-atlas", host: "pub.internal", port: 4222, created_at: now }] };
  }
  if (path === "/v1/bucket-stores") {
    return { bucket_stores: [{ id: "bkt-atlas", name: "store", status: "running", project_id: "proj-atlas", host: "bkt.internal", port: 9000, created_at: now }] };
  }
  if (path === "/v1/tasks") {
    return { tasks: [{ id: "task-atlas", name: "job", state: "active", form: "cron", project_id: "proj-atlas", image: "busybox", created_at: now }] };
  }
  return {};
}

export async function mockConsole(page: Page, theme: "dark" | "light" = "dark"): Promise<void> {
  await page.addInitScript(
    ({ theme }) => {
      localStorage.setItem("fleetly_console_token", "visual");
      localStorage.setItem("fleetly_console_theme", theme);
      document.documentElement.classList.toggle("dark", theme === "dark");
    },
    { theme },
  );
  await page.route("**/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(bodyFor(path)) });
  });
}
