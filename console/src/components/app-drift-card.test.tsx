// 应用漂移卡测试（2026-09-25 审查 backlog #9 / §3 P1-5）：三态渲染
//（in sync / drifted / no baseline）与 opt-in 回显（开）、Converge 确认框
// 与载荷、开关载荷、角色门三态（developer 可收敛+开关（deploy 门，2026-09-25
// 复核对齐）/ viewer 只读 / 平台管理员说明态）、信封错误（409 在途部署
// 冲突）。所有 fetch 均为原始 stub（与 AppOverviewPage.test 同形态），断言
// URL + method + JSON 载荷。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AppDriftCard } from "@/components/app-drift-card";
import { setToken } from "@/api/client";
import { TeamProjectProvider } from "@/lib/context";

interface FetchLogEntry {
  url: string;
  method: string;
  body?: unknown;
}

function jsonResponse(body: unknown, ok = true, status = 200) {
  return {
    ok,
    status,
    statusText: ok ? "OK" : "Error",
    json: () => Promise.resolve(body),
  };
}

/** 漂移卡 fetch stub：drift 响应可随会话演进（收敛测试用可变闭包）。 */
function stubDriftFetch(opts: {
  log: FetchLogEntry[];
  drift: () => unknown;
  converge?: () => { status: number; body: unknown };
  me?: { is_platform_admin: boolean; role: string } | null;
}) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = init?.body !== undefined ? JSON.parse(String(init.body)) : undefined;
    opts.log.push({ url, method, body });
    if (url.endsWith("/auth/me")) {
      if (!opts.me) {
        // 无 me 投影（fail-open 口径——能力全开，与直挂页面测试同境）。
        return jsonResponse({});
      }
      return jsonResponse({
        user: { id: "01U1", email: "u@t.test", is_platform_admin: opts.me.is_platform_admin },
        teams: [
          { team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: opts.me.role },
        ],
        project_overrides: [],
      });
    }
    if (url.endsWith("/drift")) {
      return jsonResponse(opts.drift());
    }
    if (url.endsWith("/drift/converge")) {
      const res = opts.converge?.() ?? { status: 200, body: { app: "demo", deployment_id: "dep1", desired_hash: "abc123" } };
      return jsonResponse(res.body, res.status < 400, res.status);
    }
    if (url.endsWith("/drift/convergence")) {
      return jsonResponse({ app: "demo", enabled: (body as { enabled?: boolean })?.enabled ?? false });
    }
    // 其余（TeamProjectProvider 的 /projects 等）一律空投影。
    return jsonResponse({});
  });
}

const IN_SYNC = {
  app: "demo",
  desired_deployment: "dep1",
  drifted: false,
  services: [{ service: "web", drifted: false }],
};

const DRIFTED = {
  app: "demo",
  desired_deployment: "dep1",
  drifted: true,
  services: [
    {
      service: "web",
      drifted: true,
      diff: [{ field: "image", expected: "sha256:aaa", actual: "sha256:bbb" }],
    },
    {
      service: "ghost",
      drifted: true,
      missing: true,
      diff: [{ field: "service", expected: "present", actual: "absent" }],
    },
  ],
};

function renderCard() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <TeamProjectProvider>
        <AppDriftCard app="demo" />
      </TeamProjectProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  setToken("flt_test");
});

describe("AppDriftCard status rendering", () => {
  it("renders in sync when drifted is false (baseline present)", async () => {
    vi.stubGlobal("fetch", stubDriftFetch({ log: [], drift: () => IN_SYNC }));
    renderCard();

    const badge = await screen.findByTestId("drift-status-badge");
    await waitFor(() => expect(badge.textContent).toContain("in sync"));
    expect(screen.queryByTestId("drift-summary")).not.toBeInTheDocument();
  });

  it("renders drifted with per-service diff summary (missing + field diffs)", async () => {
    vi.stubGlobal("fetch", stubDriftFetch({ log: [], drift: () => DRIFTED }));
    renderCard();

    const badge = await screen.findByTestId("drift-status-badge");
    await waitFor(() => expect(badge.textContent).toContain("drifted"));
    const summary = screen.getByTestId("drift-summary");
    expect(summary.textContent).toContain("web");
    expect(summary.textContent).toContain("image");
    expect(summary.textContent).toContain("sha256:aaa");
    expect(summary.textContent).toContain("absent from runtime");
    expect(summary.textContent).toContain("ghost");
  });

  it("renders no baseline when desired_deployment is absent (no succeeded deployment)", async () => {
    vi.stubGlobal("fetch", stubDriftFetch({ log: [], drift: () => ({ app: "demo" }) }));
    renderCard();

    const badge = await screen.findByTestId("drift-status-badge");
    await waitFor(() => expect(badge.textContent).toContain("no baseline"));
    expect(screen.queryByTestId("drift-summary")).not.toBeInTheDocument();
  });

  it("renders an envelope error from the read face", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
        void init;
        const url = String(input);
        if (url.endsWith("/drift")) {
          return jsonResponse(
            { code: "E_INTERNAL", message: "drift probe failed", suggestion: "retry" },
            false,
            500,
          );
        }
        return jsonResponse({});
      }),
    );
    renderCard();

    const alert = await screen.findByTestId("error-envelope");
    await waitFor(() => expect(alert.textContent).toContain("E_INTERNAL"));
    expect(alert.textContent).toContain("drift probe failed");
    expect(alert.textContent).toContain("retry");
  });
});

describe("AppDriftCard converge", () => {
  it("opens a confirm dialog on first click, posts the converge payload, and tracks to done", async () => {
    const log: FetchLogEntry[] = [];
    let drifted = true;
    vi.stubGlobal(
      "fetch",
      stubDriftFetch({
        log,
        drift: () => (drifted ? DRIFTED : IN_SYNC),
      }),
    );
    renderCard();

    await screen.findByTestId("drift-status-badge");
    fireEvent.click(screen.getByTestId("drift-converge-button"));

    // 首次点击只弹确认框（不发光）——说明可能的服务重建后果。
    const dialog = await screen.findByTestId("drift-converge-dialog");
    expect(dialog.textContent).toContain("recreated");
    expect(dialog.textContent).toContain("removed");
    const convergePosts = log.filter(
      (e) => e.method === "POST" && e.url.endsWith("/drift/converge"),
    );
    expect(convergePosts).toHaveLength(0);

    fireEvent.click(screen.getByTestId("drift-converge-submit"));
    drifted = false; // 收敛部署达成：漂移清零。

    await waitFor(() =>
      expect(screen.getByTestId("drift-converge-done")).toBeInTheDocument(),
    );
    const post = log.find(
      (e) => e.method === "POST" && e.url.endsWith("/apps/demo/drift/converge"),
    );
    expect(post).toBeTruthy();
    expect(post?.body).toEqual({}); // 契约载荷只有 app（路径参数）——空对象。
  });

  it("surfaces the envelope error on 409 conflict (deployment in flight)", async () => {
    const log: FetchLogEntry[] = [];
    vi.stubGlobal(
      "fetch",
      stubDriftFetch({
        log,
        drift: () => DRIFTED,
        converge: () => ({
          status: 409,
          body: {
            code: "E_STATE_VERSION_CONFLICT",
            message: "app demo has a deployment in flight: wait for it to reach a terminal state before converging",
            suggestion: "Wait for the in-flight deployment to finish.",
          },
        }),
      }),
    );
    renderCard();

    await screen.findByTestId("drift-converge-button");
    fireEvent.click(screen.getByTestId("drift-converge-button"));
    fireEvent.click(screen.getByTestId("drift-converge-submit"));

    const alert = await screen.findByTestId("error-envelope");
    await waitFor(() => expect(alert.textContent).toContain("E_STATE_VERSION_CONFLICT"));
    expect(alert.textContent).toContain("deployment in flight");
  });
});

describe("AppDriftCard convergence opt-in toggle", () => {
  it("shows unknown opt-in state initially and posts enable/disable payloads with the echo", async () => {
    const log: FetchLogEntry[] = [];
    vi.stubGlobal("fetch", stubDriftFetch({ log, drift: () => IN_SYNC }));
    renderCard();

    await screen.findByTestId("drift-convergence-toggle");
    // 无读取面：初态如实 unknown，不冒充 off。
    expect(screen.getByTestId("drift-convergence-state").textContent).toContain("unknown");

    fireEvent.click(screen.getByTestId("drift-convergence-enable"));
    await waitFor(() =>
      expect(screen.getByTestId("drift-convergence-state").textContent).toContain("on"),
    );
    fireEvent.click(screen.getByTestId("drift-convergence-disable"));
    await waitFor(() =>
      expect(screen.getByTestId("drift-convergence-state").textContent).toContain("off"),
    );

    const puts = log.filter(
      (e) => e.method === "PUT" && e.url.endsWith("/apps/demo/drift/convergence"),
    );
    expect(puts).toHaveLength(2);
    expect(puts[0]?.body).toEqual({ enabled: true });
    expect(puts[1]?.body).toEqual({ enabled: false });
  });
});

describe("AppDriftCard role gates", () => {
  function renderCardInRole(me: { is_platform_admin: boolean; role: string }) {
    const log: FetchLogEntry[] = [];
    vi.stubGlobal(
      "fetch",
      stubDriftFetch({ log, drift: () => IN_SYNC, me }),
    );
    renderCard();
    return log;
  }

  it("developer: converge button and opt-in toggle both visible (deploy-scope gate)", async () => {
    // SetDriftConverge 服务端登记即 ScopeDeploy——developer 的开关可见性
    // 与服务端同口径（2026-09-25 复核：不再前端收口到 admin）。
    renderCardInRole({ is_platform_admin: false, role: "developer" });

    await screen.findByTestId("drift-converge-button");
    expect(screen.getByTestId("drift-convergence-toggle")).toBeInTheDocument();
    expect(screen.queryByTestId("platform-readonly-note")).not.toBeInTheDocument();
  });

  it("viewer: read face visible, converge and toggle hidden (hint rendered)", async () => {
    renderCardInRole({ is_platform_admin: false, role: "viewer" });

    // 读面全角色可见。
    const badge = await screen.findByTestId("drift-status-badge");
    await waitFor(() => expect(badge.textContent).toContain("in sync"));
    expect(screen.queryByTestId("drift-converge-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("drift-convergence-toggle")).not.toBeInTheDocument();
    expect(screen.getByText("Converge requires the deploy face (developer role or higher).")).toBeInTheDocument();
  });

  it("platform administrator: read face visible, both write actions replaced by the readonly note", async () => {
    renderCardInRole({ is_platform_admin: true, role: "owner" });

    const badge = await screen.findByTestId("drift-status-badge");
    await waitFor(() => expect(badge.textContent).toContain("in sync"));
    expect(screen.queryByTestId("drift-converge-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("drift-convergence-toggle")).not.toBeInTheDocument();
    const note = screen.getByTestId("platform-readonly-note");
    expect(note.textContent).toContain("read-only access to resources");
    expect(note.textContent).toContain("fleetly drift enable|disable");
  });
});
