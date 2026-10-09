import { describe, expect, it } from "vitest";
import { formatFreshness, lastPointValue } from "./freshness";

describe("lastPointValue", () => {
  it("returns the first finite last-point across series (aggregation query is single-series)", () => {
    const series = [
      { points: [{ time: "1", value: 3 }, { time: "2", value: 7 }] },
      { points: [{ time: "3", value: 1 }] },
    ] as never;
    expect(lastPointValue(series as never)).toBe(7);
    expect(lastPointValue(undefined)).toBeUndefined();
    expect(lastPointValue([{ points: [{ time: "1", value: Number.NaN }] }] as never)).toBeUndefined();
  });
});

describe("formatFreshness", () => {
  it("formats by push-chain thresholds", () => {
    expect(formatFreshness(12)).toEqual({ label: "12s ago", tone: "success" });
    expect(formatFreshness(299)).toEqual({ label: "299s ago", tone: "success" });
    expect(formatFreshness(600)).toEqual({ label: "10m ago", tone: "warning" });
    expect(formatFreshness(7200)).toEqual({ label: "2h ago", tone: "danger" });
    expect(formatFreshness(undefined)).toEqual({ label: "n/a", tone: "neutral" });
  });
});
