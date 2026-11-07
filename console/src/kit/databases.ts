import { clientCollection, restFetchAll } from "@lynxland/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynxland/uikit-core/kernel";
import { collectionQuerySchema } from "@lynxland/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";

// 数据库域资源适配（2026-10-26 批1 useCollection 化；批2 写路径命令化）：
// REST 端点与收敛口径照抄 lib/catalog.ts 的 useDatabases；实体动词
// （create/delete/backup/rotate）挂 resource commands，动作类（browse 开窗）
// 裸 descriptor。uikit 0.5.0 回流批（M4 整取面）：守卫门 + restFetchAll
//（fetchRows/qs/toApiError 样板归零）。

export type DatabaseRow = structureSchemas["schemas"]["v1Database"];

// B3（ADR-0094 决策 4）：引擎值域硬编码常量（DATABASE_ENGINES）退役——引擎/
// 版本/旋钮的唯一取值面是 Engine Catalog（kit/data-engines 的
// dataEnginesResource）；列表引擎下拉经 databasesFilterSchema 喂目录值域。

const databasesAll = restFetchAll<DatabaseRow>({
  path: () => "/v1/databases",
  params: (ctx) => ({ project_id: ctx.projectId }),
  envelope: (body) => (body as { databases?: Array<DatabaseRow | undefined> }).databases ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（旧 fetcher 守卫语义保留）：projectId 缺席 = 空段不打请求。
async function fetchDatabases(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<DatabaseRow>> {
  return ctx.projectId ? databasesAll(ctx, signal) : [];
}

// ---- 写路径命令（批2） ----

// create 输入 = 创建对话框表单形（projectId 经 ctx scope 线程注入，不进
// 表单值）；restoreFrom 同字段即 restore 语义（ADR-0039：restore 恒新建
// 库，源库不覆写——BackupRow 行内 restore 与创建对话框共用本命令）。
// B3 目录驱动面：version 空 = 引擎 Default（受理位解析）；settings 只携
// 已设置键（缺席 = 引擎缺省）；资源轴空串/0 = 该轴无限制（双轴皆无 =
// resources 消息缺席——现状语义）。
export interface CreateDatabaseInput {
  name: string;
  engine: string;
  restoreFrom?: string;
  version?: string;
  settings?: Readonly<Record<string, string>>;
  cpuMillis?: string;
  memoryMb?: string;
}

// 资源轴表单值 → 整数（空串 = 0 = 该轴无限制；非有限值同按 0——数值输入
// 控件不产出非数字串，受理位上限另有执法）。
function resourceAxis(value: string | undefined): number {
  const trimmed = (value ?? "").trim();
  if (trimmed === "") return 0;
  const parsed = Math.trunc(Number(trimmed));
  return Number.isFinite(parsed) ? parsed : 0;
}

// resourcesBody 折资源轴为 DatabaseResources 线上形（0 轴不出键——protojson
// 零值缺省同义）。B4c 起 pubsub 族共用（Update/Preview 载荷同形）。
export function resourcesBody(cpuMillis: string | undefined, memoryMb: string | undefined): { cpu_millis?: number; memory_mb?: number } {
  const cpu = resourceAxis(cpuMillis);
  const memory = resourceAxis(memoryMb);
  return { ...(cpu !== 0 ? { cpu_millis: cpu } : {}), ...(memory !== 0 ? { memory_mb: memory } : {}) };
}

// createResourcesPayload：创建面资源消息（双轴皆无限制 = 缺席，现状语义）。
export function createResourcesPayload(
  cpuMillis: string | undefined,
  memoryMb: string | undefined,
): { cpu_millis?: number; memory_mb?: number } | undefined {
  const body = resourcesBody(cpuMillis, memoryMb);
  return Object.keys(body).length === 0 ? undefined : body;
}

const createDatabaseCommand: CommandDescriptor<CreateDatabaseInput, { id?: string }> = {
  run: (input, ctx) =>
    runCommandMapped(() =>
      apiSend<{ id?: string }>("/v1/databases", "POST", {
        project_id: ctx.projectId,
        name: input.name,
        engine: input.engine,
        restore_from_backup: input.restoreFrom || undefined,
        version: input.version || undefined,
        settings: input.settings && Object.keys(input.settings).length > 0 ? input.settings : undefined,
        resources: createResourcesPayload(input.cpuMillis, input.memoryMb),
      }),
    ),
};

// ---- Update 双车道（B3 用户面；B2d ADR-0095 决策 5/9 + ADR-0089）----
//
// update 输入 = Settings tab 的全集表单（Console 持有期望态全集——直发全量
// 替换，无需 CLI 的补丁合并）：settings 在场 = 旋钮期望态全集（空对象 =
// 清空全部旋钮）、缺席 = 旋钮面不变；resources 在场 = 资源面全集（空串轴 =
// 该轴无限制）、缺席 = 资源面不变。ticket = PreviewUpdateDatabase 铸的
// Confirm Ticket（票绑合并后期望态摘要——确认发的必须是预览过的那份输入，
// 组件侧以快照保证）。
export interface UpdateDatabaseInput {
  id: string;
  settings?: Readonly<Record<string, string>>;
  resources?: { cpuMillis: string; memoryMb: string };
  ticket?: string;
}

export type PreviewUpdateDatabasePayload = structureSchemas["schemas"]["v1PreviewUpdateDatabaseResponse"];
export type DatabaseSettingChange = structureSchemas["schemas"]["v1DatabaseSettingChange"];
export type UpdateDatabasePayload = structureSchemas["schemas"]["v1UpdateDatabaseResponse"];

// updateBody：Update/Preview 共用的期望态载荷（预演与执行同源——票面摘要
// 与 diff 的单一真源在服务端 admitUpdate，客户端只保证两次发同一份）。
function updateBody(input: UpdateDatabaseInput): Record<string, unknown> {
  return {
    ...(input.settings !== undefined ? { settings: { values: { ...input.settings } } } : {}),
    ...(input.resources !== undefined
      ? { resources: resourcesBody(input.resources.cpuMillis, input.resources.memoryMb) }
      : {}),
  };
}

// previewUpdate：只读预演 + 铸票（POST /v1/databases/preview-update，请求
// 内嵌同一 UpdateDatabaseRequest——受理拒绝面与 Update 逐规则一致）。铸票
// 动词不进失效面（invalidates 空——无终态可持有，previewDeploy 同理）。
const previewUpdateDatabaseCommand: CommandDescriptor<UpdateDatabaseInput, PreviewUpdateDatabasePayload> = {
  invalidates: [],
  run: (input) =>
    runCommandMapped(() =>
      apiSend<PreviewUpdateDatabasePayload>("/v1/databases/preview-update", "POST", {
        update: { database_id: input.id, ...updateBody(input) },
      }),
    ),
};

// update：PATCH /v1/databases/{id}（ConfirmableVerbs 强制集——带票发）。结果
// 两分：E_INVALID_ARGUMENT/确认族 → rejected（code 保留，STALE/MISMATCH
// 分诊重预览）；E_CONFLICT（未 running/在滚）与 E_SETTINGS_APPLY_FAILED
//（引擎拒改、行不动）→ conflict（409）。ok 后失效同资源（行回显换新值）。
const updateDatabaseCommand: CommandDescriptor<UpdateDatabaseInput, UpdateDatabasePayload> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<UpdateDatabasePayload>(`/v1/databases/${encodeURIComponent(input.id)}`, "PATCH", {
        ...updateBody(input),
        ...(input.ticket ? { confirmation: { ticket: input.ticket } } : {}),
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
  commands: {
    create: createDatabaseCommand,
    delete: deleteDatabaseCommand,
    backup: backupDatabaseCommand,
    rotate: rotateDatabaseCommand,
    previewUpdate: previewUpdateDatabaseCommand,
    update: updateDatabaseCommand,
  },
});

// URL 搜索 schema：text 快筛 name/engine（与旧页 fieldsOf 同字段集）；
// engine 下拉走 schema filters。B3：值域不再静态——URL 编解码与路由
// validateSearch 用本常量（filters 只服务 FilterBar 渲染，编解码不按值域
// 校验），FilterBar 渲染用 databasesFilterSchema(目录引擎名) 的同形实例。
const DATABASES_QUERY_TEXT = ["name", "engine"] as const;

export const databasesSearch = collectionQuerySchema({
  text: DATABASES_QUERY_TEXT,
  filters: [{ field: "engine", label: "Engine" }],
  defaultSize: 20,
});

// databasesFilterSchema：FilterBar 的同形 schema（text/filters 字段集与
// databasesSearch 逐项一致，仅 engine 下拉值域换成目录现值——Engine
// Catalog 是唯一取值面）。
export function databasesFilterSchema(engines: ReadonlyArray<string>) {
  return collectionQuerySchema({
    text: DATABASES_QUERY_TEXT,
    filters: [
      {
        field: "engine",
        label: "Engine",
        options: engines.map((engine) => ({ value: engine, label: engine })),
      },
    ],
    defaultSize: 20,
  });
}

// ---- per-Database 备份列表（2026-10-27 批4 读面收编：catalog
// useDatabaseBackups 退役）——详情页 Backups tab 的单库恢复面。与
// kit/backups 的跨项目 database-backups 台账分立（那是台账行形态，这是
// 单库备份原行）；端点与收敛照抄 catalog，轮询由消费方
// query.refetchInterval 注入 15s 同拍。 ----

export type DatabaseBackupEntry = structureSchemas["schemas"]["v1Backup"];

const databaseBackupListAll = restFetchAll<DatabaseBackupEntry>({
  path: (ctx) => `/v1/databases/${encodeURIComponent(ctx.databaseId ?? "")}/backups`,
  envelope: (body) => (body as { backups?: Array<DatabaseBackupEntry | undefined> }).backups ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（旧 fetcher 守卫语义保留）：databaseId 缺席 = 空段不打请求。
async function fetchDatabaseBackupList(
  ctx: ResourceScope,
  signal?: AbortSignal,
): Promise<ReadonlyArray<DatabaseBackupEntry>> {
  return ctx.databaseId ? databaseBackupListAll(ctx, signal) : [];
}

export const databaseBackupListResource = defineResource({
  name: "database-backup-list",
  getId: (row: DatabaseBackupEntry) => row.id ?? "",
  scope: ["projectId", "databaseId"],
  collection: clientCollection(fetchDatabaseBackupList),
  // 存量键失效桥：catalog useDatabaseBackups 的
  // ["resources","database-backups",databaseId] 前缀
});

// 备份 tab 读面用大页 schema（恢复台账要全集——窗口不截断行集）。
export const databaseBackupListLookup = collectionQuerySchema({ defaultSize: 200 });

// ---- 行查找/join 读面大页 schema（2026-10-27 批4）：详情页的库行查找要
// 项目全集——窗口不截断行集；无 text/filters（列表页的快筛不串台）；只读
// 消费不写 URL，与 databasesSearch 分立。 ----
export const databasesLookup = collectionQuerySchema({ defaultSize: 200 });
