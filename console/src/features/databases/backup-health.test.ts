import { describe, expect, it } from "vitest";
import { backupHealth } from "./backup-health";

describe("backupHealth", () => {
  const now = new Date("2026-10-09T12:00:00Z");

  it("never-backed-up rows read neutral", () => {
    expect(backupHealth(undefined, now)).toEqual({ label: "never", tone: "neutral" });
    expect(backupHealth("", now)).toEqual({ label: "never", tone: "neutral" });
    expect(backupHealth("not-a-date", now)).toEqual({ label: "never", tone: "neutral" });
  });

  it("recent successes read ok", () => {
    expect(backupHealth("2026-10-09T09:00:00Z", now)).toEqual({ label: "3h ago · ok", tone: "success" });
    expect(backupHealth("2026-10-09T12:00:30Z", now)).toEqual({ label: "<1h ago · ok", tone: "success" });
  });

  it("rows older than 48h read stale", () => {
    expect(backupHealth("2026-10-08T13:00:00Z", now)).toEqual({ label: "23h ago · ok", tone: "success" });
    expect(backupHealth("2026-10-07T13:00:00Z", now)).toEqual({ label: "47h ago · ok", tone: "success" });
    expect(backupHealth("2026-10-07T12:00:00Z", now)).toEqual({ label: "2d ago · stale", tone: "warning" });
    expect(backupHealth("2026-10-07T11:00:00Z", now)).toEqual({ label: "2d ago · stale", tone: "warning" });
  });
});
