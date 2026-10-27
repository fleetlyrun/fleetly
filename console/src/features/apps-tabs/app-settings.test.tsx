import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppSettingsTab } from "./app-settings";
import { setToken } from "@/lib/token";

// GitHookCard 移除通道钉测（W-8）：确认框 → DELETE /v1/apps/A1/hook →
// hook 查询失效重取 404 → 卡片翻回未配置空态（走查反馈 2026-10-27 后
// 不再裸出创建表单——空态 + Configure 展开）。Link 依赖 router 上下文
//（useLinkProps 读 router 实例），单测无路由树——换纯 <a> 桩
//（app-overview 同款）。
//
// Deployment 摘要卡（走查反馈 2026-10-27）：部署身份答案面三态——
// git 托管 / 非 hook（compose 判定）/ 未部署。

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
  it("removes a configured hook through the confirm dialog and flips back to the not-configured state", async () => {
    renderSettings();

    // 配置态在册（repo 回显）。
    expect(await screen.findByText("https://github.com/acme/shop.git")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Remove…" }));
    expect(await screen.findByText("Remove the git deploy hook?")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith("/v1/apps/A1/hook", expect.objectContaining({ method: "DELETE" })),
    );

    // 删除生效：hook 查询失效重取 404 → 未配置空态（不再裸出创建表单）。
    expect(await screen.findByText(/Not configured/)).toBeTruthy();
    expect(screen.queryByLabelText("Repo")).toBeNull();
    expect(screen.getByRole("button", { name: "Configure hook…" })).toBeTruthy();
  });
});

// Deployment 摘要卡：source kind + 生效锚 + 按所有权的更新路径一句话。
// fetch 面三态：git 托管（hook 在册）/ compose（非 hook，GetRevision 原文
// 判定）/ 未部署（workloads 404）。
function renderSummary({
  hook,
  workloads,
  sourceKind,
  revisionSpec,
}: {
  hook: "configured" | "absent";
  workloads: "ok" | 404;
  sourceKind: string;
  revisionSpec?: { source_text: string };
}) {
  fetchMock.mockImplementation(async (path: string) => {
    if (path === "/v1/apps/A1/hook") {
      return hook === "configured"
        ? new Response(
            JSON.stringify({
              hook: {
                app_id: "A1", repo: "https://github.com/acme/shop.git", branch: "main",
                dockerfile: "", watch_paths: [], token_prefix: "flthook_ab", updated_at: "2026-10-17T00:00:00Z",
              },
            }),
            { status: 200 },
          )
        : new Response("not found", { status: 404 });
    }
    if (path === "/v1/apps/A1/workloads") {
      if (workloads === 404) return new Response("not found", { status: 404 });
      return new Response(
        JSON.stringify({
          revision: {
            seq: "3", digest: "sha256:0123456789abcdef", created_at: "2026-10-20T00:00:00Z",
            source_kind: sourceKind,
          },
          workloads: [],
        }),
        { status: 200 },
      );
    }
    if (path === "/v1/revisions/A1/3") {
      return new Response(JSON.stringify({ spec: {}, revision: revisionSpec ?? {} }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
  const { wrapper } = withClient();
  render(
    <AppSettingsTab projectId="P1" appId="A1" appName="web" createdAt="2026-10-17T00:00:00Z" onDeleted={() => {}} />,
    { wrapper },
  );
}

describe("AppSettingsTab deployment summary", () => {
  it("shows the git-owned update path when a hook owns the material", async () => {
    renderSummary({ hook: "configured", workloads: "ok", sourceKind: "SOURCE_KIND_GIT" });

    expect(await screen.findByText("git")).toBeTruthy();
    expect(screen.getByText("R3")).toBeTruthy();
    expect(screen.getByText("sha256:01234")).toBeTruthy();
    // git 托管更新路径：push 为主，runtime 走 Variables/Processes。
    expect(screen.getByText(/Updates: push to/)).toBeTruthy();
    expect(screen.getByText(/pull the branch HEAD any time from the git hook card/)).toBeTruthy();
  });

  it("reports compose for an image-kind revision with frozen source text, and points updates at the header deploy", async () => {
    renderSummary({
      hook: "absent",
      workloads: "ok",
      sourceKind: "SOURCE_KIND_IMAGE",
      revisionSpec: { source_text: "services:\n  web:\n" },
    });

    expect(await screen.findByText("compose")).toBeTruthy();
    expect(screen.getByText(/Updates: Deploy in the header/)).toBeTruthy();
    expect(screen.getByText("Variables")).toBeTruthy();
    expect(screen.getByText("Processes")).toBeTruthy();
  });

  it("shows the not-deployed empty state when the workloads view is 404", async () => {
    renderSummary({ hook: "absent", workloads: 404, sourceKind: "SOURCE_KIND_IMAGE" });

    expect(await screen.findByText(/No effective revision yet/)).toBeTruthy();
    expect(screen.getByText(/the spec freezes on the first deploy/)).toBeTruthy();
  });
});
