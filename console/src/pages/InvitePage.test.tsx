// 邀请链接页测试（/auth/invite?token=… 两分支）：
// - 未登录：提示先登录/注册（注册窗口开放时出注册链接），不发起 accept；
// - 已登录：进入即自动 accept（一次性消费——仅一次 POST），成功展示入队
//   结果；无效邀请的 409 信封如实展示（code/message/suggestion 全字段）；
// - 缺 token 参数：缺票提示，不发起 accept。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { InvitePage } from "@/pages/InvitePage";
import { AuthProvider } from "@/auth";

function jsonResponse(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 401 ? "Unauthorized" : "",
    json: () => Promise.resolve(body),
  };
}

const ME_AUTHED = {
  user: { id: "u1", email: "op@example.com", display_name: "Operator", is_platform_admin: false },
  teams: [],
};

function stubFetch(routes: {
  me?: () => unknown;
  registration?: () => unknown;
  accept?: () => unknown;
}) {
  return vi.fn((url: unknown) => {
    const u = String(url);
    if (u.includes("/auth/me")) return Promise.resolve(routes.me ? routes.me() : jsonResponse(401, {}));
    if (u.includes("/auth/registration")) {
      return Promise.resolve(
        routes.registration ? routes.registration() : jsonResponse(200, { open: false, has_users: true }),
      );
    }
    if (u.includes("/auth/invite:accept")) {
      return Promise.resolve(routes.accept ? routes.accept() : jsonResponse(404, {}));
    }
    return Promise.resolve(jsonResponse(404, { message: `unmocked ${u}` }));
  });
}

function renderInvite(query: string) {
  return render(
    <MemoryRouter initialEntries={[`/auth/invite${query}`]}>
      <AuthProvider>
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <Routes>
            <Route path="/auth/invite" element={<InvitePage />} />
            <Route path="/login" element={<p>login-page</p>} />
            <Route path="/apps" element={<p>apps-page</p>} />
          </Routes>
        </QueryClientProvider>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("InvitePage anon branch", () => {
  it("prompts sign-in first and does not consume the invite", async () => {
    const fetchMock = stubFetch({
      me: () => jsonResponse(401, {}),
      registration: () => jsonResponse(200, { open: true, has_users: true }),
    });
    vi.stubGlobal("fetch", fetchMock);

    renderInvite("?token=invitetoken123");
    expect(await screen.findByTestId("invite-anon")).toBeInTheDocument();
    // 未登录不发起一次性消费。
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/auth/invite:accept"))).toBe(false);
    // 注册窗口开放 → 出注册入口；登录链接携带回跳（from 暂存在路由 state）。
    expect(screen.getByTestId("invite-signin")).toBeInTheDocument();
    expect(screen.getByTestId("invite-register")).toBeInTheDocument();
  });

  it("navigates to the sign-in page via the accept link", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch({ me: () => jsonResponse(401, {}) }),
    );
    renderInvite("?token=invitetoken123");
    const user = userEvent.setup();
    await user.click(await screen.findByTestId("invite-signin"));
    expect(await screen.findByText("login-page")).toBeInTheDocument();
  });

  it("hides the register hint when registration is closed", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch({
        me: () => jsonResponse(401, {}),
        registration: () => jsonResponse(200, { open: false, has_users: true }),
      }),
    );
    renderInvite("?token=invitetoken123");
    expect(await screen.findByTestId("invite-anon")).toBeInTheDocument();
    expect(screen.queryByTestId("invite-register")).not.toBeInTheDocument();
  });
});

describe("InvitePage authed branch", () => {
  it("accepts the invite exactly once and shows the joined team and role", async () => {
    let acceptCalls = 0;
    vi.stubGlobal(
      "fetch",
      stubFetch({
        me: () => jsonResponse(200, ME_AUTHED),
        accept: () => {
          acceptCalls += 1;
          return jsonResponse(200, {
            team_id: "t1",
            team_slug: "acme",
            team_name: "Acme",
            role: "developer",
          });
        },
      }),
    );

    renderInvite("?token=invitetoken123");

    await waitFor(() => expect(acceptCalls).toBe(1));
    const ok = await screen.findByTestId("invite-ok");
    expect(ok).toHaveTextContent("Acme");
    expect(ok).toHaveTextContent("developer");
    // StrictMode/依赖抖动不得二次消费一次性 token。
    await new Promise((r) => setTimeout(r, 20));
    expect(acceptCalls).toBe(1);
  });

  it("renders the 409 envelope verbatim for an invalid invite", async () => {
    vi.stubGlobal(
      "fetch",
      stubFetch({
        me: () => jsonResponse(200, ME_AUTHED),
        accept: () =>
          jsonResponse(409, {
            code: "E_INVITE_INVALID",
            message: "invite is invalid, expired, or already consumed",
            suggestion: "ask your team owner for a fresh invite link",
          }),
      }),
    );

    renderInvite("?token=bad-or-used");

    const err = await screen.findByTestId("invite-error");
    const envelope = err.querySelector('[data-testid="error-envelope"]');
    expect(envelope).toHaveTextContent("E_INVITE_INVALID");
    expect(envelope).toHaveTextContent("invite is invalid, expired, or already consumed");
    expect(envelope).toHaveTextContent("ask your team owner for a fresh invite link");
  });
});

describe("InvitePage without token", () => {
  it("shows the missing-token notice and never POSTs", async () => {
    const fetchMock = stubFetch({ me: () => jsonResponse(401, {}) });
    vi.stubGlobal("fetch", fetchMock);

    renderInvite("");
    expect(await screen.findByTestId("invite-missing-token")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes("/auth/invite:accept"))).toBe(false);
  });
});
