// 库实例详情测试（E4 W4-S6）：连接卡脱敏默认 + reveal 显式展开（admin 面
// 敏感性标注）、状态相宜的操作面（paused → resume 可用 suspend 不可用）、
// rotate 破坏性两段式确认（名字回填 + 引用 app 自动重部署的诚实文案）、
// 备份卡 rustfs 诚实口径 + verify 徽章 + restore 两段式。fetch 按 URL 分路
// mock（SystemPage.test 同款）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DatabaseDetailPage } from "@/pages/DatabaseDetailPage";
import { setToken } from "@/api/client";

const DB = {
  id: "db1",
  name: "pg-prod",
  template: "postgres-16",
  status: "paused",
  image_digest: "postgres:16@sha256:a3b7",
  placement: "node-a",
  volume: { name: "fleetly-db-pg-prod-data-ab12", status: "active", platform_node_id: "node-a" },
  connection: {
    host: "pg-prod",
    port: 5432,
    user: "fleetly",
    database: "pg_prod",
    url: "postgres://fleetly:********@pg-prod:5432/pg_prod",
    password_fingerprint: "1a2b3c4d",
  },
  limits: { cpu_seconds: 1, memory_bytes: "1073741824" },
  backup_plan: { interval_hours: 24, keep: 7, hour_utc: 3 },
  upgrade_available: true,
  last_error: "",
};

const BACKUPS = {
  backups: [
    {
      id: "bk1",
      kind: "manual",
      snapshot: "db/pg-prod/0f1e2d3c",
      size_bytes: "1048576",
      verify_status: "verified",
      created_at: new Date(Date.now() - 60_000).toISOString(),
    },
    {
      id: "bk2",
      kind: "daily",
      snapshot: "db/pg-prod/aabbccdd",
      verify_status: "failed",
      error: "read-back checksum mismatch",
      created_at: new Date(Date.now() - 3600_000).toISOString(),
    },
  ],
};

function stubDetailFetch(opts: { s3Mode?: string; status?: string } = {}) {
  const db = { ...DB, status: opts.status ?? "ready" };
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.endsWith("/databases/pg-prod") && method === "GET") {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve({ database: db }),
      });
    }
    if (url.includes("/credentials")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () =>
          Promise.resolve({
            name: "pg-prod",
            host: "pg-prod",
            port: 5432,
            user: "fleetly",
            database: "pg_prod",
            password: "hunter2Plaintext",
            url: "postgres://fleetly:hunter2Plaintext@pg-prod:5432/pg_prod",
          }),
      });
    }
    if (url.includes("/backups") && method === "POST") {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve({ name: "pg-prod", kind: "manual", status: "accepted" }),
      });
    }
    if (url.includes("/backups")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve(BACKUPS),
      });
    }
    if (url.includes("/rotate") && method === "POST") {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () =>
          Promise.resolve({ database: DB, redeployed_apps: ["api", "worker"] }),
      });
    }
    if (url.includes("/restore") && method === "POST") {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () =>
          Promise.resolve({ name: "pg-prod", snapshot: "db/pg-prod/0f1e2d3c", status: "accepted" }),
      });
    }
    if (url.includes("/system/s3")) {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve({ settings: { mode: opts.s3Mode ?? "unset" } }),
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
    <MemoryRouter initialEntries={["/databases/pg-prod"]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route path="/databases/:name" element={<DatabaseDetailPage />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

async function renderReady(opts: { s3Mode?: string; status?: string } = {}) {
  setToken("flt_test");
  const fetchMock = stubDetailFetch({ s3Mode: "rustfs", ...opts });
  vi.stubGlobal("fetch", fetchMock);
  renderAt();
  await screen.findByTestId("database-detail-page");
  await waitFor(() => screen.getByTestId("database-connection-card"));
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("DatabaseDetailPage", () => {
  it("renders state-appropriate actions: paused → resume/rotate/upgrade enabled, suspend/retry disabled", async () => {
    await renderReady({ status: "paused" });
    expect(screen.getByTestId("database-resume-button")).toBeEnabled();
    expect(screen.getByTestId("database-suspend-button")).toBeDisabled();
    expect(screen.getByTestId("database-retry-button")).toBeDisabled();
    expect(screen.getByTestId("database-rotate-button")).toBeEnabled();
    expect(screen.getByTestId("database-upgrade-button")).toBeEnabled();
    // paused 备份/恢复不可用（§2.3 操作表）——按钮如实禁用。
    await waitFor(() => screen.getByTestId("database-backups-card"));
    expect(screen.getByTestId("database-backup-trigger-button")).toBeDisabled();
    expect(screen.getAllByTestId("database-restore-button")[0]).toBeDisabled();
  });

  it("keeps the connection masked by default and reveals the password only on demand", async () => {
    const fetchMock = await renderReady();
    expect(screen.getByText("1a2b3c4d")).toBeInTheDocument();
    expect(screen.getByText(/postgres:\/\/fleetly:\*\*\*\*\*\*\*\*/)).toBeInTheDocument();
    expect(screen.queryByText("hunter2Plaintext")).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByTestId("database-reveal-button"));

    await waitFor(() => {
      expect(screen.getByTestId("database-revealed-password")).toHaveTextContent("hunter2Plaintext");
    });
    expect(
      fetchMock.mock.calls.some((c) => String(c[0]).includes("/credentials")),
    ).toBeTruthy();
    expect(screen.getByText(/recorded in the audit log/i)).toBeInTheDocument();

    // 隐藏即弃：明文回到指纹投影。
    await user.click(screen.getByRole("button", { name: "Hide password" }));
    expect(screen.queryByTestId("database-revealed-password")).not.toBeInTheDocument();
  });

  it("rotates only after the destructive two-phase name confirm", async () => {
    const fetchMock = await renderReady();
    const user = userEvent.setup();

    await user.click(screen.getByTestId("database-rotate-button"));
    const dialog = screen.getByTestId("database-rotate-dialog");
    expect(dialog).toBeInTheDocument();
    expect(dialog).toHaveTextContent(/redeployed automatically/i);

    // 未回填名字 → 提交禁用。
    expect(screen.getByTestId("database-rotate-submit")).toBeDisabled();
    await user.type(screen.getByTestId("database-rotate-confirm-input"), "pg-prod");
    await user.click(screen.getByTestId("database-rotate-submit"));

    await waitFor(() => {
      expect(screen.getByTestId("database-rotate-result")).toHaveTextContent(/api, worker/);
    });
    const posted = fetchMock.mock.calls.find((c) => String(c[0]).includes("/rotate"));
    expect(posted).toBeTruthy();
    expect(JSON.parse(String(posted![1]?.body))).toEqual({ confirm: "pg-prod" });
  });

  it("shows the rustfs honesty note and the failed verify row on the backups card", async () => {
    await renderReady();
    await waitFor(() => screen.getByTestId("database-backups-card"));

    expect(screen.getByTestId("database-honesty-note")).toHaveTextContent(/convenience layer/i);
    expect(screen.getByTestId("database-honesty-note")).toHaveTextContent(/not disaster recovery/i);

    const rows = screen.getAllByTestId("database-backup-row");
    expect(rows).toHaveLength(2);
    expect(rows[0].getAttribute("data-verify")).toBe("verified");
    expect(rows[1].getAttribute("data-verify")).toBe("failed");
    expect(rows[1].textContent).toContain("read-back checksum mismatch");
  });

  it("triggers a manual backup from a ready instance (POST /backups)", async () => {
    const fetchMock = await renderReady();
    await waitFor(() => screen.getByTestId("database-backups-card"));
    const user = userEvent.setup();

    const trigger = screen.getByTestId("database-backup-trigger-button");
    expect(trigger).toBeEnabled();
    await user.click(trigger);

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) => String(c[0]).includes("/backups") && c[1]?.method === "POST",
      );
      expect(posted).toBeTruthy();
      expect(JSON.parse(String(posted![1]?.body))).toEqual({ kind: "manual" });
    });
  });

  it("restores a snapshot only after the destructive name confirm", async () => {
    const fetchMock = await renderReady();
    await waitFor(() => screen.getByTestId("database-backups-card"));
    const user = userEvent.setup();

    const restoreButtons = screen.getAllByTestId("database-restore-button");
    expect(restoreButtons[0]).toBeEnabled();
    await user.click(restoreButtons[0]);

    const dialog = screen.getByTestId("database-restore-dialog");
    expect(dialog).toHaveTextContent("db/pg-prod/0f1e2d3c");
    expect(dialog).toHaveTextContent(/overwritten/i);

    // 未回填名字 → 提交禁用；回填后 POST /restore 携带 confirm。
    expect(screen.getByTestId("database-restore-submit")).toBeDisabled();
    await user.type(screen.getByTestId("database-restore-confirm-input"), "pg-prod");
    await user.click(screen.getByTestId("database-restore-submit"));

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        (c) => String(c[0]).includes("/restore") && c[1]?.method === "POST",
      );
      expect(posted).toBeTruthy();
      expect(JSON.parse(String(posted![1]?.body))).toEqual({
        snapshot: "db/pg-prod/0f1e2d3c",
        confirm: "pg-prod",
      });
    });
  });
});
