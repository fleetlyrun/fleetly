import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { fetchNodes, nodesSearch } from "./nodes";

// 节点域适配器单测（批1 Wave2-A）：/v1/nodes 端点锚、protojson 行收敛、
// URL 编解码往返 + available 降序默认排序（平台域无 scope——fetch 恒发）。
// fetch 面 stub 在 global（fetchRows 走 apiFetch 裸 fetch 管线）。

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

describe("fetchNodes（端点 + 收敛）", () => {
  it("整取 /v1/nodes 并展平 undefined 行（catalog rowsOf 口径：不滤缺字段行）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        nodes: [
          { platform_id: "N1", hostname: "manager-1", role: "manager", available: true },
          undefined,
          { hostname: "no-platform-id" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchNodes({});

    expect(rows).toEqual([
      { platform_id: "N1", hostname: "manager-1", role: "manager", available: true },
      { hostname: "no-platform-id" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/nodes");
  });

  it("非 2xx 走统一错误信封（code/message 抛出）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(fetchNodes({})).rejects.toThrowError(/boom/);
  });
});

describe("nodesSearch（URL 编解码契约 + 默认排序）", () => {
  it("q 编码往返为 contains 谓词（hostname/platform_id/role）；decode 容忍缺省", () => {
    const fields = ["hostname", "platform_id", "role"];
    const encoded = nodesSearch.encode({
      predicate: textPredicate(fields, "worker"),
      window: { size: 20 },
    });
    expect(encoded.q).toBe("worker");
    const decoded = nodesSearch.decode({ q: "worker" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "worker"));
    expect(decoded.window.size).toBe(20);
  });

  it("URL 无 sort 时 decode 落 available 降序默认（旧页渲染前排序的同义形）", () => {
    const decoded = nodesSearch.decode({});
    expect(decoded.order).toEqual([{ field: "available", dir: "desc" }]);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = nodesSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
