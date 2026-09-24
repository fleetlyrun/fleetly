// 覆写感知能力测试（v0.3 W3-S3，rbac-teams §3.3 B 形 + §7 消费裁决）：
// useTeamCapabilities 按「当前项目覆写行优先于团队角色」计算——升权者见
// 写按钮（canDeploy）、降权者隐藏；未选项目按团队角色；无覆写行团队角色
// 生效。capabilitiesForRole 词表直测。
//
// 夹具形态：localStorage 预置选中上下文（provider 挂载时读取）+ fetch stub
// 供 /auth/me（teams + project_overrides）与 /projects（可见项目集）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { TeamProjectProvider, useTeamCapabilities, capabilitiesForRole, type TeamCapabilities } from "@/lib/context";

const CONTEXT_KEY = "fleetly.console.context";

interface Fixture {
  teamRole: string;
  overrideRole?: string;
  project: string | null;
  prjSlug?: string;
}

function stubFetch(fx: Fixture) {
  return vi.fn().mockImplementation((input: RequestInfo | URL) => {
    const url = String(input);
    const overrides =
      fx.overrideRole && fx.prjSlug
        ? [
            {
              project_id: `01PRJ-${fx.prjSlug}`,
              team_id: "01TEAM",
              prj_slug: fx.prjSlug,
              role: fx.overrideRole,
            },
          ]
        : [];
    if (url.endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "u@t.test" },
            teams: [
              { team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: fx.teamRole },
            ],
            project_overrides: overrides,
          }),
      });
    }
    if (url.endsWith("/projects")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            projects: fx.prjSlug
              ? [
                  {
                    id: `01PRJ-${fx.prjSlug}`,
                    team_id: "01TEAM",
                    team_slug: "acme",
                    slug: fx.prjSlug,
                    name: "Default",
                  },
                ]
              : [],
          }),
      });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
}

function renderCaps(fx: Fixture): TeamCapabilities[] {
  localStorage.setItem(
    CONTEXT_KEY,
    JSON.stringify({ team: "acme", project: fx.project }),
  );
  const seen: TeamCapabilities[] = [];
  function Probe() {
    seen.push(useTeamCapabilities());
    return <div data-testid="probe" />;
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <TeamProjectProvider>
        <Probe />
      </TeamProjectProvider>
    </QueryClientProvider>,
  );
  return seen;
}

async function capabilitiesFor(fx: Fixture): Promise<TeamCapabilities> {
  const seen = renderCaps(fx);
  // Me / projects 查询异步落定后能力重算——末次渲染即终态。
  await waitFor(() => {
    expect(screen.getByTestId("probe")).toBeInTheDocument();
    expect(seen.length).toBeGreaterThan(1);
  });
  await waitFor(() => {
    expect(seen[seen.length - 1].role).toBeTruthy();
  });
  return seen[seen.length - 1];
}

afterEach(() => {
  localStorage.removeItem(CONTEXT_KEY);
  vi.unstubAllGlobals();
});

describe("useTeamCapabilities override awareness (W3-S3)", () => {
  it("升权：团队 viewer + default 项目覆写 developer → 见部署按钮（canDeploy）", async () => {
    vi.stubGlobal("fetch", stubFetch({ teamRole: "viewer", overrideRole: "developer", project: "default", prjSlug: "default" }));
    const caps = await capabilitiesFor({ teamRole: "viewer", overrideRole: "developer", project: "default", prjSlug: "default" });
    expect(caps.role).toBe("developer");
    expect(caps.canDeploy).toBe(true);
    expect(caps.canRead).toBe(true);
    expect(caps.canManageTeam).toBe(false);
  });

  it("降权：团队 developer + default 项目覆写 viewer → 写按钮隐藏（canDeploy=false）", async () => {
    vi.stubGlobal("fetch", stubFetch({ teamRole: "developer", overrideRole: "viewer", project: "default", prjSlug: "default" }));
    const caps = await capabilitiesFor({ teamRole: "developer", overrideRole: "viewer", project: "default", prjSlug: "default" });
    expect(caps.role).toBe("viewer");
    expect(caps.canDeploy).toBe(false);
    expect(caps.canRead).toBe(true);
  });

  it("未选项目：覆写行存在但不命中 → 团队角色生效", async () => {
    vi.stubGlobal("fetch", stubFetch({ teamRole: "developer", overrideRole: "viewer", project: null, prjSlug: "default" }));
    const caps = await capabilitiesFor({ teamRole: "developer", overrideRole: "viewer", project: null, prjSlug: "default" });
    expect(caps.role).toBe("developer");
    expect(caps.canDeploy).toBe(true);
  });

  it("无覆写行：团队角色直接生效", async () => {
    vi.stubGlobal("fetch", stubFetch({ teamRole: "viewer", project: "default", prjSlug: "default" }));
    const caps = await capabilitiesFor({ teamRole: "viewer", project: "default", prjSlug: "default" });
    expect(caps.role).toBe("viewer");
    expect(caps.canDeploy).toBe(false);
  });
});

describe("capabilitiesForRole", () => {
  it("四档角色矩阵（§3.2 蕴含序）", () => {
    expect(capabilitiesForRole(null).canRead).toBe(false);
    expect(capabilitiesForRole("viewer")).toMatchObject({ canRead: true, canDeploy: false });
    expect(capabilitiesForRole("developer")).toMatchObject({ canDeploy: true, canAdminResources: false });
    expect(capabilitiesForRole("admin")).toMatchObject({ canAdminResources: true, canManageTeam: false, canInvite: true });
    expect(capabilitiesForRole("owner")).toMatchObject({ canManageTeam: true, canInvite: true });
    expect(capabilitiesForRole("unknown")).toMatchObject({ canRead: false, canDeploy: false });
  });
});
