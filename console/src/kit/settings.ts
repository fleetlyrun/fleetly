import { restCollection, restRecord } from "@lynx-go/uikit-core/adapters";
import { defineResource } from "@lynx-go/uikit-core/kernel";
import type { components as identitySchemas } from "@/api/identity";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";

// 平台设置域资源（2026-10-26 批2 写面 useCommand 化；批4 读面收尾补集合/
// record 读形）：API token 铸造/吊销、治理冻结 set/lift、平台备份触发、
// whoami 会话读。写动词失效 = kit selector（["settings",…] 旧键桥零读者
// 已退役——F1 回流批审计）；run 统一 runCommandMapped 结果两分。
// F1 回流批 D 组（M4 rest 工厂试点域 1/3）：三集合 + 会话 record 走
// restCollection/restRecord——组 URL/信封展平/undefined 行过滤/错误收敛
// 全归工厂内置（fetchRows/qs/toApiError 样板在本域归零；三集合是纯
// data 层消费——useCollectionData 固定窗，无 URL 求值方言依赖）。

export type TokenEntry = identitySchemas["schemas"]["v1Token"];

// 治理冻结行形：governance 服务不在 console 生成上下文（同 platform 备份
// 面）——类型内联手写，与 lib/catalog.ts 原内联同形。
export interface FreezeEntry {
  id?: string;
  team_id?: string;
  reason?: string;
  created_at?: string;
  lifted_at?: string;
}

// 平台备份行形（C2 台账面）：PlatformSnapshot = { id, time, hostname }。
export interface PlatformSnapshot {
  id: string;
  time: string;
  hostname: string;
}

// token 铸造的 reveal 载荷：secret 只在铸造响应出现一次（CreateTokenDialog
// mint→reveal 揭示链）。
export interface MintTokenPayload {
  token?: { id?: string };
  secret?: string;
}

export const tokensResource = defineResource({
  name: "tokens",
  getId: (row: TokenEntry) => row.id ?? "",
  collection: restCollection<TokenEntry>({
    path: () => "/v1/tokens",
    envelope: (body) => (body as { tokens?: Array<TokenEntry | undefined> }).tokens ?? [],
    fetch: authFetch,
  }),
  commands: {
    // 铸造无失效（语义保留：旧 onSuccess 只进揭示链，台账新鲜度靠轮询；
    // 显式置空声明意图）。
    mint: {
      run: (input: { name: string; roleId?: string }) =>
        runCommandMapped<MintTokenPayload>(() =>
          apiSend<MintTokenPayload>("/v1/tokens", "POST", {
            name: input.name,
            role_id: input.roleId || undefined,
          }),
        ),
      // 收尾批裁决翻转（原「无失效=旧语义保守保留」）：mint 后台账即时
      // 可见（kit 集合键，R1 的 tokens collection 读面）。
      invalidates: [{ resource: "tokens" }],
    },
    revoke: {
      run: (input: { id: string }) =>
        runCommandMapped(() => apiSend(`/v1/tokens/${encodeURIComponent(input.id)}/revoke`, "POST")),
    },
  },
});

// 治理冻结（platform-wide）：set 后 change verbs 拒绝、lift 恢复。
export const freezesResource = defineResource({
  name: "freezes",
  getId: (row: FreezeEntry) => row.id ?? "",
  collection: restCollection<FreezeEntry>({
    path: () => "/v1/governance/freezes",
    envelope: (body) => (body as { freezes?: Array<FreezeEntry | undefined> }).freezes ?? [],
    fetch: authFetch,
  }),
  commands: {
    freeze: {
      run: (input: { reason: string }) =>
        runCommandMapped(() => apiSend("/v1/governance/freezes", "POST", { reason: input.reason })),
    },
    lift: {
      run: (input: { id: string }) =>
        runCommandMapped(() => apiSend(`/v1/governance/freezes/${encodeURIComponent(input.id)}/lift`, "POST")),
    },
  },
});

// 平台备份触发（同步全量快照：SQLite + secrets + object store）：pre-upgrade
// 快照是换装硬门。ok 载荷带 backup id 供 toast。
export const platformBackupsResource = defineResource({
  name: "platform-backups",
  getId: (row: PlatformSnapshot) => row.id,
  collection: restCollection<PlatformSnapshot>({
    path: () => "/v1/platform/backups",
    envelope: (body) => (body as { snapshots?: Array<PlatformSnapshot | undefined> }).snapshots ?? [],
    fetch: authFetch,
  }),
  commands: {
    trigger: {
      run: () =>
        runCommandMapped<{ backup?: { id?: string } }>(() =>
          apiSend<{ backup?: { id?: string } }>("/v1/platform/backups", "POST", {}),
        ),
      // Fleet 备份台账联动（收尾批挂账清偿）：触发后 snapshots 资源一并
      // 失效——Backups 页台账即时出新行。
      invalidates: [{ resource: "platform-snapshots" }],
    },
  },
});

// ---- 会话读面（批4 whoami 资源化）----
// 壳层（user-menu/shell-layout/command-palette）与 settings IdentityCard 的
// 身份展示单源。platformAdmin 读服务端 platform_admin（H8 raw PromQL 入口
// 显隐）——Console 不复制 role/team 判定（设计 §5.1/§5.4）。收敛形
// camelCase 化，字段面与 lib/catalog.ts useWhoami 同名。

export interface SessionInfo {
  tokenName: string;
  roleName: string;
  userName: string;
  platformAdmin: boolean;
}

// whoami 无 id 维度——id 恒 "self"（record 键 ["record","session","self"]，
// 壳层各持有点共享同一缓存条目）；无 scope。token 缺席的禁查门在调用点：
// useRecord 对 undefined id 自动停查。收敛形（camelCase 投影）在 envelope。
export const sessionResource = defineResource({
  name: "session",
  record: restRecord<SessionInfo>({
    path: () => "/v1/whoami",
    envelope: (body) => {
      const res = body as Record<string, unknown>;
      return {
        tokenName: typeof res.token_name === "string" ? res.token_name : "",
        roleName: typeof res.role_name === "string" ? res.role_name : "",
        userName: typeof res.user_name === "string" ? res.user_name : "",
        platformAdmin: res.platform_admin === true,
      };
    },
    fetch: authFetch,
  }),
});
