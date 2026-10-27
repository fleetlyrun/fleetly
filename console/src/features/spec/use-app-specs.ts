import type { components } from "@/api/structure";

export type AppSpec = components["schemas"]["v1AppSpec"];

// specIndex 把 Spec 集合反查成"引用 → 引用方 App 集合"索引（secret_refs
// / volume_id / upload id 三类锚共用）。批4（2026-10-27）起 Spec 拉取面单源
// 在 kit/app-specs（appSpecsResource，fan-out 内联于 fetch）——本文件只剩
// 纯函数索引帮手（resources/panels 与 databases 反查共用）；原 useAppSpecs
// useQueries 形态随消费面迁移退役。
export function specIndex<T>(specs: Map<string, AppSpec>, anchorsOf: (spec: AppSpec) => T[]): Map<T, string[]> {
  const index = new Map<T, string[]>();
  for (const [appId, spec] of specs) {
    for (const anchor of anchorsOf(spec)) {
      const list = index.get(anchor) ?? [];
      list.push(appId);
      index.set(anchor, list);
    }
  }
  return index;
}
