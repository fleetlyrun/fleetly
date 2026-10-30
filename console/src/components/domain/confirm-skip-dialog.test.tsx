import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import type { CommandDescriptor } from "@lynx-go/uikit-core/kernel";
import { runCommandMapped } from "@/kit/errors";
import { apiSend } from "@/api/client";
import { ConfirmSkipDialog } from "./confirm-skip-dialog";
import { setToken } from "@/lib/token";

// ConfirmSkipDialog 双击 skip 形态钉测（B1 W4 遗留直发面收口，自 rollback-
// confirm 抽出的通用件）：首击无票直发收 E_CONFIRMATION_REQUIRED（428 →
// rejected 结果通道）→ 弹窗「已请求确认」态（状态行 + 钮词换 Confirm 词）
// → 二击 confirmation.skip=true（--yes 逃生门通道，proof=skipped）；ok →
// onOk + 关窗复位；关窗复位双击态；非 REQUIRED 拒绝不切双击态。命令用桩
// descriptor（POST /v1/things）钉机制，调用点接线归各面测试。

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  };
}

const fetchMock = vi.fn();
// /v1/things 的响应序（逐次 shift；空 = 200 {id:"T9"}）。
let thingResponses: Array<{ status: number; body: unknown }> = [];

beforeEach(() => {
  fetchMock.mockReset();
  thingResponses = [];
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
    if (path === "/v1/things" && (init?.method ?? "GET") === "POST") {
      const next = thingResponses.shift();
      if (next != null && next.status !== 200) {
        return new Response(JSON.stringify(next.body), { status: next.status });
      }
      return new Response(JSON.stringify({ id: "T9" }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  });
}

// 桩命令：POST /v1/things（apiSend 管线——非 2xx 抛 ApiError，428/400 经
// runCommandMapped 落 rejected 结果通道，与真实 kit 命令同口径）。
const stubDescriptor: CommandDescriptor<{ payload: string; skip?: boolean }, { id: string }> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<{ id: string }>("/v1/things", "POST", {
        payload: input.payload,
        ...(input.skip ? { confirmation: { skip: true } } : {}),
      }),
    ),
};

function renderDialog() {
  const { client, wrapper } = withClient();
  const onOk = vi.fn();
  render(
    <ConfirmSkipDialog
      label="Do it"
      title="Do the thing?"
      description="Does the thing."
      confirmLabel="Do it"
      confirmAgainLabel="Confirm do"
      pendingLabel="Doing…"
      askedSubject="Doing the thing"
      askedNote="there is no preview for this action"
      descriptor={stubDescriptor}
      buildInput={(skip) => ({ payload: "p1", skip: skip || undefined })}
      onOk={onOk}
    />,
    { wrapper },
  );
  return { client, onOk };
}

function thingCalls(): Array<[string, RequestInit]> {
  return fetchMock.mock.calls.filter(([p, i]) => p === "/v1/things" && i?.method === "POST") as Array<[string, RequestInit]>;
}

describe("ConfirmSkipDialog 双击 skip 形态", () => {
  it("first click is refused with REQUIRED, second click resends with an explicit skip", async () => {
    stubFetch();
    thingResponses = [
      { status: 428, body: { code: "E_CONFIRMATION_REQUIRED", message: "requires confirmation" } },
    ];
    const { client, onOk } = renderDialog();

    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    expect(await screen.findByText("Do the thing?")).toBeTruthy();

    // 首击：无 confirmation 键。
    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    await waitFor(() => expect(thingCalls()).toHaveLength(1));
    expect(JSON.parse(String(thingCalls()[0][1].body))).toEqual({ payload: "p1" });

    // REQUIRED 后切「已请求确认」态：状态行（调用点主语/无预览说明 + 审计
    // 语义句）+ 钮词换 Confirm 词。
    expect(await screen.findByText(/Doing the thing requires an explicit confirmation/)).toBeTruthy();
    expect(screen.getByText(/there is no preview for this action/)).toBeTruthy();
    expect(screen.getByText(/recorded as an intentional skip \(proof = skipped\)/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Confirm do" })).toBeTruthy();

    // 二击：skip=true 显式跳过；ok → onOk 载荷 + 关窗。
    fireEvent.click(screen.getByRole("button", { name: "Confirm do" }));
    await waitFor(() => expect(thingCalls()).toHaveLength(2));
    expect(JSON.parse(String(thingCalls()[1][1].body))).toEqual({ payload: "p1", confirmation: { skip: true } });
    await waitFor(() => expect(onOk).toHaveBeenCalledWith({ id: "T9" }));
    await waitFor(() => expect(screen.queryByText("Do the thing?")).toBeNull());
    client.clear();
  });

  it("closing the dialog resets the asked-confirm state (next open starts over)", async () => {
    stubFetch();
    thingResponses = [
      { status: 428, body: { code: "E_CONFIRMATION_REQUIRED", message: "requires confirmation" } },
    ];
    const { client } = renderDialog();

    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    expect(await screen.findByText(/requires an explicit confirmation/)).toBeTruthy();

    // 关窗 → 双击态复位：重开后首击仍是无票形态（不是直接 skip）。
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText(/requires an explicit confirmation/)).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    expect(await screen.findByText("Do the thing?")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Confirm do" })).toBeNull();
    client.clear();
  });

  it("non-REQUIRED rejections stay in the first-click form (no asked state)", async () => {
    stubFetch();
    thingResponses = [{ status: 400, body: { code: "E_INVALID_ARGUMENT", message: "bad input" } }];
    const { client, onOk } = renderDialog();

    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    fireEvent.click(screen.getByRole("button", { name: "Do it" }));
    await waitFor(() => expect(thingCalls()).toHaveLength(1));

    // 400 拒绝不切双击态：弹窗保持首击形态，onOk 不触发。
    await waitFor(() => expect(screen.queryByText(/requires an explicit confirmation/)).toBeNull());
    expect(screen.getByRole("button", { name: "Do it" })).toBeTruthy();
    expect(onOk).not.toHaveBeenCalled();
    client.clear();
  });
});
