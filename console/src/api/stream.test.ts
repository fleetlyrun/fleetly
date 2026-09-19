// 流式面 401 接全局登出的机制测试（D4-①）：openNdjsonStream 的 consume
// 对 401 响应复用 client.ts 的统一处置（clearToken + unauthorizedListener），
// 再以 StreamError 上抛；非 401 错误不动凭据。fetch 打桩，不起真服务。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  clearToken,
  getToken,
  setToken,
  setUnauthorizedListener,
} from "@/api/client";
import { openNdjsonStream, StreamError } from "@/api/stream";

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
