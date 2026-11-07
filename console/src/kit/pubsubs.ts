import { clientCollection, restFetchAll } from "@lynxland/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynxland/uikit-core/kernel";
import { collectionQuerySchema } from "@lynxland/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";
import { createResourcesPayload, resourcesBody } from "./databases";

// pubsub 域资源适配（B4c Console 姐妹页，ADR-0096 决策 8 / design §6.2）：
// kit/databases 同构——REST 端点与命令面逐形照抄，family 词差异（
// /v1/pubsubs、pubsub 载体、nats 目录族）。Engine Catalog 已含 pubsub 族
// （B4b），引擎下拉/创建表单/Settings 编辑全部目录驱动零硬编码。

export type PubSubRow = structureSchemas["schemas"]["v1PubSub"];

const pubsubsAll = restFetchAll<PubSubRow>({
  path: () => "/v1/pubsubs",
  params: (ctx) => ({ project_id: ctx.projectId }),
  envelope: (body) => (body as { pubsubs?: Array<PubSubRow | undefined> }).pubsubs ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（databases 同款）：projectId 缺席 = 空段不打请求。
async function fetchPubSubs(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<PubSubRow>> {
  return ctx.projectId ? pubsubsAll(ctx, signal) : [];
}

// ---- 写路径命令 ----

// create 输入 = 创建对话框表单形（databases 同构；restoreFrom = 逐流恢复源）。
export interface CreatePubSubInput {
  name: string;
  engine: string;
  restoreFrom?: string;
  version?: string;
  settings?: Readonly<Record<string, string>>;
  cpuMillis?: string;
  memoryMb?: string;
}

const createPubSubCommand: CommandDescriptor<CreatePubSubInput, { id?: string }> = {
  run: (input, ctx) =>
    runCommandMapped(() =>
      apiSend<{ id?: string }>("/v1/pubsubs", "POST", {
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

// ---- Update 确认面（B4c 三家族对齐；kit/databases update 命令同构）----

export interface UpdatePubSubInput {
  id: string;
  settings?: Readonly<Record<string, string>>;
  resources?: { cpuMillis: string; memoryMb: string };
  ticket?: string;
}

export type PreviewUpdatePubSubPayload = structureSchemas["schemas"]["v1PreviewUpdatePubSubResponse"];
export type PubSubSettingChange = structureSchemas["schemas"]["v1DatabaseSettingChange"];
export type UpdatePubSubPayload = structureSchemas["schemas"]["v1UpdatePubSubResponse"];

function pubsubUpdateBody(input: UpdatePubSubInput): Record<string, unknown> {
  return {
    ...(input.settings !== undefined ? { settings: { values: { ...input.settings } } } : {}),
    ...(input.resources !== undefined
      ? { resources: resourcesBody(input.resources.cpuMillis, input.resources.memoryMb) }
      : {}),
  };
}

// previewUpdate：只读预演 + 铸票（POST /v1/pubsubs/preview-update）。本族首批
// 旋钮全 static：rollout 恒 replace——确认框的「将触发滚动重启」披露照常由
// rollout 值驱动（settings-editor 共用体）。
const previewUpdatePubSubCommand: CommandDescriptor<UpdatePubSubInput, PreviewUpdatePubSubPayload> = {
  invalidates: [],
  run: (input) =>
    runCommandMapped(() =>
      apiSend<PreviewUpdatePubSubPayload>("/v1/pubsubs/preview-update", "POST", {
        update: { pubsub_id: input.id, ...pubsubUpdateBody(input) },
      }),
    ),
};

// update：PATCH /v1/pubsubs/{id}（ConfirmableVerbs 强制集——带票发）。
const updatePubSubCommand: CommandDescriptor<UpdatePubSubInput, UpdatePubSubPayload> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<UpdatePubSubPayload>(`/v1/pubsubs/${encodeURIComponent(input.id)}`, "PATCH", {
        ...pubsubUpdateBody(input),
        ...(input.ticket ? { confirmation: { ticket: input.ticket } } : {}),
      }),
    ),
};

const deletePubSubCommand: CommandDescriptor<{ id: string }, void> = {
  run: ({ id }) => runCommandMapped(() => apiSend<void>(`/v1/pubsubs/${encodeURIComponent(id)}`, "DELETE")),
};

const backupPubSubCommand: CommandDescriptor<{ id: string }, void> = {
  run: ({ id }) =>
    runCommandMapped(() => apiSend<void>(`/v1/pubsubs/${encodeURIComponent(id)}/backups`, "POST", {})),
};

const rotatePubSubCommand: CommandDescriptor<{ id: string }, void> = {
  run: ({ id }) =>
    runCommandMapped(() => apiSend<void>(`/v1/pubsubs/${encodeURIComponent(id)}/rotate-password`, "POST", {})),
};

export const pubsubsResource = defineResource({
  name: "pubsubs",
  getId: (row: PubSubRow) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchPubSubs),
  commands: {
    create: createPubSubCommand,
    delete: deletePubSubCommand,
    backup: backupPubSubCommand,
    rotate: rotatePubSubCommand,
    previewUpdate: previewUpdatePubSubCommand,
    update: updatePubSubCommand,
  },
});

// URL 搜索 schema（databases 同形）：text 快筛 name/engine；engine 下拉走
// schema filters（值域 = 目录 pubsub 族现值，pubsubsFilterSchema）。
const PUBSUBS_QUERY_TEXT = ["name", "engine"] as const;

export const pubsubsSearch = collectionQuerySchema({
  text: PUBSUBS_QUERY_TEXT,
  filters: [{ field: "engine", label: "Engine" }],
  defaultSize: 20,
});

export function pubsubsFilterSchema(engines: ReadonlyArray<string>) {
  return collectionQuerySchema({
    text: PUBSUBS_QUERY_TEXT,
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

// 行查找读面大页 schema（详情页项目全集；databasesLookup 同形）。
export const pubsubsLookup = collectionQuerySchema({ defaultSize: 200 });

// ---- per-PubSub 备份列表（详情页 Backups tab；databaseBackupListResource
// 同构——台账行共用 v1Backup，实例轴在 database_id 字段承载实例行 id）----

export type PubSubBackupEntry = structureSchemas["schemas"]["v1Backup"];

const pubsubBackupListAll = restFetchAll<PubSubBackupEntry>({
  path: (ctx) => `/v1/pubsubs/${encodeURIComponent(ctx.pubsubId ?? "")}/backups`,
  envelope: (body) => (body as { backups?: Array<PubSubBackupEntry | undefined> }).backups ?? [],
  fetch: authFetch,
});

async function fetchPubSubBackupList(
  ctx: ResourceScope,
  signal?: AbortSignal,
): Promise<ReadonlyArray<PubSubBackupEntry>> {
  return ctx.pubsubId ? pubsubBackupListAll(ctx, signal) : [];
}

export const pubsubBackupListResource = defineResource({
  name: "pubsub-backup-list",
  getId: (row: PubSubBackupEntry) => row.id ?? "",
  scope: ["projectId", "pubsubId"],
  collection: clientCollection(fetchPubSubBackupList),
});

export const pubsubBackupListLookup = collectionQuerySchema({ defaultSize: 200 });
