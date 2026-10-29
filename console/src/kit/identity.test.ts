import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { collectionQueryOptions, invalidateAfterCommand } from "@lynx-go/uikit-core";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { fetchInvitations, fetchRoles, fetchTeams, fetchUsers, teamsSearch, usersResource, usersSearch } from "./identity";

// 身份域适配器单测（批1 Wave2-B）：四 resource 端点锚、信封行收敛、共享
// schema 的 URL 编解码往返（平台域无 scope——fetch 恒发）。fetch 面 stub
// 在 global（fetchRows 走 apiFetch 裸 fetch 管线）。

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

describe("身份域四 fetcher（端点 + 收敛）", () => {
  it.each([
    { name: "fetchUsers", fetcher: fetchUsers, path: "/v1/users", envelope: "users", row: { id: "u1", name: "alice" } },
    { name: "fetchTeams", fetcher: fetchTeams, path: "/v1/teams", envelope: "teams", row: { id: "t1", name: "platform" } },
    { name: "fetchRoles", fetcher: fetchRoles, path: "/v1/roles", envelope: "roles", row: { id: "r1", name: "deployer", scopes: ["apps:admin"] } },
    {
      name: "fetchInvitations",
      fetcher: fetchInvitations,
      path: "/v1/invitations",
      envelope: "invitations",
      row: { id: "i1", team_id: "t1", role_id: "r1", created_by: "user:alice" },
    },
  ])("$name 打 $path 并展平 undefined 行（catalog rowsOf 口径）", async ({ fetcher, path, envelope, row }) => {
    const fetchMock = vi.fn(async () => jsonResponse({ [envelope]: [row, undefined] }));
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetcher({});

    expect(rows).toEqual([row]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain(path);
  });
});

describe("usersSearch（URL 编解码契约，四 namespace 实例之一）", () => {
  it("q 编码往返为并集字段集的 contains 谓词；decode 容忍缺省", () => {
    const fields = ["name", "id", "scopes", "team_id", "role_id", "created_by"];
    const encoded = usersSearch.encode({
      predicate: textPredicate(fields, "alice"),
      window: { size: 20 },
    });
    // namespace 物理键（F1 回流批）：users.q；他命名空间键不携带
    expect(encoded["users.q"]).toBe("alice");
    const decoded = usersSearch.decode({ "users.q": "alice" });
    expect(teamsSearch.decode({ "users.q": "alice" }).predicate).toBeUndefined();
    expect(decoded.predicate).toEqual(textPredicate(fields, "alice"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = usersSearch.patch({ predicate: null });
    expect("users.q" in patch).toBe(true);
    expect(patch["users.q"]).toBeUndefined();
    expect(patch["users.page"]).toBeUndefined();
  });
});

// 命令锚（批2 写面 useCommand 化）：每域 1 锚——runCommandMapped 映射 +
// 失效桥（kit 集合键默认失效 × legacyKeys 存量键）。identity 锚在 users
// remove（create/setPassword 等同族同管线）。
describe("命令锚：users remove（映射 + 失效桥）", () => {
  it("ok → kit users 集合键与存量 [identity,users] 键同批失效", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({})));
    const result = await usersResource.commands.remove.run({ id: "u1" });
    expect(result.status).toBe("ok");

    const queryClient = new QueryClient();
    // kit 键经 collectionQueryOptions 单源取（0.4.0 键形含 scopeKeys 段）
    const kitKey = collectionQueryOptions(usersResource, { window: { size: 20 } }).queryKey;
    queryClient.setQueryData(kitKey, []);
    queryClient.setQueryData(["identity", "users"], []);
    invalidateAfterCommand(queryClient, usersResource.commands.remove, {});
    expect(queryClient.getQueryState(kitKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(["identity", "users"])?.isInvalidated).toBe(true);
  });

  it("400 → rejected 保留 apperr code；remove 打 DELETE /v1/users/{id}", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ code: "E_IDENTITY_LAST_ADMIN", message: "cannot delete" }, 400));
    vi.stubGlobal("fetch", fetchMock);
    await expect(usersResource.commands.remove.run({ id: "u1" })).resolves.toEqual({
      status: "rejected",
      code: "E_IDENTITY_LAST_ADMIN",
      message: "cannot delete",
    });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(url)).toBe("/v1/users/u1");
    expect(init.method).toBe("DELETE");
  });
});
