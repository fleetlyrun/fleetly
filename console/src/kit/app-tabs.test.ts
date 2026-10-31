import { afterEach, describe, expect, it, vi } from "vitest";
import { appTabCommands } from "./app-tabs";

// App 子面写动词命令锚（批2 写路径命令化）：run 映射（端点/体/结果两分）
// + 失效清单（invalidates kit selector × legacyKeys 前缀桥）+ M2 scopeOf
// 同源（工厂闭包派生 {projectId, appId}——调用点零手传）。RestartProcess
// 的零 lineage 锚：失效面刻意不含 apps/deployments/spec——与 redeploy 的
// 分立是 ADR-0069 的存在理由，清单在此钉死防回归。

afterEach(() => {
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("restartProcess（ADR-0069 零 lineage）", () => {
  it("POST 进程分域端点 + 空体；零失效声明（kit exec-instances 读键靠 15s 轮询）+ scopeOf 闭包派生", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    const { restartProcess } = appTabCommands("P1", "A1");
    const result = await restartProcess.run({ process: "web" }, { projectId: "P1" });

    expect(result).toEqual({ status: "ok", payload: { process: "web" } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/v1/apps/A1/processes/web/restart");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({});
    // M2：失效身份经 scopeOf 从闭包派生；零 lineage 锚——无 invalidates、
    // 无 legacyKeys（exec-instances 旧前缀桥已无读者，F1 回流批审计退役）。
    expect(restartProcess.invalidates).toBeUndefined();
    expect(restartProcess.legacyKeys).toBeUndefined();
    expect(restartProcess.scopeOf?.({ process: "web" })).toEqual({ projectId: "P1", appId: "A1" });
  });

  it("400 走结果通道（rejected 保留信封 code）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "invalid_argument", message: "boom" }, 400)));

    const { restartProcess } = appTabCommands("P1", "A1");
    await expect(restartProcess.run({ process: "web" }, {})).resolves.toEqual({
      status: "rejected",
      code: "invalid_argument",
      message: "boom",
    });
  });
});

describe("deleteApp（失效清单锚 + B1 票 query 形态）", () => {
  it("DELETE app 端点；失效面 = apps/deployments 两 kit selector + deployments legacy 前缀（AppTile 存量键面）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    const { deleteApp } = appTabCommands("P1", "A1");
    const result = await deleteApp.run({ ticket: "tkt-1" }, { projectId: "P1" });

    expect(result?.status).toBe("ok");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    // DeleteApp 无 body（proto 注解），confirmation 走 query 参数——
    // B1 W4 拆除确认票的 REST 形态（api/structure.ts 生成面同形）。header
    // 形态不可行（gateway 生成物只从 query 装配字段，修复批 F4），带票
    // URL 以 cache:"no-store" 收口（不入浏览器 HTTP 缓存）。
    expect(url).toBe("/v1/apps/A1?confirmation.ticket=tkt-1");
    expect(init.method).toBe("DELETE");
    expect(init.cache).toBe("no-store");
    expect(deleteApp.invalidates).toEqual([{ resource: "apps" }, { resource: "deployments" }]);
    // catalog 前缀桥零读者退役；["deployments"] 服务 project-overview
    // AppTile 的裸键（["deployments","latest",appId]）——唯一存活存量键面。
    expect(deleteApp.legacyKeys?.({ projectId: "P1" })).toEqual([["deployments"]]);
    expect(deleteApp.scopeOf?.({})).toEqual({ projectId: "P1", appId: "A1" });
  });

  it("previewTeardown：POST 预演端点空体，回 PreviewTeardownResponse 原形；无失效面（只读孪生 + 铸票）", async () => {
    const payload = {
      cascade: [{ kind: "TEARDOWN_ITEM_KIND_WORKLOAD", name: "A1-web" }],
      kept: [{ kind: "KEPT_ITEM_KIND_VOLUME", reason: "volumes are project-scoped" }],
      driving_conflict: false,
      ticket: "tkt-2",
      ticket_expires_in: 600,
    };
    const fetchMock = vi.fn(async () => jsonResponse(payload));
    vi.stubGlobal("fetch", fetchMock);

    const { previewTeardown } = appTabCommands("P1", "A1");
    const result = await previewTeardown.run(void 0, { projectId: "P1" });

    expect(result).toEqual({ status: "ok", payload });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/v1/apps/A1/preview-teardown");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({});
    expect(previewTeardown.invalidates).toBeUndefined();
  });
});

describe("redeploy（声明式路径 + B1 W4 双击 skip）", () => {
  it("POST /v1/deployments (app_id + spec_file)；skip=true 附着 confirmation（Processes 暂存提交二击形态）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ deployment: { id: "D1" } }));
    vi.stubGlobal("fetch", fetchMock);

    const { redeploy } = appTabCommands("P1", "A1");
    const specFile = JSON.stringify({ processes: [{ name: "web", replicas: "2" }] });
    const result = await redeploy.run({ specFile, skip: true }, { projectId: "P1" });

    expect(result).toEqual({ status: "ok", payload: { deployment: { id: "D1" } } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/v1/deployments");
    expect(JSON.parse(String(init.body))).toEqual({
      app_id: "A1",
      spec_file: specFile,
      confirmation: { skip: true }, // --yes 逃生门通道，proof=skipped 进审计
    });
    expect(redeploy.invalidates).toEqual([{ resource: "deployments" }, { resource: "app-spec-sheet" }]);
  });
});
