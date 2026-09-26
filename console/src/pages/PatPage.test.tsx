// PAT 自服务页测试（v0.3 W2-S2，设计 §7 验收面）：列表渲染（name/scopes/
// 绑定项目/last_used）、创建全链（POST /tokens 载荷含 scopes + project_id
// + 明文一次性展示 + 复制按钮）、吊销两步（对话框确认 → DELETE）、越权
// scope 声明的 400 信封如实展示。AppSecretsPage.test 同款 mock 形态。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PatPage } from "@/pages/PatPage";
import { setToken } from "@/api/client";

// W2-1：令牌带属主（user_id）——本页只渲染 Me 自己的 PAT；他人 PAT 由
// Admin 平台台账承载（见 PlatformTokensCard 测试）。
const ME = {
  user: {
    id: "01USERME",
    email: "owner@fleetly.run",
    display_name: "Owner",
    is_platform_admin: false,
  },
};

const TOKENS = {
  tokens: [
    {
      id: "01TOK1",
      note: "laptop",
      scopes: ["read", "deploy"],
      project_id: "01PRJ1",
      hash_prefix: "1a2b3c4d5e6f",
      created_at: "2026-09-20T10:00:00Z",
      last_used_at: "2026-09-21T09:00:00Z",
      user_id: "01USERME",
    },
    {
      id: "01TOK2",
      note: "ci",
      scopes: ["read"],
      hash_prefix: "998877665544",
      created_at: "2026-09-19T08:00:00Z",
      user_id: "01USERME",
    },
    {
      id: "01TOKOTHER",
      note: "not-mine",
      scopes: ["read"],
      hash_prefix: "aabbccddeeff",
      created_at: "2026-09-18T08:00:00Z",
      user_id: "01USEROTHER",
    },
  ],
};

const PROJECTS = {
  projects: [
    { id: "01PRJ1", team_id: "01TEAM", team_slug: "acme", slug: "web", name: "Web" },
    { id: "01PRJ2", team_id: "01TEAM", team_slug: "acme", slug: "api", name: "API" },
  ],
};

function stubFetch(overrides: {
  onCreate?: (body: Record<string, unknown>) => { status: number; body: unknown };
} = {}) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/tokens") && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      const result = overrides.onCreate?.(body) ?? {
        status: 200,
        body: { id: "01TOKNEW", token: "flt_newplaintext48hex", note: body.note, scopes: body.scopes },
      };
      return Promise.resolve({
        ok: result.status === 200,
        status: result.status,
        statusText: "",
        json: () => Promise.resolve(result.body),
      });
    }
    if (url.includes("/tokens/") && method === "DELETE") {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve({ id: "01TOK2" }),
      });
    }
    if (url.endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve(ME),
      });
    }
    if (url.endsWith("/tokens")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve(TOKENS),
      });
    }
    if (url.endsWith("/projects")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve(PROJECTS),
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
    <MemoryRouter initialEntries={["/pat"]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/pat" element={<PatPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("PatPage", () => {
  it("lists tokens with scopes, project binding and usage (no plaintext)", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubFetch());

    renderAt();
    await screen.findByTestId("pat-page");

    const rows = await screen.findAllByTestId("pat-row");
    expect(rows).toHaveLength(2); // W2-1：只渲染 Me 自己的 PAT
    expect(screen.getByText("laptop")).toBeInTheDocument();
    expect(screen.getByText("01PRJ1")).toBeInTheDocument(); // 绑定项目回显
    expect(screen.getAllByText("read").length).toBeGreaterThan(0);
    expect(screen.queryByText("not-mine")).not.toBeInTheDocument(); // 他人令牌不进本页
    expect(screen.queryByTestId("pat-token-value")).not.toBeInTheDocument(); // 明文永不回读
  });

  it("creates a token and shows the plaintext exactly once with a copy action", async () => {
    setToken("flt_test");
    const fetchMock = stubFetch();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("pat-page");

    await user.type(screen.getByTestId("pat-name-input"), "cli laptop");
    await user.click(screen.getByTestId("pat-scope-deploy"));
    // 项目绑定下拉（原生 select）。
    await user.selectOptions(screen.getByTestId("pat-project-select"), "01PRJ2");
    await user.click(screen.getByTestId("pat-create-submit"));

    await waitFor(() => {
      expect(screen.getByTestId("pat-token-value")).toHaveTextContent("flt_newplaintext48hex");
    });
    // POST 载荷：scopes 勾选集 + 项目绑定。
    const posted = fetchMock.mock.calls.find(
      (c) => String(c[0]).endsWith("/tokens") && c[1]?.method === "POST",
    );
    const postedBody = JSON.parse(String(posted?.[1]?.body)) as Record<string, unknown>;
    expect(postedBody.scopes).toEqual(["read", "deploy"]);
    expect(postedBody.note).toBe("cli laptop");
    expect(postedBody.project_id).toBe("01PRJ2");

    // 复制按钮 + Done 关闭即弃。
    expect(screen.getByTestId("pat-token-copy")).toBeInTheDocument();
    await user.click(screen.getByTestId("pat-token-dismiss"));
    await waitFor(() => {
      expect(screen.queryByTestId("pat-token-value")).not.toBeInTheDocument();
    });
  });

  it("surfaces a scope-overshoot 400 envelope verbatim (server guardrail)", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubFetch({
        onCreate: () => ({
          status: 400,
          body: {
            code: "",
            message: 'scope "admin" exceeds your granted capabilities (reachable scopes: read)',
          },
        }),
      }),
    );
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("pat-page");

    await user.type(screen.getByTestId("pat-name-input"), "greedy");
    await user.click(screen.getByTestId("pat-scope-admin"));
    await user.click(screen.getByTestId("pat-create-submit"));

    const alert = await screen.findByTestId("pat-error");
    expect(alert).toHaveTextContent("exceeds your granted capabilities");
  });

  it("revokes via the confirm dialog (DELETE issued, row disappears)", async () => {
    setToken("flt_test");
    let revoked = false;
    const fetchMock = stubFetch();
    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/auth/me")) {
        return Promise.resolve({
          ok: true, status: 200, statusText: "",
          json: () => Promise.resolve(ME),
        });
      }
      if (url.includes("/tokens/") && method === "DELETE") {
        revoked = true;
        return Promise.resolve({
          ok: true, status: 200, statusText: "",
          json: () => Promise.resolve({ id: "01TOK2" }),
        });
      }
      if (url.endsWith("/tokens")) {
        return Promise.resolve({
          ok: true, status: 200, statusText: "",
          json: () =>
            Promise.resolve(
              revoked
                ? { tokens: TOKENS.tokens.filter((t) => t.id !== "01TOK2") }
                : TOKENS,
            ),
        });
      }
      if (url.endsWith("/projects")) {
        return Promise.resolve({
          ok: true, status: 200, statusText: "",
          json: () => Promise.resolve(PROJECTS),
        });
      }
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve({}),
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderAt();
    await screen.findByTestId("pat-page");

    // ci 行（第二行）的吊销按钮。
    const rows = await screen.findAllByTestId("pat-row");
    const ciRow = rows.find((r) => r.getAttribute("data-name") === "ci");
    expect(ciRow).toBeDefined();
    await user.click(within(ciRow!).getByTestId("pat-revoke"));
    await screen.findByTestId("pat-revoke-dialog");
    await user.click(screen.getByTestId("pat-revoke-submit"));

    await waitFor(() => {
      expect(screen.queryByTestId("pat-revoke-dialog")).not.toBeInTheDocument();
    });
    await waitFor(() => {
      expect(screen.queryByText("ci")).not.toBeInTheDocument();
    });
  });
});
