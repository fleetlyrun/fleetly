import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import {
  composeSizeValue,
  dataEnginesResource,
  defaultVersionOf,
  groupKnobsByCategory,
  knobClassBadge,
  knobControlFor,
  knobFormatHint,
  knobValuesEqual,
  parseSizeValue,
  setKnobValue,
  versionOptionLabel,
  type EngineCatalog,
} from "./data-engines";

// Engine Catalog kit 单测（B3，ADR-0094 决策 4 / ADR-0095 决策 10）：读面端点
// 锚 + 收敛（无 engine 行丢弃），纯函数半边（class 徽标 / Kind→控件 / size
// 拆合 / 分组 / 版本 / 期望态比对）。夹具引擎名刻意非真引擎词——表单面零
// 引擎知识的证据（取值面单源 = 目录）。

afterEach(() => {
  vi.unstubAllGlobals();
  setToken("");
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("dataEnginesResource 读面", () => {
  it("GET /v1/data-engines，丢弃无 engine 的失形行", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ engines: [{ engine: "kvstore", kind: "database" }, undefined, { kind: "database" }] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await dataEnginesResource.record!("current", {}, undefined);

    expect(rows).toEqual([{ engine: "kvstore", kind: "database" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/data-engines");
  });

  it("非 2xx 收敛 kernel ApiError（403 → forbidden）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_FORBIDDEN", message: "no scope" }, 403)));
    await expect(dataEnginesResource.record!("current", {}, undefined)).rejects.toEqual({ kind: "forbidden" });
  });
});

describe("class 徽标与 Kind→控件映射（design §3.5）", () => {
  it("dynamic → instant；static → rolling；未知 class 保守按 rolling", () => {
    expect(knobClassBadge("dynamic")).toBe("instant");
    expect(knobClassBadge("static")).toBe("rolling");
    expect(knobClassBadge(undefined)).toBe("rolling");
    expect(knobClassBadge("future-class")).toBe("rolling");
  });

  it("bool=开关 / int=数字 / size=数字+单位 / enum=下拉 / duration=文本+提示 / 未登记=文本", () => {
    expect(knobControlFor("bool")).toBe("switch");
    expect(knobControlFor("int")).toBe("number");
    expect(knobControlFor("size")).toBe("size");
    expect(knobControlFor("enum")).toBe("select");
    expect(knobControlFor("duration")).toBe("duration");
    expect(knobControlFor("unknown")).toBe("text");
    expect(knobControlFor(undefined)).toBe("text");
  });

  it("值形态提示与受理位同口径（size/duration 单位闭集、int 范围）", () => {
    expect(knobFormatHint({ key: "k", kind: "size" })).toContain("b, kb, mb, gb");
    expect(knobFormatHint({ key: "k", kind: "duration" })).toContain("ms, s, m, h, d");
    expect(knobFormatHint({ key: "k", kind: "int", min: "1", max: "10000" })).toBe("Integer in 1..10000.");
    expect(knobFormatHint({ key: "k", kind: "bool" })).toBeNull();
  });
});

describe("size 拆合（受理口径 <整数><b|kb|mb|gb>，大小写不敏感）", () => {
  it("parse：合形态拆数值+单位（单位归一小写）；空/失形 → 空数值 + mb", () => {
    expect(parseSizeValue("512mb")).toEqual({ amount: "512", unit: "mb" });
    expect(parseSizeValue("2GB")).toEqual({ amount: "2", unit: "gb" });
    expect(parseSizeValue("64b")).toEqual({ amount: "64", unit: "b" });
    expect(parseSizeValue("")).toEqual({ amount: "", unit: "mb" });
    expect(parseSizeValue("1.5gb")).toEqual({ amount: "", unit: "mb" });
    expect(parseSizeValue(undefined)).toEqual({ amount: "", unit: "mb" });
  });

  it("compose：数值空 = 空串（未设置）；否则 <数值><单位>", () => {
    expect(composeSizeValue("256", "mb")).toBe("256mb");
    expect(composeSizeValue(" 4 ", "gb")).toBe("4gb");
    expect(composeSizeValue("", "kb")).toBe("");
  });
});

describe("分组 / 版本 / 期望态", () => {
  const engine: EngineCatalog = {
    engine: "sqlish",
    versions: [{ id: "1" }, { id: "2", deprecated: true, note: "removal after two releases" }, { id: "3", default: true }],
    knobs: [
      { key: "cache_size", kind: "size", category: "memory" },
      { key: "max_clients", kind: "int", category: "connections" },
      { key: "scratch_size", kind: "size", category: "memory" },
      { key: "flag", kind: "bool" },
      { kind: "int", category: "memory" },
    ],
  };

  it("groupKnobsByCategory：首现序分组、无分组词归 general、无 key 丢弃", () => {
    const groups = groupKnobsByCategory(engine.knobs);
    expect(groups.map((group) => group.category)).toEqual(["memory", "connections", "general"]);
    expect(groups[0]?.knobs.map((knob) => knob.key)).toEqual(["cache_size", "scratch_size"]);
    expect(groupKnobsByCategory(undefined)).toEqual([]);
  });

  it("defaultVersionOf：Default 条目；缺失回落最新声明；无版本 = 空串", () => {
    expect(defaultVersionOf(engine)).toBe("3");
    expect(defaultVersionOf({ engine: "x", versions: [{ id: "a" }, { id: "b" }] })).toBe("b");
    expect(defaultVersionOf(undefined)).toBe("");
  });

  it("versionOptionLabel：Default / deprecated 标注", () => {
    expect(versionOptionLabel({ id: "3", default: true })).toBe("3 (default)");
    expect(versionOptionLabel({ id: "2", deprecated: true })).toBe("2 (deprecated)");
    expect(versionOptionLabel({ id: "1" })).toBe("1");
  });

  it("setKnobValue：写键 / 空串移除（回到引擎缺省）；knobValuesEqual 序无关", () => {
    const set = setKnobValue({ a: "1" }, "b", "2");
    expect(set).toEqual({ a: "1", b: "2" });
    expect(setKnobValue(set, "a", "")).toEqual({ b: "2" });
    expect(knobValuesEqual({ a: "1", b: "2" }, { b: "2", a: "1" })).toBe(true);
    expect(knobValuesEqual({ a: "1" }, { a: "2" })).toBe(false);
    expect(knobValuesEqual({ a: "1" }, { a: "1", b: "2" })).toBe(false);
  });
});
