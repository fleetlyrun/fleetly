import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppSettingsTab } from "./app-settings";
import { setToken } from "@/lib/token";

// GitHookCard 移除通道钉测（W-8）：确认框 → DELETE /v1/apps/A1/hook →
// hook 查询失效重取 404 → 卡片翻回配置态。Link 依赖 router 上下文
//（useLinkProps 读 router 实例），单测无路由树——换纯 <a> 桩
//（app-overview 同款）。

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, Link: ({ children }: { children?: ReactNode }) => <a href="#">{children}</a> };
});

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> };
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

// TestDeleteGitHook 的 console 面（与 apitest/hooks_delete_test.go 同一
// 契约）：GET hook 在删后必须 404（未配置口径），DELETE 走解除通道。
function renderSettings() {
  let configured = true;
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path === "/v1/apps/A1/hook" && (init?.method ?? "GET") === "GET") {
      if (!configured) return new Response("not found", { status: 404 });
      return new Response(
        JSON.stringify({
          hook: {
            app_id: "A1", repo: "https://github.com/acme/shop.git", branch: "main",
            dockerfile: "Dockerfile", watch_paths: [], token_prefix: "flthook_ab",
            updated_at: "2026-10-17T00:00:00Z",
          },
        }),
        { status: 200 },
      );
    }
    if (path === "/v1/apps/A1/hook" && init?.method === "DELETE") {
      configured = false;
      return new Response("{}", { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });

  const { wrapper } = withClient();
  render(
    <AppSettingsTab projectId="P1" appId="A1" appName="web" createdAt="2026-10-17T00:00:00Z" onDeleted={() => {}} />,
    { wrapper },
  );
}

describe("AppSettingsTab git hook remove", () => {
  it("removes a configured hook through the confirm dialog and flips back to the configure form", async () => {
    renderSettings();

    // 配置态在册（repo 回显）。
    expect(await screen.findByText("https://github.com/acme/shop.git")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Remove…" }));
    expect(await screen.findByText("Remove the git deploy hook?")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith("/v1/apps/A1/hook", expect.objectContaining({ method: "DELETE" })),
    );

    // 删除生效：hook 查询失效重取 404 → 卡片翻回配置态。
    await screen.findByText("Configure hook");
    expect(screen.queryByText("https://github.com/acme/shop.git")).toBeNull();
  });
});
