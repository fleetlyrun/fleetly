import { defineResource, type ResourceScope } from "@lynxland/uikit-core/kernel";
import type { components as structureSchemas } from "@/api/structure";
import { apiFetch } from "@/api/client";
import { toApiError } from "./errors";

// Engine Catalog 读面（B3，ADR-0094 决策 4 / ADR-0095 决策 10；design §2.3/
// §3.5）：GET /v1/data-engines 是 Console 引擎/版本/旋钮取值的唯一面——
// DATABASE_ENGINES 硬编码退役，创建对话框、Settings tab 表单与列表引擎下拉
// 全从目录渲染（零硬编码引擎页面）。目录是服务端模板注册表的现场投影
// （非存储、零缓存零漂移），record id 恒 "current"（templatesResource 同
// 形）。纯函数半边（class 徽标 / Kind→控件 / size 拆合 / 分组 / 期望态
// 比对）同住本模块，vitest 直测；值域校验不在表单复刻——受理位
// ValidateSettings 单源执法，拒绝消息经结果盒原样呈现（校验口径同源）。

export type EngineCatalog = structureSchemas["schemas"]["v1EngineCatalog"];
export type EngineKnob = structureSchemas["schemas"]["v1EngineKnob"];
export type EngineVersionInfo = structureSchemas["schemas"]["v1EngineVersionInfo"];

export const dataEnginesResource = defineResource({
  name: "data-engines",
  record: (_id: string, _ctx: ResourceScope, signal?: AbortSignal) =>
    apiFetch<{ engines?: Array<EngineCatalog | undefined> }>("/v1/data-engines", { signal })
      .then((res) => (res.engines ?? []).filter((entry): entry is EngineCatalog => Boolean(entry?.engine)))
      .catch((err: unknown) => {
        throw toApiError(err);
      }),
});

// 目录 record 的固定 id（全站同键共享缓存——对话框与 Settings tab 同读）。
export const DATA_ENGINES_RECORD_ID = "current";

// 目录拍子：注册表随发布变更（新 major/弃用标记），会话内 5 分钟足够新鲜。
export const DATA_ENGINES_STALE_MS = 5 * 60_000;

// ---- class 徽标（design §3.5：instant / rolling） ----

export type KnobBadge = "instant" | "rolling";

// knobClassBadge：目录 class 词面（static | dynamic）→ 用户面徽标词（CLI
// databases config describe 同词）。未知 class 按 rolling 呈现——宁可多披
// 露一次滚动重启，不把可能的重启说成即时生效。
export function knobClassBadge(knobClass: string | undefined): KnobBadge {
  return knobClass === "dynamic" ? "instant" : "rolling";
}

// ---- Kind → 控件映射（design §3.5） ----

export type KnobControl = "switch" | "number" | "size" | "select" | "duration" | "text";

// knobControlFor：bool = 开关；int = 数字输入；size = 数字输入 + 单位；
// enum = 下拉；duration = 文本 + 格式提示；未登记 kind 回落文本（服务端
// 受理位是值域真源，表单只负责把值送到）。
export function knobControlFor(kind: string | undefined): KnobControl {
  switch (kind) {
    case "bool":
      return "switch";
    case "int":
      return "number";
    case "size":
      return "size";
    case "enum":
      return "select";
    case "duration":
      return "duration";
    default:
      return "text";
  }
}

// size 单位闭集（受理位 knobSizeToBytes 同口径：整数 + b|kb|mb|gb，大小写
// 不敏感——表单统一产出小写）。
export const SIZE_UNITS = ["b", "kb", "mb", "gb"] as const;
export type SizeUnit = (typeof SIZE_UNITS)[number];

const SIZE_RE = /^(\d+)(b|kb|mb|gb)$/i;

// parseSizeValue：受理口径的 size 值拆为数值 + 单位；空或不合形态 →
// 数值空、单位 mb（表单缺省单位）。
export function parseSizeValue(value: string | undefined): { amount: string; unit: SizeUnit } {
  const match = SIZE_RE.exec((value ?? "").trim());
  if (!match) return { amount: "", unit: "mb" };
  return { amount: match[1] ?? "", unit: (match[2] ?? "mb").toLowerCase() as SizeUnit };
}

// composeSizeValue：数值 + 单位合为受理口径值；数值空 = 空串（调用方据
// 此判定「未设置」）。
export function composeSizeValue(amount: string, unit: SizeUnit): string {
  const trimmed = amount.trim();
  return trimmed === "" ? "" : `${trimmed}${unit}`;
}

// knobFormatHint：值形态提示（受理位 ValidateKnobValue 报错同口径的正向
// 表述）。
export function knobFormatHint(knob: EngineKnob): string | null {
  switch (knob.kind) {
    case "size":
      return "Integer plus unit (b, kb, mb, gb), e.g. 512mb.";
    case "duration":
      return "Integer plus unit (ms, s, m, h, d), e.g. 30s.";
    case "int":
      if (knob.min || knob.max) return `Integer in ${knob.min || "…"}..${knob.max || "…"}.`;
      return "Integer.";
    default:
      return null;
  }
}

// ---- 分组 / 版本 ----

export interface KnobGroup {
  category: string;
  knobs: EngineKnob[];
}

// groupKnobsByCategory：按 Category 分组（首现序 = 目录声明序；无分组词
// 归 general）。无 key 的条目丢弃（目录失形防御）。
export function groupKnobsByCategory(knobs: ReadonlyArray<EngineKnob> | undefined): KnobGroup[] {
  const groups = new Map<string, EngineKnob[]>();
  for (const knob of knobs ?? []) {
    if (!knob.key) continue;
    const category = knob.category || "general";
    const bucket = groups.get(category);
    if (bucket) bucket.push(knob);
    else groups.set(category, [knob]);
  }
  return [...groups].map(([category, entries]) => ({ category, knobs: entries }));
}

// defaultVersionOf：引擎的 Default 版本 id（每引擎恰一真；缺失时回落最新
// 声明——目录声明序旧→新）。
export function defaultVersionOf(engine: EngineCatalog | undefined): string {
  const versions = engine?.versions ?? [];
  return versions.find((version) => version.default)?.id ?? versions.at(-1)?.id ?? "";
}

// versionOptionLabel：版本下拉文案（Default 标注 / 弃用标注）。
export function versionOptionLabel(version: EngineVersionInfo): string {
  if (version.deprecated) return `${version.id} (deprecated)`;
  if (version.default) return `${version.id} (default)`;
  return version.id ?? "";
}

// ---- 期望态（表单 ↔ 行） ----

// KnobValues 是旋钮期望态（键在场 = 已设置；缺席 = 引擎缺省生效）。
export type KnobValues = Readonly<Record<string, string>>;

// setKnobValue：写一个键；值空串 = 移除该键（回到引擎缺省）。
export function setKnobValue(values: KnobValues, key: string, value: string): KnobValues {
  const next: Record<string, string> = { ...values };
  if (value === "") delete next[key];
  else next[key] = value;
  return next;
}

// knobValuesEqual：期望态等值（键集 + 值逐键相等，序无关）。
export function knobValuesEqual(a: KnobValues, b: KnobValues): boolean {
  const keys = Object.keys(a);
  if (keys.length !== Object.keys(b).length) return false;
  return keys.every((key) => Object.prototype.hasOwnProperty.call(b, key) && a[key] === b[key]);
}
