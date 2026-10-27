import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FrameSource } from "@lynx-go/uikit-core/kernel";
import { ApiError } from "@/api/client";
import type { EventRow } from "@/api/streams";
import { followEvents, mintEventTicket } from "@/api/streams";
import { eventsTransport } from "./event-stream";

// kit/event-stream transport 契约锚：open 铸一次性票据并按 cursor 组
// after_seq、onEvent 帧直通、onEnd 分诊（410=gapped / 0=network 交引擎
// 退避 / 其他>0=auth）、铸票失败分诊、重开重新铸票、abort 收流。
// 不用真实计时器——引擎侧退避不在本文件（vi.mock api/streams，直驱 transport）。

vi.mock("@/api/streams", () => ({ mintEventTicket: vi.fn(), followEvents: vi.fn() }));

const mintMock = vi.mocked(mintEventTicket);
const followMock = vi.mocked(followEvents);

beforeEach(() => {
  vi.clearAllMocks(); // 调用计数逐测试归零（实现由各测试自设）
});

type EventSource = FrameSource<EventRow>;
type StreamEnd = { status: number; code?: string };

function row(seq: string): EventRow {
  return { seq, name: "app.deployed", aggregate: "app", aggregate_id: "app-1", payload: "{}", created_at: "2026-10-27T00:00:00Z" };
}

// scenario 让测试直握 onEvent/onEnd（followEvents 的 push 面回执）。
function scenario(rows: EventRow[], end: StreamEnd | null, stopSpy?: () => void) {
  let fired: ((event: EventRow) => void) | undefined;
  let finisher: ((end: StreamEnd) => void) | undefined;
  followMock.mockImplementation((_ticket, _afterSeq, onEvent, onEnd) => {
    fired = onEvent;
    finisher = onEnd;
    for (const event of rows) onEvent(event);
    if (end !== null) onEnd(end);
    return { stop: () => stopSpy?.() };
  });
  return {
    emit: (event: EventRow) => fired?.(event),
    finish: (stop: StreamEnd) => finisher?.(stop),
  };
}

async function pullAll(source: EventSource) {
  const iterator = source[Symbol.asyncIterator]();
  const frames: EventRow[] = [];
  for (;;) {
    const step = await iterator.next();
    if (step.done) break;
    frames.push(step.value);
  }
  return frames;
}

describe("eventsTransport", () => {
  it("open 铸一次性票据并按 cursor 组 after_seq；onEvent 帧直通（方言原样）", async () => {
    mintMock.mockResolvedValue("ticket-1");
    scenario([row("7"), row("8")], null); // end=null = follow 长连
    const controller = new AbortController();
    const source = eventsTransport().open("6", controller.signal);
    const iterator = source[Symbol.asyncIterator]();
    const first = await iterator.next();
    const second = await iterator.next();
    expect(first.value).toEqual(row("7")); // EventRow 原样，无信封
    expect(second.value).toEqual(row("8"));
    controller.abort(); // 收场：主动中断不经 onEnd
    await expect(iterator.next()).resolves.toEqual({ done: true });
    expect(followMock).toHaveBeenCalledWith("ticket-1", "6", expect.any(Function), expect.any(Function));
  });

  it("游标投影：after_seq 随 open cursor；重开重新铸票（一次性票据语义）", async () => {
    mintMock.mockResolvedValueOnce("ticket-a").mockResolvedValueOnce("ticket-b");
    scenario([], { status: 410 }); // 断档收口：pullAll 干净终态
    const transport = eventsTransport();
    await pullAll(transport.open("0", new AbortController().signal));
    await pullAll(transport.open("1", new AbortController().signal));
    expect(mintMock).toHaveBeenCalledTimes(2);
    expect(followMock.mock.calls[0]!.slice(0, 2)).toEqual(["ticket-a", "0"]);
    expect(followMock.mock.calls[1]!.slice(0, 2)).toEqual(["ticket-b", "1"]);
  });

  it("onEnd 0（传输收口）→ 抛 kernel network 交引擎退避重开新票", async () => {
    mintMock.mockResolvedValue("ticket-1");
    scenario([], { status: 0 });
    const source = eventsTransport().open("5", new AbortController().signal);
    const iterator = source[Symbol.asyncIterator]();
    await expect(iterator.next()).rejects.toEqual({ kind: "network", cause: { status: 0 } });
  });

  it("onEnd 410（断档/E_EVENTS_GONE）→ closeReason gapped（页面 resync 编排）", async () => {
    mintMock.mockResolvedValue("ticket-1");
    scenario([], { status: 410, code: "E_EVENTS_GONE" });
    const source = eventsTransport().open("5", new AbortController().signal);
    await expect(pullAll(source)).resolves.toEqual([]);
    expect(source.closeReason).toBe("gapped");
  });

  it("onEnd 其他 >0（票据/凭证类）→ closeReason auth 终态", async () => {
    mintMock.mockResolvedValue("ticket-1");
    scenario([], { status: 401 });
    const source = eventsTransport().open("5", new AbortController().signal);
    await expect(pullAll(source)).resolves.toEqual([]);
    expect(source.closeReason).toBe("auth");
  });

  it("铸票 401 → auth 终态（不触底层流）；铸票暂时失败 → network 退避重铸", async () => {
    mintMock.mockRejectedValueOnce(new ApiError(401, "unauthenticated", "x"));
    const authSource = eventsTransport().open("0", new AbortController().signal);
    await expect(pullAll(authSource)).resolves.toEqual([]);
    expect(authSource.closeReason).toBe("auth");
    expect(followMock).not.toHaveBeenCalled();

    const glitch = new ApiError(503, "unavailable", "x");
    mintMock.mockRejectedValueOnce(glitch);
    const netSource = eventsTransport().open("0", new AbortController().signal);
    await expect(netSource[Symbol.asyncIterator]().next()).rejects.toEqual({ kind: "network", cause: glitch });
  });

  it("引擎 abort → 生成器退出并 stop 底层流（主动中断不经 onEnd）", async () => {
    mintMock.mockResolvedValue("ticket-1");
    const stopSpy = vi.fn();
    scenario([row("1")], null, stopSpy);
    const controller = new AbortController();
    const source = eventsTransport().open("0", controller.signal);
    const iterator = source[Symbol.asyncIterator]();
    const first = await iterator.next();
    expect(first.value?.seq).toBe("1");
    controller.abort(); // 引擎 pause/stop/重启接管面
    await expect(iterator.next()).resolves.toEqual({ done: true });
    expect(stopSpy).toHaveBeenCalledTimes(1);
  });

  it("onEnd 前到达的帧不丢（push→pull 桥：帧先吐、end 随后收口分诊）", async () => {
    mintMock.mockResolvedValue("ticket-1");
    const stream = scenario([], null);
    const source = eventsTransport().open("0", new AbortController().signal);
    const iterator = source[Symbol.asyncIterator]();
    // 首个 next() 悬在挂起点（微任务排空后 followEvents 已开流）
    const pending = iterator.next();
    for (let i = 0; i < 10; i++) await Promise.resolve();
    expect(followMock).toHaveBeenCalledTimes(1);
    stream.emit(row("2"));
    expect((await pending).value?.seq).toBe("2");
    stream.finish({ status: 410 });
    await expect(iterator.next()).resolves.toEqual({ done: true });
    expect(source.closeReason).toBe("gapped");
  });
});
