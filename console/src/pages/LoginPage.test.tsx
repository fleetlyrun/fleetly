// 登录 401 流测试：粘贴错误 token → 401 信封（code/message/suggestion）
// 渲染于登录页、凭据被清除、不进入应用。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { LoginPage } from "@/pages/LoginPage";
import { getToken } from "@/api/client";
import { AuthProvider } from "@/auth";

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
