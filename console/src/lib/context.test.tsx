// 覆写感知能力测试（v0.3 W3-S3，rbac-teams §3.3 B 形 + §7 消费裁决）：
// useTeamCapabilities 按「当前项目覆写行优先于团队角色」计算——升权者见
// 写按钮（canDeploy）、降权者隐藏；未选项目按团队角色；无覆写行团队角色
// 生效。capabilitiesForRole 词表直测。
//
// 平台管理员双门（P0-3，2026-09-25 审查）：资源面（canDeploy/
// canAdminResources）对平台管理员恒关——服务端 ownership.go 无条件
// levelRead 硬拒，前端不得 fail-open；身份面（canManageTeam/canInvite）
// 保持按团队角色解析——服务端不禁平台管理员。
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
  /** Me 投影的 is_platform_admin（缺省 false——非管理员路径不受扰）。 */
  isPlatformAdmin?: boolean;
  /** teams 置空（Me 可得但无任何团队——平台管理员跨队只读投影的极端形）。 */
  noTeams?: boolean;
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
            user: { id: "01U1", email: "u@t.test", is_platform_admin: fx.isPlatformAdmin ?? false },
            teams: fx.noTeams
              ? []
              : [
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

// until：终态判据。缺省 = 角色已解析（Me 落定）；平台管理员无团队形态
// role 恒 null，须显式给判据（资源面已关门即 Me 已落定的终态）。
async function capabilitiesFor(
  fx: Fixture,
  until?: (caps: TeamCapabilities) => boolean,
): Promise<TeamCapabilities> {
  const seen = renderCaps(fx);
  // Me / projects 查询异步落定后能力重算——末次渲染即终态。
  await waitFor(() => {
    expect(screen.getByTestId("probe")).toBeInTheDocument();
    expect(seen.length).toBeGreaterThan(1);
  });
  await waitFor(() => {
    const last = seen[seen.length - 1];
    expect(until ? until(last) : last.role !== null).toBe(true);
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

describe("useTeamCapabilities platform-admin dual gate (P0-3)", () => {
  it("平台管理员 + owner：资源面关门（canDeploy/canAdminResources=false）、身份面按角色（canManageTeam/canInvite=true）", async () => {
    const fx = { teamRole: "owner", isPlatformAdmin: true, project: null };
    vi.stubGlobal("fetch", stubFetch(fx));
    const caps = await capabilitiesFor(fx);
    expect(caps.canRead).toBe(true);
    expect(caps.canDeploy).toBe(false);
    expect(caps.canAdminResources).toBe(false);
    expect(caps.canManageTeam).toBe(true);
    expect(caps.canInvite).toBe(true);
  });

  it("平台管理员 + admin：身份面按角色收窄（canInvite=true、canManageTeam=false）、资源面仍关门", async () => {
    const fx = { teamRole: "admin", isPlatformAdmin: true, project: null };
    vi.stubGlobal("fetch", stubFetch(fx));
    const caps = await capabilitiesFor(fx);
    expect(caps.canDeploy).toBe(false);
    expect(caps.canAdminResources).toBe(false);
    expect(caps.canManageTeam).toBe(false);
    expect(caps.canInvite).toBe(true);
  });

  it("平台管理员 + viewer：身份面与资源面全关（只读支援视角）", async () => {
    const fx = { teamRole: "viewer", isPlatformAdmin: true, project: null };
    vi.stubGlobal("fetch", stubFetch(fx));
    const caps = await capabilitiesFor(fx);
    expect(caps.canDeploy).toBe(false);
    expect(caps.canAdminResources).toBe(false);
    expect(caps.canManageTeam).toBe(false);
    expect(caps.canInvite).toBe(false);
  });

  it("平台管理员 + 无团队：不得 fail-open——资源面关门、身份面无角色全关", async () => {
    const fx = { teamRole: "owner", isPlatformAdmin: true, noTeams: true, project: null };
    vi.stubGlobal("fetch", stubFetch(fx));
    // role 恒 null——终态判据改用「资源面已关门」（初始 fail-open 形态
    // canDeploy=true，Me 落定后翻 false，翻转即终态）。
    const caps = await capabilitiesFor(fx, (c) => !c.canDeploy && !c.canAdminResources);
    expect(caps.canRead).toBe(true);
    expect(caps.canDeploy).toBe(false);
    expect(caps.canAdminResources).toBe(false);
    expect(caps.canManageTeam).toBe(false);
    expect(caps.canInvite).toBe(false);
  });

  it("非管理员 owner 不受扰：资源面照常全开（P0-3 改动零波及防回归）", async () => {
    const fx = { teamRole: "owner", isPlatformAdmin: false, project: null };
    vi.stubGlobal("fetch", stubFetch(fx));
    const caps = await capabilitiesFor(fx);
    expect(caps.canDeploy).toBe(true);
    expect(caps.canAdminResources).toBe(true);
    expect(caps.canManageTeam).toBe(true);
    expect(caps.canInvite).toBe(true);
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
