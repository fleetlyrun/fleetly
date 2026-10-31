import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppDiagnosisTab } from "./app-diagnosis";
import { setToken } from "@/lib/token";

// 诊断 tab（B2a W4）：四层条目卡（severity/fault_class/message/evidence 折叠
// /fix 深链）+ unreadable 横幅 + 空态健康绿 + platform 条目「等管理员修」
// 形态。纯读锚：诊断面只发 GET（写面零触碰——fix 是深链不是直发）。

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

// 绑定层读 useSearch（路由归因集合）——裸 render 无路由树，桩化为恒空。
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

// 诊断响应可组合 fixture（findings + unreadable_faces）。
let diagnosisBody: { findings: unknown[]; unreadable_faces: string[] } = { findings: [], unreadable_faces: [] };
function stubFetch() {
  fetchMock.mockImplementation(async (path: string) => {
    if (path === "/v1/apps/A1/diagnosis") return new Response(JSON.stringify(diagnosisBody), { status: 200 });
    if (path.startsWith("/v1/apps?")) return new Response(JSON.stringify({ apps: [{ id: "A1", name: "api", project_id: "P1" }] }), { status: 200 });
    return new Response("{}", { status: 200 });
  });
}

const crashLoop = {
  finding_id: "F_WORKLOAD_CRASH_LOOP",
  severity: "warning",
  fault_class: "app",
  workload_id: "w1",
  process: "web",
  message: "The workload keeps terminating and restarting in the near window.",
  evidence: { state: "failed", exit_code: 1, restart_count: 4, recent_event: "workload.terminated" },
  fix: { verb: "restart", params: { process: "web" }, console_path: "/p/P1/apps/A1/processes" },
};

const oomNoVerb = {
  finding_id: "F_WORKLOAD_OOM_WITH_LIMIT",
  severity: "warning",
  fault_class: "app",
  workload_id: "w1",
  process: "web",
  message: "The workload was killed for exceeding its declared memory limit.",
  evidence: { state: "failed", exit_code: 137, restart_count: 2 },
  fix: { verb: "", params: { field: "memory" }, console_path: "/p/P1/apps/A1" },
};

const platformFinding = {
  finding_id: "F_PLATFORM_REWRITE_PLACEHOLDER",
  severity: "warning",
  fault_class: "platform",
  workload_id: "w1",
  process: "web",
  message: "A platform component failure is affecting this workload.",
  evidence: { state: "failed" },
  fix: { verb: "", params: {}, console_path: "" },
};

describe("AppDiagnosisTab 条目卡四层形态", () => {
  it("warning 徽章/fault_class 标签/message/evidence 折叠展开/verb 深链——且诊断面零写请求", async () => {
    diagnosisBody = { findings: [crashLoop, oomNoVerb], unreadable_faces: [] };
    stubFetch();
    const { client, wrapper } = withClient();
    render(<AppDiagnosisTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/diagnosis" />, { wrapper });

    // 发生了什么（message 直通）+ severity/fault_class 徽章。
    expect(await screen.findByText("The workload keeps terminating and restarting in the near window.")).toBeTruthy();
    expect(screen.getAllByText("warning").length).toBeGreaterThan(0);
    expect(screen.getAllByText("app").length).toBeGreaterThan(0);

    // 为什么：evidence 折叠区（默认收起，展开见四行证据）。
    fireEvent.click(screen.getByRole("button", { name: "Toggle evidence of F_WORKLOAD_CRASH_LOOP" }));
    expect(await screen.findByText("workload.terminated")).toBeTruthy();
    expect(screen.getByText("4")).toBeTruthy(); // restarts (near window)

    // 怎么办：verb 在册 → Processes 深链钮；无 verb → 部署面入口 + 建议
    // 文案（field=memory）。
    const restart = screen.getByRole("link", { name: /Restart web/ }) as HTMLAnchorElement;
    expect(restart.getAttribute("href")).toBe("/p/P1/apps/A1/processes");
    expect(screen.getByRole("button", { name: /Open deploy…/ })).toBeTruthy();
    expect(screen.getByText(/Adjust the memory limit in the declaration/)).toBeTruthy();

    // 纯读锚：诊断 tab 生命周期内零写请求（全 GET）。
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(0));
    for (const [, init] of fetchMock.mock.calls as Array<[string, RequestInit]>) {
      expect(init?.method ?? "GET").toBe("GET");
    }
    client.clear();
  });

  it("unreadable 横幅诚实展示（读不了 ≠ 不存在，防「安静 = 健康」误读）", async () => {
    diagnosisBody = { findings: [crashLoop], unreadable_faces: ["components", "events"] };
    stubFetch();
    const { client, wrapper } = withClient();
    render(<AppDiagnosisTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/diagnosis" />, { wrapper });

    expect(await screen.findByText("2 faces unreadable")).toBeTruthy();
    expect(screen.getByText(/Diagnosis could not read: components, events/)).toBeTruthy();
    expect(screen.getByText(/Silence here does not mean healthy/)).toBeTruthy();
    client.clear();
  });

  it("空态 = 健康绿（无条目即无失败态可解释）", async () => {
    diagnosisBody = { findings: [], unreadable_faces: [] };
    stubFetch();
    const { client, wrapper } = withClient();
    render(<AppDiagnosisTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/diagnosis" />, { wrapper });

    expect(await screen.findByText("No findings — nothing to fix right now")).toBeTruthy();
    // 无条目卡、无横幅。
    expect(screen.queryByText("The workload keeps terminating")).toBeNull();
    expect(screen.queryByText(/unreadable/)).toBeNull();
    client.clear();
  });

  it("platform 条目：无 fix 钮，「等管理员修」形态（platform 的错永远不读成用户的错）", async () => {
    diagnosisBody = { findings: [platformFinding], unreadable_faces: [] };
    stubFetch();
    const { client, wrapper } = withClient();
    render(<AppDiagnosisTab projectId="P1" appId="A1" routeId="/_shell/p/$projectId/apps/$appId/diagnosis" />, { wrapper });

    expect(await screen.findByText("A platform component failure is affecting this workload.")).toBeTruthy();
    expect(screen.getByText(/Not caused by your app — an administrator fixes the platform side/)).toBeTruthy();
    // 无动作钮、无建议文案（描述即全部）。
    expect(screen.queryByRole("link", { name: /Restart/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Open deploy…/ })).toBeNull();
    expect(screen.queryByRole("link", { name: /Open the app/ })).toBeNull();
    client.clear();
  });
});
