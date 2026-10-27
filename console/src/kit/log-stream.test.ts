import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FrameSource } from "@lynx-go/uikit-core/kernel";
import { ApiError } from "@/api/client";
import type { LogFrame } from "@/api/streams";
import { streamLogs } from "@/api/streams";
import { buildLogQuery, logsTransport, type EventFrame } from "./log-stream";

// kit/log-stream transport 契约锚：query 组装（照抄 use-log-stream 参数
// 面）、帧方言（payload 原样 + seq 跨重连单调 + at）、closeReason 分诊
// （resolve=ended / 401,403=auth / 其余=network 交引擎退避）、signal 直传。
// 不用真实计时器——引擎侧退避不在本文件（vi.mock api/streams，直驱 transport）。

vi.mock("@/api/streams", () => ({ streamLogs: vi.fn() }));

const streamLogsMock = vi.mocked(streamLogs);

beforeEach(() => {
  vi.clearAllMocks(); // 调用计数逐测试归零（实现由各测试自设）
});

type LogSource = FrameSource<EventFrame<LogFrame>>;

function okStream(frames: LogFrame[]) {
  return (_query: URLSearchParams, _signal: AbortSignal, onFrame: (frame: LogFrame) => void) => {
    for (const frame of frames) onFrame(frame);
    return Promise.resolve();
  };
}

async function pullAll(source: LogSource) {
  const iterator = source[Symbol.asyncIterator]();
  const frames: Array<EventFrame<LogFrame>> = [];
  for (;;) {
    const step = await iterator.next();
    if (step.done) break;
    frames.push(step.value);
  }
  return frames;
}

describe("buildLogQuery", () => {
  it("三轴互斥 + processes/containers 多值 + tail/text/follow 面同 use-log-stream", () => {
    const query = buildLogQuery({
      appId: "app-1",
      databaseId: undefined,
      runId: undefined,
      processes: ["api", "", "web"],
      containers: ["c-1"],
      tailLines: "200",
      text: "err",
      follow: true,
    });
    expect(query.get("app_id")).toBe("app-1");
    expect(query.getAll("processes")).toEqual(["api", "web"]); // 空串剔除
    expect(query.getAll("containers")).toEqual(["c-1"]);
    expect(query.get("tail_lines")).toBe("200");
    expect(query.get("text")).toBe("err");
    expect(query.get("follow")).toBe("1");
  });

  it("database/run 轴与 follow=0；空 tail/text 不落参", () => {
    const query = buildLogQuery({
      databaseId: "db-1",
      runId: "run-1",
      processes: [],
      containers: [],
      tailLines: "",
      text: "",
      follow: false,
    });
    expect(query.get("database_id")).toBe("db-1");
    expect(query.get("run_id")).toBe("run-1");
    expect(query.get("follow")).toBe("0");
    expect(query.has("tail_lines")).toBe(false);
    expect(query.has("text")).toBe(false);
  });
});

describe("logsTransport", () => {
  const controls = {
    appId: "app-1",
    processes: ["api"],
    containers: [],
    tailLines: "200",
    text: "",
    follow: true,
  };

  it("query 组装直传 streamLogs；signal 直传（引擎 abort 收流面）", async () => {
    streamLogsMock.mockImplementation(okStream([]));
    const controller = new AbortController();
    const source = logsTransport(controls).open(null, controller.signal);
    await pullAll(source);
    expect(streamLogsMock).toHaveBeenCalledTimes(1);
    const [query, signal] = streamLogsMock.mock.calls[0]!;
    expect(query.get("app_id")).toBe("app-1");
    expect(query.get("follow")).toBe("1");
    expect(signal).toBe(controller.signal);
  });

  it("帧方言：payload 原样 + seq 从 1 单调 + at 到达时刻；resolve → ended", async () => {
    streamLogsMock.mockImplementation(okStream([{ line: "aGk=", time: "t1" }, { line: "eW8=" }]));
    const source = logsTransport(controls).open(null, new AbortController().signal);
    const frames = await pullAll(source);
    expect(frames.map((frame) => frame.seq)).toEqual([1, 2]);
    expect(frames[0]!.payload).toEqual({ line: "aGk=", time: "t1" });
    expect(frames[1]!.payload).toEqual({ line: "eW8=" });
    expect(frames[0]!.at).toBeInstanceOf(Date);
    expect(source.closeReason).toBe("ended"); // 非 follow 收完即止 / follow 服务端关流
  });

  it("重开同 transport（引擎重连）：seq 跨 open 单调递增不回绕", async () => {
    streamLogsMock.mockImplementation(okStream([{ line: "x=" }]));
    const transport = logsTransport(controls);
    const first = await pullAll(transport.open(null, new AbortController().signal));
    const second = await pullAll(transport.open(null, new AbortController().signal));
    expect(first.map((frame) => frame.seq)).toEqual([1]);
    expect(second.map((frame) => frame.seq)).toEqual([2]);
  });

  it("401/403 → closeReason auth（终态，不退避）", async () => {
    streamLogsMock.mockImplementation(() => Promise.reject(new ApiError(401, "unauthenticated", "x")));
    const source = logsTransport(controls).open(null, new AbortController().signal);
    await expect(pullAll(source)).resolves.toEqual([]);
    expect(source.closeReason).toBe("auth");

    streamLogsMock.mockImplementation(() => Promise.reject(new ApiError(403, "permission_denied", "x")));
    const source403 = logsTransport(controls).open(null, new AbortController().signal);
    await expect(pullAll(source403)).resolves.toEqual([]);
    expect(source403.closeReason).toBe("auth");
  });

  it("其余错误（5xx/传输层）→ 抛 kernel network 交引擎退避重连", async () => {
    const boom = new ApiError(500, "internal", "boom");
    streamLogsMock.mockImplementation(() => Promise.reject(boom));
    const source = logsTransport(controls).open(null, new AbortController().signal);
    const iterator = source[Symbol.asyncIterator]();
    await expect(iterator.next()).rejects.toEqual({ kind: "network", cause: boom });

    const glitch = new TypeError("Failed to fetch");
    streamLogsMock.mockImplementation(() => Promise.reject(glitch));
    const next = logsTransport(controls).open(null, new AbortController().signal);
    await expect(next[Symbol.asyncIterator]().next()).rejects.toEqual({ kind: "network", cause: glitch });
  });

  it("buffer 先灌后抽：settle 前到达的帧不丢（push→pull 桥）", async () => {
    let release: (() => void) | undefined;
    streamLogsMock.mockImplementation((_query, _signal, onFrame) => {
      onFrame({ line: "MQ==" });
      return new Promise<void>((resolve) => {
        release = resolve;
      });
    });
    const source = logsTransport(controls).open(null, new AbortController().signal);
    const iterator = source[Symbol.asyncIterator]();
    const first = await iterator.next();
    expect(first.value?.payload).toEqual({ line: "MQ==" });
    release?.();
    await expect(iterator.next()).resolves.toEqual({ done: true });
  });
});
