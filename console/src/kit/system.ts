import { defineResource } from "@lynx-go/uikit-core/kernel";
import { apiFetch, apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import type { components } from "@/api/system";

type StatusState = components["schemas"]["v1StatusState"];
type ComponentHealth = components["schemas"]["v1ComponentHealth"];

// GetStatus 收敛形（原 features/fleet/use-system-status 的查询态上收；
// 该文件保留 componentHealth 寻址帮手与类型再导出）。
export interface SystemStatus {
  state: StatusState | undefined;
  version: string | undefined;
  components: ComponentHealth[];
}

// 系统面（2026-10-27 批4 读面收尾）：status record 读（Components 页经
// useQuery(recordQueryOptions(systemResource, "current")) + 30s 轮询消费）
// + restart 写命令（批2，零改动）。失效语义保留——重启后健康面靠
// onRestarted refetch 与轮询兜底，legacyKeys 桥接存量 ["system","status"]
// 键。滚动重排期间 unhealthy 是预期中间态。

export const systemResource = defineResource({
  name: "system",
  record: (_id, _ctx, signal) =>
    apiFetch<{
      state?: StatusState;
      version?: string;
      components?: Array<ComponentHealth | undefined>;
    }>("/v1/system/status", { signal }).then((res) => ({
      state: res.state,
      version: res.version,
      components: (res.components ?? []).flatMap((component) => (component != null ? [component] : [])),
    })),
  commands: {
    restart: {
      run: (input: { name: string }) =>
        runCommandMapped<{ restarted?: number }>(() =>
          apiSend<{ restarted?: number }>(`/v1/system/components/${encodeURIComponent(input.name)}/restart`, "POST", {}),
        ),
      legacyKeys: () => [["system", "status"]],
    },
  },
});
