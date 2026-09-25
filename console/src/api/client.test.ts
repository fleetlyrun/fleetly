// API 客户端单测（stubbed fetch，不起真服务）：双凭据（Bearer 注入 / 会话
// cookie credentials）、错误信封类型化、401 时清凭据 + 触发未授权监听、
// 认证面 optionalAuth 豁免、bytes base64 上行（Deploy 契约）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  api,
  clearToken,
  getToken,
  setToken,
  setUnauthorizedListener,
  utf8ToBase64,
} from "@/api/client";
import { ApiError } from "@/api/errors";

function jsonResponse(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 401 ? "Unauthorized" : "",
    json: () => Promise.resolve(body),
  };
}

beforeEach(() => clearToken());
afterEach(() => {
  vi.unstubAllGlobals();
  clearToken();
});

describe("api client", () => {
  it("sends Authorization: Bearer when a token is stored", async () => {
    setToken("flt_abc");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { ok: true }));
    vi.stubGlobal("fetch", fetchMock);

    await api("/apps");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/v1/apps");
    expect((init.headers as Record<string, string>).Authorization).toBe(
      "Bearer flt_abc",
    );
  });

  it("stores tokens in sessionStorage and prefers it over a legacy localStorage copy (P1-4)", async () => {
    setToken("flt_session");
    // 存量迁移面：localStorage 里残留一份 f1b98f7 前的旧 token。
    localStorage.setItem("fleetly.console.token", "flt_legacy");
    // 写入只落 sessionStorage（标签页生命周期），不动 localStorage 存量。
    expect(sessionStorage.getItem("fleetly.console.token")).toBe("flt_session");
    expect(localStorage.getItem("fleetly.console.token")).toBe("flt_legacy");

    // 读取 sessionStorage 优先：Bearer 取新 token 而非残留旧 token。
    expect(getToken()).toBe("flt_session");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { ok: true }));
    vi.stubGlobal("fetch", fetchMock);

    await api("/apps");
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>).Authorization).toBe(
      "Bearer flt_session",
    );
  });

  it("falls back to a legacy localStorage token when no session copy exists (migration)", async () => {
    localStorage.setItem("fleetly.console.token", "flt_legacy");
    expect(sessionStorage.getItem("fleetly.console.token")).toBeNull();
    expect(getToken()).toBe("flt_legacy");

    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { ok: true }));
    vi.stubGlobal("fetch", fetchMock);

    await api("/apps");
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>).Authorization).toBe(
      "Bearer flt_legacy",
    );
  });

  it("clears both sessionStorage and the legacy localStorage copy on 401 (P1-4)", async () => {
    setToken("flt_session");
    localStorage.setItem("fleetly.console.token", "flt_legacy");
    const listener = vi.fn();
    setUnauthorizedListener(listener);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(401, { message: "invalid or revoked token" }),
      ),
    );

    await expect(api("/apps")).rejects.toBeInstanceOf(ApiError);
    // 双清：残留 token 不得在任何存储位存活。
    expect(sessionStorage.getItem("fleetly.console.token")).toBeNull();
    expect(localStorage.getItem("fleetly.console.token")).toBeNull();
    expect(getToken()).toBe("");
    expect(listener).toHaveBeenCalledWith(
      expect.objectContaining({ message: "invalid or revoked token" }),
    );
    setUnauthorizedListener(null);
  });

  it("omits the Authorization header without a token", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { ok: true }));
    vi.stubGlobal("fetch", fetchMock);

    await api("/system/ping");

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>).Authorization).toBeUndefined();
  });

  it("always sends credentials: include (session-cookie carrier)", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { ok: true }));
    vi.stubGlobal("fetch", fetchMock);

    await api("/auth/me");

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.credentials).toBe("include");
  });

  it("optionalAuth: a 401 neither clears the token nor notifies the listener", async () => {
    setToken("flt_probe");
    const listener = vi.fn();
    setUnauthorizedListener(listener);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(401, { message: "not signed in" }),
      ),
    );

    // 认证面自身的 401（登录失败/启动探测）是业务结果——全局登出不得触发。
    await expect(api("/auth/me", { optionalAuth: true })).rejects.toBeInstanceOf(ApiError);
    expect(getToken()).toBe("flt_probe");
    expect(listener).not.toHaveBeenCalled();
    setUnauthorizedListener(null);
  });

  it("throws a typed ApiError carrying the snake_case envelope", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(409, {
          code: "E_STATE_VERSION_CONFLICT",
          message: "deployment already healthy",
          suggestion: "use rollback instead of cancel",
        }),
      ),
    );

    const err = await api("/deployments/x/cancel", { method: "POST", json: {} }).then(
      () => null,
      (e: unknown) => e,
    );

    expect(err).toBeInstanceOf(ApiError);
    const apiErr = err as ApiError;
    expect(apiErr.status).toBe(409);
    expect(apiErr.code).toBe("E_STATE_VERSION_CONFLICT");
    expect(apiErr.suggestion).toBe("use rollback instead of cancel");
  });

  it("clears the token and notifies the listener on 401", async () => {
    setToken("flt_revoked");
    const listener = vi.fn();
    setUnauthorizedListener(listener);

    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(401, { message: "invalid or revoked token" }),
      ),
    );

    await expect(api("/apps")).rejects.toBeInstanceOf(ApiError);
    expect(getToken()).toBe("");
    expect(listener).toHaveBeenCalledWith(
      expect.objectContaining({ message: "invalid or revoked token" }),
    );
    setUnauthorizedListener(null);
  });

  it("aborts hanging requests after the 30s default timeout (D4-⑤)", async () => {
    vi.useFakeTimers();
    try {
      // 挂起 fetch（真 fetch 语义：signal abort 即 reject，永不自行 settle）。
      const fetchMock = vi.fn(
        (_url: unknown, init?: RequestInit) =>
          new Promise((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () => {
              reject(init.signal?.reason);
            });
          }),
      );
      vi.stubGlobal("fetch", fetchMock);

      const pending = api("/apps").then(
        () => null,
        (e: unknown) => e,
      );
      await vi.advanceTimersByTimeAsync(0);
      expect(fetchMock).toHaveBeenCalled();

      await vi.advanceTimersByTimeAsync(30_000);
      const err = await pending;
      expect((err as { name?: string }).name).toBe("AbortError");
    } finally {
      vi.useRealTimers();
    }
  });

  it("combines a caller signal with the default timeout (either aborts)", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(
        (_url: unknown, init?: RequestInit) =>
          new Promise((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () => {
              reject(init.signal?.reason);
            });
          }),
      );
      vi.stubGlobal("fetch", fetchMock);
      const caller = new AbortController();
      const pending = api("/apps", { signal: caller.signal }).then(
        () => null,
        (e: unknown) => e,
      );
      await vi.advanceTimersByTimeAsync(0);

      caller.abort(); // 请求方取消先于 30s 超时
      const err = await pending;
      expect((err as { name?: string }).name).toBe("AbortError");
    } finally {
      vi.useRealTimers();
    }
  });

  it("base64-encodes compose bytes for the Deploy contract", () => {
    expect(utf8ToBase64("services: {}")).toBe(
      Buffer.from("services: {}", "utf8").toString("base64"),
    );
    // 非 ASCII（UTF-8 多字节）不炸。
    expect(utf8ToBase64("créé ✓")).toBe(
      Buffer.from("créé ✓", "utf8").toString("base64"),
    );
  });
});
