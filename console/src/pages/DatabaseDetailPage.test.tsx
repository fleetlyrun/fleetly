// 库实例详情测试（E4 W4-S6）：连接卡脱敏默认 + reveal 显式展开（admin 面
// 敏感性标注）、状态相宜的操作面（paused → resume 可用 suspend 不可用）、
// rotate 破坏性两段式确认（名字回填 + 引用 app 自动重部署的诚实文案）、
// 备份卡 rustfs 诚实口径 + verify 徽章 + restore 两段式。fetch 按 URL 分路
// mock（SystemPage.test 同款）。平台管理员双门（P0-3 残余面收口）：资源面
// 写钮全部隐藏、原位只读说明；非管理员 owner 零变化（防回归）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DatabaseDetailPage } from "@/pages/DatabaseDetailPage";
import { TeamProjectProvider } from "@/lib/context";
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
    if (url.includes("/databases/pg-prod") && method === "DELETE") {
      return Promise.resolve({
        ok: true, status: 200, statusText: "",
        json: () => Promise.resolve({ name: "pg-prod", status: "deleting" }),
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

// /auth/me 包装（TeamProjectProvider 生产接线；其余请求透传给既有分路
// stub）。两个 describe 共享：平台管理员态 / 成员角色态（owner/viewer）。
function stubMe(
  opts: { isPlatformAdmin: boolean; role?: string },
  inner: ReturnType<typeof stubDetailFetch>,
) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "f@t.test", is_platform_admin: opts.isPlatformAdmin },
            teams: [
              {
                team_id: "01TEAM",
                team_slug: "acme",
                team_name: "Acme",
                role: opts.role ?? "owner",
              },
            ],
            project_overrides: [],
          }),
      });
    }
    return inner(input, init);
  });
}

function renderAtInTeamContext() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={["/databases/pg-prod"]}>
      <QueryClientProvider client={client}>
        <TeamProjectProvider>
          <Routes>
            <Route path="/databases/:name" element={<DatabaseDetailPage />} />
          </Routes>
        </TeamProjectProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

async function renderReadyInContext(
  opts: { isPlatformAdmin: boolean; role?: string; s3Mode?: string; status?: string } = {
    isPlatformAdmin: false,
  },
) {
  setToken("flt_test");
  const inner = stubDetailFetch({
    s3Mode: opts.s3Mode ?? "unset",
    status: opts.status ?? "ready",
  });
  vi.stubGlobal("fetch", stubMe(opts, inner));
  renderAtInTeamContext();
  await screen.findByTestId("database-detail-page");
  await waitFor(() => screen.getByTestId("database-connection-card"));
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
    // S3 可达性探测（rustfs 口径判断）仅平台管理员发起（GET /system/s3 =
    // admin scope，非平台管理员跳过探测）——本测试按平台管理员视角渲染。
    await renderReadyInContext({ isPlatformAdmin: true, s3Mode: "rustfs" });
    await waitFor(() => screen.getByTestId("database-backups-card"));

    expect(screen.getByTestId("database-honesty-note")).toHaveTextContent(/convenience layer/i);
    expect(screen.getByTestId("database-honesty-note")).toHaveTextContent(/not disaster recovery/i);

    const rows = screen.getAllByTestId("database-backup-row");
    expect(rows).toHaveLength(2);
    expect(rows[0].getAttribute("data-verify")).toBe("verified");
    expect(rows[1].getAttribute("data-verify")).toBe("failed");
    expect(rows[1].textContent).toContain("read-back checksum mismatch");
    // 探测门：非平台管理员跳过 GET /system/s3（enabled:false）。
    const s3Calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.filter((c) =>
      String(c[0]).includes("/system/s3"),
    );
    expect(s3Calls).toHaveLength(1);
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
    // 受理即反馈（2026-09-25 走查：此前点击后零反馈）：异步受理成功行 +
    // 按钮在途态标签。
    expect(screen.getByTestId("database-backup-accepted")).toHaveTextContent(
      /Backup accepted/,
    );
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
  it("deletes only after the destructive name confirm and passes confirm via the query string", async () => {
    // confirm 走 query（2026-09-25 走查修复：DELETE 无 body 绑定，此前
    // JSON body 上送被网关忽略 → 恒 400 destructive operation）。
    const fetchMock = await renderReady();
    const user = userEvent.setup();

    await user.click(screen.getByTestId("database-delete-button"));
    const dialog = screen.getByTestId("database-delete-dialog");
    expect(dialog).toBeInTheDocument();

    // 未回填名字 → 提交禁用。
    expect(screen.getByTestId("database-delete-submit")).toBeDisabled();
    // 卷保留开关：默认关（delete_volumes=false 上行）。
    await user.click(screen.getByTestId("database-delete-volumes-toggle"));
    await user.click(screen.getByTestId("database-delete-volumes-toggle"));
    await user.type(screen.getByTestId("database-delete-confirm-input"), "pg-prod");
    await user.click(screen.getByTestId("database-delete-submit"));

    await waitFor(() => {
      const deleted = fetchMock.mock.calls.find(
        (c) => String(c[0]).includes("/databases/pg-prod") && c[1]?.method === "DELETE",
      );
      expect(deleted).toBeTruthy();
      const url = String(deleted![0]);
      expect(url).toContain("confirm=pg-prod");
      expect(url).toContain("delete_volumes=false");
    });
  });
});

describe("DatabaseDetailPage platform-admin read-only (P0-3 residual)", () => {
  it("平台管理员：生命周期/reveal/备份/恢复写钮全部隐藏，只读说明原位渲染，读面骨架照常", async () => {
    await renderReadyInContext({ isPlatformAdmin: true });

    // 说明卡三处（操作面 / 连接卡 / 备份卡）。
    const notes = screen.getAllByTestId("platform-readonly-note");
    expect(notes.length).toBe(3);
    expect(notes[0]).toHaveTextContent(
      "Platform administrators have read-only access to resources",
    );
    // 生命周期写钮（ready 态原本可用）不再渲染。
    expect(screen.queryByTestId("database-suspend-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-resume-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-retry-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-upgrade-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-rotate-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-delete-button")).not.toBeInTheDocument();
    // reveal / 备份 / 恢复写钮不再渲染。
    expect(screen.queryByTestId("database-reveal-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-backup-trigger-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-restore-button")).not.toBeInTheDocument();
    // 读面骨架不塌：状态徽章 / 连接投影 / 备份台账照常。
    expect(screen.getByTestId("database-connection-card")).toBeInTheDocument();
    await waitFor(() => screen.getByTestId("database-backups-card"));
    expect(screen.getAllByTestId("database-backup-row")).toHaveLength(2);
    expect(screen.getByText("1a2b3c4d")).toBeInTheDocument();
  });

  it("非管理员 owner：全部写钮照常渲染、无只读说明（零变化防回归）", async () => {
    await renderReadyInContext({ isPlatformAdmin: false, role: "owner" });

    expect(screen.queryByTestId("platform-readonly-note")).not.toBeInTheDocument();
    await waitFor(() => screen.getByTestId("database-backups-card"));
    // ready 态：生命周期钮可见且可用（与基线行为一致）。
    expect(screen.getByTestId("database-suspend-button")).toBeEnabled();
    expect(screen.getByTestId("database-rotate-button")).toBeEnabled();
    expect(screen.getByTestId("database-delete-button")).toBeEnabled();
    expect(screen.getByTestId("database-reveal-button")).toBeInTheDocument();
    expect(screen.getByTestId("database-backup-trigger-button")).toBeEnabled();
    expect(screen.getAllByTestId("database-restore-button")[0]).toBeEnabled();
  });

  it("viewer（成员角色门，2026-09-25 走查）：全部写钮隐藏、成员角色说明卡渲染、读面骨架照常", async () => {
    await renderReadyInContext({ isPlatformAdmin: false, role: "viewer" });

    // 说明卡：成员角色语义（platform-readonly-note 形态复用）——页面级
    // 一张 + 备份卡内说明行一条。
    const pageNote = await screen.findByTestId("database-role-note");
    expect(pageNote).toHaveTextContent(
      "Database lifecycle actions require the admin role in this project.",
    );
    expect(screen.getByTestId("database-backups-role-note")).toBeInTheDocument();
    expect(screen.queryByTestId("platform-readonly-note")).not.toBeInTheDocument();
    // 生命周期/reveal/备份/恢复写钮不再渲染（此前是 enabled 假按钮）。
    expect(screen.queryByTestId("database-suspend-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-resume-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-retry-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-upgrade-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-rotate-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-delete-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-reveal-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-backup-trigger-button")).not.toBeInTheDocument();
    expect(screen.queryByTestId("database-restore-button")).not.toBeInTheDocument();
    // 读面骨架不塌：连接脱敏投影 + 备份台账照常。
    await waitFor(() => screen.getByTestId("database-backups-card"));
    expect(screen.getAllByTestId("database-backup-row")).toHaveLength(2);
    expect(screen.getByText("1a2b3c4d")).toBeInTheDocument();
  });
});
