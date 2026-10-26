import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { apiSend } from "@/api/client";
import { fetchRows, qs } from "./http";
import { runCommandMapped } from "./errors";

// 数据库域资源适配（2026-10-26 批1 useCollection 化；批2 写路径命令化）：
// REST 端点与收敛口径照抄 lib/catalog.ts 的 useDatabases（rowsOf 同款
// undefined 行过滤）；实体动词（create/delete/backup/rotate）挂 resource
// commands，动作类（browse 开窗）裸 descriptor。

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

// ---- 写路径命令（批2） ----

// create 输入 = 创建对话框表单形（projectId 经 ctx scope 线程注入，不进
// 表单值）；restoreFrom 同字段即 restore 语义（ADR-0039：restore 恒新建
// 库，源库不覆写——BackupRow 行内 restore 与创建对话框共用本命令）。
export interface CreateDatabaseInput {
  name: string;
  engine: string;
  restoreFrom?: string;
}

const createDatabaseCommand: CommandDescriptor<CreateDatabaseInput, { id?: string }> = {
  run: (input, ctx) =>
    runCommandMapped(() =>
      apiSend<{ id?: string }>("/v1/databases", "POST", {
        project_id: ctx.projectId,
        name: input.name,
        engine: input.engine,
        restore_from_backup: input.restoreFrom || undefined,
      }),
    ),
};

const deleteDatabaseCommand: CommandDescriptor<{ id: string }, void> = {
  run: ({ id }) => runCommandMapped(() => apiSend<void>(`/v1/databases/${encodeURIComponent(id)}`, "DELETE")),
};

const backupDatabaseCommand: CommandDescriptor<{ id: string }, void> = {
  run: ({ id }) =>
    runCommandMapped(() => apiSend<void>(`/v1/databases/${encodeURIComponent(id)}/backups`, "POST", {})),
};

const rotateDatabaseCommand: CommandDescriptor<{ id: string }, void> = {
  run: ({ id }) =>
    runCommandMapped(() => apiSend<void>(`/v1/databases/${encodeURIComponent(id)}/rotate-password`, "POST", {})),
};

// browse 开窗（动作类裸 descriptor，ADR-0051 一次性 ticket 会话）：受理
// read-only 会话并回跳 url，无资源失效面（无 __resource 锚——useCommand
// 不做任何 invalidate）。
export const browseDatabaseCommand: CommandDescriptor<{ id: string }, { url?: string }> = {
  run: ({ id }) =>
    runCommandMapped(() => apiSend<{ url?: string }>(`/v1/databases/${encodeURIComponent(id)}/browse`, "POST", {})),
};

export const databasesResource = defineResource({
  name: "databases",
  getId: (row: DatabaseRow) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchDatabases),
  // 存量键失效桥：对齐旧页 invalidateDatabases 的 catalog 键
  // （["resources","databases",projectId]——useDatabases 的读键）。
  legacyKeys: (scope) => [["resources", "databases", scope.projectId]],
  commands: {
    create: createDatabaseCommand,
    delete: deleteDatabaseCommand,
    backup: backupDatabaseCommand,
    rotate: rotateDatabaseCommand,
  },
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
