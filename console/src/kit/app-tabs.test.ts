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

describe("deleteApp（失效清单锚）", () => {
  it("DELETE app 端点；失效面 = apps/deployments 两 kit selector + deployments legacy 前缀（AppTile 存量键面）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    const { deleteApp } = appTabCommands("P1", "A1");
    const result = await deleteApp.run(undefined, { projectId: "P1" });

    expect(result?.status).toBe("ok");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/v1/apps/A1");
    expect(init.method).toBe("DELETE");
    expect(deleteApp.invalidates).toEqual([{ resource: "apps" }, { resource: "deployments" }]);
    // catalog 前缀桥零读者退役；["deployments"] 服务 project-overview
    // AppTile 的裸键（["deployments","latest",appId]）——唯一存活存量键面。
    expect(deleteApp.legacyKeys?.({ projectId: "P1" })).toEqual([["deployments"]]);
    expect(deleteApp.scopeOf?.(undefined)).toEqual({ projectId: "P1", appId: "A1" });
  });
});
