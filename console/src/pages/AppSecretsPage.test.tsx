// app Secrets 页测试（E4 W4-S6）：无值读回面渲染（name/hash8/updated）、
// set 提交（POST /secrets 载荷）、remove 两步确认（对话框 → DELETE）、
// 空态指引。AppEnvPage.test 同款 mock 形态。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AppSecretsPage } from "@/pages/AppSecretsPage";
import { TeamProjectProvider } from "@/lib/context";
import { setToken } from "@/api/client";

const SECRETS = {
  secrets: [
    { name: "API_TOKEN", hash8: "1a2b3c4d", updated_at: new Date().toISOString() },
    { name: "smtp.password", hash8: "99887766", updated_at: new Date().toISOString() },
  ],
};

function stubSecretsFetch() {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.includes("/secrets") && method === "POST") {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve({ app: "web", name: "NEW_KEY", hash8: "deadbeef" }),
      });
    }
    if (url.includes("/secrets/API_TOKEN") && method === "DELETE") {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve({ app: "web", name: "API_TOKEN" }),
      });
    }
    if (url.includes("/secrets")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve(SECRETS),
      });
    }
    return Promise.resolve({
      ok: true, status: 200, statusText: "",
      json: () => Promise.resolve({}),
    });
  });
}

function renderAt() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={["/apps/web/secrets"]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/apps/:name/secrets" element={<AppSecretsPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AppSecretsPage", () => {
  it("lists secrets with fingerprints (no value readback) and an empty-state hint", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubSecretsFetch());

    renderAt();
    await screen.findByTestId("secrets-page");

    const rows = await screen.findAllByTestId("secret-row");
    expect(rows).toHaveLength(2);
    expect(screen.getByText("API_TOKEN")).toBeInTheDocument();
    expect(screen.getByText("1a2b3c4d")).toBeInTheDocument();
    // 无值读回面：值列不存在（只有指纹）。
    expect(screen.queryByTestId("secret-value-column")).not.toBeInTheDocument();
  });

  it("sets a secret via the form (POST with app/name/value)", async () => {
    setToken("flt_test");
    const fetchMock = stubSecretsFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("secrets-page");

    await user.type(screen.getByTestId("secret-name-input"), "NEW_KEY");
    await user.type(screen.getByTestId("secret-value-input"), "s3cr3t-value");
    await user.click(screen.getByTestId("secret-set-submit"));

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) => String(c[0]).includes("/secrets") && c[1]?.method === "POST",
      );
      expect(posted).toBeTruthy();
      expect(JSON.parse(String(posted![1]?.body))).toEqual({
        app: "web",
        name: "NEW_KEY",
        value: "s3cr3t-value",
      });
    });
  });

  it("removes a secret only through the confirm dialog (DELETE)", async () => {
    setToken("flt_test");
    const fetchMock = stubSecretsFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("secrets-page");
    await screen.findAllByTestId("secret-row");

    await user.click(screen.getAllByTestId("secret-remove-button")[0]);
    expect(screen.getByTestId("secret-remove-dialog")).toHaveTextContent(/never readable again/i);
    await user.click(screen.getByTestId("secret-remove-submit"));

    await waitFor(() => {
      const deleted = fetchMock.mock.calls.find(
        (c) => String(c[0]).includes("/secrets/API_TOKEN") && c[1]?.method === "DELETE",
      );
      expect(deleted).toBeTruthy();
    });
  });

  it("renders the empty-state hint when no secrets are stored", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() =>
        Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () => Promise.resolve({ secrets: [] }),
        }),
      ),
    );

    renderAt();
    await screen.findByTestId("secrets-empty");
  });

  it("viewer（写面门 + 文案，2026-09-25 走查）：空态不指向不存在的 Set 表单", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubSecretsFetchRole({ role: "viewer", secrets: [] }),
    );

    renderAtInRole();
    const empty = await screen.findByTestId("secrets-empty");
    // Me 投影解析前能力为 fail-open（表单可见）——等角色门把空态文案切到
    // 只读变体后再断言。
    await waitFor(() =>
      expect(empty.textContent).toContain("Declare it under the compose file's top-level"),
    );
    // 表单不在渲染时，空态文案不再指路 "Set one above"。
    expect(empty.textContent).not.toContain("Set one above");
    // 写表单与行删除钮不渲染（此前只门平台管理员——viewer 见假按钮）。
    await waitFor(() =>
      expect(screen.queryByTestId("secret-name-input")).not.toBeInTheDocument(),
    );
    expect(screen.queryByTestId("secret-remove-button")).not.toBeInTheDocument();
    // 平台管理员说明态不渲染（成员是普通 viewer）。
    expect(screen.queryByTestId("platform-readonly-note")).not.toBeInTheDocument();
  });
});

/** 带角色视角的 fetch 桩（/auth/me + secrets 投影）。 */
function stubSecretsFetchRole(opts: { role: string; secrets: unknown[] }) {
  return vi.fn().mockImplementation((input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "f@t.test", is_platform_admin: false },
            teams: [
              { team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: opts.role },
            ],
            project_overrides: [],
          }),
      });
    }
    if (url.includes("/secrets")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve({ secrets: opts.secrets }),
      });
    }
    return Promise.resolve({
      ok: true, status: 200, statusText: "",
      json: () => Promise.resolve({}),
    });
  });
}

function renderAtInRole() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={["/apps/web/secrets"]}>
      <QueryClientProvider client={client}>
        <TeamProjectProvider>
          <Routes>
            <Route path="/apps/:name/secrets" element={<AppSecretsPage />} />
          </Routes>
        </TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}
