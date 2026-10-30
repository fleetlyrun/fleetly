import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { DeploySheet } from "./deploy-sheet";
import type { AppEntry } from "@/kit/apps";
import { setToken } from "@/lib/token";

// DeploySheet 两段式确认流钉测（B1 W4，设计稿 §1.6）：
// ① Preview & deploy → POST /v1/deployments/preview，请求体 = {deploy: …}
//    （内嵌同一 DeployRequest——形状层单源）；
// ② 预览面板落定（rollout 徽章/warnings/票 TTL）→ 确认钮出现；
// ③ Confirm deploy → POST /v1/deployments 带 confirmation.ticket（体 =
//    预览过的载荷 + 票——确认的正是展示的）；
// ④ E_CONFIRMATION_STALE（428）→ 自动重预览 + 增量展示（新载荷 vs 上一次
//    所见：added/dropped 两组）+ 新票替换确认锚。
// hook 托管面（Deploy now = RefreshGitHook）不进预览流（豁免族）。
// uploads 读面的 useCollection 吃路由 useSearch（from 锚）——裸 render 无
// 路由树，桩化为恒空（app-processes 同款）。

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useSearch: () => ({}) };
});

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> };
}

const fetchMock = vi.fn();
// 预演/确认两步的响应序列按调用次序翻转（staleLeg = 确认步回 428）。
let confirmResponses: Array<{ status: number; body: unknown }> = [];
// 预演调用计数（第二次预演 = STALE 重预览，回增量场景的新载荷 + 新票）。
let previewCalls = 0;

beforeEach(() => {
  fetchMock.mockReset();
  confirmResponses = [];
  previewCalls = 0;
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

const apps: readonly AppEntry[] = [{ id: "A1", project_id: "P1", name: "web", created_at: "2026-10-01T00:00:00Z" }];

function previewBody(ticket: string, extraWorkload = false): Record<string, unknown> {
  return {
    spec_diff: [{ path: "processes.web.image", old_value: "nginx:1.27", new_value: "nginx:1.31" }],
    workload_changes: [
      {
        workload_id: "A1-web",
        action: "WORKLOAD_CHANGE_ACTION_CHANGED",
        image: "nginx:1.31",
        replicas_from: "1",
        replicas_to: "1",
        rollout: "WORKLOAD_ROLLOUT_REPLACE",
      },
      // 第二次预演多出的 workload——增量展示 added 组的断言锚。
      ...(extraWorkload
        ? [
            {
              workload_id: "A1-worker",
              action: "WORKLOAD_CHANGE_ACTION_ADDED",
              image: "redis:7",
              replicas_from: "0",
              replicas_to: "1",
              rollout: "WORKLOAD_ROLLOUT_SCALE_ONLY",
            },
          ]
        : []),
    ],
    warnings: [{ kind: "PREVIEW_WARNING_KIND_LIVE_DRIFT", detail: "live replicas 2 differ from the declared 1" }],
    ticket,
    ticket_expires_in: 600,
  };
}

function stubFetch() {
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (path.startsWith("/v1/apps/A1/spec")) return new Response("not found", { status: 404 });
    if (path.startsWith("/v1/apps/A1/hook")) return new Response("not found", { status: 404 });
    if (path.startsWith("/v1/storage/uploads")) return new Response(JSON.stringify({ uploads: [] }), { status: 200 });
    if (path === "/v1/deployments/preview" && method === "POST") {
      previewCalls += 1;
      return new Response(JSON.stringify(previewBody(previewCalls > 1 ? "tkt-2" : "tkt-1", previewCalls > 1)), {
        status: 200,
      });
    }
    if (path === "/v1/deployments" && method === "POST") {
      const next = confirmResponses.shift();
      if (next != null && next.status !== 200) {
        return new Response(JSON.stringify(next.body), { status: next.status });
      }
      return new Response(JSON.stringify({ deployment: { id: "D1" } }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
}

function renderSheet() {
  const { client, wrapper } = withClient();
  const onDeployed = vi.fn();
  const onOpenChange = vi.fn();
  render(
    <DeploySheet
      open
      onOpenChange={onOpenChange}
      apps={apps}
      defaultAppId="A1"
      projectId="P1"
      onDeployed={onDeployed}
      from="test-route"
    />,
    { wrapper },
  );
  return { client, onDeployed, onOpenChange };
}

describe("DeploySheet 预览确认流", () => {
  it("previews first, then confirms with the minted ticket on the exact previewed payload", async () => {
    stubFetch();
    const { client, onOpenChange } = renderSheet();

    // form 段主钮 = Preview & deploy；确认钮尚不存在。
    const previewButton = await screen.findByRole("button", { name: "Preview & deploy" });
    expect(screen.queryByRole("button", { name: "Confirm deploy" })).toBeNull();

    fireEvent.click(previewButton);

    // 预演请求：{deploy: …} 内嵌同一 DeployRequest。
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([p]) => p === "/v1/deployments/preview"),
      ).toBe(true),
    );
    const [, previewInit] = fetchMock.mock.calls.find(([p]) => p === "/v1/deployments/preview") as [string, RequestInit];
    expect(JSON.parse(String(previewInit.body))).toEqual({
      deploy: expect.objectContaining({ app_id: "A1", image: "nginx:1.27" }),
    });

    // 预览面板落定：rollout 徽章 + LIVE_DRIFT 指引 + 票 TTL；表单隐藏。
    expect(await screen.findByText("replace")).toBeTruthy();
    expect(screen.getByText(/converges the cluster back to the declared count/)).toBeTruthy();
    expect(screen.getByText(/Ticket valid for ~10 min/)).toBeTruthy();
    expect(screen.queryByLabelText(/Source/)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Confirm deploy" }));

    // 确认请求：同载荷 + confirmation.ticket（确认的正是展示的）。
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([p, i]) => p === "/v1/deployments" && i?.method === "POST"),
      ).toBe(true),
    );
    const [, deployInit] = fetchMock.mock.calls.find(
      ([p, i]) => p === "/v1/deployments" && i?.method === "POST",
    ) as [string, RequestInit];
    const deployBody = JSON.parse(String(deployInit.body));
    expect(deployBody).toEqual({
      app_id: "A1",
      image: "nginx:1.27",
      confirmation: { ticket: "tkt-1" },
    });
    // 受理成功：Sheet 收窗（成功反馈走 toast 带 View 动作链接——既有形态）。
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    client.clear();
  });

  it("auto re-previews with an incremental display after E_CONFIRMATION_STALE", async () => {
    stubFetch();
    // 确认步 428 STALE → 重预览（第二次 preview 调用）→ 增量展示 → 新票确认。
    confirmResponses = [
      { status: 428, body: { code: "E_CONFIRMATION_STALE", message: "the confirmation no longer matches the current state" } },
    ];
    const { client, onOpenChange } = renderSheet();

    fireEvent.click(await screen.findByRole("button", { name: "Preview & deploy" }));
    expect(await screen.findByText("replace")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Confirm deploy" }));

    // STALE 落定后自动重预览：第二次 preview 调用出现。
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(([p]) => p === "/v1/deployments/preview"),
      ).toHaveLength(2),
    );
    // 增量块（对比组件态保存的上一次所见：added 组带 A1-worker）+ 基线
    // 漂移横幅。
    expect(await screen.findByText("Since the preview you last saw")).toBeTruthy();
    expect(screen.getByText(/The baseline moved since your last preview/)).toBeTruthy();
    expect(screen.getByText(/add A1-worker/)).toBeTruthy();
    // 确认锚已换新票（tkt-2）——再确认走通。
    fireEvent.click(screen.getByRole("button", { name: "Confirm deploy" }));
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(([p, i]) => p === "/v1/deployments" && i?.method === "POST"),
      ).toHaveLength(2),
    );
    const [, secondInit] = fetchMock.mock.calls.filter(
      ([p, i]) => p === "/v1/deployments" && i?.method === "POST",
    )[1] as [string, RequestInit];
    expect(JSON.parse(String(secondInit.body)).confirmation).toEqual({ ticket: "tkt-2" });
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    client.clear();
  });

  it("Edit returns to the form without a deploy request", async () => {
    stubFetch();
    const { client } = renderSheet();

    fireEvent.click(await screen.findByRole("button", { name: "Preview & deploy" }));
    expect(await screen.findByText("replace")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));

    // 回 form 段：主钮回到 Preview & deploy，无 Deploy 直发。
    expect(await screen.findByRole("button", { name: "Preview & deploy" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Confirm deploy" })).toBeNull();
    expect(
      fetchMock.mock.calls.some(([p, i]) => p === "/v1/deployments" && i?.method === "POST"),
    ).toBe(false);
    client.clear();
  });

  it("preview rejections land in the result box without leaving the form stage", async () => {
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/apps/A1/spec")) return new Response("not found", { status: 404 });
      if (path.startsWith("/v1/apps/A1/hook")) return new Response("not found", { status: 404 });
      if (path.startsWith("/v1/storage/uploads")) return new Response(JSON.stringify({ uploads: [] }), { status: 200 });
      if (path === "/v1/deployments/preview") {
        return new Response(
          JSON.stringify({ code: "E_INVALID_DEPLOY_REQUEST", message: "sources are mutually exclusive" }),
          { status: 400 },
        );
      }
      return new Response("{}", { status: 200 });
    });
    const { client } = renderSheet();

    fireEvent.click(await screen.findByRole("button", { name: "Preview & deploy" }));

    // 400（CEL 拒收）→ rejected 结果盒（code 前缀）；仍处 form 段。
    expect(await screen.findByText(/sources are mutually exclusive/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Preview & deploy" })).toBeTruthy();
    client.clear();
  });
});
