import { describe, expect, it } from "vitest";
import { hashToPath } from "./legacy-hash";

// 旧 hash 深链迁移 shim 契约。
describe("legacy-hash", () => {
  it("maps legacy flat routes to clean paths", () => {
    expect(hashToPath("#/apps")).toBe("/apps");
    expect(hashToPath("#/templates")).toBe("/templates");
    expect(hashToPath("#/")).toBeNull();
  });

  it("maps deployment detail deep links", () => {
    expect(hashToPath("#/deployments/dpl_01j9ya7c")).toBe("/deployments/dpl_01j9ya7c");
    expect(hashToPath("#/deployments")).toBe("/deployments");
  });

  it("ignores unknown and non-hash forms", () => {
    expect(hashToPath("#/unknown-page")).toBeNull();
    expect(hashToPath("")).toBeNull();
    expect(hashToPath("#not-a-route")).toBeNull();
  });
});
