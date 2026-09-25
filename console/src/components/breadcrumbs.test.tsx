// 面包屑测试（2026-09-25 审查 P2-9 前序遗留：应用子页签在详情查询落地前
// 渲染字面 "Application" 占位）：落地前骨架占位「—」、落地后反解业务名
// （详情缓存观察者对子页签路径同样生效）、列表缓存行名优先。ProjectDetail
// 测试同款 TeamProjectProvider 装配。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Breadcrumbs } from "@/components/breadcrumbs";
import { TeamProjectProvider } from "@/lib/context";
import { setToken } from "@/api/client";

// 26 字符规范 ULID（与 breadcrumbs.tsx 的 ULID_RE 判据同形——平台 id 段）。
const APP_ID = "01APP000000000000000000000";

function jsonResponse(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: "",
    json: () => Promise.resolve(body),
  };
}

function stubContextFetch(opts: { hangApp?: boolean } = {}) {
  return vi.fn((input: unknown) => {
    const u = String(input);
    if (u.includes("/auth/me")) {
      return Promise.resolve(
        jsonResponse(200, {
          user: { id: "01U1", email: "op@t.test", is_platform_admin: false },
          teams: [
            { team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: "owner" },
          ],
        }),
      );
    }
    if (u.endsWith("/projects")) {
      return Promise.resolve(jsonResponse(200, { projects: [] }));
    }
    if (u.includes(`/apps/${APP_ID}`)) {
      if (opts.hangApp) return new Promise(() => undefined);
      return Promise.resolve(jsonResponse(200, { id: APP_ID, name: "demo" }));
    }
    return Promise.resolve(jsonResponse(404, { message: `unmocked ${u}` }));
  });
}

function renderCrumbs(path: string, client: QueryClient) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <QueryClientProvider client={client}>
        <TeamProjectProvider>
          <Breadcrumbs />
        </TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function appLinkIn(nav: HTMLElement) {
  return nav.querySelector(`a[href="/apps/${APP_ID}"]`);
}

// 详情路径（id = 末段）的应用段是纯文本当前页节点（aria-current），非链接。
function appCurrentIn(nav: HTMLElement) {
  return nav.querySelector('span[aria-current="page"]');
}

beforeEach(() => {
  setToken("");
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Breadcrumbs app id resolution (2026-09-25 review P2-9)", () => {
  it("shows a skeleton placeholder before the app detail resolves (sub-tab path)", async () => {
    vi.stubGlobal("fetch", stubContextFetch({ hangApp: true }));
    renderCrumbs(`/apps/${APP_ID}/logs`, newClient());

    // 子页签路径（id 非末段）：应用段既有列表缓存也没有、详情查询在途——
    // 骨架占位「—」，不再渲染字面 "Application"，也绝不裸显 ULID。
    const nav = await screen.findByRole("navigation", { name: "Breadcrumb" });
    await waitFor(() => expect(appLinkIn(nav)).not.toBeNull());
    expect(appLinkIn(nav)?.textContent).toBe("—");
    expect(screen.queryByText("Application")).not.toBeInTheDocument();
    expect(nav.textContent).not.toContain(APP_ID);
  });

  it("resolves the app name once the detail data lands (sub-tab path)", async () => {
    vi.stubGlobal("fetch", stubContextFetch());
    renderCrumbs(`/apps/${APP_ID}/logs`, newClient());

    const nav = await screen.findByRole("navigation", { name: "Breadcrumb" });
    // 详情查询落地（与详情壳共用 ["app", id] 键）→ 反解业务名。
    await waitFor(() =>
      expect(appLinkIn(nav)?.textContent).toBe("demo"),
    );
  });

  it("prefers the apps list cache row name on the detail path", async () => {
    vi.stubGlobal("fetch", stubContextFetch({ hangApp: true }));
    const client = newClient();
    // 列表缓存先行（一级页/切换器场景）——行名直接可用，详情在途不回退。
    client.setQueryData(["apps", ""], {
      apps: [{ id: APP_ID, name: "listed-app" }],
    });
    renderCrumbs(`/apps/${APP_ID}`, client);

    const nav = await screen.findByRole("navigation", { name: "Breadcrumb" });
    await waitFor(() =>
      expect(appCurrentIn(nav)?.textContent).toBe("listed-app"),
    );
  });
});
