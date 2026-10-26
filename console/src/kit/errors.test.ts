import { describe, expect, it } from "vitest";
import { isApiError } from "@lynx-go/uikit-core/kernel";
import { ApiError as FetchApiError } from "@/api/client";
import { describeApiError, runCommandMapped, toApiError } from "./errors";

// kit/errors 单源锚：fetch ApiError → kernel 封闭集映射、结果两分矩阵、
// 文案口径（每 kind 有话可说——与 lib/api-errors describeError 同纪律）。

describe("toApiError", () => {
  it("401/403/404 → auth/forbidden/not-found", () => {
    expect(toApiError(new FetchApiError(401, "unauthenticated", "x"))).toEqual({ kind: "auth" });
    expect(toApiError(new FetchApiError(403, "permission_denied", "x"))).toEqual({ kind: "forbidden" });
    expect(toApiError(new FetchApiError(404, "not_found", "x"))).toEqual({ kind: "not-found" });
  });

  it("其余状态 → server 携带信封原文（code: message）", () => {
    expect(toApiError(new FetchApiError(500, "internal", "boom"))).toEqual({
      kind: "server",
      status: 500,
      message: "internal: boom",
    });
    expect(toApiError(new FetchApiError(400, "invalid_argument", "bad"))).toEqual({
      kind: "server",
      status: 400,
      message: "invalid_argument: bad",
    });
  });

  it("网络断层 TypeError → network；kernel 形直通", () => {
    const net = toApiError(new TypeError("Failed to fetch"));
    expect(net.kind).toBe("network");
    const kernel: Parameters<typeof toApiError>[0] = { kind: "not-found" };
    expect(toApiError(kernel)).toBe(kernel);
    expect(isApiError(toApiError("boom"))).toBe(true);
  });
});

describe("runCommandMapped", () => {
  it("ok 透传 payload", async () => {
    await expect(runCommandMapped(async () => 7)).resolves.toEqual({ status: "ok", payload: 7 });
  });

  it("400 → rejected 保留 apperr code", async () => {
    await expect(
      runCommandMapped(async () => {
        throw new FetchApiError(400, "name_taken", "name is taken");
      }),
    ).resolves.toEqual({ status: "rejected", code: "name_taken", message: "name is taken" });
  });

  it("409 → conflict", async () => {
    await expect(
      runCommandMapped(async () => {
        throw new FetchApiError(409, "already_exists", "exists");
      }),
    ).resolves.toEqual({ status: "conflict", message: "exists" });
  });

  it("其余 → throw kernel ApiError（treatAs 在 descriptor 裁决）", async () => {
    await expect(
      runCommandMapped(async () => {
        throw new FetchApiError(404, "not_found", "gone");
      }),
    ).rejects.toEqual({ kind: "not-found" });
  });
});

describe("describeApiError 文案口径", () => {
  it("每 kind 非空英文文案，server 附状态与信封", () => {
    expect(describeApiError({ kind: "auth" })).toMatch(/sign in/i);
    expect(describeApiError({ kind: "server", status: 502, message: "internal: bad gateway" })).toBe(
      "Server error 502: internal: bad gateway",
    );
    for (const e of [
      { kind: "network" },
      { kind: "forbidden" },
      { kind: "not-found" },
      { kind: "decode", detail: "bad json" },
    ] as const) {
      expect(describeApiError(e).length).toBeGreaterThan(5);
    }
  });
});
