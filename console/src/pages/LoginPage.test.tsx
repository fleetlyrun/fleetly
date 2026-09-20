// 登录 401 流测试：粘贴错误 token → 401 信封（code/message/suggestion）
// 渲染于登录页、凭据被清除、不进入应用。组合形态（M9-1）：经真实 App/
// Gate 渲染——authed 翻转即卸载 LoginPage，修复前失败信封不可达；修复
// 后校验失败仍停登录页、localStorage 未持久化、信封可见。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { LoginPage } from "@/pages/LoginPage";
import { getToken } from "@/api/client";
import { AuthProvider } from "@/auth";
import { App } from "@/App";

function renderLogin() {
  return render(
    <MemoryRouter initialEntries={["/login"]}>
      <AuthProvider>
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <LoginPage />
        </QueryClientProvider>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("LoginPage 401 flow", () => {
  it("renders the error envelope (message + suggestion) and keeps the user on login", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      statusText: "Unauthorized",
      json: () =>
        Promise.resolve({
          message: "invalid or revoked token",
          suggestion: "create a token with `fleetly tokens create` and retry",
        }),
    });
    vi.stubGlobal("fetch", fetchMock);

    renderLogin();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("API token"), "flt_wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    await waitFor(() => {
      const alert = screen.getByRole("alert");
      expect(alert).toHaveTextContent("Sign-in failed: invalid or revoked token");
      expect(alert).toHaveTextContent(
        "create a token with `fleetly tokens create` and retry",
      );
    });
    // 401 → 凭据被清（不残留无效 token）。
    expect(getToken()).toBe("");
  });

  it("signs in with a valid token and navigates to /apps", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      statusText: "",
      json: () => Promise.resolve({ apps: [] }),
    });
    vi.stubGlobal("fetch", fetchMock);

    render(
      <MemoryRouter initialEntries={["/apps/deep-link-target"]}>
        <AuthProvider>
          <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
            <LoginPage />
          </QueryClientProvider>
        </AuthProvider>
      </MemoryRouter>,
    );
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("API token"), "flt_good");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    await waitFor(() => expect(getToken()).toBe("flt_good"));
  });
});

describe("LoginPage through Gate (M9-1 validate-before-persist)", () => {
  it("stays on login with the error envelope and no persisted token when validation fails", async () => {
    // 校验请求挂起可控：先断言 loading 态，再放行 401。
    let respond: (v: unknown) => void = () => undefined;
    const fetchMock = vi
      .fn()
      .mockReturnValue(new Promise((res) => (respond = res)));
    vi.stubGlobal("fetch", fetchMock);
    window.history.replaceState({}, "", "/ui/login");

    render(<App />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("API token"), "flt_wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    // 校验进行中：提交按钮 loading/禁用、页面仍在（Gate 未卸载 LoginPage）、
    // 校验请求已带待验证 token 的 Bearer。
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled();
    expect(screen.getByLabelText("API token")).toBeInTheDocument();
    expect(
      (fetchMock.mock.calls[0]?.[1] as { headers?: Record<string, string> })
        ?.headers?.Authorization,
    ).toBe("Bearer flt_wrong");

    respond({
      ok: false,
      status: 401,
      statusText: "Unauthorized",
      json: () =>
        Promise.resolve({
          code: "E_UNAUTHENTICATED",
          message: "invalid or revoked token",
          suggestion: "create a token with `fleetly tokens create` and retry",
        }),
    });

    await waitFor(() => {
      const alert = screen.getByRole("alert");
      expect(alert).toHaveTextContent(
        "Sign-in failed: invalid or revoked token",
      );
      expect(alert).toHaveTextContent(
        "create a token with `fleetly tokens create` and retry",
      );
    });
    // 失败终态：无凭据残留、仍停登录页、Gate 级 401 横幅被本页信封取代。
    expect(getToken()).toBe("");
    expect(screen.getByLabelText("API token")).toBeInTheDocument();
    expect(screen.queryByText(/Session invalid/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled();
  });
});
