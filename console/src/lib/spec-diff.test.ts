// spec-diff 单测：字段级 diff 的四态（改字段/加字段/删字段/空 diff）+
// 标识数组逐元素 + spec_hash 排除 + 解析失败诚实返回 null。

import { describe, expect, it } from "vitest";

import { diffSpecJSONs } from "@/lib/spec-diff";

interface ServiceFixture {
  name: string;
  image?: string;
  replicas?: number;
  environment?: { key: string; hash: string; source: string }[];
  domains?: string[];
}

function specJSON(services: ServiceFixture[], specHash = "deadbeef"): string {
  return JSON.stringify({
    name: "demo",
    services,
    spec_hash: specHash,
  });
}

describe("diffSpecJSONs", () => {
  it("reports changed fields (image, replicas) with old → new values", () => {
    const diffs = diffSpecJSONs(
      specJSON([{ name: "web", image: "nginx:1.26", replicas: 2 }]),
      specJSON([{ name: "web", image: "nginx:1.27", replicas: 3 }]),
    );
    expect(diffs).not.toBeNull();
    expect(diffs).toEqual([
      { path: "services.web.image", kind: "changed", old: "nginx:1.26", new: "nginx:1.27" },
      { path: "services.web.replicas", kind: "changed", old: "2", new: "3" },
    ]);
  });

  it("reports added and removed fields (env group appears, domain dropped)", () => {
    const diffs = diffSpecJSONs(
      specJSON([
        { name: "web", image: "nginx:1.26", domains: ["old.example.com"] },
      ]),
      specJSON([
        {
          name: "web",
          image: "nginx:1.26",
          environment: [
            { key: "LOG_LEVEL", hash: "aa11", source: "environment" },
          ],
        },
      ]),
    );
    expect(diffs).not.toBeNull();
    expect(diffs).toEqual([
      {
        path: "services.web.domains",
        kind: "removed",
        old: '["old.example.com"]',
        new: "—",
      },
      {
        path: "services.web.environment",
        kind: "added",
        old: "—",
        new: '[{"key":"LOG_LEVEL","hash":"aa11","source":"environment"}]',
      },
    ]);
  });

  it("diffs keyed array elements per key when present on both sides (env hash change)", () => {
    const diffs = diffSpecJSONs(
      specJSON([
        {
          name: "web",
          environment: [
            { key: "LOG_LEVEL", hash: "aa11", source: "environment" },
          ],
        },
      ]),
      specJSON([
        {
          name: "web",
          environment: [
            { key: "LOG_LEVEL", hash: "bb22", source: "environment" },
          ],
        },
      ]),
    );
    expect(diffs).not.toBeNull();
    expect(diffs).toContainEqual({
      path: "services.web.environment.LOG_LEVEL.hash",
      kind: "changed",
      old: "aa11",
      new: "bb22",
    });
  });

  it("returns an empty list for identical snapshots (redeploy, same revision content)", () => {
    const a = specJSON([{ name: "web", image: "nginx:1.27" }], "same");
    const diffs = diffSpecJSONs(a, a);
    expect(diffs).toEqual([]);
  });

  it("excludes the derived spec_hash from the diff (flips with any real change)", () => {
    const diffs = diffSpecJSONs(
      specJSON([{ name: "web", image: "nginx:1.26" }], "hash-a"),
      specJSON([{ name: "web", image: "nginx:1.27" }], "hash-b"),
    );
    expect(diffs).not.toBeNull();
    expect(diffs?.some((d) => d.path.includes("spec_hash"))).toBe(false);
    expect(diffs).toHaveLength(1);
  });

  it("returns null when a snapshot is not parseable JSON (honest failure, not a fake empty diff)", () => {
    expect(diffSpecJSONs("not json", specJSON([]))).toBeNull();
  });
});
