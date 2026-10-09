import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";
import type { components } from "@/api/system";

type StatusState = components["schemas"]["v1StatusState"];
type ComponentHealth = components["schemas"]["v1ComponentHealth"];

export interface SystemStatus {
  state: StatusState | undefined;
  version: string | undefined;
  components: ComponentHealth[];
}

// useSystemStatus 拉控制面状态 + 逐项 Provider 健康（IA v3 二期③：
// GetStatus components——架构 §8 降级矩阵驱动）。Managed Providers 页
// 与状态徽标的数据源；30s 轮询同工作台口径。
export function useSystemStatus() {
  return useQuery({
    queryKey: ["system", "status"],
    queryFn: async (): Promise<SystemStatus> => {
      const res = await apiFetch<{ state?: StatusState; version?: string; components?: Array<ComponentHealth | undefined> }>(
        "/v1/system/status",
      );
      return {
        state: res.state,
        version: res.version,
        components: (res.components ?? []).flatMap((entry) => (entry != null ? [entry] : [])),
      };
    },
    refetchInterval: 30_000,
  });
}

// componentHealth 按 provider 名取逐项健康（cards 以实名对齐——ADR-0058
// 实名即寻址锚）。
export function componentHealth(components: ComponentHealth[] | undefined, name: string): ComponentHealth | undefined {
  return (components ?? []).find((component) => component.name === name);
}
