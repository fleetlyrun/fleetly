// 部署历史测试：失败行展示错误信封形态（error_code + verdict + recovery
// 可见）；中间态徽章（observing/blocked_waiting）一等渲染。跟踪轮询持续
// 失败（M9-5）：错误信封一等渲染而非永远转圈；refetchInterval 回调对空
// data 形态可选链守卫（M9-7）。平台管理员双门（P0-3）：资源面写卡换成
// 只读说明卡（不做静默消失），非管理员 owner 的写卡不受扰（防回归）。
// Deploy triggers 卡（P1-8）：读态字段（git_remote_hint/分支/secret configured
// 位/接收端 URL）/secret 载荷与不回显断言/source 载荷（整体替换、none 不带
// 材料键）/角色门三态（admin+ 见卡；成员说明态；平台管理员职责分离说明态）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AppDeploymentsPage } from "@/pages/AppDeploymentsPage";
import { TeamProjectProvider } from "@/lib/context";
import { setToken } from "@/api/client";

function ok(body: unknown) {
  return {
    ok: true,
    status: 200,
    statusText: "",
    json: () => Promise.resolve(body),
  };
}

function stubFetch(deployments: unknown[]) {
  return vi.fn().mockImplementation((url: string) => {
    if (String(url).includes("/deployments")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ deployments }),
      });
    }
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: "",
      json: () => Promise.resolve({ revisions: [] }),
    });
  });
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={["/apps/demo/deployments"]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/apps/:name/deployments" element={<AppDeploymentsPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("AppDeploymentsPage failure envelope", () => {
  it("renders code + message + suggestion for a failed deployment row", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetch([
        {
          id: "dep_01",
          app: "demo",
          kind: "deploy",
          status: "failed",
          phase: "",
          revision_id: "rev_1",
          error_code: "E_BUILD_FAILED",
          verdict: "buildkit solve failed at step 4/6",
          recovery: "fix Dockerfile and redeploy; build log in the artifacts dir",
          created_at: "2026-09-18T10:00:00Z",
        },
      ]),
    );

    renderPage();

    await waitFor(() => screen.getByRole("alert"));
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("E_BUILD_FAILED");
    expect(alert).toHaveTextContent("buildkit solve failed at step 4/6");
    expect(alert).toHaveTextContent(
      "fix Dockerfile and redeploy; build log in the artifacts dir",
    );
  });

  it("renders intermediate states (observing / blocked_waiting) as first-class badges", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetch([
        {
          id: "dep_obs",
          app: "demo",
          kind: "deploy",
          status: "observing",
          phase: "",
          revision_id: "rev_2",
          error_code: "",
          verdict: "",
          recovery: "",
        },
        {
          id: "dep_bw",
          app: "demo",
          kind: "deploy",
          status: "releasing",
          phase: "blocked_waiting",
          revision_id: "rev_3",
          error_code: "",
          verdict: "",
          recovery: "",
        },
      ]),
    );

    renderPage();

    await waitFor(() => screen.getByText("dep_bw"));
    const rows = screen.getAllByTestId("deployment-row");
    expect(rows).toHaveLength(2);
    const badges = screen.getAllByTestId("state-badge");
    const states = badges.map((b) => b.getAttribute("data-state"));
    expect(states).toContain("observing");
    expect(states).toContain("blocked_waiting");
  });
});

describe("AppDeploymentsPage platform-admin read-only (P0-3)", () => {
  // 夹具：/auth/me 供 owner 成员关系 + is_platform_admin 开关（经
  // TeamProjectProvider 挂载——能力判定走生产接线，非 fail-open 缺省）。
  function stubMe(isPlatformAdmin: boolean) {
    return vi.fn().mockImplementation((url: string) => {
      const u = String(url);
      if (u.endsWith("/auth/me")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () =>
            Promise.resolve({
              user: { id: "01U1", email: "f@t.test", is_platform_admin: isPlatformAdmin },
              teams: [
                { team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: "owner" },
              ],
              project_overrides: [],
            }),
        });
      }
      if (u.includes("/deployments")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () => Promise.resolve({ deployments: [] }),
        });
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ revisions: [] }),
      });
    });
  }

  function renderPageInTeamContext() {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    return render(
      <MemoryRouter initialEntries={["/apps/demo/deployments"]}>
        <QueryClientProvider client={client}>
          <TeamProjectProvider>
            <Routes>
              <Route path="/apps/:name/deployments" element={<AppDeploymentsPage />} />
            </Routes>
          </TeamProjectProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    );
  }

  it("平台管理员（owner 角色）：只读说明卡替代 Deploy/Rollback 卡，部署历史照常渲染", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubMe(true));
    renderPageInTeamContext();

    // 说明卡在写卡原位（诚实说明，不做静默消失）。
    await waitFor(() => {
      expect(screen.getByTestId("platform-readonly-note")).toHaveTextContent(
        "Platform administrators have read-only access to resources",
      );
    });
    // 资源面写卡（含可访问名锚点）不再渲染。
    expect(screen.queryByText("Deploy compose")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Deploy" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rollback" })).not.toBeInTheDocument();
    // 页面骨架不塌：部署历史空态照常。
    await waitFor(() => expect(screen.getByText("No deployments yet.")).toBeInTheDocument());
  });

  it("非管理员 owner：Deploy/Rollback 卡照常渲染、无只读说明（防回归）", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubMe(false));
    renderPageInTeamContext();

    await waitFor(() => expect(screen.getByText("Deploy compose")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Deploy" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Rollback" })).toBeInTheDocument();
    expect(screen.queryByTestId("platform-readonly-note")).not.toBeInTheDocument();
  });
});

describe("AppDeploymentsPage deployment tracking (M9-5 / M9-7)", () => {
  it("renders the error envelope when the tracked deployment poll keeps failing (no infinite spinner)", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: { method?: string }) => {
        const u = String(url);
        // Deploy 提交成功入队 → 开始跟踪。
        if (init?.method === "POST" && u.includes("/apps/demo/deployments")) {
          return Promise.resolve(
            ok({ deployment_id: "dep_track", warnings: [] }),
          );
        }
        // 跟踪轮询持续 500（空 data 形态——refetchInterval 回调不得抛
        // TypeError，页面不得永远转圈）。
        if (u.includes("/deployments/dep_track")) {
          return Promise.resolve({
            ok: false,
            status: 500,
            statusText: "",
            json: () =>
              Promise.resolve({
                code: "E_INTERNAL",
                message: "tracking unavailable",
                suggestion: "check daemon logs",
              }),
          });
        }
        return Promise.resolve(ok({ deployments: [], revisions: [] }));
      }),
    );

    renderPage();
    const user = userEvent.setup();
    await user.type(
      screen.getByLabelText("Compose YAML"),
      "services:\n  web:\n    image: nginx:1.27-alpine\n",
    );
    await user.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const envelope = screen.getByTestId("error-envelope");
      expect(envelope).toHaveTextContent("E_INTERNAL");
      expect(envelope).toHaveTextContent("tracking unavailable");
      expect(envelope).toHaveTextContent("check daemon logs");
    });
    // 跟踪块仍在（部署 ID 可见），只是状态以错误信封呈现。
    expect(screen.getByTestId("deployment-tracker")).toHaveTextContent(
      "dep_track",
    );
  });
});

// ── Deploy triggers 卡（P1-8「Git push 通道不可发现」）────────────────────

// 26 字符规范 ULID：Console 详情导航以平台 id 寻址（路由参数 = 平台 id，
// 非业务名）——触发卡测试挂 id 路由，钉「接收端 URL 用业务名而非 id」。
const APP_REF = "01ARZ3NDEKTSV4RRFFQ69G5FAV";

const WEBHOOK_CFG = {
  name: "demo",
  secret_configured: true,
  source_url: "https://git.example.com/acme/demo.git",
  source_branch: "release",
  source_auth_kind: "https_token",
  git_remote_hint: "ssh://git@10.0.0.8:8424/demo.git",
};

/** 触发面全 stub：Me（角色开关）+ 部署历史 + webhook 读面 + 写面回显。 */
function stubTriggers(role: string, isPlatformAdmin: boolean, cfg: unknown) {
  const calls: Array<{ url: string; method?: string; body?: unknown }> = [];
  const fetchMock = vi.fn().mockImplementation((url: string, init?: { method?: string; body?: string }) => {
    const u = String(url);
    calls.push({ url: u, method: init?.method, body: init?.body ? JSON.parse(init.body) : undefined });
    if (u.endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "f@t.test", is_platform_admin: isPlatformAdmin },
            teams: [{ team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role }],
            project_overrides: [],
          }),
      });
    }
    if (u.endsWith("/webhook") && (!init?.method || init.method === "GET")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(cfg) });
    }
    if (init?.method === "PUT" && u.endsWith("/webhook-secret")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ name: "demo", configured: true }) });
    }
    if (init?.method === "PUT" && u.endsWith("/source")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ name: "demo" }) });
    }
    if (u.includes("/deployments")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ deployments: [] }) });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ revisions: [] }) });
  });
  return { fetchMock, calls };
}

function renderTriggersPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={[`/apps/${APP_REF}/deployments`]}>
      <QueryClientProvider client={client}>
        <TeamProjectProvider>
          <Routes>
            <Route path="/apps/:name/deployments" element={<AppDeploymentsPage />} />
          </Routes>
        </TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("AppDeploymentsPage deploy triggers (P1-8)", () => {
  it("admin+ read state: remote hint with copy, trigger branch, secret configured badge, source", async () => {
    setToken("flt_test");
    const { fetchMock } = stubTriggers("owner", false, WEBHOOK_CFG);
    vi.stubGlobal("fetch", fetchMock);
    renderTriggersPage();

    // admin+（owner）见卡。
    await waitFor(() => expect(screen.getByTestId("deploy-triggers-card")).toBeInTheDocument());
    // 读态字段全部来自 ShowAppWebhook 响应（异步到达——以远端提示为就绪信号）。
    await waitFor(() =>
      expect(screen.getByTestId("triggers-git-remote")).toHaveTextContent("ssh://git@10.0.0.8:8424/demo.git"),
    );
    expect(screen.getByTestId("triggers-branch")).toHaveTextContent("release");
    expect(screen.getByTestId("triggers-secret-configured")).toHaveTextContent("Configured (never displayed)");
    expect(screen.getByTestId("triggers-source-state")).toHaveTextContent("https://git.example.com/acme/demo.git");
    // 接收端 URL（gateway 既有路由拼装）：路径段用响应的业务名（name
    // 字段）而非路由参数（平台 id）——id 形态永不匹配服务端接收端分派
    // 正则，拼进去就是恒 404 死链。
    const receiver = screen.getByTestId("triggers-webhook-url");
    expect(receiver).toHaveTextContent("/v1/apps/demo/webhooks/github");
    expect(receiver.textContent).not.toContain(APP_REF);

    const user = userEvent.setup();
    await user.click(screen.getByTestId("triggers-git-remote-copy"));
    expect(screen.getByTestId("triggers-git-remote-copy")).toHaveTextContent("Copied");
  });

  it("discloses honestly when the app name cannot match the receiver path pattern", async () => {
    setToken("flt_test");
    // 下划线名：服务端接收端分派正则（[a-z0-9][a-z0-9-]{0,62}）不收——
    // URL 照拼（如实展示），卡内出说明（服务端限制），不静默冒充可用。
    const { fetchMock } = stubTriggers("owner", false, { ...WEBHOOK_CFG, name: "demo_app" });
    vi.stubGlobal("fetch", fetchMock);
    renderTriggersPage();

    await waitFor(() =>
      expect(screen.getByTestId("triggers-webhook-url")).toHaveTextContent(
        "/v1/apps/demo_app/webhooks/github",
      ),
    );
    expect(screen.getByTestId("triggers-webhook-name-note")).toHaveTextContent(
      "server-side",
    );
  });

  it("secret rotation: PUT payload carries the value, success never echoes the plaintext", async () => {
    setToken("flt_test");
    // secret 未配置态起手（缺 secret_configured 位）。
    const { fetchMock, calls } = stubTriggers("owner", false, { name: "demo" });
    vi.stubGlobal("fetch", fetchMock);
    renderTriggersPage();
    const user = userEvent.setup();

    await waitFor(() => expect(screen.getByTestId("triggers-secret-missing")).toBeInTheDocument());

    const secretValue = "whsec-0123456789abcdef";
    await user.type(screen.getByTestId("triggers-secret-input"), secretValue);
    await user.click(screen.getByTestId("triggers-secret-open"));
    expect(screen.getByTestId("triggers-secret-dialog")).toHaveTextContent("takes effect immediately");

    await user.click(screen.getByTestId("triggers-secret-submit"));
    await waitFor(() => {
      const put = calls.find((c) => c.method === "PUT" && c.url.endsWith("/webhook-secret"));
      expect(put?.url.endsWith(`/v1/apps/${APP_REF}/webhook-secret`)).toBe(true);
      expect(put?.body).toEqual({ secret: secretValue });
    });

    // 不回显断言：成功提示出现、确认框关闭、明文在文档任何位置都不存在。
    await waitFor(() => expect(screen.getByTestId("triggers-secret-stored")).toBeInTheDocument());
    expect(screen.queryByTestId("triggers-secret-dialog")).not.toBeInTheDocument();
    expect(screen.queryByText(secretValue)).not.toBeInTheDocument();
    // 输入复位（type=password 输入框同样不得残留明文）。
    expect(screen.getByTestId("triggers-secret-input")).toHaveValue("");
  });

  it("set source: PUT payload is the whole replacement (url+branch+kind; no secret key for none)", async () => {
    setToken("flt_test");
    const { fetchMock, calls } = stubTriggers("owner", false, { name: "demo" });
    vi.stubGlobal("fetch", fetchMock);
    renderTriggersPage();
    const user = userEvent.setup();

    await waitFor(() => expect(screen.getByTestId("deploy-triggers-card")).toBeInTheDocument());
    // 未水合到 source 字段时分支缺省 main（proto source_branch 默认语义）。
    expect(screen.getByTestId("source-branch-input")).toHaveValue("main");

    await user.type(screen.getByTestId("source-url-input"), "https://git.example.com/acme/demo.git");
    await user.click(screen.getByTestId("source-submit"));

    await waitFor(() => {
      const put = calls.find((c) => c.method === "PUT" && c.url.endsWith("/source"));
      expect(put?.url.endsWith(`/v1/apps/${APP_REF}/source`)).toBe(true);
      expect(put?.body).toEqual({
        source_url: "https://git.example.com/acme/demo.git",
        source_branch: "main",
        source_auth_kind: "none",
      });
    });
    expect(screen.getByTestId("source-stored")).toBeInTheDocument();
  });

  it("role gate: card for admin+, honest note for developer, separation-of-duties note for platform admin", async () => {
    setToken("flt_test");
    const { fetchMock: adminFetch } = stubTriggers("owner", false, WEBHOOK_CFG);
    vi.stubGlobal("fetch", adminFetch);
    const { unmount } = renderTriggersPage();
    await waitFor(() => expect(screen.getByTestId("deploy-triggers-card")).toBeInTheDocument());
    unmount();

    // developer（canDeploy ✓ / canAdminResources ✗）：说明态，不发 webhook 读请求之外无卡。
    const { fetchMock: devFetch } = stubTriggers("developer", false, WEBHOOK_CFG);
    vi.stubGlobal("fetch", devFetch);
    const dev = renderTriggersPage();
    await waitFor(() => expect(screen.getByTestId("triggers-admin-note")).toBeInTheDocument());
    expect(screen.getByTestId("triggers-admin-note")).toHaveTextContent("visible to team admins only");
    expect(screen.queryByTestId("deploy-triggers-card")).not.toBeInTheDocument();
    dev.unmount();

    // 平台管理员：职责分离口径说明态（P0-3 双门），卡同样不渲染。
    const { fetchMock: paFetch } = stubTriggers("owner", true, WEBHOOK_CFG);
    vi.stubGlobal("fetch", paFetch);
    renderTriggersPage();
    await waitFor(() => expect(screen.getByTestId("triggers-admin-note")).toBeInTheDocument());
    expect(screen.getByTestId("triggers-admin-note")).toHaveTextContent(
      "Platform administrators have read-only access to resources",
    );
    expect(screen.queryByTestId("deploy-triggers-card")).not.toBeInTheDocument();
  });
});

// ── DeployCard 项目归属上下文（2026-09-25 走查）──────────────────────────
// 多团队成员从应用详情 Deploy 卡重部署 → deploy() 未传 project → 服务端
// 缺省解析不到归属项目 → 400 no default project resolvable。修复：选中
// team+project 即随载荷携带 `team/prj`；多团队未选禁用 Deploy 并卡内指路；
// 单团队用户缺省（服务端回落个人队 default）——载荷形态不变防回归。
describe("AppDeploymentsPage DeployCard project context", () => {
  const TEAMS = [
    { team_id: "01TA", team_slug: "acme", team_name: "Acme", role: "admin" },
    { team_id: "01TB", team_slug: "globex", team_name: "Globex", role: "owner" },
  ];
  const PROJECTS = {
    projects: [
      { id: "01PA", team_id: "01TA", team_slug: "acme", slug: "staging", name: "Staging" },
    ],
  };

  /** 多团队 stub：Me + /projects + 部署历史 + Deploy POST 捕获。带
   * resolveAppAttribution 开关时 /apps/demo GET 返回归属（W2-4）。 */
  function stubMultiTeam(opts: { appAttribution?: boolean } = {}) {
    const calls: Array<{ url: string; method?: string; body?: unknown }> = [];
    const fetchMock = vi.fn().mockImplementation((url: string, init?: { method?: string; body?: string }) => {
      const u = String(url);
      calls.push({ url: u, method: init?.method, body: init?.body ? JSON.parse(init.body) : undefined });
      if (u.endsWith("/auth/me")) {
        return Promise.resolve({
          ok: true, status: 200, statusText: "",
          json: () =>
            Promise.resolve({
              user: { id: "01U1", email: "f@t.test", is_platform_admin: false },
              teams: TEAMS,
              project_overrides: [],
            }),
        });
      }
      if (u.endsWith("/projects")) {
        return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(PROJECTS) });
      }
      if (opts.appAttribution && u.endsWith("/apps/demo") && (!init?.method || init.method === "GET")) {
        // W2-4：详情壳的 GetApp 响应带应用归属 slug（Deploy 卡优先消费）。
        return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ name: "demo", team_slug: "acme", project_slug: "staging" }) });
      }
      if (init?.method === "POST" && u.includes("/apps/demo/deployments")) {
        return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ deployment_id: "dep_ctx", warnings: [] }) });
      }
      if (u.includes("/deployments")) {
        return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ deployments: [] }) });
      }
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ revisions: [] }) });
    });
    return { fetchMock, calls };
  }

  function renderWithContext() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
      <MemoryRouter initialEntries={["/apps/demo/deployments"]}>
        <QueryClientProvider client={client}>
          <TeamProjectProvider>
            <Routes>
              <Route path="/apps/:name/deployments" element={<AppDeploymentsPage />} />
            </Routes>
          </TeamProjectProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    );
  }

  it("多团队+已选上下文：Deploy 载荷携带 team/prj（修复 400 no default project）", async () => {
    setToken("flt_test");
    window.localStorage.setItem(
      "fleetly.console.context",
      JSON.stringify({ team: "acme", project: "staging" }),
    );
    const { fetchMock, calls } = stubMultiTeam();
    vi.stubGlobal("fetch", fetchMock);

    renderWithContext();
    await waitFor(() => expect(screen.getByText("Deploy compose")).toBeInTheDocument());
    const user = userEvent.setup();
    await user.type(
      screen.getByLabelText("Compose YAML"),
      "services:\n  web:\n    image: nginx:1.27-alpine\n",
    );
    await user.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const post = calls.find((c) => c.method === "POST" && c.url.includes("/apps/demo/deployments"));
      expect(post).toBeTruthy();
      // project 以 team/prj 限定形随载荷上行（deployments.proto body:"*"）。
      expect(post?.body).toHaveProperty("project", "acme/staging");
    });
    expect(screen.getByTestId("deployment-tracker")).toHaveTextContent("dep_ctx");
  });

  it("多团队未选项目 + 应用归属可解析（W2-4）：Deploy 启用，载荷携带应用自身归属", async () => {
    setToken("flt_test");
    // 只选团队未选项目：顶栏上下文为空，但应用自身归属（GetApp 的
    // team_slug/project_slug）已返回 → 优先消费，Deploy 不再被全局上下文
    // 卡住（隐式全局状态依赖是缺口本身）。
    window.localStorage.setItem(
      "fleetly.console.context",
      JSON.stringify({ team: "acme", project: null }),
    );
    const { fetchMock, calls } = stubMultiTeam({ appAttribution: true });
    vi.stubGlobal("fetch", fetchMock);

    renderWithContext();
    await waitFor(() => expect(screen.getByText("Deploy compose")).toBeInTheDocument());
    const user = userEvent.setup();
    await user.type(
      screen.getByLabelText("Compose YAML"),
      "services:\n  web:\n    image: nginx:1.27-alpine\n",
    );
    // 等待应用归属解析完成（按钮从禁用翻转为可用）。
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Deploy" })).toBeEnabled(),
    );
    await user.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const post = calls.find((c) => c.method === "POST" && c.url.includes("/apps/demo/deployments"));
      expect(post).toBeTruthy();
      // 载荷 project = 应用自身归属（acme/staging），与顶栏选择无关。
      expect(post?.body).toHaveProperty("project", "acme/staging");
    });
  });

  it("多团队未选项目 + 应用归属未达：Deploy 禁用 + 卡内指路提示（不发请求）", async () => {
    setToken("flt_test");
    // 应用归属未返回（stub 无 /apps/demo GET）且顶栏未选 → 保持旧门：
    // 禁用 + 指路，不静默发缺省部署。
    window.localStorage.setItem(
      "fleetly.console.context",
      JSON.stringify({ team: "acme", project: null }),
    );
    const { fetchMock, calls } = stubMultiTeam();
    vi.stubGlobal("fetch", fetchMock);

    renderWithContext();
    await waitFor(() => expect(screen.getByText("Deploy compose")).toBeInTheDocument());
    const user = userEvent.setup();
    await user.type(
      screen.getByLabelText("Compose YAML"),
      "services:\n  web:\n    image: nginx:1.27-alpine\n",
    );

    const deployButton = screen.getByRole("button", { name: "Deploy" });
    expect(deployButton).toBeDisabled();
    expect(screen.getByTestId("deploy-project-context-hint")).toHaveTextContent(
      "Select a project above to deploy",
    );
    // 有 compose 内容也不得发出 Deploy POST（按钮禁用即无请求面）。
    expect(
      calls.some((c) => c.method === "POST" && c.url.includes("/apps/demo/deployments")),
    ).toBe(false);
  });

  it("单团队回归：未选上下文 Deploy 载荷不带 project（服务端个人队缺省）", async () => {
    setToken("flt_test");
    const calls: Array<{ url: string; method?: string; body?: unknown }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: { method?: string; body?: string }) => {
        const u = String(url);
        calls.push({ url: u, method: init?.method, body: init?.body ? JSON.parse(init.body) : undefined });
        if (u.endsWith("/auth/me")) {
          return Promise.resolve({
            ok: true, status: 200, statusText: "",
            json: () =>
              Promise.resolve({
                user: { id: "01U1", email: "f@t.test", is_platform_admin: false },
                teams: [TEAMS[0]],
                project_overrides: [],
              }),
          });
        }
        if (u.endsWith("/projects")) {
          return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(PROJECTS) });
        }
        if (init?.method === "POST" && u.includes("/apps/demo/deployments")) {
          return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ deployment_id: "dep_solo", warnings: [] }) });
        }
        if (u.includes("/deployments")) {
          return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ deployments: [] }) });
        }
        return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({ revisions: [] }) });
      }),
    );

    renderWithContext();
    await waitFor(() => expect(screen.getByText("Deploy compose")).toBeInTheDocument());
    const user = userEvent.setup();
    await user.type(
      screen.getByLabelText("Compose YAML"),
      "services:\n  web:\n    image: nginx:1.27-alpine\n",
    );
    // 单团队用户零选择也有正确能力（useTeamCapabilities 回落唯一团队）。
    expect(screen.getByRole("button", { name: "Deploy" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const post = calls.find((c) => c.method === "POST" && c.url.includes("/apps/demo/deployments"));
      expect(post).toBeTruthy();
      expect(post?.body).not.toHaveProperty("project");
    });
  });
});
