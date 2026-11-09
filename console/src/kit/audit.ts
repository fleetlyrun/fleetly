import { restCollection } from "@lynxland/uikit-core/adapters";
import { defineResource, evalPredicate } from "@lynxland/uikit-core/kernel";
import { collectionQuerySchema } from "@lynxland/uikit-core/schema";
import type { components as identitySchemas } from "@/api/identity";
import { authFetch } from "./http";

// 审计域资源适配（2026-11-10 游标翻页批）：GET /v1/audit 服务端 keyset
// 方言——before_seq（URL window.cursor）取更旧一页，limit 恒 100；nextCursor
// 仅在整页占满时下发（末条 seq 回显），短页即到底。旧整取方言（limit 100
// 客户端求值）只能看到最新 100 条，审计取证面在数据增长后不可翻页。
//
// 文本快筛（q）保持客户端语义，但只作用于当前页（服务端无通用 q 谓词；
// 精确 source/action/actor/resource 过滤仍走 proto 字段）。过滤变化由
// uikit 绑定层清游标回第一页。

export type AuditRow = identitySchemas["schemas"]["v1AuditEntry"];

export const AUDIT_PAGE_SIZE = 100;

function auditRows(body: unknown): Array<AuditRow | undefined> {
  return (body as { entries?: Array<AuditRow | undefined> }).entries ?? [];
}

const auditPage = restCollection<AuditRow>({
  path: () => "/v1/audit",
  params: (query) => ({
    before_seq: query.window.cursor,
    limit: AUDIT_PAGE_SIZE,
  }),
  envelope: (body) => auditRows(body),
  nextCursor: (body) => {
    const rows = auditRows(body);
    if (rows.length < AUDIT_PAGE_SIZE) return undefined; // 短页 = 到底
    const last = rows[rows.length - 1]?.seq;
    return typeof last === "string" && last !== "" ? last : undefined;
  },
  fetch: authFetch,
});

export const auditResource = defineResource({
  name: "audit",
  getId: (row: AuditRow) => row.id ?? "",
  collection: async (query, ctx, signal) => {
    const segment = await auditPage(query, ctx, signal);
    if (query.predicate === undefined) return segment;
    // 页内快筛：保留服务端游标（nextCursor 来自整页原始行数），行投影
    // 只影响展示。
    return { ...segment, rows: segment.rows.filter((row) => evalPredicate(query.predicate, row)) };
  },
});

export const auditSearch = collectionQuerySchema({
  text: ["source", "actor", "action", "resource"],
  defaultSize: AUDIT_PAGE_SIZE,
});
