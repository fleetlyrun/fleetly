import { describe, expect, it } from "vitest";
import { parseDokploy } from "./dokploy";

// dokploy 解析器 parity 锚（C3）：夹具 = CLI golden（templates_golden_test.go
// 的 dokployExportFixture）同源——同一份导出，Go 侧与 TS 侧计划必须一致
// （skip 文本逐位对齐是迁移钩子的诚实契约：不静默丢）。对象形态经
// JSON.stringify 还原为导出文本（composeContent 含真换行与 ${VAR} 孔，
// 与 Go 夹具的 JSON 转义形态解析等价）。
const dokployExportFixture = JSON.stringify({
  applications: [
    { applicationId: "app-1", name: "landing", buildType: "dockerimage", dockerImage: "nginx:1.27", env: "TITLE=hello" },
    { applicationId: "app-2", name: "builder", buildType: "dockerfile", repository: "github.com/acme/builder", env: "" },
  ],
  compose: [
    {
      composeId: "cmp-1",
      name: "echo-app",
      composeContent: 'services:\n  web:\n    image: hashicorp/http-echo:1.0\n    environment:\n      GREETING: ${GREETING}\n    ports: ["5678"]\n',
      env: "GREETING=hi",
    },
    {
      composeId: "cmp-2",
      name: "holey",
      composeContent: 'services:\n  web:\n    image: nginx:1.27\n    environment:\n      X: ${MISSING}\n',
      env: "",
    },
  ],
  domains: [
    { domainId: "dom-1", host: "landing.127.0.0.1.sslip.io", port: 80, applicationId: "app-1", serviceName: "" },
    { domainId: "dom-2", host: "echo.127.0.0.1.sslip.io", port: 5678, composeId: "cmp-1", serviceName: "web" },
  ],
  databases: [
    { databaseId: "db-1", name: "shop", type: "postgres" },
    { databaseId: "db-2", name: "maria", type: "mariadb" },
  ],
});

describe("parseDokploy", () => {
  it("parses the CLI golden fixture into the same plan (parity with internal/spec/dokploy.go)", () => {
    const plan = parseDokploy(dokployExportFixture);
    expect(plan.apps).toHaveLength(2);
    expect(plan.apps[0]).toMatchObject({
      name: "landing",
      image: "nginx:1.27",
      composeYaml: "",
      env: { TITLE: "hello" },
    });
    expect(plan.apps[0].routes).toEqual([{ host: "landing.127.0.0.1.sslip.io", process: "web", port: 80 }]);
    expect(plan.apps[1].name).toBe("echo-app");
    expect(plan.apps[1].image).toBe("");
    expect(plan.apps[1].composeYaml).toContain("GREETING: hi");
    expect(plan.apps[1].composeYaml).not.toContain("${GREETING}");
    expect(plan.apps[1].routes).toEqual([{ host: "echo.127.0.0.1.sslip.io", process: "web", port: 5678 }]);
    expect(plan.databases).toEqual([{ name: "shop", engine: "postgres" }]);
    expect(plan.skipped).toEqual([
      {
        kind: "application",
        item: "builder",
        reason:
          'build type "dockerfile" with repository "github.com/acme/builder" is a build-source app; re-deploy it from fleetly source intake (uploads or git push) and migrate only its configuration here',
      },
      {
        kind: "compose",
        item: "holey",
        reason:
          "compose references variables absent from the dokploy env panel (MISSING); fleetly compose intake lands unresolved holes as literal values, so this app is skipped rather than silently corrupted",
      },
      {
        kind: "database",
        item: "maria",
        reason: 'dokploy engine "mariadb" has no fleetly mapping (engines: postgres, mysql, mongo, redis)',
      },
    ]);
  });

  it("throws on invalid JSON and skips nameless draft rows", () => {
    expect(() => parseDokploy("not json")).toThrow(/not valid JSON/);
    const plan = parseDokploy(JSON.stringify({ applications: [{ applicationId: "x", buildType: "dockerimage", dockerImage: "nginx:1" }] }));
    expect(plan.apps).toHaveLength(0);
    expect(plan.skipped[0]).toMatchObject({ kind: "application", item: "x", reason: /carries no name/ });
  });

  it("defaults the route process to web and skips domains of unmapped resources", () => {
    const plan = parseDokploy(
      JSON.stringify({
        applications: [{ applicationId: "a1", name: "web", buildType: "dockerimage", dockerImage: "nginx:1" }],
        domains: [
          { domainId: "d1", host: "a.example.test", port: 8080, applicationId: "a1" },
          { domainId: "d2", host: "gone.example.test", port: 80, applicationId: "a2" },
        ],
      }),
    );
    expect(plan.apps[0].routes).toEqual([{ host: "a.example.test", process: "web", port: 8080 }]);
    expect(plan.skipped[0]).toMatchObject({ kind: "domain", item: "gone.example.test", reason: /itself unmapped/ });
  });
});
