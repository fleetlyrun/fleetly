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

// DangerZone 拆除确认闭环（B1 W4）：开窗即 PreviewTeardown → consequences
// 由载荷填充（级联清单 + 保留清单带理由 + 驱动权冲突）→ 确认发带票
// DeleteApp（query 参数 confirmation.ticket——DeleteApp 无 body 的 REST
// 形态）；STALE → 重开窗重拉预演。
const TEARDOWN_PAYLOAD = {
  cascade: [
    { kind: "TEARDOWN_ITEM_KIND_WORKLOAD", name: "A1-web" },
    { kind: "TEARDOWN_ITEM_KIND_HOOK_ROW", name: "https://github.com/acme/shop.git" },
    { kind: "TEARDOWN_ITEM_KIND_ROUTE_ROW", name: "shop.test" },
  ],
  kept: [{ kind: "KEPT_ITEM_KIND_VOLUME", reason: "volumes are project-scoped and survive app deletion" }],
  driving_conflict: true,
  driving_detail: "deployment D5 is driving this app",
  ticket: "tkt-9",
  ticket_expires_in: 600,
};

function renderDangerZone({ deleteStatus = 200 }: { deleteStatus?: number } = {}) {
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (path.startsWith("/v1/apps/A1/hook")) return new Response("not found", { status: 404 });
    if (path.startsWith("/v1/apps/A1/workloads")) return new Response("not found", { status: 404 });
    if (path === "/v1/apps/A1/preview-teardown" && method === "POST") {
      return new Response(JSON.stringify(TEARDOWN_PAYLOAD), { status: 200 });
    }
    if (path.startsWith("/v1/apps/A1") && method === "DELETE") {
      if (deleteStatus !== 200) {
        return new Response(
          JSON.stringify({ code: "E_CONFIRMATION_STALE", message: "the confirmation no longer matches the current state" }),
          { status: deleteStatus },
        );
      }
      return new Response("{}", { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
  const { wrapper } = withClient();
  const onDeleted = vi.fn();
  render(
    <AppSettingsTab projectId="P1" appId="A1" appName="web" createdAt="2026-10-17T00:00:00Z" onDeleted={onDeleted} />,
    { wrapper },
  );
  return { onDeleted };
}

describe("AppSettingsTab danger zone teardown preview", () => {
  it("fills the consequences from PreviewTeardown and deletes with the minted ticket", async () => {
    const { onDeleted } = renderDangerZone();

    fireEvent.click(screen.getByRole("button", { name: "Delete app…" }));
    expect(await screen.findByText("Delete app web?")).toBeTruthy();

    // 开窗即预演：级联清单 + 保留清单带理由 + 驱动权冲突透出。
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([p, i]) => p === "/v1/apps/A1/preview-teardown" && i?.method === "POST"),
      ).toBe(true),
    );
    expect(await screen.findByText("Will be removed")).toBeTruthy();
    expect(screen.getByText(/workload/)).toBeTruthy();
    expect(screen.getByText("A1-web")).toBeTruthy();
    expect(screen.getByText(/git hook row/)).toBeTruthy();
    expect(screen.getByText(/route row/)).toBeTruthy();
    expect(screen.getByText(/Kept \(not deleted\)/)).toBeTruthy();
    expect(screen.getByText(/volumes are project-scoped and survive app deletion/)).toBeTruthy();
    expect(screen.getByText(/blocked by a driving deployment/)).toBeTruthy();

    fireEvent.click(screen.getAllByRole("button", { name: "Delete" }).at(-1)!);
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([p, i]) => p === "/v1/apps/A1?confirmation.ticket=tkt-9" && i?.method === "DELETE"),
      ).toBe(true),
    );
    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
  });

  it("re-previews after E_CONFIRMATION_STALE (the ticket no longer covers the current state)", async () => {
    renderDangerZone({ deleteStatus: 428 });

    fireEvent.click(screen.getByRole("button", { name: "Delete app…" }));
    await screen.findByText("Will be removed");
    fireEvent.click(screen.getAllByRole("button", { name: "Delete" }).at(-1)!);

    // STALE：票作废——重开窗重拉预演（第二次 preview-teardown 调用），清
    // 单重新在场（toast 文案在 sonner Toaster，jsdom 无挂载点——以调用与
    // 弹窗重开为锚）。
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(([p, i]) => p === "/v1/apps/A1/preview-teardown" && i?.method === "POST"),
      ).toHaveLength(2),
    );
    expect(await screen.findByText("Delete app web?")).toBeTruthy();
    expect(screen.getByText("Will be removed")).toBeTruthy();
  });
});
