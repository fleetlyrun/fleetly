// 备份台账卡测试（E3-3 上传轨 + T2.22 台账）：每行 upload_status 锚点
//（none/ok/failed + uploaded_at；failed 红色行带 upload_error 摘要）、
// verify 结论渲染、空态、Back up now 手动触发（backlog #4-②）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { BackupsCard } from "@/components/backups-card";
import { setToken } from "@/api/client";

/** /auth/me 包装（useIsPlatformAdmin 生产接线）：Back up now 写面门
 * （TriggerBackup = 平台管理员专属，2026-09-25 走查）测试用。 */
function withMe(
  isPlatformAdmin: boolean,
  inner: (input: RequestInfo | URL, init?: RequestInit) => Promise<unknown>,
) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith("/auth/me")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            user: { id: "01U1", email: "f@t.test", is_platform_admin: isPlatformAdmin },
            teams: [
              { team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: "owner" },
            ],
            project_overrides: [],
          }),
      });
    }
    return inner(input, init);
  });
}

function stubBackupsFetch(backups: Record<string, unknown>[]) {
  return vi.fn().mockImplementation((input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/system/backups")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ backups }),
      });
    }
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: "",
      json: () => Promise.resolve({}),
    });
  });
}

function renderCard() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <BackupsCard />
    </QueryClientProvider>,
  );
}

describe("BackupsCard upload status (E3-3)", () => {
  it("renders per-row upload status: ok with uploaded_at, failed in red with error, none muted", async () => {
    setToken("flt_test");
    vi.stubGlobal(
      "fetch",
      stubBackupsFetch([
        {
          id: "b_ok",
          kind: "daily",
          size_bytes: "2097152",
          verify_status: "verified",
          created_at: "2026-09-21T02:00:00Z",
          upload_status: "ok",
          uploaded_at: "2026-09-21T02:01:30Z",
        },
        {
          id: "b_failed",
          kind: "manual",
          size_bytes: "1048576",
          verify_status: "verified",
          created_at: "2026-09-20T02:00:00Z",
          upload_status: "failed",
          uploaded_at: "2026-09-20T02:01:30Z",
          upload_error: "restic: dial tcp 203.0.113.9:9000: connect refused",
        },
        {
          id: "b_none",
          kind: "post_deploy",
          size_bytes: "524288",
          verify_status: "verified",
          created_at: "2026-09-19T02:00:00Z",
        },
      ]),
    );

    renderCard();

    await screen.findByText(/connect refused/);

    const cells = screen.getAllByTestId("backup-upload-status");
    expect(cells).toHaveLength(3);
    // ok 行：绿色点 + 上传时间。
    const okCell = cells.find((c) => c.getAttribute("data-upload-status") === "ok");
    expect(okCell?.textContent).toContain("uploaded");
    expect(okCell?.innerHTML).toContain("bg-emerald-500");
    // failed 行：红色点 + upload_error 摘要 + tooltip 原文。
    const failedCell = cells.find(
      (c) => c.getAttribute("data-upload-status") === "failed",
    );
    expect(failedCell?.innerHTML).toContain("bg-red-500");
    expect(failedCell?.getAttribute("title")).toContain("connect refused");
    // none 行：诚实显示未上传（s3.mode=unset 是合法态）。
    const noneCell = cells.find(
      (c) => c.getAttribute("data-upload-status") === "none",
    );
    expect(noneCell?.textContent).toContain("not uploaded");

    // verify 列：与 upload 正交——failed 上传不回写 verify。
    expect(screen.getAllByText("verified")).toHaveLength(3);
  });

  it("shows the empty state before any backup exists", async () => {
    setToken("flt_test");
    vi.stubGlobal("fetch", stubBackupsFetch([]));

    renderCard();

    await waitFor(() =>
      expect(screen.getByText("No backups yet")).toBeInTheDocument(),
    );
    expect(screen.queryByTestId("backup-upload-status")).not.toBeInTheDocument();
  });

  it("Back up now posts a manual state backup and refetches the ledger (backlog #4-②)", async () => {
    setToken("flt_test");
    const log: { url: string; method: string; body?: unknown }[] = [];
    const inner = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      log.push({ url, method: init?.method ?? "GET", body: init?.body });
      if (url.includes("/system/backups")) {
        if (init?.method === "POST") {
          // TriggerBackupResponse：响应即落账后的台账行（同步语义）。
          return Promise.resolve({
            ok: true,
            status: 200,
            statusText: "",
            json: () =>
              Promise.resolve({
                backup: {
                  id: "b_manual",
                  kind: "manual",
                  verify_status: "verified",
                  created_at: "2026-09-25T12:00:00Z",
                },
              }),
          });
        }
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () => Promise.resolve({ backups: [] }),
        });
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({}),
      });
    });
    vi.stubGlobal("fetch", withMe(true, inner));

    renderCard();

    // 初始台账 GET 恰一次（15s 轮询间隔内测试不会触发第二次自然轮询）。
    await waitFor(() => expect(screen.getByTestId("backup-trigger")).toBeInTheDocument());
    const ledgerGets = () =>
      log.filter((e) => e.method === "GET" && e.url.includes("/system/backups")).length;
    expect(ledgerGets()).toBe(1);

    fireEvent.click(screen.getByTestId("backup-trigger"));

    await waitFor(() => {
      const post = log.find(
        (e) => e.method === "POST" && e.url.includes("/system/backups"),
      );
      expect(post).toBeTruthy();
      expect(JSON.parse(String(post?.body))).toEqual({ kind: "manual" });
    });
    // 台账失效 → 立即重取（GET 第二次），新行不经 15s 轮询即可见。
    await waitFor(() => expect(ledgerGets()).toBeGreaterThanOrEqual(2));
    expect(screen.getByTestId("backup-trigger-hint").textContent).toContain(
      "platform state backup",
    );
  });

  it("viewer（写面门，2026-09-25 走查）：无 Back up now 按钮、原位只读说明，台账读面照常", async () => {
    setToken("flt_test");
    const inner = stubBackupsFetch([
      {
        id: "b1",
        kind: "daily",
        size_bytes: "1024",
        verify_status: "verified",
        created_at: "2026-09-21T02:00:00Z",
      },
    ]);
    vi.stubGlobal("fetch", withMe(false, inner));

    renderCard();

    await waitFor(() =>
      expect(screen.getByTestId("backup-trigger-readonly-note")).toHaveTextContent(
        "Platform administrator required.",
      ),
    );
    expect(screen.queryByTestId("backup-trigger")).not.toBeInTheDocument();
    // 台账读面（ListBackups = read scope）不受写面门影响。
    await waitFor(() => expect(screen.getByText("daily")).toBeInTheDocument());
  });
});
