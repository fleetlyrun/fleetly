import { clientCollection, restFetchAll } from "@lynx-go/uikit-core/adapters";
import { defineResource } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as identitySchemas } from "@/api/identity";
import { authFetch } from "./http";

// 审计域资源适配（2026-10-26 批1 useCollection 化，平台域——无 scope）：
// GET /v1/audit 只读面，整取 limit 100 + clientCollection 客户端求值
// （text 快筛 source/actor/action/resource 即时过滤进 URL，替换旧页提交式
// 服务器过滤——服务端数据全量本就钳在 100 条上限，客户端过滤是等价且更
// 简的路线）。REST 端点与收敛口径照抄 lib/catalog.ts 的 useAudit。
// uikit 0.5.0 回流批（M4 整取面）：restFetchAll 直挂（limit 取数上限经
// params 闭包常量——整取方言 query 与服务端无关）。

export type AuditRow = identitySchemas["schemas"]["v1AuditEntry"];

const AUDIT_FETCH_LIMIT = 100;

export const auditResource = defineResource({
  name: "audit",
  getId: (row: AuditRow) => row.id ?? "",
  collection: clientCollection(
    restFetchAll<AuditRow>({
      path: () => "/v1/audit",
      params: () => ({ limit: AUDIT_FETCH_LIMIT }),
      envelope: (body) => (body as { entries?: Array<AuditRow | undefined> }).entries ?? [],
      fetch: authFetch,
    }),
  ),
});

export const auditSearch = collectionQuerySchema({
  text: ["source", "actor", "action", "resource"],
  defaultSize: 20,
});
