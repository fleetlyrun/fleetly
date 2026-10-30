import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import type { DeployFormState } from "@/lib/deployPayload";
import {
  appDeploymentsLookup,
  appDeploymentsResource,
  deploymentsResource,
  deploymentsSearch,
} from "./deployments";

// 项目级部署 fan-out 适配器单测（批1；uikit 0.5.0 回流批改 restFetchAll
// 口径）：apps 整取 → 逐 app deployments 合并按 updated_at 倒序、per-app
// 失败吞为空、空 scope/无 app 不发请求、URL 编解码往返。apps 段与
// deployments 段全走 authFetch 管线（同一 global stub 覆盖）。
// 批2 增写路径命令锚：五源 deploy 载荷、redeploy 两步链（含 OCC
// restart_seq 陈旧的 409→conflict 诚实面）、rollback/hookRefresh 端点、
// legacyKeys 桥。

afterEach(() => {
  vi.unstubAllGlobals();
  setToken("");
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const ALL = { window: { size: 20 } } as const;

const IMAGE_FORM: DeployFormState = {
  appId: "a1",
  source: "image",
  image: "nginx:1.27",
  processName: "",
  port: "",
  protocol: "",
  envText: "",
  httpProbe: "",
  tcpProbe: "",
  composeYaml: "",
  specJson: "",
  uploadId: "",
  builder: "dockerfile",
  dockerfile: "",
  railpackVersion: "",
  outputDir: "",
  idempotencyKey: "",
  commitSha: "",
  supersede: false,
};

describe("deploymentsResource 读面（fan-out 复刻）", () => {
  it("apps → 逐 app /v1/deployments?app_id 合并，按 updated_at 倒序收敛", async () => {
    const fetchMock = vi.fn(async (input: unknown) => {
      const url = String(input);
      if (url.includes("/v1/apps")) {
        return jsonResponse({
          apps: [
            { id: "a1", name: "web", project_id: "shop", created_at: "2026-01-01T00:00:00Z" },
            { id: "a2", name: "api", project_id: "shop", created_at: "2026-01-01T00:00:00Z" },
          ],
        });
      }
      if (url.includes("app_id=a1")) {
        return jsonResponse({ deployments: [{ id: "d1", app_id: "a1", updated_at: "2026-02-02T00:00:00Z" }] });
      }
      if (url.includes("app_id=a2")) {
        return jsonResponse({
          deployments: [{ id: "d2", app_id: "a2", updated_at: "2026-01-01T00:00:00Z" }, undefined, { app_id: "a2" }],
        });
      }
      return jsonResponse({}, 404);
    });
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await deploymentsResource.collection!(ALL, { projectId: "shop" }, undefined)).rows;

    expect(rows.map((row) => row.id)).toEqual(["d1", "d2"]);
    const urls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(urls.some((url) => url.includes("/v1/deployments?app_id=a1&limit=20"))).toBe(true);
    expect(urls.some((url) => url.includes("/v1/deployments?app_id=a2&limit=20"))).toBe(true);
  });

  it("空 projectId 不发请求", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await deploymentsResource.collection!(ALL, { projectId: "" }, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("单 app deployments 失败吞为空段（fan-out 局部失败不塌）", async () => {
    const fetchMock = vi.fn(async (input: unknown) => {
      const url = String(input);
      if (url.includes("/v1/apps")) {
        return jsonResponse({ apps: [{ id: "a1", name: "web", project_id: "shop", created_at: "" }] });
      }
      return jsonResponse({ code: "E_X", message: "boom" }, 503);
    });
    vi.stubGlobal("fetch", fetchMock);
    expect((await deploymentsResource.collection!(ALL, { projectId: "shop" }, undefined)).rows).toEqual([]);
  });
});

describe("deploymentsSearch（URL 编解码契约）", () => {
  it("q 编码往返为 contains 谓词；decode 容忍缺省", () => {
    const fields = ["id", "app_id", "state", "to_revision"];
    const encoded = deploymentsSearch.encode({
      predicate: textPredicate(fields, "running"),
      window: { size: 20 },
    });
    expect(encoded.q).toBe("running");
    const decoded = deploymentsSearch.decode({ q: "running" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "running"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = deploymentsSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});

describe("deployments commands（批2 写路径命令锚）", () => {
  it("deploy：五源表单状态经 buildDeployPayload 后 POST /v1/deployments", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ deployment: { id: "dep_1" } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await deploymentsResource.commands.deploy.run(IMAGE_FORM, { projectId: "shop" });

    expect(result).toEqual({ status: "ok", payload: { deployment: { id: "dep_1" } } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/deployments");
    expect(init.method).toBe("POST");
    // 载荷单源 lib/deployPayload：image 源只发 app_id/image（空值键缺席）
    expect(JSON.parse(String(init.body))).toEqual({ app_id: "a1", image: "nginx:1.27" });
  });

  it("deploy：spec/current 源 spec_file 键（restart_seq 随冻结体原样携带）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ deployment: { id: "dep_2" } }));
    vi.stubGlobal("fetch", fetchMock);
    const specJson = JSON.stringify({ processes: [{ name: "web", restart_seq: 3 }] });

    await deploymentsResource.commands.deploy.run({ ...IMAGE_FORM, source: "spec", specJson }, { projectId: "shop" });

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({ app_id: "a1", spec_file: specJson });
  });

  it("deploy：OCC 陈旧序数的服务端拒绝（409）→ conflict 结果，不复现为 ok", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ code: "E_CONFLICT", message: "spec was changed concurrently" }, 409)),
    );
    const result = await deploymentsResource.commands.deploy.run(
      { ...IMAGE_FORM, source: "current", specJson: "{}" },
      { projectId: "shop" },
    );
    expect(result).toEqual({ status: "conflict", message: "spec was changed concurrently" });
  });

  it("redeploy：回读冻结 Spec → 原样 spec_file 第四源提交", async () => {
    const frozenSpec = { processes: [{ name: "web", restart_seq: 3 }] };
    const fetchMock = vi.fn(async (input: unknown) => {
      if (String(input).includes("/spec")) {
        return jsonResponse({ spec: frozenSpec, revision_seq: "5" });
      }
      return jsonResponse({ deployment: { id: "dep_3" } });
    });
    vi.stubGlobal("fetch", fetchMock);

    const result = await deploymentsResource.commands.redeploy.run({ appId: "a1" }, { projectId: "shop" });

    expect(result).toEqual({ status: "ok", payload: { deploymentId: "dep_3" } });
    const [url, init] = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/deployments");
    expect(JSON.parse(String(init.body))).toEqual({ app_id: "a1", spec_file: JSON.stringify(frozenSpec) });
  });

  it("redeploy：未部署（GetAppSpec 404 → 无冻结体）→ 诚实 rejected", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: unknown) => (String(input).includes("/spec") ? jsonResponse({}, 404) : jsonResponse({}))),
    );
    const result = await deploymentsResource.commands.redeploy.run({ appId: "a1" }, { projectId: "shop" });
    expect(result).toEqual({ status: "rejected", message: "the app has no frozen spec yet — deploy first" });
  });

  it("rollback：POST /v1/apps/{id}/rollback → deploymentId 投影", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ deployment: { id: "dep_4" } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await deploymentsResource.commands.rollback.run({ appId: "a1" }, { projectId: "shop" });

    expect(result).toEqual({ status: "ok", payload: { deploymentId: "dep_4" } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/apps/a1/rollback");
    expect(init.method).toBe("POST");
    // B1 W4：无票形态 body 空体（首击——服务端 ConfirmableVerbs 拒 REQUIRED）。
    expect(JSON.parse(String(init.body))).toEqual({});
  });

  // B1 W4 确认闭环命令锚：
  it("deploy 带 ticket：confirmation.ticket 附加在诚实载荷上（其余键零变化）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ deployment: { id: "dep_6" } }));
    vi.stubGlobal("fetch", fetchMock);

    await deploymentsResource.commands.deploy.run({ ...IMAGE_FORM, ticket: "tkt-1" }, { projectId: "shop" });

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      app_id: "a1",
      image: "nginx:1.27",
      confirmation: { ticket: "tkt-1" },
    });
  });

  it("previewDeploy：POST /v1/deployments/preview，体 = {deploy: 同一 DeployRequest}", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ ticket: "tkt-2", ticket_expires_in: 600 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await deploymentsResource.commands.previewDeploy.run(IMAGE_FORM, { projectId: "shop" });

    expect(result).toEqual({ status: "ok", payload: { ticket: "tkt-2", ticket_expires_in: 600 } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/deployments/preview");
    expect(init.method).toBe("POST");
    // 内嵌同一 DeployRequest（形状层单源，设计稿 §1.3）。
    expect(JSON.parse(String(init.body))).toEqual({ deploy: { app_id: "a1", image: "nginx:1.27" } });
  });

  it("rollback 双击二段：skip=true 显式跳过（--yes 逃生门通道）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ deployment: { id: "dep_7" } }));
    vi.stubGlobal("fetch", fetchMock);

    await deploymentsResource.commands.rollback.run({ appId: "a1", skip: true }, { projectId: "shop" });

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({ confirmation: { skip: true } });
  });

  // B1 W4 遗留直发面收口（2026-10-31）：deploy/redeploy 的 skip 形态——
  // Spec 编辑器与 Variables 的 deploy step / hero Redeploy 经
  // ConfirmSkipDialog 双击确认，二击携 skip=true（proof=skipped 进审计）。
  it("deploy 双击二段：skip=true 附加在诚实载荷上（spec_file 源）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ deployment: { id: "dep_8" } }));
    vi.stubGlobal("fetch", fetchMock);
    const specJson = JSON.stringify({ processes: [{ name: "web" }] });

    await deploymentsResource.commands.deploy.run(
      { ...IMAGE_FORM, source: "spec", specJson, skip: true },
      { projectId: "shop" },
    );

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      app_id: "a1",
      spec_file: specJson,
      confirmation: { skip: true },
    });
  });

  it("redeploy 双击二段：skip=true 附着在冻结体重发载荷上", async () => {
    const frozenSpec = { processes: [{ name: "web" }] };
    const fetchMock = vi.fn(async (input: unknown) => {
      if (String(input).includes("/spec")) return jsonResponse({ spec: frozenSpec });
      return jsonResponse({ deployment: { id: "dep_9" } });
    });
    vi.stubGlobal("fetch", fetchMock);

    await deploymentsResource.commands.redeploy.run({ appId: "a1", skip: true }, { projectId: "shop" });

    const [, init] = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      app_id: "a1",
      spec_file: JSON.stringify(frozenSpec),
      confirmation: { skip: true },
    });
  });

  it("hookRefresh：POST /v1/apps/{id}/hook/refresh → deploymentId/commit/outcome 投影", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ deployment_id: "dep_5", commit: "abc1234", admission: { outcome: "deduplicated" } }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const result = await deploymentsResource.commands.hookRefresh.run({ appId: "a1" }, { projectId: "shop" });

    expect(result).toEqual({
      status: "ok",
      payload: { deploymentId: "dep_5", commit: "abc1234", outcome: "deduplicated" },
    });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/apps/a1/hook/refresh");
    expect(init.method).toBe("POST");
  });

  it("legacyKeys 桥对齐旧 invalidate 键（[\"deployments\"] 前缀盖 hooks 双形态）", () => {
    expect(deploymentsResource.commands.deploy.legacyKeys?.({ projectId: "shop" })).toEqual([["deployments"]]);
    expect(deploymentsResource.commands.rollback.legacyKeys?.({})).toEqual([["deployments"]]);
  });
});

// ---- per-App 部署集合（2026-10-27 批4 读面收编：features/deployments/
// hooks.ts 的 useAppDeployments 退役——行级/hero 消费按 app 键共享缓存，
// 不放大成项目 fan-out） ----

describe("appDeploymentsResource 读面（端点 + 收敛）", () => {
  it("GET /v1/deployments?app_id&limit=50，id 缺行过滤同旧 hooks rowsOf 口径", async () => {
    const fetchMock = vi.fn(async (path: string) => {
      if (path.startsWith("/v1/deployments")) {
        return jsonResponse({
          deployments: [{ id: "D2", app_id: "A1", updated_at: "2026-10-18T00:00:00Z" }, { app_id: "A1" }, undefined],
        });
      }
      return jsonResponse({}, 404);
    });
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await appDeploymentsResource.collection!(
      appDeploymentsLookup.decode({}),
      { projectId: "P1", appId: "A1" },
      undefined,
    )).rows;

    expect(rows).toEqual([{ id: "D2", app_id: "A1", updated_at: "2026-10-18T00:00:00Z" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/deployments?app_id=A1&limit=50");
  });

  it("scope 缺 appId 时零请求空段（守卫门——catalog enabled 门同语义）", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect((await appDeploymentsResource.collection!(appDeploymentsLookup.decode({}), { projectId: "P1" }, undefined)).rows).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("非 2xx 收敛 kernel ApiError；scope 声明与存量键桥对齐 hooks 前缀", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(
      appDeploymentsResource.collection!(appDeploymentsLookup.decode({}), { projectId: "P1", appId: "A1" }, undefined),
    ).rejects.toMatchObject({ kind: "server", status: 503, message: "boom" });
    expect(appDeploymentsResource.scope).toEqual(["projectId", "appId"]);
    expect(appDeploymentsResource.legacyKeys?.({ projectId: "P1", appId: "A1" })).toEqual([["deployments"]]);
    expect(appDeploymentsLookup.decode({}).window.size).toBe(200);
  });
});
