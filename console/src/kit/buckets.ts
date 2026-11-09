import { clientCollection, restFetchAll } from "@lynxland/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynxland/uikit-core/kernel";
import { collectionQuerySchema } from "@lynxland/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";
import { createResourcesPayload, resourcesBody } from "./databases";

// bucket store 域资源适配（B5 Console 姐妹页，ADR-0096 决策 8 / design §6.2）：
// kit/pubsubs 同构——REST 端点与命令面逐形照抄，kind 词差异（
// /v1/bucket-stores、bucket store 载体、silo 目录族）。**无备份命令族**（silo
// 转储挂账——目录 capabilities.backup=false，Console 不设 Backups tab）。
// Engine Catalog 已含 bucketstore 族，引擎下拉/创建表单/Settings 编辑全部
// 目录驱动零硬编码（silo v1 零旋钮——Settings 面只剩 resources）。

export type BucketStoreRow = structureSchemas["schemas"]["v1BucketStore"];

const bucketStoresAll = restFetchAll<BucketStoreRow>({
  path: () => "/v1/bucket-stores",
  params: (ctx) => ({ project_id: ctx.projectId }),
  envelope: (body) => (body as { bucket_stores?: Array<BucketStoreRow | undefined> }).bucket_stores ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（databases/pubsubs 同款）：projectId 缺席 = 空段不打请求。
async function fetchBucketStores(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<BucketStoreRow>> {
  return ctx.projectId ? bucketStoresAll(ctx, signal) : [];
}

// ---- 写路径命令 ----

// create 输入 = 创建对话框表单形（pubsubs 同构；无 restoreFrom——无备份族）。
export interface CreateBucketStoreInput {
  name: string;
  engine: string;
  version?: string;
  settings?: Readonly<Record<string, string>>;
  cpuMillis?: string;
  memoryMb?: string;
}

const createBucketStoreCommand: CommandDescriptor<CreateBucketStoreInput, { id?: string }> = {
  run: (input, ctx) =>
    runCommandMapped(() =>
      apiSend<{ id?: string }>("/v1/bucket-stores", "POST", {
        project_id: ctx.projectId,
        name: input.name,
        engine: input.engine,
        version: input.version || undefined,
        settings: input.settings && Object.keys(input.settings).length > 0 ? input.settings : undefined,
        resources: createResourcesPayload(input.cpuMillis, input.memoryMb),
      }),
    ),
};

// ---- Update 确认面（三家族对齐；kit/pubsubs update 命令同构）----

export interface UpdateBucketStoreInput {
  id: string;
  settings?: Readonly<Record<string, string>>;
  resources?: { cpuMillis: string; memoryMb: string };
  ticket?: string;
}

export type PreviewUpdateBucketStorePayload = structureSchemas["schemas"]["v1PreviewUpdateBucketStoreResponse"];
export type BucketStoreSettingChange = structureSchemas["schemas"]["v1DatabaseSettingChange"];
export type UpdateBucketStorePayload = structureSchemas["schemas"]["v1UpdateBucketStoreResponse"];

function bucketStoreUpdateBody(input: UpdateBucketStoreInput): Record<string, unknown> {
  return {
    ...(input.settings !== undefined ? { settings: { values: { ...input.settings } } } : {}),
    ...(input.resources !== undefined
      ? { resources: resourcesBody(input.resources.cpuMillis, input.resources.memoryMb) }
      : {}),
  };
}

// previewUpdate：只读预演 + 铸票（POST /v1/bucket-stores/preview-update）。
// 零旋钮族：settings diff 恒空、资源变更即 rollout=replace——确认框的
// 「将触发滚动重启」披露由 rollout 值驱动（settings-editor 共用体）。
const previewUpdateBucketStoreCommand: CommandDescriptor<UpdateBucketStoreInput, PreviewUpdateBucketStorePayload> = {
  invalidates: [],
  run: (input) =>
    runCommandMapped(() =>
      apiSend<PreviewUpdateBucketStorePayload>("/v1/bucket-stores/preview-update", "POST", {
        update: { bucket_store_id: input.id, ...bucketStoreUpdateBody(input) },
      }),
    ),
};

// update：PATCH /v1/bucket-stores/{id}（ConfirmableVerbs 强制集——带票发）。
const updateBucketStoreCommand: CommandDescriptor<UpdateBucketStoreInput, UpdateBucketStorePayload> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<UpdateBucketStorePayload>(`/v1/bucket-stores/${encodeURIComponent(input.id)}`, "PATCH", {
        ...bucketStoreUpdateBody(input),
        ...(input.ticket ? { confirmation: { ticket: input.ticket } } : {}),
      }),
    ),
};

const deleteBucketStoreCommand: CommandDescriptor<{ id: string }, void> = {
  run: ({ id }) => runCommandMapped(() => apiSend<void>(`/v1/bucket-stores/${encodeURIComponent(id)}`, "DELETE")),
};

const rotateBucketStoreCommand: CommandDescriptor<{ id: string }, void> = {
  run: ({ id }) =>
    runCommandMapped(() => apiSend<void>(`/v1/bucket-stores/${encodeURIComponent(id)}/rotate-password`, "POST", {})),
};

export const bucketStoresResource = defineResource({
  name: "bucket-stores",
  getId: (row: BucketStoreRow) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchBucketStores),
  commands: {
    create: createBucketStoreCommand,
    delete: deleteBucketStoreCommand,
    rotate: rotateBucketStoreCommand,
    previewUpdate: previewUpdateBucketStoreCommand,
    update: updateBucketStoreCommand,
  },
});

// URL 搜索 schema（pubsubs 同形）：text 快筛 name/engine；engine 下拉走
// schema filters（值域 = 目录 bucketstore 族现值，bucketStoresFilterSchema）。
const BUCKET_STORES_QUERY_TEXT = ["name", "engine"] as const;

export const bucketStoresSearch = collectionQuerySchema({
  text: BUCKET_STORES_QUERY_TEXT,
  filters: [{ field: "engine", label: "Engine" }],
  defaultSize: 20,
});

export function bucketStoresFilterSchema(engines: ReadonlyArray<string>) {
  return collectionQuerySchema({
    text: BUCKET_STORES_QUERY_TEXT,
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

// 行查找读面大页 schema（详情页项目全集；pubsubsLookup 同形）。
export const bucketStoresLookup = collectionQuerySchema({ defaultSize: 200 });
