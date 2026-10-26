import { defineResource } from "@lynx-go/uikit-core/kernel";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";

// 平台设置写面（2026-10-26 批2 useCommand 化）：settings 页三族动词——
// API token 铸造/吊销、治理冻结 set/lift、平台备份触发。command-only 资源
//（读面暂留 lib/catalog 的存量键 ["settings",…]——无 kit 集合形态，失效
// 全靠各 descriptor 的 legacyKeys 桥；run 统一 runCommandMapped 结果两分）。

// token 铸造的 reveal 载荷：secret 只在铸造响应出现一次（CreateTokenDialog
// mint→reveal 揭示链）。
export interface MintTokenPayload {
  token?: { id?: string };
  secret?: string;
}

export const tokensResource = defineResource({
  name: "tokens",
  commands: {
    // 铸造无失效（语义保留：旧 onSuccess 只进揭示链，台账新鲜度靠轮询；
    // kit "tokens" 集合尚不存在，默认失效也是空转，显式置空声明意图）。
    mint: {
      run: (input: { name: string; roleId?: string }) =>
        runCommandMapped<MintTokenPayload>(() =>
          apiSend<MintTokenPayload>("/v1/tokens", "POST", {
            name: input.name,
            role_id: input.roleId || undefined,
          }),
        ),
      invalidates: [],
    },
    revoke: {
      run: (input: { id: string }) =>
        runCommandMapped(() => apiSend(`/v1/tokens/${encodeURIComponent(input.id)}/revoke`, "POST")),
      legacyKeys: () => [["settings", "tokens"]],
    },
  },
});

// 治理冻结（platform-wide）：set 后 change verbs 拒绝、lift 恢复——两动词
// 共用 ["settings","freezes"] 台账键。
export const freezesResource = defineResource({
  name: "freezes",
  commands: {
    freeze: {
      run: (input: { reason: string }) =>
        runCommandMapped(() => apiSend("/v1/governance/freezes", "POST", { reason: input.reason })),
      legacyKeys: () => [["settings", "freezes"]],
    },
    lift: {
      run: (input: { id: string }) =>
        runCommandMapped(() => apiSend(`/v1/governance/freezes/${encodeURIComponent(input.id)}/lift`, "POST")),
      legacyKeys: () => [["settings", "freezes"]],
    },
  },
});

// 平台备份触发（同步全量快照：SQLite + secrets + object store）：pre-upgrade
// 快照是换装硬门。ok 载荷带 backup id 供 toast。
export const platformBackupsResource = defineResource({
  name: "platform-backups",
  commands: {
    trigger: {
      run: () =>
        runCommandMapped<{ backup?: { id?: string } }>(() =>
          apiSend<{ backup?: { id?: string } }>("/v1/platform/backups", "POST", {}),
        ),
      legacyKeys: () => [["settings", "platform-backups"]],
    },
  },
});
