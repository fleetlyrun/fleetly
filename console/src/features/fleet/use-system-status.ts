import type { components } from "@/api/system";

type ComponentHealth = components["schemas"]["v1ComponentHealth"];

// 系统状态收敛形与逐项健康寻址（2026-10-27 批4 读面收尾后本文件只剩帮手
// ——GetStatus 取数面与收敛形在 kit/system.ts 的 record 读，类型自此再
// 导出单源；Components 页经 recordQueryOptions + 30s 轮询消费）。

export type { SystemStatus } from "@/kit/system";

// componentHealth 按 provider 名取逐项健康（cards 以实名对齐——ADR-0058
// 实名即寻址锚）。
export function componentHealth(components: ComponentHealth[] | undefined, name: string): ComponentHealth | undefined {
  return (components ?? []).find((component) => component.name === name);
}
