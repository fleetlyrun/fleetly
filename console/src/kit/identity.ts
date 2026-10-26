import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as identitySchemas } from "@/api/identity";
import { fetchRows } from "./http";

// 身份域资源适配（2026-10-26 批1 Wave2-B useCollection 化，平台域——无
// scope）：users/teams/roles/invitations 四资源共享同一 search schema 实例
//（四 tab 四列表——q/分页跨 tab 持久是接受的语义，text 字段取四表过滤
// 字段的并集）。REST 端点与收敛口径照抄 lib/catalog.ts 的
// useUsers/useTeams/useRoles/useInvitations（rowsOf 同款 undefined 行过滤；
// catalog 钩子保留——settings/写面伴读等其它消费面还在）。

export type UserEntry = identitySchemas["schemas"]["v1User"];
export type TeamEntry = identitySchemas["schemas"]["v1Team"];
export type RoleEntry = identitySchemas["schemas"]["v1Role"];
export type InvitationEntry = identitySchemas["schemas"]["v1Invitation"];

export async function fetchUsers(_ctx: ResourceScope, signal?: AbortSignal): Promise<UserEntry[]> {
  return fetchRows<UserEntry>("/v1/users", (res) => (res as { users?: Array<UserEntry | undefined> }).users, signal);
}

export async function fetchTeams(_ctx: ResourceScope, signal?: AbortSignal): Promise<TeamEntry[]> {
  return fetchRows<TeamEntry>("/v1/teams", (res) => (res as { teams?: Array<TeamEntry | undefined> }).teams, signal);
}

export async function fetchRoles(_ctx: ResourceScope, signal?: AbortSignal): Promise<RoleEntry[]> {
  return fetchRows<RoleEntry>("/v1/roles", (res) => (res as { roles?: Array<RoleEntry | undefined> }).roles, signal);
}

export async function fetchInvitations(_ctx: ResourceScope, signal?: AbortSignal): Promise<InvitationEntry[]> {
  return fetchRows<InvitationEntry>(
    "/v1/invitations",
    (res) => (res as { invitations?: Array<InvitationEntry | undefined> }).invitations,
    signal,
  );
}

export const usersResource = defineResource({
  name: "users",
  getId: (row: UserEntry) => row.id ?? "",
  collection: clientCollection(fetchUsers),
});

export const teamsResource = defineResource({
  name: "teams",
  getId: (row: TeamEntry) => row.id ?? "",
  collection: clientCollection(fetchTeams),
});

export const rolesResource = defineResource({
  name: "roles",
  getId: (row: RoleEntry) => row.id ?? "",
  collection: clientCollection(fetchRoles),
});

export const invitationsResource = defineResource({
  name: "invitations",
  getId: (row: InvitationEntry) => row.id ?? "",
  collection: clientCollection(fetchInvitations),
});

// 四 tab 共享实例：text = Users（name/id）∪ Teams（name/id）∪
// Roles（name/scopes——数组字段客户端求值走 String() 逗连，与旧 fieldsOf
// 展平同语义）∪ Invitations（id/team_id/role_id/created_by）。
export const identitySearch = collectionQuerySchema({
  text: ["name", "id", "scopes", "team_id", "role_id", "created_by"],
  defaultSize: 20,
});
