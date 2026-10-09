import { describe, expect, it } from "vitest";
import { ApiError } from "@/api/client";
import { describeError } from "./api-errors";

// 分状态诚实文案契约（万金油文案已被守卫禁用）。
describe("describeError", () => {
  it("classifies auth/permission/missing/server states", () => {
    expect(describeError(new ApiError(401, "E_AUTH", "bad token")).title).toBe("Session expired");
    expect(describeError(new ApiError(403, "E_FORBIDDEN", "scope")).title).toBe("Not allowed");
    expect(describeError(new ApiError(404, "E_NOT_FOUND", "gone")).title).toBe("Not found");
    expect(describeError(new ApiError(503, "E_INTERNAL", "down")).title).toBe("Server error");
    expect(describeError(new ApiError(400, "E_INVALID_ARGUMENT", "bad")).title).toBe("Request rejected");
  });

  it("keeps the honest envelope in detail", () => {
    const described = describeError(new ApiError(403, "E_FORBIDDEN", "role scope"));
    expect(described.detail).toBe("E_FORBIDDEN: role scope");
  });

  it("classifies network failures", () => {
    expect(describeError(new TypeError("Failed to fetch")).title).toBe("Cannot reach the API");
  });

  it("falls back for unknown shapes", () => {
    expect(describeError("boom").title).toBe("Something went wrong");
    expect(describeError("boom").detail).toBe("boom");
  });
});
