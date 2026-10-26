import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { fetchInvitations, fetchRoles, fetchTeams, fetchUsers, identitySearch } from "./identity";

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

describe("identitySearch（URL 编解码契约，四 tab 共享实例）", () => {
  it("q 编码往返为并集字段集的 contains 谓词；decode 容忍缺省", () => {
    const fields = ["name", "id", "scopes", "team_id", "role_id", "created_by"];
    const encoded = identitySearch.encode({
      predicate: textPredicate(fields, "alice"),
      window: { size: 20 },
    });
    expect(encoded.q).toBe("alice");
    const decoded = identitySearch.decode({ q: "alice" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "alice"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = identitySearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
