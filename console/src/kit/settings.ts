import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import type { components as identitySchemas } from "@/api/identity";
import { apiFetch, apiSend } from "@/api/client";
import { runCommandMapped, toApiError } from "./errors";
import { fetchRows } from "./http";

// 平台设置域资源（2026-10-26 批2 写面 useCommand 化；批4 读面收尾补集合/
// record 读形）：API token 铸造/吊销、治理冻结 set/lift、平台备份触发、
// whoami 会话读。写动词沿用 legacyKeys 桥（存量 ["settings",…] 键——
// command-only 时期的读面键，kit 集合落位后桥待 catalog 退役统一清理）；
// run 统一 runCommandMapped 结果两分。

export type TokenEntry = identitySchemas["schemas"]["v1Token"];

export async function fetchTokens(_ctx: ResourceScope, signal?: AbortSignal): Promise<TokenEntry[]> {
  try {
    return await fetchRows<TokenEntry>(
      "/v1/tokens",
      (res) => (res as { tokens?: Array<TokenEntry | undefined> }).tokens,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（错误态分诊契约）
    throw toApiError(err);
  }
}

// 治理冻结行形：governance 服务不在 console 生成上下文（同 platform 备份
// 面）——类型内联手写，与 lib/catalog.ts 原内联同形。
export interface FreezeEntry {
  id?: string;
  team_id?: string;
  reason?: string;
  created_at?: string;
  lifted_at?: string;
}

export async function fetchFreezes(_ctx: ResourceScope, signal?: AbortSignal): Promise<FreezeEntry[]> {
  try {
    return await fetchRows<FreezeEntry>(
      "/v1/governance/freezes",
      (res) => (res as { freezes?: Array<FreezeEntry | undefined> }).freezes,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

// 平台备份行形（C2 台账面）：PlatformSnapshot = { id, time, hostname }。
export interface PlatformSnapshot {
  id: string;
  time: string;
  hostname: string;
}

export async function fetchPlatformBackups(_ctx: ResourceScope, signal?: AbortSignal): Promise<PlatformSnapshot[]> {
  try {
    return await fetchRows<PlatformSnapshot>(
      "/v1/platform/backups",
      (res) => (res as { snapshots?: Array<PlatformSnapshot | undefined> }).snapshots,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
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
  collection: clientCollection(fetchTokens),
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
      invalidates: [],
    },
    revoke: {
      run: (input: { id: string }) =>
        runCommandMapped(() => apiSend(`/v1/tokens/${encodeURIComponent(input.id)}/revoke`, "POST")),
      legacyKeys: () => [["settings", "tokens"]],
    },
  },
});

// 治理冻结（platform-wide）：set 后 change verbs 拒绝、lift 恢复。
export const freezesResource = defineResource({
  name: "freezes",
  getId: (row: FreezeEntry) => row.id ?? "",
  collection: clientCollection(fetchFreezes),
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
  getId: (row: PlatformSnapshot) => row.id,
  collection: clientCollection(fetchPlatformBackups),
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

export async function fetchSession(_id: string, _ctx: ResourceScope, signal?: AbortSignal): Promise<SessionInfo> {
  try {
    const res = await apiFetch<Record<string, unknown>>("/v1/whoami", { signal });
    return {
      tokenName: typeof res.token_name === "string" ? res.token_name : "",
      roleName: typeof res.role_name === "string" ? res.role_name : "",
      userName: typeof res.user_name === "string" ? res.user_name : "",
      platformAdmin: res.platform_admin === true,
    };
  } catch (err) {
    throw toApiError(err);
  }
}

// whoami 无 id 维度——id 恒 "self"（record 键 ["record","session","self"]，
// 壳层各持有点共享同一缓存条目）；无 scope。token 缺席的禁查门在调用点：
// useRecord 对 undefined id 自动停查。
export const sessionResource = defineResource({
  name: "session",
  record: fetchSession,
});
