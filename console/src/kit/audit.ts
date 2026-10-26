import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as identitySchemas } from "@/api/identity";
import { fetchRows, qs } from "./http";

// 审计域资源适配（2026-10-26 批1 useCollection 化，平台域——无 scope）：
// GET /v1/audit 只读面，整取 limit 100 + clientCollection 客户端求值
// （text 快筛 source/actor/action/resource 即时过滤进 URL，替换旧页提交式
// 服务器过滤——服务端数据全量本就钳在 100 条上限，客户端过滤是等价且更
// 简的路线）。REST 端点与收敛口径照抄 lib/catalog.ts 的 useAudit。

export type AuditRow = identitySchemas["schemas"]["v1AuditEntry"];

const AUDIT_FETCH_LIMIT = 100;

export async function fetchAuditEntries(_ctx: ResourceScope, signal?: AbortSignal): Promise<AuditRow[]> {
  return fetchRows<AuditRow>(
    `/v1/audit${qs({ limit: AUDIT_FETCH_LIMIT })}`,
    (res) => (res as { entries?: Array<AuditRow | undefined> }).entries,
    signal,
  );
}

export const auditResource = defineResource({
  name: "audit",
  getId: (row: AuditRow) => row.id ?? "",
  collection: clientCollection(fetchAuditEntries),
});

export const auditSearch = collectionQuerySchema({
  text: ["source", "actor", "action", "resource"],
  defaultSize: 20,
});
