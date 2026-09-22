// app Secrets 页测试（E4 W4-S6）：无值读回面渲染（name/hash8/updated）、
// set 提交（POST /secrets 载荷）、remove 两步确认（对话框 → DELETE）、
// 空态指引。AppEnvPage.test 同款 mock 形态。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AppSecretsPage } from "@/pages/AppSecretsPage";
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
});
