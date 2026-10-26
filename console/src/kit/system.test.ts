import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { invalidateAfterCommand } from "@lynx-go/uikit-core";
import { setToken } from "@/lib/token";
import { systemResource } from "./system";

// 系统面命令锚（批2 useCommand 化）：providers restart——payload 透传 +
// ["system","status"] 存量键桥（useSystemStatus 读面键，语义保留）。

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

describe("命令锚：system restart（映射 + 存量键桥）", () => {
  it("ok 透传 restarted 载荷并失效 [system,status]；restart 打 POST /v1/system/components/{name}/restart", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ restarted: 2 }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await systemResource.commands.restart.run({ name: "traefik" });
    expect(result).toEqual({ status: "ok", payload: { restarted: 2 } });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/system/components/traefik/restart");
    expect(init.method).toBe("POST");

    const queryClient = new QueryClient();
    queryClient.setQueryData(["system", "status"], { state: "STATUS_STATE_HEALTHY" });
    invalidateAfterCommand(queryClient, systemResource.commands.restart, {});
    expect(queryClient.getQueryState(["system", "status"])?.isInvalidated).toBe(true);
  });

  it("403 → throw kernel ApiError（treatAs 未覆写 forbidden——走失败通道）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "permission_denied", message: "no" }, 403)));
    await expect(systemResource.commands.restart.run({ name: "zot" })).rejects.toEqual({ kind: "forbidden" });
  });
});
