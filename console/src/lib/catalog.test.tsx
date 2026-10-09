import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { useApps, useProjects, useAlertRules, useAlertStates, useMetricsSeries, useUsers, useTeams, useInvitations, usePlatformBackups, useNodes } from "./catalog";
import { setToken } from "./token";

// 目录查询消费面锚（F2.6 只读面 + F3.1 扩面共用）：REST 路径/查询串、
// Bearer 头、行展平（undefined 行过滤）、ListApps 的 project_id 必填
// enabled 门（走查 F4 的回归锚——空项目不发请求）。

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
  vi.unstubAllGlobals();
  setToken("");
});

describe("useProjects", () => {
  it("fetches with the bearer header and flattens rows", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ projects: [{ id: "01M4P1", name: "shop" }, undefined, { id: "01M4P2", name: "lab" }] }), { status: 200 }),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useProjects(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([
      { id: "01M4P1", name: "shop" },
      { id: "01M4P2", name: "lab" },
    ]);
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/v1/projects?limit=100");
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer flt_test_token");
    client.clear();
  });
});

describe("useApps", () => {
  it("does not query with an empty project id (ListApps requires project_id)", async () => {
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useApps(""), { wrapper });
    // enabled=false：挂载一个微任务拍后 fetch 仍零调用。
    await Promise.resolve();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(result.current.data).toBeUndefined();
    client.clear();
  });

  it("queries per project and drops nameless rows", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ apps: [{ id: "01M4A1", project_id: "01M4P1", name: "web" }, { id: "01M4A2" }] }), { status: 200 }),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useApps("01M4P1"), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([{ id: "01M4A1", project_id: "01M4P1", name: "web" }]);
    expect((fetchMock.mock.calls[0] as [string])[0]).toBe("/v1/apps?project_id=01M4P1&limit=200");
    client.clear();
  });
});

// C1 可观测批 hooks 锚：告警规则/评估态 REST 面 + 指标时序（多序列行展平
// + 空 query 不发请求 + step_seconds 取窗/240 下限 15s）。
describe("useAlertRules / useAlertStates", () => {
  it("fetches rules and states from the alerting REST face", async () => {
    fetchMock
      .mockResolvedValueOnce(new Response(JSON.stringify({ rules: [{ id: "R1", app_id: "A1", metric: "cpu_percent", threshold: 90 }] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ states: [{ rule_id: "R1", state: "firing", observed_value: 97.5 }] }), { status: 200 }));
    const { client, wrapper } = withClient();
    const rules = renderHook(() => useAlertRules(), { wrapper });
    const states = renderHook(() => useAlertStates(), { wrapper });
    await waitFor(() => expect(rules.result.current.isSuccess).toBe(true));
    await waitFor(() => expect(states.result.current.isSuccess).toBe(true));
    expect(rules.result.current.data).toEqual([{ id: "R1", app_id: "A1", metric: "cpu_percent", threshold: 90 }]);
    expect((fetchMock.mock.calls[0] as [string])[0]).toBe("/v1/alerts/rules");
    expect((fetchMock.mock.calls[1] as [string])[0]).toBe("/v1/alerts");
    client.clear();
  });
});

describe("useMetricsSeries", () => {
  it("does not query with an empty promql", async () => {
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useMetricsSeries("", "1h"), { wrapper });
    await Promise.resolve();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(result.current.data).toBeUndefined();
    client.clear();
  });

  it("queries with an explicit window and flattens multi-series rows", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          series: [
            { labels: { name: "web.1" }, points: [{ time: "2026-10-08T12:00:00Z", value: 1.5 }] },
            undefined,
            { labels: { name: "web.2" }, points: [] },
          ],
        }),
        { status: 200 },
      ),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useMetricsSeries('rate(container_cpu_usage_seconds_total{container_label_fleetly_ns_app="01m4a1"}[2m])', "30m"), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([
      { labels: { name: "web.1" }, points: [{ time: "2026-10-08T12:00:00Z", value: 1.5 }] },
      { labels: { name: "web.2" }, points: [] },
    ]);
    const [path] = fetchMock.mock.calls[0] as [string];
    expect(path.startsWith("/v1/metrics?query=")).toBe(true);
    expect(path).toContain("step_seconds=15"); // 30m/240 = 7.5s → 下限 15s
    expect(path).toContain("start=");
    expect(path).toContain("end=");
    client.clear();
  });
});

// C2 治理批 hooks 锚：身份管理面（users/teams/invitations）与平台备份
// 台账的 REST 路径/行展平。
describe("identity and platform backup hooks", () => {
  it("fetches users, teams and invitations from the identity REST face", async () => {
    fetchMock
      .mockResolvedValueOnce(new Response(JSON.stringify({ users: [{ id: "U1", name: "alice" }] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ teams: [{ id: "T1", name: "platform" }] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ invitations: [{ id: "I1", role_id: "builtin-admin" }] }), { status: 200 }));
    const { client, wrapper } = withClient();
    const users = renderHook(() => useUsers(), { wrapper });
    const teams = renderHook(() => useTeams(), { wrapper });
    const invitations = renderHook(() => useInvitations(), { wrapper });
    await waitFor(() => expect(users.result.current.isSuccess).toBe(true));
    await waitFor(() => expect(teams.result.current.isSuccess).toBe(true));
    await waitFor(() => expect(invitations.result.current.isSuccess).toBe(true));
    expect(users.result.current.data).toEqual([{ id: "U1", name: "alice" }]);
    expect((fetchMock.mock.calls[0] as [string])[0]).toBe("/v1/users");
    expect((fetchMock.mock.calls[1] as [string])[0]).toBe("/v1/teams");
    expect((fetchMock.mock.calls[2] as [string])[0]).toBe("/v1/invitations");
    client.clear();
  });

  it("fetches platform backup snapshots", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ snapshots: [{ id: "64b39b57", time: "2026-10-08T16:39:43Z", hostname: "fleetly-dev" }, undefined] }), { status: 200 }),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => usePlatformBackups(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([{ id: "64b39b57", time: "2026-10-08T16:39:43Z", hostname: "fleetly-dev" }]);
    expect((fetchMock.mock.calls[0] as [string])[0]).toBe("/v1/platform/backups");
    client.clear();
  });
});

// C4 节点面 hook 锚：list 路径 + 行展平。
describe("useNodes", () => {
  it("fetches cluster nodes from the runtime REST face", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ nodes: [{ platform_id: "N1", hostname: "fleetly-dev", role: "manager", available: true, relay_online: true }, undefined] }), { status: 200 }),
    );
    const { client, wrapper } = withClient();
    const { result } = renderHook(() => useNodes(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([
      { platform_id: "N1", hostname: "fleetly-dev", role: "manager", available: true, relay_online: true },
    ]);
    expect((fetchMock.mock.calls[0] as [string])[0]).toBe("/v1/nodes");
    client.clear();
  });
});
