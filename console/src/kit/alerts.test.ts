import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import {
  alertRulesResource,
  alertStatesLookup,
  alertStatesResource,
  channelsResource,
  channelsSearch,
  rulesSearch,
} from "./alerts";

// 告警域适配器单测（批1 Wave2-A；uikit 0.5.0 回流批改 restFetchAll 口径）：
// rules/channels 端点锚、protojson 行收敛、双 tab 共享 schema 的 URL 编解码
// 往返（平台域无 scope——fetch 恒发）。fetch 面 stub 在 global（authFetch→
// 裸 fetch 管线）；断言经资源适配器调用取数。

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

describe("alertRulesResource/channelsResource 读面（端点 + 收敛）", () => {
  it("整取 /v1/alerts/rules 并展平 undefined 行（不滤缺字段行）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        rules: [
          { id: "r1", app_id: "app_1", metric: "cpu_percent", threshold: 90 },
          undefined,
          { app_id: "app_2" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await alertRulesResource.collection!(ALL, {}, undefined)).rows;

    expect(rows).toEqual([
      { id: "r1", app_id: "app_1", metric: "cpu_percent", threshold: 90 },
      { app_id: "app_2" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/alerts/rules");
  });

  it("channels 打 /v1/channels 同口径收敛", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ channels: [{ id: "c1", name: "ops-hook", kind: "webhook" }, undefined] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await channelsResource.collection!(ALL, {}, undefined)).rows;

    expect(rows).toEqual([{ id: "c1", name: "ops-hook", kind: "webhook" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/channels");
  });

  it("非 2xx 收敛 kernel ApiError（工厂 Response→ApiError 单源）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(alertRulesResource.collection!(ALL, {}, undefined)).rejects.toMatchObject({
      kind: "server",
      status: 503,
      message: "boom",
    });
    await expect(channelsResource.collection!(ALL, {}, undefined)).rejects.toMatchObject({
      kind: "server",
      status: 503,
      message: "boom",
    });
  });
});

describe("rulesSearch/channelsSearch（URL 编解码契约，双 namespace 实例）", () => {
  it("q 编码往返为并集字段集的 contains 谓词；decode 容忍缺省", () => {
    const fields = ["app_id", "metric", "name", "kind"];
    const encoded = rulesSearch.encode({
      predicate: textPredicate(fields, "cpu"),
      window: { size: 20 },
    });
    // namespace 物理键（F1 回流批）：rules.q；channels 键不携带
    expect(encoded["rules.q"]).toBe("cpu");
    const decoded = rulesSearch.decode({ "rules.q": "cpu" });
    expect(channelsSearch.decode({ "rules.q": "cpu" }).predicate).toBeUndefined();
    expect(decoded.predicate).toEqual(textPredicate(fields, "cpu"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = rulesSearch.patch({ predicate: null });
    expect("rules.q" in patch).toBe(true);
    expect(patch["rules.q"]).toBeUndefined();
    expect(patch["rules.page"]).toBeUndefined();
  });
});

describe("alertRules commands（批2 写路径命令锚）", () => {
  it("create：POST /v1/alerts/rules 载荷形锚（threshold Number / for_seconds 字符串）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    const result = await alertRulesResource.commands.create.run(
      { appId: "a1", metric: "cpu_percent", threshold: 90, forSeconds: "0" },
      {},
    );

    expect(result).toMatchObject({ status: "ok" });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toContain("/v1/alerts/rules");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      app_id: "a1",
      metric: "cpu_percent",
      threshold: 90,
      for_seconds: "0",
    });
  });

  it("delete 命令端点锚；legacyKeys 桥零读者退役（F1 回流批审计）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);
    await alertRulesResource.commands.delete.run({ id: "r1" });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/alerts/rules/r1");
    expect(init.method).toBe("DELETE");
    expect(alertRulesResource.commands.delete.legacyKeys).toBeUndefined();
    expect(channelsResource.commands.delete).toBeDefined();
  });
});

// ---- alert states（2026-10-27 批4 读面收编：catalog useAlertStates 退役） ----

describe("alertStatesResource 读面（端点 + 收敛）", () => {
  it("GET /v1/alerts 并展平 undefined 行（restFetchAll 信封过滤）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ states: [{ rule_id: "r1", app_id: "A1", state: "firing" }, undefined] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await alertStatesResource.collection!(ALL, {}, undefined)).rows;

    expect(rows).toEqual([{ rule_id: "r1", app_id: "A1", state: "firing" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/alerts");
  });

  it("非 2xx 收敛 kernel ApiError；平台域无 scope（states 桥零读者退役）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(alertStatesResource.collection!(ALL, {}, undefined)).rejects.toMatchObject({
      kind: "server",
      status: 503,
      message: "boom",
    });
    expect(alertStatesResource.scope).toBeUndefined();
    expect(alertStatesResource.legacyKeys).toBeUndefined();
    expect(alertStatesLookup.decode({}).window.size).toBe(200);
  });
});
