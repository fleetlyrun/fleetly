import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { invalidateAfterCommand } from "@lynx-go/uikit-core";
import { setToken } from "@/lib/token";
import { freezesResource, tokensResource } from "./settings";

// 平台设置写面命令锚（批2 useCommand 化）：command-only 资源——读面在存
// 量 catalog 键上，失效全走 legacyKeys 桥；映射走 runCommandMapped 单源
// （errors.test.ts 已锚矩阵，此处锚域名 + 桥）。

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

  it("400 → rejected 保留 apperr code；mint 无失效声明（语义保留：旧 onSuccess 只进揭示链）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_TOKEN_NAME_TAKEN", message: "name taken" }, 400)));
    await expect(tokensResource.commands.mint.run({ name: "ci" })).resolves.toEqual({
      status: "rejected",
      code: "E_TOKEN_NAME_TAKEN",
      message: "name taken",
    });

    const queryClient = new QueryClient();
    queryClient.setQueryData(["settings", "tokens"], []);
    invalidateAfterCommand(queryClient, tokensResource.commands.mint, {});
    expect(queryClient.getQueryState(["settings", "tokens"])?.isInvalidated).toBeFalsy();
  });
});
