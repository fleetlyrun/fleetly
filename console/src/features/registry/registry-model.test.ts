import { describe, expect, it } from "vitest";
import { digestByRevisionId, latestDeploymentPerApp } from "./registry-model";

describe("latestDeploymentPerApp", () => {
  it("keeps the first deployment seen per app (list is newest-first)", () => {
    const deployments = [
      { id: "d2", app_id: "a1", to_revision: "r3" },
      { id: "d1", app_id: "a1", to_revision: "r1" },
      { id: "d9", app_id: "a2", to_revision: "r7" },
      { id: "d0", app_id: "", to_revision: "r0" },
    ] as never[];
    const latest = latestDeploymentPerApp(deployments);
    expect(latest.get("a1")?.id).toBe("d2");
    expect(latest.get("a2")?.id).toBe("d9");
    expect(latest.size).toBe(2);
  });

  it("tolerates undefined", () => {
    expect(latestDeploymentPerApp(undefined).size).toBe(0);
  });
});

describe("digestByRevisionId", () => {
  it("indexes digests across per-app revision lists, skipping digestless rows", () => {
    const index = digestByRevisionId([
      [{ id: "r1", digest: "sha256:a" } as never],
      [{ id: "r2", digest: "sha256:b" } as never, { id: "r3" } as never],
      undefined,
    ]);
    expect(index.get("r1")).toBe("sha256:a");
    expect(index.get("r2")).toBe("sha256:b");
    expect(index.has("r3")).toBe(false);
  });
});
