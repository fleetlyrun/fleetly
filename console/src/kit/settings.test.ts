import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { invalidateAfterCommand } from "@lynx-go/uikit-core";
import { setToken } from "@/lib/token";
import {
  fetchFreezes,
  fetchPlatformBackups,
  fetchSession,
  fetchTokens,
  freezesResource,
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
  it("ok → [settings,freezes] 存量键失效；freeze 打 POST /v1/governance/freezes", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);
    const result = await freezesResource.commands.freeze.run({ reason: "maintenance window" });
    expect(result.status).toBe("ok");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/governance/freezes");
    expect(JSON.parse(String(init.body))).toEqual({ reason: "maintenance window" });

    const queryClient = new QueryClient();
    queryClient.setQueryData(["settings", "freezes"], []);
    invalidateAfterCommand(queryClient, freezesResource.commands.freeze, {});
    expect(queryClient.getQueryState(["settings", "freezes"])?.isInvalidated).toBe(true);
  });

  it("400 → rejected 保留 apperr code；mint 失效 tokens 集合（收尾批翻转：mint 后台账即时可见）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_TOKEN_NAME_TAKEN", message: "name taken" }, 400)));
    await expect(tokensResource.commands.mint.run({ name: "ci" })).resolves.toEqual({
      status: "rejected",
      code: "E_TOKEN_NAME_TAKEN",
      message: "name taken",
    });

    const queryClient = new QueryClient();
    const collectionKey = ["collection", "tokens", {}, "all"];
    queryClient.setQueryData(collectionKey, { rows: [], total: 0 });
    invalidateAfterCommand(queryClient, tokensResource.commands.mint, {});
    expect(queryClient.getQueryState(collectionKey)?.isInvalidated).toBe(true);
  });
});

// 读面锚（批4）：session record + 三集合 fetcher——端点与收敛口径钉死
//（catalog rowsOf 同款 undefined 行过滤；whoami camelCase 收敛 + platform_admin
// 布尔判别）。
describe("读面锚：session record + settings 三集合", () => {
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

    const session = await fetchSession("self", {}, undefined);

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
    { name: "fetchTokens", fetcher: fetchTokens, path: "/v1/tokens", envelope: "tokens", row: { id: "t1", name: "ci" } },
    {
      name: "fetchFreezes",
      fetcher: fetchFreezes,
      path: "/v1/governance/freezes",
      envelope: "freezes",
      row: { id: "f1", reason: "maintenance window" },
    },
    {
      name: "fetchPlatformBackups",
      fetcher: fetchPlatformBackups,
      path: "/v1/platform/backups",
      envelope: "snapshots",
      row: { id: "b1", time: "2026-10-27T00:00:00Z", hostname: "cp-1" },
    },
  ])("$name 打 $path 并展平 undefined 行（catalog rowsOf 口径）", async ({ fetcher, path, envelope, row }) => {
    const fetchMock = vi.fn(async () => jsonResponse({ [envelope]: [row, undefined] }));
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetcher({}, undefined);

    expect(rows).toEqual([row]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain(path);
  });
});
