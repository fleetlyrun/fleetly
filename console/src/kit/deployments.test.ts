import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { deploymentsSearch, fetchProjectDeployments } from "./deployments";

// 项目级部署 fan-out 适配器单测（批1）：apps 整取 → 逐 app deployments
// 合并按 updated_at 倒序、per-app 失败吞为空、空 scope/无 app 不发请求、
// URL 编解码往返。apps 段经 kit/apps 的 uikit http 管线（裸 fetch），
// deployments 段经 apiFetch——同一 global stub 覆盖两管线。

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

describe("fetchProjectDeployments（fan-out 复刻）", () => {
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

    const rows = await fetchProjectDeployments({ projectId: "shop" });

    expect(rows.map((row) => row.id)).toEqual(["d1", "d2"]);
    const urls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(urls.some((url) => url.includes("/v1/deployments?app_id=a1&limit=20"))).toBe(true);
    expect(urls.some((url) => url.includes("/v1/deployments?app_id=a2&limit=20"))).toBe(true);
  });

  it("空 projectId 不发请求", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchProjectDeployments({ projectId: "" })).toEqual([]);
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
    expect(await fetchProjectDeployments({ projectId: "shop" })).toEqual([]);
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
