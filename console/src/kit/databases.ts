import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { fetchRows, qs } from "./http";

// 数据库域资源适配（2026-10-26 批1 useCollection 化）：REST 端点与收敛
// 口径照抄 lib/catalog.ts 的 useDatabases（rowsOf 同款 undefined 行过滤）。

export type DatabaseRow = structureSchemas["schemas"]["v1Database"];

// DATABASE_ENGINES 是平台引擎值域（创建对话框与列表 engine 下拉共用——
// 服务端模板矩阵的钉版枚举，值域静态故可进 schema filters 让 FilterBar
// 原生渲染下拉，不必页内自持动态值域）。
export const DATABASE_ENGINES = ["postgres", "pgvector", "redis", "mysql", "mongo"] as const;

export async function fetchDatabases(ctx: ResourceScope, signal?: AbortSignal): Promise<DatabaseRow[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  return fetchRows<DatabaseRow>(
    `/v1/databases${qs({ project_id: projectId })}`,
    (res) => (res as { databases?: Array<DatabaseRow | undefined> }).databases,
    signal,
  );
}

export const databasesResource = defineResource({
  name: "databases",
  getId: (row: DatabaseRow) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchDatabases),
});

// URL 搜索 schema：text 快筛 name/engine（与旧页 fieldsOf 同字段集）；
// engine 下拉走 schema filters（静态值域 = DATABASE_ENGINES，替换旧页
// 行内去重的自持 select——行为增量：即时过滤 + 进 URL）。
export const databasesSearch = collectionQuerySchema({
  text: ["name", "engine"],
  filters: [
    {
      field: "engine",
      label: "Engine",
      options: DATABASE_ENGINES.map((engine) => ({ value: engine, label: engine })),
    },
  ],
  defaultSize: 20,
});
