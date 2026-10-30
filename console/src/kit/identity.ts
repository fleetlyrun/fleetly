import { clientCollection, restFetchAll } from "@lynx-go/uikit-core/adapters";
import { defineResource } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as identitySchemas } from "@/api/identity";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";

// 身份域资源适配（2026-10-26 批1 Wave2-B useCollection 化 + 批2 写面
// useCommand 化，平台域——无 scope）：users/teams/roles/invitations 四资源
// 共享同一 search schema 实例（四 tab 四列表——q/分页跨 tab 持久是接受的
// 语义，text 字段取四表过滤字段的并集）。REST 端点与收敛口径照抄
// lib/catalog.ts 的 useUsers/useTeams/useRoles/useInvitations。
// uikit 0.5.0 回流批（M4 整取面）：四集合走 clientCollection(restFetchAll)
// ——组 URL/信封展平/undefined 行过滤/错误收敛全归工厂内置（fetchRows 微
// 工厂与 toApiError 样板在本域归零）。

export type UserEntry = identitySchemas["schemas"]["v1User"];
export type TeamEntry = identitySchemas["schemas"]["v1Team"];
export type RoleEntry = identitySchemas["schemas"]["v1Role"];
export type InvitationEntry = identitySchemas["schemas"]["v1Invitation"];

// ---- 写面命令（批2 useCommand 化）----
// run 统一 runCommandMapped（400→rejected 保 apperr code、409→conflict、
// 其余 throw kernel ApiError）；失效 = kit selector 同资源缺省（M2 投影
// 匹配——["identity",…] 旧键桥零读者已退役，F1 回流批审计）。空串选值在
// run 内归一为 undefined（JSON 体省略字段——与旧弹窗手工三目同口径）。

export interface CreateUserInput {
  name: string;
  /** 空串 = default team（服务端裁定） */
  teamId?: string;
  roleId?: string;
  /** 空串 = 不设密码（后续 users 行内设密） */
  password?: string;
}

export const usersResource = defineResource({
  name: "users",
  getId: (row: UserEntry) => row.id ?? "",
  collection: clientCollection(
    restFetchAll<UserEntry>({
      path: () => "/v1/users",
      envelope: (body) => (body as { users?: Array<UserEntry | undefined> }).users ?? [],
      fetch: authFetch,
    }),
  ),
  commands: {
    create: {
      run: (input: CreateUserInput) =>
        runCommandMapped(() =>
          apiSend("/v1/users", "POST", {
            name: input.name,
            team_id: input.teamId || undefined,
            role_id: input.roleId || undefined,
            password: input.password || undefined,
          }),
        ),
    },
    remove: {
      run: (input: { id: string }) =>
        runCommandMapped(() => apiSend(`/v1/users/${encodeURIComponent(input.id)}`, "DELETE")),
    },
    // 行内设密：不改行集（旧行此动词无 invalidate——语义保留，invalidates
    // 显式置空也免默认同资源集合失效的白跑一趟）。
    setPassword: {
      run: (input: { id: string; password: string }) =>
        runCommandMapped(() =>
          apiSend(`/v1/users/${encodeURIComponent(input.id)}/password`, "POST", { password: input.password }),
        ),
      invalidates: [],
    },
  },
});

export const teamsResource = defineResource({
  name: "teams",
  getId: (row: TeamEntry) => row.id ?? "",
  collection: clientCollection(
    restFetchAll<TeamEntry>({
      path: () => "/v1/teams",
      envelope: (body) => (body as { teams?: Array<TeamEntry | undefined> }).teams ?? [],
      fetch: authFetch,
    }),
  ),
  commands: {
    create: {
      run: (input: { name: string }) => runCommandMapped(() => apiSend("/v1/teams", "POST", { name: input.name })),
    },
    remove: {
      run: (input: { id: string }) =>
        runCommandMapped(() => apiSend(`/v1/teams/${encodeURIComponent(input.id)}`, "DELETE")),
    },
  },
});

export const rolesResource = defineResource({
  name: "roles",
  getId: (row: RoleEntry) => row.id ?? "",
  collection: clientCollection(
    restFetchAll<RoleEntry>({
      path: () => "/v1/roles",
      envelope: (body) => (body as { roles?: Array<RoleEntry | undefined> }).roles ?? [],
      fetch: authFetch,
    }),
  ),
  commands: {
    create: {
      run: (input: { name: string; teamId?: string; scopes: string[] }) =>
        runCommandMapped(() =>
          apiSend("/v1/roles", "POST", {
            name: input.name,
            team_id: input.teamId || undefined,
            scopes: input.scopes,
          }),
        ),
      // roles 读面的存量键异类：catalog useRoles 在 ["settings","roles"]
    },
    remove: {
      run: (input: { id: string }) =>
        runCommandMapped(() => apiSend(`/v1/roles/${encodeURIComponent(input.id)}`, "DELETE")),
    },
  },
});

// 铸造响应的 reveal 载荷：secret 只在这一帧出现（SecretReveal 揭示链）。
export interface MintInvitationPayload {
  invitation?: InvitationEntry;
  secret?: string;
}

export const invitationsResource = defineResource({
  name: "invitations",
  getId: (row: InvitationEntry) => row.id ?? "",
  collection: clientCollection(
    restFetchAll<InvitationEntry>({
      path: () => "/v1/invitations",
      envelope: (body) => (body as { invitations?: Array<InvitationEntry | undefined> }).invitations ?? [],
      fetch: authFetch,
    }),
  ),
  commands: {
    create: {
      run: (input: { teamId?: string; roleId?: string; ttl?: string }) =>
        runCommandMapped<MintInvitationPayload>(() =>
          apiSend<MintInvitationPayload>("/v1/invitations", "POST", {
            team_id: input.teamId || undefined,
            role_id: input.roleId || undefined,
            ttl: input.ttl || undefined,
          }),
        ),
    },
  },
});

// 四列表各持 namespace 实例（F1 回流批 M1：users.q/teams.q/roles.q/
// invitations.q 并存互不携带，「q 跨 tab 持久」妥协退役；字段集保持旧并集
// ——text = Users（name/id）∪ Teams（name/id）∪ Roles（name/scopes——数组
// 字段客户端求值走 String() 逗连）∪ Invitations（id/team_id/role_id/
// created_by））。
export const usersSearch = collectionQuerySchema({
  namespace: "users",
  text: ["name", "id", "scopes", "team_id", "role_id", "created_by"],
  defaultSize: 20,
});
export const teamsSearch = collectionQuerySchema({
  namespace: "teams",
  text: ["name", "id", "scopes", "team_id", "role_id", "created_by"],
  defaultSize: 20,
});
export const rolesSearch = collectionQuerySchema({
  namespace: "roles",
  text: ["name", "id", "scopes", "team_id", "role_id", "created_by"],
  defaultSize: 20,
});
export const invitationsSearch = collectionQuerySchema({
  namespace: "invitations",
  text: ["name", "id", "scopes", "team_id", "role_id", "created_by"],
  defaultSize: 20,
});
