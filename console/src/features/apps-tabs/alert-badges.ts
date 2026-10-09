import type { components } from "@/api/telemetry";

type AlertState = components["schemas"]["v1AlertState"];

// Overview hero 的 firing 徽标（IA v3 T2）：按 app 过滤现行告警（v1AlertState
// 带 app_id，telemetry.ts:249；state 值域 firing/ok，alerts 页同款过滤）。
export function firingAlerts(states: AlertState[] | undefined, appId: string): AlertState[] {
  return (states ?? []).filter((state) => state.app_id === appId && state.state === "firing");
}
