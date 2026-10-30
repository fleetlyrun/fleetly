import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppSpecTab } from "./app-spec";
import { setToken } from "@/lib/token";

// Spec tab（ADR-0071 配置可观测面）：三段式渲染——生效锚（缺省寻址 =
// /v1/apps/{id}/workloads 无 seq）、冻结全文（GetRevision）、在服观测
//（/workloads/live）；"最新冻结 ≠ 当前生效"提示条的分野锚。
// B1 W4 遗留直发面收口（2026-10-31）：Edit & deploy 的提交钮接
// ConfirmSkipDialog 双击 skip 形态（首击无票 428 → 二击 skip=true）。

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

// 批4 useCollection 化：绑定层读 useSearch（revisions 集合）——裸 render
// 无路由树，桩化为恒空（lookup schema 走缺省窗口）。
vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useNavigate: () => navigateMock, useSearch: () => ({}) };
});

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> };
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  navigateMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

const VIEW = {
  revision: { seq: "2", digest: "sha256:eff", created_at: "t", source_kind: "SOURCE_KIND_IMAGE" },
  workloads: [
    {
      id: "A1-web",
      process: "web",
      image: "nginx:1.27",
      replicas: "3",
      command: ["nginx", "-g", "daemon off;"],
      env: { ROLE: "edge" },
      ports: [{ port: 8080, protocol: "PROTOCOL_HTTP" }],
      networks: ["default"],
      addressing: ["web"],
    },
  ],
};

function stubFetch(opts?: { latestFrozenSeq?: number; live?: unknown }) {
  const latest = opts?.latestFrozenSeq ?? 2;
  fetchMock.mockImplementation(async (path: string) => {
    if (path === "/v1/apps/A1/workloads") {
      return new Response(JSON.stringify(VIEW), { status: 200 });
    }
    if (path === "/v1/apps/A1/workloads/live") {
      return new Response(JSON.stringify({ observations: opts?.live ?? [] }), { status: 200 });
    }
    if (path.startsWith("/v1/revisions/A1/2")) {
      return new Response(JSON.stringify({ spec: { processes: [{ name: "web" }] } }), { status: 200 });
    }
    if (path.startsWith("/v1/revisions")) {
      return new Response(JSON.stringify({ revisions: [{ id: "r1", seq: "1", digest: "d" }, { id: "r2", seq: String(latest), digest: "d" }] }), { status: 200 });
    }
    return new Response(JSON.stringify({}), { status: 404 });
  });
}

describe("AppSpecTab", () => {
  it("renders the three observability layers from the effective revision", async () => {
    stubFetch({ live: [{ workload_id: "A1-web", generation: "4", image: "nginx:1.27", replicas: "3", state: "running" }] });
    const { wrapper } = withClient();
    render(<AppSpecTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/spec" />, { wrapper });

    await waitFor(() => expect(screen.getByText("R2")).toBeTruthy());
    expect(fetchMock).toHaveBeenCalledWith("/v1/apps/A1/workloads", expect.anything());
    expect(fetchMock).toHaveBeenCalledWith("/v1/apps/A1/workloads/live", expect.anything());
    expect(fetchMock).toHaveBeenCalledWith("/v1/revisions/A1/2", expect.anything());
    expect(screen.getByText("Frozen Spec")).toBeTruthy();
    expect(screen.getByText("Projected Workloads")).toBeTruthy();
    expect(screen.getByText("Live Carriers")).toBeTruthy();
    expect(screen.getAllByText(/nginx:1.27/).length).toBeGreaterThan(0);
    expect(screen.getByText("ROLE")).toBeTruthy();
  });

  it("warns when the latest frozen revision is not effective", async () => {
    stubFetch({ latestFrozenSeq: 3 });
    const { wrapper } = withClient();
    render(<AppSpecTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/spec" />, { wrapper });

    await waitFor(() =>
      expect(screen.getByText(/Latest frozen R3 is not effective yet — the cluster is running R2/)).toBeTruthy(),
    );
  });

  it("addresses an explicit seq via the query parameter", async () => {
    stubFetch();
    const { wrapper } = withClient();
    render(<AppSpecTab projectId="P1" appId="A1" seq={1} routeId="/_shell/p/$projectId/apps/$appId/spec" />, { wrapper });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/v1/apps/A1/workloads?seq=1", expect.anything()));
  });

  it("edit & deploy: first click is refused with REQUIRED, second click sends an explicit skip", async () => {
    // deploy 步接线（B1 W4 双击 skip）：首击 spec_file 无票直发（428）→
    // 「已请求确认」态 → 二击 confirmation.skip=true → 编辑 Dialog 关窗 +
    // 落部署详情导航。
    const deployResponses: Array<{ status: number; body: unknown }> = [
      { status: 428, body: { code: "E_CONFIRMATION_REQUIRED", message: "requires confirmation: pass the ticket from a preview, or set skip" } },
    ];
    const specBody = { processes: [{ name: "web" }] };
    fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
      if (path === "/v1/deployments" && (init?.method ?? "GET") === "POST") {
        const next = deployResponses.shift();
        if (next != null && next.status !== 200) {
          return new Response(JSON.stringify(next.body), { status: next.status });
        }
        return new Response(JSON.stringify({ deployment: { id: "D7" } }), { status: 200 });
      }
      if (path === "/v1/apps/A1/workloads") return new Response(JSON.stringify(VIEW), { status: 200 });
      if (path === "/v1/apps/A1/workloads/live") return new Response(JSON.stringify({ observations: [] }), { status: 200 });
      if (path.startsWith("/v1/revisions/A1/2")) return new Response(JSON.stringify({ spec: specBody }), { status: 200 });
      if (path.startsWith("/v1/revisions")) {
        return new Response(JSON.stringify({ revisions: [{ id: "r1", seq: "1", digest: "d" }, { id: "r2", seq: "2", digest: "d" }] }), { status: 200 });
      }
      return new Response(JSON.stringify({}), { status: 404 });
    });
    const { client, wrapper } = withClient();
    render(<AppSpecTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/spec" />, { wrapper });

    // 打开编辑器（冻结体预填——textarea 受控值承载，非 textContent；触发
    // 钮在冻结体落盘前禁用，先等门闩开）。
    const editButton = (await screen.findByRole("button", { name: /Edit & deploy/ })) as HTMLButtonElement;
    await waitFor(() => expect(editButton.disabled).toBe(false));
    fireEvent.click(editButton);
    const textarea = (await screen.findByDisplayValue(/"name": "web"/)) as HTMLTextAreaElement;
    expect(textarea.value).toContain("web");

    // 触发钮 → 双击壳；首击：spec_file = 编辑体原文，无 confirmation 键。
    fireEvent.click(screen.getByRole("button", { name: "Deploy" }));
    expect(await screen.findByText("Deploy the edited spec?")).toBeTruthy();
    fireEvent.click(screen.getAllByRole("button", { name: "Deploy" }).at(-1)!);
    await waitFor(() => expect(deployPostsOf(fetchMock)).toHaveLength(1));
    expect(JSON.parse(String(deployPostsOf(fetchMock)[0][1].body))).toEqual({
      app_id: "A1",
      spec_file: JSON.stringify(specBody, null, 2),
    });

    // REQUIRED → 「已请求确认」态。
    expect(await screen.findByText(/This deployment requires an explicit confirmation/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Confirm deploy" }));

    // 二击：skip=true；ok → 编辑 Dialog 关窗 + 导航落 D7。
    await waitFor(() => expect(deployPostsOf(fetchMock)).toHaveLength(2));
    expect(JSON.parse(String(deployPostsOf(fetchMock)[1][1].body))).toEqual({
      app_id: "A1",
      spec_file: JSON.stringify(specBody, null, 2),
      confirmation: { skip: true },
    });
    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith({
        to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
        params: { projectId: "P1", appId: "A1", deploymentId: "D7" },
      }),
    );
    await waitFor(() => expect(screen.queryByDisplayValue(JSON.stringify(specBody, null, 2))).toBeNull());
    client.clear();
  });
});

// POST /v1/deployments 调用清单（双击两段的载荷断言锚）。
function deployPostsOf(mock: ReturnType<typeof vi.fn>): Array<[string, RequestInit]> {
  return mock.mock.calls.filter(([p, i]) => p === "/v1/deployments" && i?.method === "POST") as Array<[string, RequestInit]>;
}
