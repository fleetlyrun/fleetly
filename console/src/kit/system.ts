import { defineResource } from "@lynx-go/uikit-core/kernel";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";

// 系统面写动词（2026-10-26 批2 useCommand 化）：受管 provider 载体重启
//（Components 页）。command-only 资源；失效语义保留——重启后健康面靠存量
// ["system","status"] 键（features/fleet/use-system-status 读面），legacyKeys
// 桥接。滚动重排期间 unhealthy 是预期中间态，刷新健康面由页面 onRestarted
// refetch 兜底。

export const systemResource = defineResource({
  name: "system",
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
