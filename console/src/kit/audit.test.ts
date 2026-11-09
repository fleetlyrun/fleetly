import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynxland/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { auditResource, auditSearch } from "./audit";

// 审计域适配器单测（2026-11-10 服务端游标翻页批）：before_seq keyset 端点
// 锚（首页不带游标、游标页回显 before_seq）、nextCursor 整页/短页两径、行
// 收敛与页内 q 快筛、URL 编解码往返（平台域无 scope——fetch 恒发）。fetch
// 面 stub 在 global（authFetch→裸 fetch 管线）；断言经资源适配器调用取数
//（错误收敛是工厂内置 Response→ApiError）。

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

function auditRow(seq: string, source = "cli"): { id: string; seq: string; source: string; action: string; resource: string } {
  return { id: `e${seq}`, seq, source, action: "app.deploy", resource: `app/a${seq}` };
}

describe("auditResource 读面（游标端点 + 收敛）", () => {
  it("首页 /v1/audit?limit=100（不带 before_seq）并展平 undefined 行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        entries: [{ id: "e1", source: "cli", actor: "tok", action: "app.deploy", resource: "app/a1" }, undefined],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const segment = await auditResource.collection!(ALL, {}, undefined);

    expect(segment.rows).toEqual([{ id: "e1", source: "cli", actor: "tok", action: "app.deploy", resource: "app/a1" }]);
    expect(segment.nextCursor).toBeUndefined();
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("/v1/audit?limit=100");
    expect(String(url)).not.toContain("before_seq");
  });

  it("游标页回显 before_seq；整页下发末条 seq，短页到底", async () => {
    const full = Array.from({ length: 100 }, (_, i) => auditRow(String(500 - i)));
    const fetchMock = vi.fn(async () => jsonResponse({ entries: full }));
    vi.stubGlobal("fetch", fetchMock);

    const segment = await auditResource.collection!(
      { window: { size: 100, cursor: "500" } },
      {},
      undefined,
    );

    expect(segment.rows).toHaveLength(100);
    expect(segment.nextCursor).toBe("401");
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toContain("before_seq=500");
    expect(String(url)).toContain("limit=100");

    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ entries: full.slice(0, 99) })));
    const short = await auditResource.collection!({ window: { size: 100, cursor: "500" } }, {}, undefined);
    expect(short.nextCursor).toBeUndefined();
  });

  it("q 页内快筛保留服务端 nextCursor（投影只影响展示行）", async () => {
    const rows = Array.from({ length: 100 }, (_, i) => auditRow(String(100 - i), i === 0 ? "api" : "cli"));
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ entries: rows })));

    const segment = await auditResource.collection!(
      { predicate: textPredicate(["source", "actor", "action", "resource"], "cli"), window: { size: 100 } },
      {},
      undefined,
    );

    expect(segment.rows).toHaveLength(99);
    expect(segment.nextCursor).toBe("1");
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
    expect(decoded.window.size).toBe(100);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = auditSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
