import { describe, expect, it } from "vitest";
import { firingAlerts } from "./alert-badges";

describe("firingAlerts", () => {
  const states = [
    { rule_id: "r1", app_id: "app1", state: "firing", metric: "m" },
    { rule_id: "r2", app_id: "app1", state: "ok", metric: "m" },
    { rule_id: "r3", app_id: "app2", state: "firing", metric: "m" },
    { rule_id: "r4", app_id: undefined, state: "firing", metric: "m", system: true },
  ];

  it("keeps only firing rows of the given app", () => {
    expect(firingAlerts(states as never, "app1")).toHaveLength(1);
    expect(firingAlerts(states as never, "app1")[0]?.rule_id).toBe("r1");
    expect(firingAlerts(states as never, "app2")).toHaveLength(1);
    expect(firingAlerts(states as never, "app3")).toHaveLength(0);
  });

  it("tolerates undefined input", () => {
    expect(firingAlerts(undefined, "app1")).toEqual([]);
  });
});
