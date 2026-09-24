// 平台管理员页测试（v0.3 W2-S5，设计 §7 验收面）：用户列表（平台管理员/
// 禁用徽章）、创建用户（临时口令一次性展示 + POST /users 载荷）、禁用
// （确认对话框 → POST disable）、注册窗口开关（PUT /auth/registration 载
// 荷）。非平台管理员渲染 denied 分支。PatPage.test 同款 mock 形态。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AdminPage } from "@/pages/AdminPage";
import { setToken } from "@/api/client";

const USERS = {
  users: [
    {
      id: "01U1",
      email: "root@t.test",
      display_name: "Root",
      is_platform_admin: true,
      created_at: "2026-09-20T10:00:00Z",
    },
    {
      id: "01U2",
      email: "mate@t.test",
      display_name: "Mate",
      created_at: "2026-09-21T10:00:00Z",
      disabled_at: "2026-09-22T10:00:00Z",
    },
  ],
};

function stubFetch(overrides: {
  onCreateUser?: (body: Record<string, unknown>) => { status: number; body: unknown };
} = {}) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/users") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      const result =
        overrides.onCreateUser?.(body) ?? {
          status: 200,
          body: {
            user: { id: "01U3", email: body.email, display_name: body.display_name },
            temporary_password: "temp-pass-abcdef123456",
          },
        };
      return Promise.resolve({
        ok: result.status === 200,
        status: result.status,
        statusText: "",
        json: () => Promise.resolve(result.body),
      });
    }
    if (url.endsWith("/disable") && method === "POST") {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ user: { ...USERS.users[1], disabled_at: "2026-09-23T00:00:00Z" } }),
      });
    }
    if (url.endsWith("/auth/registration") && method === "PUT") {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ open: body.open }),
      });
    }
    if (url.endsWith("/auth/registration")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ open: true, has_users: true }),
      });
    }
    if (url.endsWith("/users")) {
      return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve(USERS) });
    }
    if (url.endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "root@t.test", is_platform_admin: true },
            teams: [],
          }),
      });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
}

function renderAt(path = "/admin/users") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={[path]}>
      <QueryClientProvider client={client}>
        <AdminPage />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AdminPage (platform admin)", () => {
  it("lists users with platform-admin and disabled badges", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch());

    renderAt();
    await screen.findByTestId("admin-page");

    const rows = await screen.findAllByTestId("admin-user-row");
    expect(rows).toHaveLength(2);
    expect(screen.getByText("root@t.test")).toBeInTheDocument();
    expect(screen.getByTestId("admin-user-platform-badge")).toBeInTheDocument();
    expect(screen.getByTestId("admin-user-disabled-badge")).toBeInTheDocument();
    // 禁用行给 Enable，未禁用行给 Disable。
    expect(screen.getByTestId("admin-user-enable")).toBeInTheDocument();
    expect(screen.getByTestId("admin-user-disable")).toBeInTheDocument();
  });

  it("creates a user and shows the temporary password exactly once", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("admin-page");

    await user.type(screen.getByTestId("admin-user-email-input"), "new@t.test");
    await user.type(screen.getByTestId("admin-user-name-input"), "New");
    await user.click(screen.getByTestId("admin-user-create-submit"));

    const secret = await screen.findByTestId("admin-secret-value");
    expect(secret).toHaveTextContent("temp-pass-abcdef123456");
    expect(screen.getByTestId("admin-secret-copy")).toBeInTheDocument();
    await user.click(screen.getByTestId("admin-secret-dismiss"));
    await waitFor(() => {
      expect(screen.queryByTestId("admin-secret-value")).not.toBeInTheDocument();
    });

    const posted = fetchMock.mock.calls.find(
      (c) => String(c[0]).endsWith("/users") && c[1]?.method === "POST",
    );
    const body = JSON.parse(String(posted?.[1]?.body)) as Record<string, unknown>;
    expect(body.email).toBe("new@t.test");
    expect(body.display_name).toBe("New");
  });

  it("disables a user via the confirm dialog (POST disable issued)", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("admin-page");

    // 等用户行落定（/users 查询异步于页面骨架）。
    await screen.findByTestId("admin-user-disable");
    await user.click(screen.getByTestId("admin-user-disable"));
    await screen.findByTestId("admin-confirm-dialog");
    await user.click(screen.getByTestId("admin-confirm-submit"));

    // 夹具里唯一在册用户是 01U1（01U2 已禁用渲染 Enable）——断言其 disable。
    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) => String(c[0]).endsWith("/01U1/disable") && c[1]?.method === "POST",
      );
      expect(posted).toBeDefined();
    });
  });

  it("toggles the registration window (PUT /auth/registration payload)", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("admin-page");

    const toggle = await screen.findByTestId("admin-registration-toggle");
    expect(toggle).toBeChecked();
    await user.click(toggle);

    await waitFor(() => {
      const put = fetchMock.mock.calls.find(
        (c) => String(c[0]).endsWith("/auth/registration") && c[1]?.method === "PUT",
      );
      expect(put).toBeDefined();
      const body = JSON.parse(String(put?.[1]?.body)) as Record<string, unknown>;
      expect(body.open).toBe(false);
    });
  });
});
