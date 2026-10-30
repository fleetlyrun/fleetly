import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { auditResource, auditSearch } from "./audit";

// 审计域适配器单测（批1；uikit 0.5.0 回流批改 restFetchAll 口径）：整取
// limit 100 端点锚、行收敛、URL 编解码往返（平台域无 scope——fetch 恒发）。
// fetch 面 stub 在 global（authFetch→裸 fetch 管线）；断言经资源适配器
// 调用取数（错误收敛是工厂内置 Response→ApiError）。

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

const ALL = { window: { size: 100 } } as const;

describe("auditResource 读面（端点 + 收敛）", () => {
  it("整取 /v1/audit?limit=100 并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        entries: [{ id: "e1", source: "cli", actor: "tok", action: "app.deploy", resource: "app/a1" }, undefined],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = (await auditResource.collection!(ALL, {}, undefined)).rows;

    expect(rows).toEqual([{ id: "e1", source: "cli", actor: "tok", action: "app.deploy", resource: "app/a1" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/audit?limit=100");
  });

  it("非 2xx 收敛 kernel ApiError（工厂 Response→ApiError 单源）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(auditResource.collection!(ALL, {}, undefined)).rejects.toMatchObject({
      kind: "server",
      status: 503,
      message: "boom",
    });
  });
});

describe("auditSearch（URL 编解码契约）", () => {
  it("q 编码往返为 contains 谓词（source/actor/action/resource）；decode 容忍缺省", () => {
    const fields = ["source", "actor", "action", "resource"];
    const encoded = auditSearch.encode({
      predicate: textPredicate(fields, "cli"),
      window: { size: 20 },
    });
    expect(encoded.q).toBe("cli");
    const decoded = auditSearch.decode({ q: "cli" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "cli"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = auditSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
