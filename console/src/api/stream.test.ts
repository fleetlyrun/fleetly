// 流式面 401 接全局登出的机制测试（D4-①）：openNdjsonStream 的 consume
// 对 401 响应复用 client.ts 的统一处置（clearToken + unauthorizedListener），
// 再以 StreamError 上抛；非 401 错误不动凭据。fetch 打桩，不起真服务。
// 看门狗（M9-2）：读空闲超时 → abort + onEnd(StreamError)，有帧时重置。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  clearToken,
  getToken,
  setToken,
  setUnauthorizedListener,
} from "@/api/client";
import {
  STREAM_IDLE_TIMEOUT_MS,
  openNdjsonStream,
  StreamError,
} from "@/api/stream";

function errorResponse(status: number, body: unknown) {
  return {
    ok: false,
    status,
    body: null,
    json: () => Promise.resolve(body),
  };
}

beforeEach(() => clearToken());
afterEach(() => {
  vi.unstubAllGlobals();
  clearToken();
  setUnauthorizedListener(null);
});

describe("openNdjsonStream 401 → global logout", () => {
  it("clears the token and notifies the unauthorized listener on 401", async () => {
    setToken("flt_revoked");
    const listener = vi.fn();
    setUnauthorizedListener(listener);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        errorResponse(401, {
          code: "E_UNAUTHENTICATED",
          message: "invalid or revoked token",
        }),
      ),
    );

    const onEnd = vi.fn();
    await openNdjsonStream("/v1/events/stream", {}, {
      onFrame: vi.fn(),
      onEnd,
    });
    await vi.waitFor(() => expect(onEnd).toHaveBeenCalled());

    expect(getToken()).toBe("");
    expect(listener).toHaveBeenCalledTimes(1);
    expect(listener).toHaveBeenCalledWith(
      expect.objectContaining({ message: "invalid or revoked token" }),
    );
    // 页面展示分支拿到的仍是 StreamError(401)。
    const err = onEnd.mock.calls[0][0];
    expect(err).toBeInstanceOf(StreamError);
    expect((err as StreamError).status).toBe(401);
  });

  it("keeps credentials and skips the listener for non-401 errors", async () => {
    setToken("flt_ok");
    const listener = vi.fn();
    setUnauthorizedListener(listener);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(errorResponse(503, { message: "unavailable" })),
    );

    const onEnd = vi.fn();
    await openNdjsonStream("/v1/events/stream", {}, {
      onFrame: vi.fn(),
      onEnd,
    });
    await vi.waitFor(() => expect(onEnd).toHaveBeenCalled());

    expect(getToken()).toBe("flt_ok");
    expect(listener).not.toHaveBeenCalled();
    expect((onEnd.mock.calls[0][0] as StreamError).status).toBe(503);
  });
});

describe("openNdjsonStream idle watchdog (M9-2)", () => {
  afterEach(() => vi.useRealTimers());

  it("aborts the connection and signals onEnd(StreamError) when no frames arrive within the timeout", async () => {
    vi.useFakeTimers();
    const enc = new TextEncoder();
    // 半开连接形态：发一帧后既不关流也不再有数据——read 永久挂起。
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(enc.encode('{"result":{"entry":{"line":"first"}}}\n'));
      },
    });
    let signal: AbortSignal | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((_url: string, init?: { signal?: AbortSignal }) => {
        signal = init?.signal;
        return Promise.resolve({ ok: true, status: 200, body });
      }),
    );

    const onFrame = vi.fn();
    const onEnd = vi.fn();
    await openNdjsonStream("/v1/apps/x/logs/stream", {}, { onFrame, onEnd });
    await vi.advanceTimersByTimeAsync(0);
    expect(onFrame).toHaveBeenCalledTimes(1);

    // 看门狗到点：controller.abort 被触发、onEnd 以 StreamError 上抛
    //（页面退避重连路径可达）。
    await vi.advanceTimersByTimeAsync(STREAM_IDLE_TIMEOUT_MS);
    expect(signal?.aborted).toBe(true);
    expect(onEnd).toHaveBeenCalledTimes(1);
    const err = onEnd.mock.calls[0][0];
    expect(err).toBeInstanceOf(StreamError);
    expect((err as StreamError).envelope.code).toBe("E_STREAM_IDLE_TIMEOUT");
  });

  it("resets the watchdog on incoming frames and lets healthy streams run", async () => {
    vi.useFakeTimers();
    const enc = new TextEncoder();
    let streamCtrl: ReadableStreamDefaultController<Uint8Array> | null = null;
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        streamCtrl = c;
      },
    });
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, status: 200, body }),
    );

    const onFrame = vi.fn();
    const onEnd = vi.fn();
    await openNdjsonStream("/v1/events/stream", {}, { onFrame, onEnd });
    await vi.advanceTimersByTimeAsync(0);

    // 每 10s 一帧（< 30s 看门狗）：推进 60s 不断流、不触发 onEnd。
    const interval = setInterval(() => {
      streamCtrl!.enqueue(
        enc.encode('{"result":{"event":{"seq":"tick"}}}\n'),
      );
    }, 10_000);
    await vi.advanceTimersByTimeAsync(60_000);
    clearInterval(interval);
    expect(onEnd).not.toHaveBeenCalled();
    expect(onFrame).toHaveBeenCalledTimes(6);

    // 服务端正常关流：onEnd(undefined)（干净结束路径不受看门狗影响）。
    streamCtrl!.close();
    await vi.advanceTimersByTimeAsync(0);
    expect(onEnd).toHaveBeenCalledTimes(1);
    expect(onEnd.mock.calls[0][0]).toBeUndefined();
  });
});
