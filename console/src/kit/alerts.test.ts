import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { alertsSearch, fetchAlertingChannels, fetchAlertRules } from "./alerts";

// 告警域适配器单测（批1 Wave2-A）：rules/channels 端点锚、protojson 行收
// 敛、双 tab 共享 schema 的 URL 编解码往返（平台域无 scope——fetch 恒发）。
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

describe("fetchAlertRules/fetchAlertingChannels（端点 + 收敛）", () => {
  it("整取 /v1/alerts/rules 并展平 undefined 行（catalog rowsOf 口径：不滤缺字段行）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        rules: [
          { id: "r1", app_id: "app_1", metric: "cpu_percent", threshold: 90 },
          undefined,
          { app_id: "app_2" },
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchAlertRules({});

    expect(rows).toEqual([
      { id: "r1", app_id: "app_1", metric: "cpu_percent", threshold: 90 },
      { app_id: "app_2" },
    ]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/alerts/rules");
  });

  it("channels 打 /v1/channels 同口径收敛", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ channels: [{ id: "c1", name: "ops-hook", kind: "webhook" }, undefined] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchAlertingChannels({});

    expect(rows).toEqual([{ id: "c1", name: "ops-hook", kind: "webhook" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/channels");
  });

  it("非 2xx 走统一错误信封（code/message 抛出）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(fetchAlertRules({})).rejects.toThrowError(/boom/);
    await expect(fetchAlertingChannels({})).rejects.toThrowError(/boom/);
  });
});

describe("alertsSearch（URL 编解码契约，双 tab 共享实例）", () => {
  it("q 编码往返为并集字段集的 contains 谓词；decode 容忍缺省", () => {
    const fields = ["app_id", "metric", "name", "kind"];
    const encoded = alertsSearch.encode({
      predicate: textPredicate(fields, "cpu"),
      window: { size: 20 },
    });
    expect(encoded.q).toBe("cpu");
    const decoded = alertsSearch.decode({ q: "cpu" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "cpu"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = alertsSearch.patch({ predicate: null });
    expect("q" in patch).toBe(true);
    expect(patch.q).toBeUndefined();
    expect(patch.page).toBeUndefined();
  });
});
