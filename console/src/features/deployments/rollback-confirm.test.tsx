import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { RollbackButton } from "./rollback-confirm";
import { setToken } from "@/lib/token";

// RollbackButton 双击确认钉测（B1 W4，设计稿 §1.6 最简形态）：Rollback 在
// ConfirmableVerbs 强制集但无 Preview RPC——首击无票直发收
// E_CONFIRMATION_REQUIRED（428 → rejected 结果通道），弹窗切「已请求确认」
// 态；二击带 confirmation.skip=true（--yes 逃生门通道，审计 proof=skipped）。
// ok → toast + onDeployed；关窗复位双击态。
// 2026-10-31 双击机制抽出通用 ConfirmSkipDialog（components/domain）后本
// 套件原样随迁——零行为漂移的回归钉（文案措辞含 Rollback 专属主语）。

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> };
}

const fetchMock = vi.fn();
// rollback 调用序（首击无票 / 二击 skip）。
let rollbackResponses: Array<{ status: number; body: unknown }> = [];

beforeEach(() => {
  fetchMock.mockReset();
  rollbackResponses = [];
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

function stubFetch() {
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path === "/v1/apps/A1/rollback" && (init?.method ?? "GET") === "POST") {
      const next = rollbackResponses.shift();
      if (next != null && next.status !== 200) {
        return new Response(JSON.stringify(next.body), { status: next.status });
      }
      return new Response(JSON.stringify({ deployment: { id: "D9" } }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
}

function renderButton() {
  const { client, wrapper } = withClient();
  const onDeployed = vi.fn();
  render(<RollbackButton projectId="P1" appId="A1" appLabel="web" onDeployed={onDeployed} />, { wrapper });
  return { client, onDeployed };
}

function rollbackCalls(): Array<[string, RequestInit]> {
  return fetchMock.mock.calls.filter(([p, i]) => p === "/v1/apps/A1/rollback" && i?.method === "POST") as Array<[string, RequestInit]>;
}

describe("RollbackButton 双击确认", () => {
  it("first click is refused with REQUIRED, second click resends with an explicit skip", async () => {
    stubFetch();
    rollbackResponses = [
      { status: 428, body: { code: "E_CONFIRMATION_REQUIRED", message: "requires confirmation: pass the ticket from a preview, or set skip" } },
    ];
    const { client, onDeployed } = renderButton();

    fireEvent.click(screen.getByRole("button", { name: "Rollback" }));
    expect(await screen.findByText("Roll back web?")).toBeTruthy();

    // 首击：无票直发（body 空体）。
    fireEvent.click(screen.getByRole("button", { name: "Roll back" }));
    await waitFor(() => expect(rollbackCalls()).toHaveLength(1));
    expect(JSON.parse(String(rollbackCalls()[0][1].body))).toEqual({});

    // REQUIRED 后切「已请求确认」态：状态行 + 钮词换 Confirm rollback。
    expect(await screen.findByText(/Rollback requires an explicit confirmation/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Confirm rollback" })).toBeTruthy();

    // 二击：skip=true 显式跳过。
    fireEvent.click(screen.getByRole("button", { name: "Confirm rollback" }));
    await waitFor(() => expect(rollbackCalls()).toHaveLength(2));
    expect(JSON.parse(String(rollbackCalls()[1][1].body))).toEqual({ confirmation: { skip: true } });
    await waitFor(() => expect(onDeployed).toHaveBeenCalledWith("D9"));
    client.clear();
  });

  it("closing the dialog resets the asked-confirm state (next open starts over)", async () => {
    stubFetch();
    rollbackResponses = [
      { status: 428, body: { code: "E_CONFIRMATION_REQUIRED", message: "requires confirmation" } },
    ];
    const { client } = renderButton();

    fireEvent.click(screen.getByRole("button", { name: "Rollback" }));
    fireEvent.click(screen.getByRole("button", { name: "Roll back" }));
    expect(await screen.findByText(/Rollback requires an explicit confirmation/)).toBeTruthy();

    // 关窗 → 双击态复位：重开后首击仍是无票形态（不是直接 skip）。
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText(/Rollback requires an explicit confirmation/)).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: "Rollback" }));
    expect(await screen.findByText("Roll back web?")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Roll back" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Confirm rollback" })).toBeNull();
    client.clear();
  });
});
