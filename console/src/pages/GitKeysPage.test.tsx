// Git push keys 页测试（P1-8「Git push 通道不可发现」）：通道说明卡（push
// 形态 + System 页指纹卡链出）/台账渲染（名称 note/指纹/类型/添加时间——
// GitKeyView 契约字段）/空态/添加载荷（POST /v1/git/keys，authorized_keys
// 单行 + 备注）/服务端 400 信封如实透出/删除两步确认（确认才发 DELETE，
// 取消不发）。格式校验在服务端（authorized_keys 解析）——前端只做非空。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { GitKeysPage } from "@/pages/GitKeysPage";
import { setToken } from "@/api/client";

const KEY_LINE = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB0Z9cQK operator@laptop";

function ok(body: unknown) {
  return {
    ok: true,
    status: 200,
    statusText: "",
    json: () => Promise.resolve(body),
  };
}

function keyFixture(overrides: Record<string, unknown> = {}) {
  return {
    id: "01KEY01",
    fingerprint: "SHA256:AbCdEf123456",
    key_type: "ssh-ed25519",
    note: "operator laptop",
    created_at: "2026-09-20T10:00:00Z",
    user_id: "01USER1",
    ...overrides,
  };
}

/** 台账 + 添加 + 删除的全路由 stub；记录调用供载荷断言。 */
function stubGitKeys(initialKeys: unknown[]) {
  const calls: Array<{ url: string; method?: string; body?: unknown }> = [];
  const fetchMock = vi.fn().mockImplementation((url: string, init?: { method?: string; body?: string }) => {
    const u = String(url);
    calls.push({ url: u, method: init?.method, body: init?.body ? JSON.parse(init.body) : undefined });
    if (init?.method === "POST" && u.endsWith("/git/keys")) {
      return Promise.resolve(
        ok({ id: "01KEY02", fingerprint: "SHA256:NewKeyFp", key_type: "ssh-ed25519", note: "ci box", created_at: "2026-09-25T09:00:00Z" }),
      );
    }
    if (init?.method === "DELETE") {
      return Promise.resolve(ok({ id: u.slice(u.lastIndexOf("/") + 1) }));
    }
    return Promise.resolve(ok({ keys: initialKeys }));
  });
  return { fetchMock, calls };
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={["/git-keys"]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/git-keys" element={<GitKeysPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("GitKeysPage ledger (P1-8)", () => {
  it("renders the channel explainer, key rows and remove affordance", async () => {
    setToken("flt_test");
    const { fetchMock } = stubGitKeys([keyFixture()]);
    vi.stubGlobal("fetch", fetchMock);
    renderPage();

    // 说明卡：push 形态（git.go 同源事实）+ System 页指纹链出。
    expect(screen.getByTestId("git-keys-page")).toBeInTheDocument();
    expect(screen.getByTestId("git-keys-push-shape")).toHaveTextContent(
      "git push ssh://git@<host>:8424/<app>.git <branch>",
    );
    expect(screen.getByText("System page")).toHaveAttribute("href", "/system");

    // 台账行：note/指纹/类型/添加时间（契约无名称外的敏感列——user_id 为
    // 属主注记，自服务列表恒自己的 key，不做列）。
    await waitFor(() => expect(screen.getByTestId("git-key-row")).toBeInTheDocument());
    expect(screen.getByTestId("git-key-row")).toHaveTextContent("operator laptop");
    expect(screen.getByTestId("git-key-fingerprint")).toHaveTextContent("SHA256:AbCdEf123456");
    expect(screen.getByTestId("git-key-row")).toHaveTextContent("ssh-ed25519");
    expect(screen.getByTestId("git-key-remove")).toBeInTheDocument();
  });

  it("renders the empty state", async () => {
    setToken("flt_test");
    const { fetchMock } = stubGitKeys([]);
    vi.stubGlobal("fetch", fetchMock);
    renderPage();

    await waitFor(() =>
      expect(screen.getByText("No git keys yet — add one below, then push to deploy.")).toBeInTheDocument(),
    );
    expect(screen.queryByTestId("git-key-row")).not.toBeInTheDocument();
  });
});

describe("GitKeysPage add (P1-8)", () => {
  it("posts the authorized_keys line + note and surfaces the new fingerprint", async () => {
    setToken("flt_test");
    const { fetchMock, calls } = stubGitKeys([]);
    vi.stubGlobal("fetch", fetchMock);
    renderPage();
    const user = userEvent.setup();

    await user.type(screen.getByTestId("git-key-public-input"), `  ${KEY_LINE}  `);
    await user.type(screen.getByTestId("git-key-note-input"), "ci box");
    await user.click(screen.getByTestId("git-key-add-submit"));

    // 载荷：粘贴值去空白后整体上行（格式校验在服务端）；空备注不造键。
    const post = calls.find((c) => c.method === "POST");
    expect(post).toBeTruthy();
    expect(post?.url.endsWith("/v1/git/keys")).toBe(true);
    expect(post?.body).toEqual({ public_key: KEY_LINE, note: "ci box" });

    await waitFor(() => expect(screen.getByTestId("git-key-added")).toBeInTheDocument());
    expect(screen.getByTestId("git-key-added")).toHaveTextContent("SHA256:NewKeyFp");
    // 成功后表单复位。
    expect(screen.getByTestId("git-key-public-input")).toHaveValue("");
  });

  it("shows the server error envelope when the key is rejected (400 shape)", async () => {
    setToken("flt_test");
    const fetchMock = vi.fn().mockImplementation((url: string, init?: { method?: string }) => {
      const u = String(url);
      if (init?.method === "POST" && u.endsWith("/git/keys")) {
        return Promise.resolve({
          ok: false,
          status: 400,
          statusText: "",
          json: () =>
            Promise.resolve({
              code: "E_INVALID",
              message: "parse public key: not an authorized_keys line",
            }),
        });
      }
      return Promise.resolve(ok({ keys: [] }));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPage();
    const user = userEvent.setup();

    await user.type(screen.getByTestId("git-key-public-input"), "not a key");
    await user.click(screen.getByTestId("git-key-add-submit"));

    await waitFor(() => expect(screen.getByTestId("git-key-error")).toBeInTheDocument());
    expect(screen.getByTestId("git-key-error")).toHaveTextContent("E_INVALID");
    expect(screen.getByTestId("git-key-error")).toHaveTextContent(
      "parse public key: not an authorized_keys line",
    );
  });
});

describe("GitKeysPage remove (P1-8)", () => {
  it("removes only after the two-step confirmation and calls DELETE with the key id", async () => {
    setToken("flt_test");
    const { fetchMock, calls } = stubGitKeys([keyFixture()]);
    vi.stubGlobal("fetch", fetchMock);
    renderPage();
    await waitFor(() => expect(screen.getByTestId("git-key-row")).toBeInTheDocument());
    const user = userEvent.setup();

    await user.click(screen.getByTestId("git-key-remove"));
    expect(screen.getByTestId("git-key-remove-dialog")).toBeInTheDocument();

    // 取消：不发请求。
    await user.click(screen.getByTestId("git-key-remove-cancel"));
    expect(screen.queryByTestId("git-key-remove-dialog")).not.toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);

    // 确认：DELETE /v1/git/keys/<id>。
    await user.click(screen.getByTestId("git-key-remove"));
    await user.click(screen.getByTestId("git-key-remove-submit"));
    await waitFor(() => {
      const del = calls.find((c) => c.method === "DELETE");
      expect(del?.url.endsWith("/v1/git/keys/01KEY01")).toBe(true);
    });
  });
});
