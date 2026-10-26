import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { fetchGitHubApps, githubAppsSearch } from "./githubapps";

// GitHub Apps 域适配器单测（批1 Wave2-A）：双语境端点锚（project_id 面 /
// global 面）、protojson 行收敛、URL 编解码往返。fetch 面 stub 在 global
//（fetchRows 走 apiFetch 裸 fetch 管线）。

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

describe("fetchGitHubApps（双语境端点 + 收敛）", () => {
  it("projectId 在场打 /v1/github-apps?project_id=… 并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        apps: [
          { id: "g1", project_id: "shop", account_login: "acme", app_id: "123", installation_id: "456" },
          undefined,
          { account_login: "no-id" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchGitHubApps({ projectId: "shop" });

    expect(rows).toEqual([
      { id: "g1", project_id: "shop", account_login: "acme", app_id: "123", installation_id: "456" },
      { account_login: "no-id" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/github-apps?project_id=shop");
  });

  it("projectId 缺席打 global 面（?global=true，不携带 project_id）", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ apps: [{ id: "g0", account_login: "root" }] }));
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchGitHubApps({});

    expect(rows).toEqual([{ id: "g0", account_login: "root" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/github-apps?global=true");
  });

  it("非 2xx 走统一错误信封（code/message 抛出）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(fetchGitHubApps({})).rejects.toThrowError(/boom/);
  });
});

describe("githubAppsSearch（URL 编解码契约）", () => {
  it("q 编码往返为 contains 谓词（account_login/id）；decode 容忍缺省", () => {
    const fields = ["account_login", "id"];
    const encoded = githubAppsSearch.encode({
      predicate: textPredicate(fields, "acme"),
      window: { size: 20 },
    });
    expect(encoded.q).toBe("acme");
    const decoded = githubAppsSearch.decode({ q: "acme" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "acme"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = githubAppsSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
