import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { ProjectFindingsPage } from "./project-findings";
import { setToken } from "@/lib/token";
import { FINDINGS_PAGE_SIZE } from "@/kit/diagnosis";

// 「需要你」页（B2a W4）：fault_class 分组定序 + platform 组特殊形态（组头
// 钉「等管理员修」文案、条目无 fix 钮）+ keyset 分页（整窗占满 → Next 带
// after_app_seq 游标入 URL）+ 空态一切正常 + 逐 App unreadable 诚实片。

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

// URL 绑定层桩：useSearch 恒空（首页窗口）——翻页经 navigateMock 的 search
// 补丁断言（游标入 URL），游标→查询参数的映射在 kit/diagnosis.test.ts 钉。
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

// 两 App 页 fixture：A1 两组 app 类条目（verb/无 verb）；A2 declaration +
// platform + external 三档 + unreadable 两面。
const crashLoop = {
  finding_id: "F_WORKLOAD_CRASH_LOOP",
  severity: "warning",
  fault_class: "app",
  workload_id: "w1",
  process: "web",
  message: "The workload keeps terminating and restarting in the near window.",
  evidence: { state: "failed", restart_count: 4 },
  fix: { verb: "restart", params: { process: "web" }, console_path: "/p/P1/apps/A1" },
};
const oomNoVerb = {
  finding_id: "F_WORKLOAD_OOM_WITH_LIMIT",
  severity: "warning",
  fault_class: "app",
  workload_id: "w1",
  process: "web",
  message: "The workload was killed for exceeding its declared memory limit.",
  evidence: { state: "failed", exit_code: 137 },
  fix: { verb: "", params: { field: "memory" }, console_path: "/p/P1/apps/A1" },
};
const stoppedAdvice = {
  finding_id: "F_WORKLOAD_STOPPED_EXPLAINED",
  severity: "advice",
  fault_class: "declaration",
  workload_id: "w2",
  process: "worker",
  message: "The workload is stopped as declared; it stays stopped until the declaration changes.",
  evidence: { state: "stopped" },
  fix: { verb: "scale", params: { replicas: "1" }, console_path: "/p/P1/apps/A2" },
};
const platformFinding = {
  finding_id: "F_PLATFORM_REWRITE_PLACEHOLDER",
  severity: "warning",
  fault_class: "platform",
  workload_id: "w2",
  process: "worker",
  message: "A platform component failure is affecting this workload.",
  evidence: { state: "failed" },
  fix: { verb: "", params: {}, console_path: "" },
};
const imagePull = {
  finding_id: "F_WORKLOAD_IMAGE_PULL_FAILING",
  severity: "warning",
  fault_class: "external",
  workload_id: "w2",
  process: "worker",
  message: "The runtime cannot pull the workload image (bad image reference or registry unreachable).",
  evidence: { state: "rejected", reason: "" },
  fix: { verb: "", params: { field: "image" }, console_path: "/p/P1/apps/A2" },
};

const TWO_APPS = [
  { app_id: "A1", app_seq: "1", app_name: "api", unreadable_faces: [], findings: [crashLoop, oomNoVerb] },
  { app_id: "A2", app_seq: "2", app_name: "worker", unreadable_faces: ["events", "components"], findings: [stoppedAdvice, platformFinding, imagePull] },
];

function stubPage(apps: unknown[]) {
  fetchMock.mockImplementation(async (path: string) => {
    if (typeof path === "string" && path.startsWith("/v1/projects/P1/findings")) {
      return new Response(JSON.stringify({ apps }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
}

describe("ProjectFindingsPage 分组与 platform 特殊形态", () => {
  it("四档分组定序渲染；platform 组头钉「等管理员修」文案且条目无 fix 钮", async () => {
    stubPage(TWO_APPS);
    const { client, wrapper } = withClient();
    render(<ProjectFindingsPage projectId="P1" routeId="/_shell/p/$projectId/findings/" />, { wrapper });

    // 四组按定序出现（app → declaration → platform → external）。
    expect(await screen.findByText("App failures")).toBeTruthy();
    const groups = ["App failures", "Declaration changes needed", "Platform issues — waiting for an administrator", "External dependencies"];
    for (const title of groups) expect(screen.getByText(title)).toBeTruthy();

    // App 归属与 unreadable 诚实片（A2 两面读不了；A2 跨三组各出一次
    // App 块头——multiple 是分组形态的预期）。
    expect(screen.getByText("api")).toBeTruthy();
    expect(screen.getAllByRole("link", { name: "worker" }).length).toBe(3);
    expect(screen.getAllByText("2 faces unreadable").length).toBe(3);

    // platform 组：条目只有描述与定位（无 Restart/Scale/Open deploy/Open the
    // app 动作钮）；「不是我的问题」归属注记在场（组头 hint 与卡内注记各一
    // ——精确串区分两处文案）。
    expect(screen.getByText("Not caused by your apps. Actions are unavailable here; an administrator fixes the platform side.")).toBeTruthy();
    expect(screen.getByText("Not caused by your app — an administrator fixes the platform side. No action is available here.")).toBeTruthy();

    // 非 platform 组动作面在场：verb 深链（A1 Restart web → A1 Processes；
    // A2 Scale worker → A2 Processes）与无 verb 建议（Open the app 深链）。
    const restart = screen.getByRole("link", { name: /Restart web/ }) as HTMLAnchorElement;
    expect(restart.getAttribute("href")).toBe("/p/P1/apps/A1/processes");
    const scale = screen.getByRole("link", { name: /Scale worker to 1/ }) as HTMLAnchorElement;
    expect(scale.getAttribute("href")).toBe("/p/P1/apps/A2/processes");
    const openApps = screen.getAllByRole("link", { name: /Open the app/ }) as HTMLAnchorElement[];
    expect(openApps.map((a) => a.getAttribute("href"))).toEqual(expect.arrayContaining(["/p/P1/apps/A2", "/p/P1/apps/A1"]));
    // platform 条目无动作钮的负向锚：全页唯一 Restart/Scale 都已消费。
    client.clear();
  });

  it("advice 档徽章与 message 直通（stopped 解释化条目）", async () => {
    stubPage([TWO_APPS[1]]);
    const { client, wrapper } = withClient();
    render(<ProjectFindingsPage projectId="P1" routeId="/_shell/p/$projectId/findings/" />, { wrapper });

    expect(await screen.findByText("The workload is stopped as declared; it stays stopped until the declaration changes.")).toBeTruthy();
    expect(screen.getByText("advice")).toBeTruthy();
    client.clear();
  });

  it("空态 = 一切正常（绿）", async () => {
    stubPage([]);
    const { client, wrapper } = withClient();
    render(<ProjectFindingsPage projectId="P1" routeId="/_shell/p/$projectId/findings/" />, { wrapper });

    expect(await screen.findByText("Nothing needs you")).toBeTruthy();
    expect(screen.queryByText("App failures")).toBeNull();
    client.clear();
  });
});

describe("ProjectFindingsPage keyset 分页", () => {
  it("整窗占满 → Next 推进游标入 URL（after_app_seq 轴）；短页 → Next 禁用", async () => {
    // 整窗：页大小行数的 App 块（末条 app_seq = 50）。
    const full = Array.from({ length: FINDINGS_PAGE_SIZE }, (_, i) => ({
      app_id: `A${i}`,
      app_seq: String(i + 1),
      app_name: `app${i}`,
      unreadable_faces: [],
      findings: [{ ...crashLoop, workload_id: `w${i}` }],
    }));
    stubPage(full);
    const { client, wrapper } = withClient();
    render(<ProjectFindingsPage projectId="P1" routeId="/_shell/p/$projectId/findings/" />, { wrapper });

    const next = await screen.findByRole("button", { name: /Next/ });
    expect(next.hasAttribute("disabled")).toBe(false);
    fireEvent.click(next);
    await waitFor(() => expect(navigateMock).toHaveBeenCalled());
    // setWindow = push（历史即分页栈）；search 补丁带 cursor（= 末条 app_seq
    // 回显——after_app_seq 查询映射在 kit 侧钉死）。
    const call = navigateMock.mock.calls[0][0] as { to: string; search: (prev: Record<string, unknown>) => Record<string, unknown>; replace: boolean };
    expect(call.to).toBe(".");
    expect(call.replace).toBe(false);
    expect(call.search({})).toMatchObject({ cursor: String(FINDINGS_PAGE_SIZE) });
    client.clear();
  });

  it("短页（行数 < 页大小）→ 无 nextCursor，Next 禁用", async () => {
    stubPage(TWO_APPS);
    const { client, wrapper } = withClient();
    render(<ProjectFindingsPage projectId="P1" routeId="/_shell/p/$projectId/findings/" />, { wrapper });

    const next = await screen.findByRole("button", { name: /Next/ });
    expect(next.hasAttribute("disabled")).toBe(true);
    client.clear();
  });
});
