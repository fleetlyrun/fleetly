// subjectLabel / nodeLabel 单测（2026-09-26 走查 W2-7）：kind:ref 的可读
// 投影收敛单点——命中缓存出业务名；反解不到的平台 ID 降级为 kind；可读
// ref 原样保留；节点裸平台 ID（n_ 前缀）出主机名。

import { describe, expect, it } from "vitest";

import { nodeLabel, subjectLabel, type SubjectLookup } from "./subject";

const LOOKUP: SubjectLookup = {
  apps: [{ id: "01M39V8KDQNFWETYMJC5WRV4W2", name: "demo" }],
  databases: [{ id: "01M39VEWZJSMA78B5AD5DMKSEB", name: "pgshared" }],
  teams: [{ id: "01M39V8K0MKV4ZKMZRRYZ6ZPX9", name: "Founder" }],
  projects: [
    {
      id: "01M39V8K0MKV4ZKMZRS6RDPCKD",
      slug: "default",
      team_slug: "founder",
    },
  ],
  nodes: [{ platform_id: "n_01M39V6QJBTQ0JQXKV4DZXZWJ0", hostname: "fleetly-dev" }],
  users: [{ id: "01M39V8K0KAD8ZT2AS1M6NFF1K", email: "founder@fleetly.run" }],
};

describe("subjectLabel", () => {
  it("resolves cached app/team/database ids to business names", () => {
    expect(subjectLabel("app:01M39V8KDQNFWETYMJC5WRV4W2", LOOKUP)).toBe("app:demo");
    expect(subjectLabel("team:01M39V8K0MKV4ZKMZRRYZ6ZPX9", LOOKUP)).toBe("team:Founder");
    expect(subjectLabel("database:01M39VEWZJSMA78B5AD5DMKSEB", LOOKUP)).toBe(
      "database:pgshared",
    );
  });

  it("resolves project ids to team/prj", () => {
    expect(subjectLabel("project:01M39V8K0MKV4ZKMZRS6RDPCKD", LOOKUP)).toBe(
      "project:founder/default",
    );
  });

  it("resolves user ids to emails (admin users cache)", () => {
    expect(subjectLabel("user:01M39V8K0KAD8ZT2AS1M6NFF1K", LOOKUP)).toBe(
      "user:founder@fleetly.run",
    );
  });

  it("degrades unresolved platform ids to the kind alone", () => {
    expect(subjectLabel("user:01M3DX7823V0X5T02P08WQHGEG", LOOKUP)).toBe("user");
    expect(subjectLabel("deployment:01M3DYK3GSN8QRQT5T2G1F4FY7", LOOKUP)).toBe(
      "deployment",
    );
  });

  it("keeps readable refs verbatim", () => {
    expect(subjectLabel("database:pgshared", LOOKUP)).toBe("database:pgshared");
    expect(subjectLabel("platform:metrics", LOOKUP)).toBe("platform:metrics");
    expect(subjectLabel("app:demo", LOOKUP)).toBe("app:demo");
  });

  it("passes through subjects without a kind prefix", () => {
    expect(subjectLabel("bare-string", LOOKUP)).toBe("bare-string");
    expect(subjectLabel("", LOOKUP)).toBe("");
    expect(subjectLabel(undefined, LOOKUP)).toBe("");
  });

  it("resolves bare n_-prefixed placement ids to hostnames", () => {
    expect(subjectLabel("n_01M39V6QJBTQ0JQXKV4DZXZWJ0", LOOKUP)).toBe("fleetly-dev");
    expect(subjectLabel("n_01MZZZZZZZZZZZZZZZZZZZZZZZ", LOOKUP)).toBe(
      "n_01MZZZZZZZZZZZZZZZZZZZZZZZ",
    );
  });

  it("handles an empty lookup without throwing", () => {
    expect(subjectLabel("app:01M39V8KDQNFWETYMJC5WRV4W2", {})).toBe("app");
  });
});

describe("nodeLabel", () => {
  it("resolves n_-prefixed platform ids to hostnames", () => {
    expect(nodeLabel("n_01M39V6QJBTQ0JQXKV4DZXZWJ0", LOOKUP.nodes)).toBe("fleetly-dev");
  });

  it("resolves node:<id> subjects", () => {
    expect(nodeLabel("node:n_01M39V6QJBTQ0JQXKV4DZXZWJ0", LOOKUP.nodes)).toBe(
      "fleetly-dev",
    );
  });

  it("returns the ref unchanged when unresolved", () => {
    expect(nodeLabel("n_01MZZZZZZZZZZZZZZZZZZZZZZZ", LOOKUP.nodes)).toBe(
      "n_01MZZZZZZZZZZZZZZZZZZZZZZZ",
    );
    expect(nodeLabel(undefined, LOOKUP.nodes)).toBe("");
  });
});
