// 登录页测试（v0.3 RBAC W1 新形态）：
// - 注册入口门控：GET /v1/auth/registration（open 或无用户窗口）驱动；
// - 注册表单校验（口令≥8、两次一致）先于请求；注册成功自动登录；
// - 口令登录流（cookie 会话——不落 localStorage、credentials include、
//   错口令 401 = 本页信封而非 Gate 级「Session invalid」横幅）；
// - 来源页恢复（from 暂存 → 登录后回跳，邀请链接回跳的机制基础）；
// - 折叠 API token 高级路径回归（M9-1 先验后存：校验期间 Bearer 已带、
//   authed 不翻、401 清凭据停留本页）；
// - 启动 Me 探测：401 清残留 token 落登录页；200（cookie 会话）直达应用面。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { LoginPage } from "@/pages/LoginPage";
import { getToken, setToken } from "@/api/client";
import { AuthProvider } from "@/auth";
import { App } from "@/App";
import { queryClient } from "@/query";

function jsonResponse(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 401 ? "Unauthorized" : "",
    json: () => Promise.resolve(body),
  };
}

const ME_USER = {
  user: {
    id: "u1",
    email: "op@example.com",
    display_name: "Operator",
    is_platform_admin: true,
  },
  teams: [],
};

// 上一账号的 Me 投影（P1-2 身份串号复现夹具）：staleTime 5min 内的陈旧
// 缓存若登录时不清，RequireAuth/用户菜单会直接吃掉 → 身份壳仍是前任。
const ME_PREV_ACCOUNT = {
  user: {
    id: "u0",
    email: "founder@example.com",
    display_name: "Founder",
    is_platform_admin: true,
  },
  teams: [],
};

interface RouteStub {
  match: (url: string) => boolean;
  respond: (url: string, init?: RequestInit, meCall?: number) => unknown;
}

/** 匹配 /v1/{sub} 的便捷 match（不含 /auth 前缀歧义面）。 */
const path = (sub: string) => (u: string) => u.includes(`/v1/${sub}`);

/**
 * URL 装配的 fetch 桩：/v1/auth/me 按调用序次计数（探测第一发、登录后
 * 用户菜单第二发——两类测试用不同序次给不同答案）。
 */
function stubFetch(routes: RouteStub[]) {
  let meCalls = 0;
  return vi.fn((url: unknown, init?: RequestInit) => {
    const u = String(url);
    if (u.includes("/auth/me")) {
      meCalls += 1;
      const route = routes.find((r) => r.match(u));
      return Promise.resolve(
        route ? route.respond(u, init, meCalls) : jsonResponse(404, {}),
      );
    }
    const route = routes.find((r) => r.match(u));
    if (route) return Promise.resolve(route.respond(u, init));
    return Promise.resolve(jsonResponse(404, { message: `unmocked ${u}` }));
  });
}

function renderLoginStandalone(state?: { from?: string }) {
  return render(
    <MemoryRouter initialEntries={[{ pathname: "/login", state }]}>
      <AuthProvider>
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/elsewhere" element={<p>landed-elsewhere</p>} />
            <Route path="/" element={<p>landed-home</p>} />
          </Routes>
        </QueryClientProvider>
      </AuthProvider>
    </MemoryRouter>,
  );
}

function renderAppAt(pathName: string) {
  window.history.replaceState({}, "", `/ui${pathName}`);
  return render(<App />);
}

/** 注册成功的登录后应用面依赖（Layout/Home 的读面 + 事件流挂起）。 */
function appSurfaceRoutes(): RouteStub[] {
  return [
    { match: path("system/status"), respond: () => jsonResponse(200, { version: "0.3.0" }) },
    { match: path("system/nodes"), respond: () => jsonResponse(200, { nodes: [] }) },
    { match: path("apps"), respond: () => jsonResponse(200, { apps: [] }) },
    { match: path("events/stream"), respond: () => new Promise(() => undefined) },
  ];
}

beforeEach(() => {
  queryClient.clear();
  setToken("");
  // 登出提示标志（App.tsx 401 弹回路径置位、登录页读后即清）——用例间隔离。
  sessionStorage.clear();
});

describe("LoginPage registration entry gating", () => {
  it("shows the register entry when registration is open", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch([
        { match: path("auth/registration"), respond: () => jsonResponse(200, { open: true, has_users: true }) },
      ]),
    );
    renderLoginStandalone();
    expect(await screen.findByTestId("register-toggle")).toBeInTheDocument();
  });

  it("shows the register entry in the no-users window (open=false, has_users=false)", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch([
        { match: path("auth/registration"), respond: () => jsonResponse(200, { open: false, has_users: false }) },
      ]),
    );
    renderLoginStandalone();
    expect(await screen.findByTestId("register-toggle")).toBeInTheDocument();
  });

  it("hides the register entry when registration is closed", async () => {
    const fetchMock = stubFetch([
      { match: path("auth/registration"), respond: () => jsonResponse(200, { open: false, has_users: true }) },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderLoginStandalone();
    await screen.findByTestId("login-email");
    // 查询已发出且已返回后仍无入口（而非未加载的瞬态）。
    await waitFor(() => {
      expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/auth/registration"))).toBe(true);
    });
    expect(screen.queryByTestId("register-toggle")).not.toBeInTheDocument();
  });
});

describe("LoginPage register form", () => {
  it("validates password length and match locally before any request", async () => {
    const fetchMock = stubFetch([
      { match: path("auth/registration"), respond: () => jsonResponse(200, { open: true, has_users: true }) },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderLoginStandalone();
    const user = userEvent.setup();
    await user.click(await screen.findByTestId("register-toggle"));

    await user.type(screen.getByTestId("register-email"), "new@example.com");
    await user.type(screen.getByTestId("register-password"), "short");
    await user.type(screen.getByTestId("register-confirm"), "short");
    await user.click(screen.getByTestId("register-submit"));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Password must be at least 8 characters.");
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/auth/register"))).toBe(false);

    await user.clear(screen.getByTestId("register-password"));
    await user.type(screen.getByTestId("register-password"), "long-enough-8");
    await user.type(screen.getByTestId("register-confirm"), "different-123");
    await user.click(screen.getByTestId("register-submit"));
    expect(await screen.findByRole("alert")).toHaveTextContent("Passwords do not match.");
    expect(fetchMock.mock.calls.some(([u]) => String(u).endsWith("/v1/auth/register"))).toBe(false);
  });

  it("registers and lands signed in (cookie session, no Bearer persisted)", async () => {
    const registerCalls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = stubFetch([
      // me：第一发 = 启动探测（401 匿名）；注册成功后用户菜单第二发 200。
      { match: path("auth/me"), respond: (_u, _i, call) => (call === 1 ? jsonResponse(401, {}) : jsonResponse(200, ME_USER)) },
      { match: path("auth/registration"), respond: () => jsonResponse(200, { open: true, has_users: true }) },
      {
        match: path("auth/register"),
        respond: (u, init) => {
          registerCalls.push({ url: String(u), init });
          return jsonResponse(200, { user: ME_USER.user });
        },
      },
      ...appSurfaceRoutes(),
    ]);
    vi.stubGlobal("fetch", fetchMock);
    // 上一账号的 Me 投影预置入缓存（staleTime 5min）：不清缓存则用户菜单
    // 直接复用（仍显 Founder、Me 只有探测一发）——注册路径的清缓存在此现形。
    queryClient.setQueryData(["auth", "me"], ME_PREV_ACCOUNT);
    renderAppAt("/login");
    const user = userEvent.setup();
    await user.click(await screen.findByTestId("register-toggle"));
    await user.type(screen.getByTestId("register-email"), "new@example.com");
    await user.type(screen.getByTestId("register-display-name"), "New User");
    await user.type(screen.getByTestId("register-password"), "long-enough-8");
    await user.type(screen.getByTestId("register-confirm"), "long-enough-8");
    await user.click(screen.getByTestId("register-submit"));

    // 注册即自动登录：Gate 切应用面（用户菜单出现）。
    expect(await screen.findByTestId("user-menu")).toBeInTheDocument();

    expect(registerCalls).toHaveLength(1);
    expect(registerCalls[0]?.url).toBe("/v1/auth/register");
    const init = registerCalls[0]?.init as RequestInit & { credentials?: string };
    expect(init.credentials).toBe("include");
    expect((init.headers as Record<string, string>).Authorization).toBeUndefined();
    expect(JSON.parse(String(init.body))).toEqual({
      email: "new@example.com",
      password: "long-enough-8",
      display_name: "New User",
    });
    // cookie 会话路径不落 localStorage 凭据。
    expect(getToken()).toBe("");

    // P1-2：注册即换身份——预置的上一账号 Me 投影不得存活到应用面。缓存
    // 已清 → 用户菜单必然重探 Me（第二发）并以新账号渲染（按钮标签为证）。
    const meCalls = fetchMock.mock.calls.filter(([u]) => String(u).includes("/auth/me"));
    await waitFor(() => {
      expect(screen.getByTestId("user-menu")).toHaveTextContent("Operator");
      expect(screen.getByTestId("user-menu")).not.toHaveTextContent("Founder");
    });
    expect(meCalls.length).toBeGreaterThanOrEqual(2);
  });

  it("clears the previous account's query cache on sign-in (identity-switch isolation)", async () => {
    const fetchMock = stubFetch([
      // me：第一发 = 启动探测（401 匿名）；登录成功后用户菜单第二发 200。
      { match: path("auth/me"), respond: (_u, _i, call) => (call === 1 ? jsonResponse(401, {}) : jsonResponse(200, ME_USER)) },
      { match: path("auth/registration"), respond: () => jsonResponse(200, { open: false, has_users: true }) },
      { match: path("auth/login"), respond: () => jsonResponse(200, { user: ME_USER.user }) },
      ...appSurfaceRoutes(),
    ]);
    vi.stubGlobal("fetch", fetchMock);
    // 上一账号的 Me 投影已入缓存（staleTime 5min 内的应用面会直接复用——
    // P1-2 身份串号的成因）。不清缓存则重探不发生、菜单仍显 Founder。
    queryClient.setQueryData(["auth", "me"], ME_PREV_ACCOUNT);

    renderAppAt("/login");
    const user = userEvent.setup();
    await user.type(await screen.findByTestId("login-email"), "op@example.com");
    await user.type(screen.getByTestId("login-password"), "correct-horse");
    await user.click(screen.getByTestId("login-submit"));

    // 导航前整树清缓存：登录后的 Me 重探（第二发）且身份壳是新账号。
    expect(await screen.findByTestId("user-menu")).toBeInTheDocument();
    const meCalls = fetchMock.mock.calls.filter(([u]) => String(u).includes("/auth/me"));
    await waitFor(() => {
      expect(meCalls.length).toBeGreaterThanOrEqual(2);
      expect(screen.getByTestId("user-menu")).toHaveTextContent("Operator");
      expect(screen.getByTestId("user-menu")).not.toHaveTextContent("Founder");
    });
  });
});

describe("LoginPage password sign-in", () => {
  it("signs in with email+password over the cookie session", async () => {
    const loginCalls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = stubFetch([
      { match: path("auth/me"), respond: (_u, _i, call) => (call === 1 ? jsonResponse(401, {}) : jsonResponse(200, ME_USER)) },
      { match: path("auth/registration"), respond: () => jsonResponse(200, { open: false, has_users: true }) },
      {
        match: path("auth/login"),
        respond: (u, init) => {
          loginCalls.push({ url: String(u), init });
          return jsonResponse(200, { user: ME_USER.user });
        },
      },
      ...appSurfaceRoutes(),
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderAppAt("/login");
    const user = userEvent.setup();
    await user.type(await screen.findByTestId("login-email"), "op@example.com");
    await user.type(screen.getByTestId("login-password"), "correct-horse");
    await user.click(screen.getByTestId("login-submit"));

    expect(await screen.findByTestId("user-menu")).toBeInTheDocument();
    expect(loginCalls).toHaveLength(1);
    expect(loginCalls[0]?.url).toBe("/v1/auth/login");
    const init = loginCalls[0]?.init as RequestInit & { credentials?: string };
    expect(init.credentials).toBe("include");
    expect(JSON.parse(String(init.body))).toEqual({
      email: "op@example.com",
      password: "correct-horse",
    });
    expect(getToken()).toBe("");
  });

  it("renders the 401 envelope on wrong credentials without the Gate session-invalid banner", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch([
        { match: path("auth/me"), respond: () => jsonResponse(401, {}) },
        { match: path("auth/registration"), respond: () => jsonResponse(200, { open: false, has_users: true }) },
        {
          match: path("auth/login"),
          respond: () =>
            jsonResponse(401, {
              code: "E_UNAUTHENTICATED",
              message: "invalid email or password",
              suggestion: "check your credentials and retry",
            }),
        },
      ]),
    );
    renderAppAt("/login");
    const user = userEvent.setup();
    await user.type(await screen.findByTestId("login-email"), "op@example.com");
    await user.type(screen.getByTestId("login-password"), "wrong-pass");
    await user.click(screen.getByTestId("login-submit"));

    await waitFor(() => {
      const alert = screen.getByRole("alert");
      expect(alert).toHaveTextContent("Sign-in failed: invalid email or password");
      expect(alert).toHaveTextContent("check your credentials and retry");
    });
    // optionalAuth：登录面的 401 是业务结果——Gate 级「Session invalid」
    // 横幅不得出现，仍停登录页可重试。
    expect(screen.queryByText(/Session invalid/)).not.toBeInTheDocument();
    expect(screen.getByTestId("login-email")).toBeInTheDocument();
  });

  it("returns to the stored from-location after sign-in (invite/deep-link restore)", async () => {
    const fetchMock = stubFetch([
      { match: path("auth/me"), respond: () => jsonResponse(401, {}) },
      { match: path("auth/login"), respond: () => jsonResponse(200, { user: ME_USER.user }) },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderLoginStandalone({ from: "/elsewhere" });
    const user = userEvent.setup();
    await user.type(await screen.findByTestId("login-email"), "op@example.com");
    await user.type(screen.getByTestId("login-password"), "correct-horse");
    await user.click(screen.getByTestId("login-submit"));

    expect(await screen.findByText("landed-elsewhere")).toBeInTheDocument();
  });
});

describe("LoginPage invited registration (review P1-1: anon + closed window)", () => {
  // InvitePage 引导登录时塞进 from 的完整邀请 URL（/auth/invite?token=…）。
  const INVITE_FROM = { from: "/auth/invite?token=invitetoken123" };

  it("renders the invited register form as the first screen even when registration is closed", async () => {
    const fetchMock = stubFetch([
      // 关窗态（open=false, has_users=true）：受邀注册豁免注册窗，表单照常
      // 首屏出现——服务端 W3-S4 契约（携带 invite_token 的注册豁免窗口判定）。
      { match: path("auth/registration"), respond: () => jsonResponse(200, { open: false, has_users: true }) },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderLoginStandalone(INVITE_FROM);

    // 首屏主形态 = 受邀注册表单（无需任何 toggle），文案区分受邀形态。
    expect(screen.getByTestId("register-form")).toBeInTheDocument();
    expect(screen.getByText(/You've been invited to join a team/)).toBeInTheDocument();

    // 登录为次链接：可切到登录表单；受邀上下文仍保留注册入口（可切回）。
    const user = userEvent.setup();
    await user.click(screen.getByTestId("login-toggle"));
    expect(screen.getByTestId("login-form")).toBeInTheDocument();
    expect(screen.getByTestId("register-toggle")).toBeInTheDocument();
  });

  it("sends invite_token with the register payload and lands in the console (not back at the invite URL)", async () => {
    const registerCalls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = stubFetch([
      { match: path("auth/me"), respond: () => jsonResponse(401, {}) },
      { match: path("auth/registration"), respond: () => jsonResponse(200, { open: false, has_users: true }) },
      {
        match: path("auth/register"),
        respond: (u, init) => {
          registerCalls.push({ url: String(u), init });
          return jsonResponse(200, { user: ME_USER.user });
        },
      },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderLoginStandalone(INVITE_FROM);
    const user = userEvent.setup();
    await user.type(await screen.findByTestId("register-email"), "invited@example.com");
    await user.type(screen.getByTestId("register-display-name"), "Invited User");
    await user.type(screen.getByTestId("register-password"), "long-enough-8");
    await user.type(screen.getByTestId("register-confirm"), "long-enough-8");
    await user.click(screen.getByTestId("register-submit"));

    // 载荷携带 invite_token（服务端同事务消费邀请 + 下发会话 cookie）。
    expect(registerCalls).toHaveLength(1);
    expect(registerCalls[0]?.url).toBe("/v1/auth/register");
    const init = registerCalls[0]?.init as RequestInit & { credentials?: string };
    expect(init.credentials).toBe("include");
    expect(JSON.parse(String(init.body))).toEqual({
      email: "invited@example.com",
      password: "long-enough-8",
      display_name: "Invited User",
      invite_token: "invitetoken123",
    });

    // 注册事务已消费邀请——直达控制台首页而非回跳邀请 URL（二次 accept
    // 同一 token 必 409 E_INVITE_INVALID）。
    expect(await screen.findByText("landed-home")).toBeInTheDocument();
  });
});

describe("LoginPage API token path removed (2026-09-25 user ruling)", () => {
  it("no longer renders the token section on the sign-in form", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch([
        { match: path("auth/me"), respond: () => jsonResponse(401, {}) },
        { match: path("auth/registration"), respond: () => jsonResponse(200, { open: false, has_users: true }) },
      ]),
    );
    renderAppAt("/login");
    await screen.findByTestId("login-email");
    expect(screen.queryByTestId("login-token-toggle")).not.toBeInTheDocument();
    expect(screen.queryByTestId("login-token")).not.toBeInTheDocument();
    expect(screen.queryByTestId("login-token-submit")).not.toBeInTheDocument();
    expect(screen.queryByText(/API token/i)).not.toBeInTheDocument();
  });
});

describe("startup Me probe", () => {
  it("clears a stale stored token and lands on the login page when Me rejects 401", async () => {
    setToken("flt_stale");
    vi.stubGlobal(
      "fetch",
      stubFetch([
        { match: path("auth/me"), respond: () => jsonResponse(401, { message: "session expired" }) },
      ]),
    );
    renderAppAt("/login");
    await screen.findByTestId("login-email");
    // 401 → 清本地态（残留 token 不残留）回登录页。
    expect(getToken()).toBe("");
  });

  it("enters the app when the cookie session is valid (Me 200 at startup)", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch([
        { match: path("auth/me"), respond: () => jsonResponse(200, ME_USER) },
        ...appSurfaceRoutes(),
      ]),
    );
    renderAppAt("/");
    // 应用启动拉 Me：会话有效 → 直达应用面（用户菜单在顶栏）。
    expect(await screen.findByTestId("user-menu")).toBeInTheDocument();
  });

  it("bounces to the login page on a resource 401 without a Session-invalid banner", async () => {
    // 会话失效（资源面 401）：全局处置清本地态回登录页；告警条不随行——
    // P1-2 顺带收敛：登录页错误态归提交动作自身，全局 401 只负责弹回。
    vi.stubGlobal(
      "fetch",
      stubFetch([
        { match: path("auth/me"), respond: () => jsonResponse(200, ME_USER) },
        { match: path("system/status"), respond: () => jsonResponse(200, { version: "0.3.0" }) },
        { match: path("system/nodes"), respond: () => jsonResponse(200, { nodes: [] }) },
        { match: path("apps"), respond: () => jsonResponse(401, { message: "invalid or expired session" }) },
        { match: path("events/stream"), respond: () => new Promise(() => undefined) },
      ]),
    );
    renderAppAt("/");
    expect(await screen.findByTestId("login-email")).toBeInTheDocument();
    expect(screen.queryByText(/Session invalid/)).not.toBeInTheDocument();
  });
});

describe("LoginPage neutral signed-out notice (2026-09-25 leftover)", () => {
  it("shows the neutral notice after a global-401 bounce", async () => {
    // 会话失效被全局 401 处置弹回：登录页渲染中性提示条（muted，非告警）
    // ——前序阶段移除「Session invalid」告警后补的告知面。
    vi.stubGlobal(
      "fetch",
      stubFetch([
        { match: path("auth/me"), respond: () => jsonResponse(200, ME_USER) },
        { match: path("system/status"), respond: () => jsonResponse(200, { version: "0.3.0" }) },
        { match: path("system/nodes"), respond: () => jsonResponse(200, { nodes: [] }) },
        { match: path("apps"), respond: () => jsonResponse(401, { message: "invalid or expired session" }) },
        { match: path("events/stream"), respond: () => new Promise(() => undefined) },
      ]),
    );
    renderAppAt("/");

    expect(await screen.findByTestId("login-email")).toBeInTheDocument();
    expect(screen.getByTestId("signed-out-note")).toHaveTextContent(
      "You have been signed out.",
    );
    // 中性提示不是告警（无 role=alert），错误态仍归提交动作自身。
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows no notice after an explicit sign-out from the user menu", async () => {
    // 主动登出（用户菜单 Sign out）：不置标志——登录页无提示条。
    vi.stubGlobal(
      "fetch",
      stubFetch([
        { match: path("auth/me"), respond: () => jsonResponse(200, ME_USER) },
        { match: path("system/status"), respond: () => jsonResponse(200, { version: "0.3.0" }) },
        { match: path("system/nodes"), respond: () => jsonResponse(200, { nodes: [] }) },
        { match: path("apps"), respond: () => jsonResponse(200, { apps: [] }) },
        { match: path("events/stream"), respond: () => new Promise(() => undefined) },
        { match: path("auth/logout"), respond: () => jsonResponse(200, {}) },
      ]),
    );
    renderAppAt("/");
    const user = userEvent.setup();

    await user.click(await screen.findByTestId("user-menu"));
    await user.click(screen.getByTestId("logout"));

    expect(await screen.findByTestId("login-email")).toBeInTheDocument();
    expect(screen.queryByTestId("signed-out-note")).not.toBeInTheDocument();
  });
});
