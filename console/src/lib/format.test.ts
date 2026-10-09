import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { formatAbsolute, formatBytes, formatDuration, formatRelative, shortId } from "./format";

// formatRelative/Absolute 依赖当前钟——钉死系统时间断言确定性。
describe("format", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-09T14:30:00Z"));
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  describe("formatRelative", () => {
    it("renders RFC3339 as compact relative", () => {
      expect(formatRelative("2026-10-09T14:27:00Z")).toBe("3m ago");
      expect(formatRelative("2026-10-09T14:29:45Z")).toBe("15s ago");
      expect(formatRelative("2026-10-08T14:30:00Z")).toBe("1d ago");
    });
    it("handles timestamps without Z suffix (protojson leniency)", () => {
      expect(formatRelative("2026-10-09T14:29:30")).toMatch(/ago$/);
    });
    it("returns empty for empty and raw for invalid (honest boundary)", () => {
      expect(formatRelative(undefined)).toBe("");
      expect(formatRelative("")).toBe("");
      expect(formatRelative("not-a-date")).toBe("not-a-date");
    });
  });

  describe("formatAbsolute", () => {
    it("renders local absolute form", () => {
      expect(formatAbsolute("2026-10-09T14:27:00Z")).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
    });
    it("returns empty for empty", () => {
      expect(formatAbsolute(undefined)).toBe("");
    });
  });

  describe("formatBytes", () => {
    it("scales to human units", () => {
      expect(formatBytes(512)).toBe("512 B");
      expect(formatBytes(2048)).toBe("2.0 KiB");
      expect(formatBytes(5 * 1024 ** 2)).toBe("5.0 MiB");
      expect(formatBytes("1610612736")).toBe("1.50 GiB");
    });
    it("renders dash for non-finite", () => {
      expect(formatBytes(undefined)).toBe("—");
      expect(formatBytes("abc")).toBe("—");
    });
  });

  describe("formatDuration", () => {
    it("renders seconds/minutes/hours", () => {
      expect(formatDuration(0.4)).toBe("400ms");
      expect(formatDuration(38)).toBe("38s");
      expect(formatDuration(134)).toBe("2m 14s");
      expect(formatDuration(3720)).toBe("1h 02m");
    });
    it("renders dash for invalid", () => {
      expect(formatDuration(null)).toBe("—");
    });
  });

  describe("shortId", () => {
    it("truncates long ids", () => {
      expect(shortId("app_01j9x7q2m4n8r6")).toBe("app_01j9x7…");
    });
    it("renders dash for empty", () => {
      expect(shortId(undefined)).toBe("—");
    });
  });
});
