import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { collectionQueryOptions, invalidateAfterCommand } from "@lynx-go/uikit-core";
import { setToken } from "@/lib/token";
import {
  freezesResource,
  platformBackupsResource,
  sessionResource,
  tokensResource,
} from "./settings";

// 平台设置域资源锚（批2 写面 useCommand 化 + 批4 读面 kit 化）：写面命令
// 锚沿用（映射 + legacyKeys 桥）；读面锚 session record（whoami 收敛形）与
// tokens/freezes/platform-backups 三 fetcher 端点。fetch 面 stub 在 global
//（fetchRows/apiFetch 走裸 fetch 管线）。

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

describe("命令锚：freezes freeze（映射 + 存量键桥）", () => {
  it("ok → kit freezes 集合键失效；freeze 打 POST /v1/governance/freezes", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);
    const result = await freezesResource.commands.freeze.run({ reason: "maintenance window" });
    expect(result.status).toBe("ok");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/governance/freezes");
    expect(JSON.parse(String(init.body))).toEqual({ reason: "maintenance window" });

    // [settings,freezes] 旧键桥零读者退役（F1 回流批审计）——kit selector
    // 单源命中 freezes 集合键。
    const queryClient = new QueryClient();
    const kitKey = collectionQueryOptions(freezesResource, { window: { size: 100 } }).queryKey;
    queryClient.setQueryData(kitKey, { rows: [] });
    invalidateAfterCommand(queryClient, freezesResource.commands.freeze, {});
    expect(queryClient.getQueryState(kitKey)?.isInvalidated).toBe(true);
  });

  it("400 → rejected 保留 apperr code；mint 失效 tokens 集合（收尾批翻转：mint 后台账即时可见）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_TOKEN_NAME_TAKEN", message: "name taken" }, 400)));
    await expect(tokensResource.commands.mint.run({ name: "ci" })).resolves.toEqual({
      status: "rejected",
      code: "E_TOKEN_NAME_TAKEN",
      message: "name taken",
    });

    const queryClient = new QueryClient();
    // kit 键经 collectionQueryOptions 单源取（窗口与 settings.tsx allRowsQuery
    // 同形 size 100）
    const collectionKey = collectionQueryOptions(tokensResource, { window: { size: 100 } }).queryKey;
    queryClient.setQueryData(collectionKey, { rows: [], total: 0 });
    invalidateAfterCommand(queryClient, tokensResource.commands.mint, {});
    expect(queryClient.getQueryState(collectionKey)?.isInvalidated).toBe(true);
  });
});

// 读面锚（批4 → F1 回流批 D 组改 rest 工厂口径）：session record + 三集合
// 走 restCollection/restRecord——经资源适配器调用（端点/信封展平/undefined
// 行过滤/whoami camelCase 收敛钉死；错误收敛是工厂内置 Response→ApiError）。
const ALL = { window: { size: 100 } } as const;

describe("读面锚：session record + settings 三集合（rest 工厂）", () => {
  it("session record 打 GET /v1/whoami 并收敛 snake_case → camelCase", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        token_name: "ci-token",
        role_name: "admin",
        user_name: "alice",
        platform_admin: true,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const session = await sessionResource.record!("self", {}, undefined);

    expect(session).toEqual({
      tokenName: "ci-token",
      roleName: "admin",
      userName: "alice",
      platformAdmin: true,
    });
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/whoami");
  });

  it.each([
    {
      name: "tokens",
      resource: tokensResource,
      path: "/v1/tokens",
      envelope: "tokens",
      row: { id: "t1", name: "ci" },
    },
    {
      name: "freezes",
      resource: freezesResource,
      path: "/v1/governance/freezes",
      envelope: "freezes",
      row: { id: "f1", reason: "maintenance window" },
    },
    {
      name: "platform-backups",
      resource: platformBackupsResource,
      path: "/v1/platform/backups",
      envelope: "snapshots",
      row: { id: "b1", time: "2026-10-27T00:00:00Z", hostname: "cp-1" },
    },
  ])("$name 打 $path 并展平 undefined 行（restCollection 信封过滤）", async ({ resource, path, envelope, row }) => {
    const fetchMock = vi.fn(async () => jsonResponse({ [envelope]: [row, undefined] }));
    vi.stubGlobal("fetch", fetchMock);

    const segment = await resource.collection!(ALL, {}, undefined);

    expect(segment.rows).toEqual([row]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain(path);
  });
});
