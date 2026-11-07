// 子资源域资源适配（B8，ADR-0096 决策 10 / design §4.7：Topic〔kafka 协议
// 族〕/Stream〔nats〕/Bucket〔bucket store〕声明行 + admin 工具容器收敛）：
// kit/pubsubs 同构——REST 端点逐形照抄（/v1/pubsubs/{id}/topics|streams、
// /v1/bucket-stores/{id}/buckets）；Update/Delete 都是 ConfirmableVerbs
// （standard/wide 档）——确认面走 preview 铸票 + 带票发两段流（kit/pubsubs
// 的 update 同构 + delete 的删除披露行）。

import { clientCollection, restFetchAll } from "@lynxland/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynxland/uikit-core/kernel";
import { collectionQuerySchema } from "@lynxland/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { apiFetch, apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";

export type TopicRow = structureSchemas["schemas"]["v1Topic"];
export type StreamRow = structureSchemas["schemas"]["v1Stream"];
export type SubBucketRow = structureSchemas["schemas"]["v1Bucket"];
export type SubResourceChange = structureSchemas["schemas"]["v1SubResourceChange"];
export type PreviewUpdateTopicPayload = structureSchemas["schemas"]["v1PreviewUpdateTopicResponse"];
export type PreviewUpdateStreamPayload = structureSchemas["schemas"]["v1PreviewUpdateStreamResponse"];
export type PreviewUpdateBucketPayload = structureSchemas["schemas"]["v1PreviewUpdateBucketResponse"];
export type PreviewDeleteTopicPayload = structureSchemas["schemas"]["v1PreviewDeleteTopicResponse"];
export type PreviewDeleteStreamPayload = structureSchemas["schemas"]["v1PreviewDeleteStreamResponse"];
export type PreviewDeleteBucketPayload = structureSchemas["schemas"]["v1PreviewDeleteBucketResponse"];

// ---- topics（kafka 协议族 pubsub 实例子资源） ----

const topicsAll = restFetchAll<TopicRow>({
  path: (ctx) => `/v1/pubsubs/${encodeURIComponent(ctx.pubsubId ?? "")}/topics`,
  envelope: (body) => (body as { topics?: Array<TopicRow | undefined> }).topics ?? [],
  fetch: authFetch,
});

async function fetchTopics(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<TopicRow>> {
  return ctx.pubsubId ? topicsAll(ctx, signal) : [];
}

export interface CreateTopicInput {
  pubsubId: string;
  name: string;
  partitions?: number;
  retentionMs?: number;
}

const createTopicCommand: CommandDescriptor<CreateTopicInput, { id?: string }> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<{ id?: string }>(`/v1/pubsubs/${encodeURIComponent(input.pubsubId)}/topics`, "POST", {
        name: input.name,
        partitions: input.partitions || undefined,
        retention_ms: input.retentionMs || undefined,
      }),
    ),
};

export interface UpdateTopicInput {
  pubsubId: string;
  id: string;
  partitions?: number;
  retentionMs?: number | null;
  ticket?: string;
}

const topicUpdateBody = (input: UpdateTopicInput): Record<string, unknown> => ({
  ...(input.partitions !== undefined ? { partitions: input.partitions } : {}),
  ...(input.retentionMs !== undefined ? { retention_ms: input.retentionMs ?? 0 } : {}),
});

const previewUpdateTopicCommand: CommandDescriptor<UpdateTopicInput, PreviewUpdateTopicPayload> = {
  invalidates: [],
  run: (input) =>
    runCommandMapped(() =>
      apiSend<PreviewUpdateTopicPayload>("/v1/pubsubs/topics/preview-update", "POST", {
        update: { pubsub_id: input.pubsubId, topic_id: input.id, ...topicUpdateBody(input) },
      }),
    ),
};

const updateTopicCommand: CommandDescriptor<UpdateTopicInput, { topic?: TopicRow }> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<{ topic?: TopicRow }>(
        `/v1/pubsubs/${encodeURIComponent(input.pubsubId)}/topics/${encodeURIComponent(input.id)}`,
        "PATCH",
        { ...topicUpdateBody(input), ...(input.ticket ? { confirmation: { ticket: input.ticket } } : {}) },
      ),
    ),
};

const previewDeleteTopicCommand: CommandDescriptor<{ pubsubId: string; id: string }, PreviewDeleteTopicPayload> = {
  invalidates: [],
  run: ({ pubsubId, id }) =>
    runCommandMapped(() =>
      apiSend<PreviewDeleteTopicPayload>("/v1/pubsubs/topics/preview-delete", "POST", {
        pubsub_id: pubsubId,
        topic_id: id,
      }),
    ),
};

// delete：DELETE 无 body（proto 注解无 body 段）——confirmation 走 query 参数
// confirmation.ticket=…（gateway 生成面；DeleteApp 同款既有契约形态，
// cache:"no-store" 收口带票 URL 不入浏览器缓存）。
const deleteTopicCommand: CommandDescriptor<{ pubsubId: string; id: string; ticket?: string }, void> = {
  run: ({ pubsubId, id, ticket }) =>
    runCommandMapped(async () => {
      await apiFetch(
        `/v1/pubsubs/${encodeURIComponent(pubsubId)}/topics/${encodeURIComponent(id)}${ticketQuery(ticket)}`,
        { method: "DELETE", cache: "no-store" },
      );
    }),
};

export const topicsResource = defineResource({
  name: "pubsub-topics",
  getId: (row: TopicRow) => row.id ?? "",
  scope: ["projectId", "pubsubId"],
  collection: clientCollection(fetchTopics),
  commands: {
    create: createTopicCommand,
    previewUpdate: previewUpdateTopicCommand,
    update: updateTopicCommand,
    previewDelete: previewDeleteTopicCommand,
    delete: deleteTopicCommand,
  },
});

export const topicsLookup = collectionQuerySchema({ defaultSize: 200 });

// ---- streams（nats pubsub 实例子资源） ----

const streamsAll = restFetchAll<StreamRow>({
  path: (ctx) => `/v1/pubsubs/${encodeURIComponent(ctx.pubsubId ?? "")}/streams`,
  envelope: (body) => (body as { streams?: Array<StreamRow | undefined> }).streams ?? [],
  fetch: authFetch,
});

async function fetchStreams(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<StreamRow>> {
  return ctx.pubsubId ? streamsAll(ctx, signal) : [];
}

export interface CreateStreamInput {
  pubsubId: string;
  name: string;
  subjects: string;
  retention: string;
  storage: string;
  maxStreamSize?: number;
}

const createStreamCommand: CommandDescriptor<CreateStreamInput, { id?: string }> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<{ id?: string }>(`/v1/pubsubs/${encodeURIComponent(input.pubsubId)}/streams`, "POST", {
        name: input.name,
        subjects: splitSubjects(input.subjects),
        retention: input.retention || undefined,
        storage: input.storage || undefined,
        max_stream_size: input.maxStreamSize || undefined,
      }),
    ),
};

export interface UpdateStreamInput {
  pubsubId: string;
  id: string;
  subjects?: string | null;
  maxStreamSize?: number | null;
  ticket?: string;
}

const streamUpdateBody = (input: UpdateStreamInput): Record<string, unknown> => ({
  ...(input.subjects !== undefined ? { subjects: splitSubjects(input.subjects ?? "") } : {}),
  ...(input.maxStreamSize !== undefined ? { max_stream_size: input.maxStreamSize ?? 0 } : {}),
});

const previewUpdateStreamCommand: CommandDescriptor<UpdateStreamInput, PreviewUpdateStreamPayload> = {
  invalidates: [],
  run: (input) =>
    runCommandMapped(() =>
      apiSend<PreviewUpdateStreamPayload>("/v1/pubsubs/streams/preview-update", "POST", {
        update: { pubsub_id: input.pubsubId, stream_id: input.id, ...streamUpdateBody(input) },
      }),
    ),
};

const updateStreamCommand: CommandDescriptor<UpdateStreamInput, { stream?: StreamRow }> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<{ stream?: StreamRow }>(
        `/v1/pubsubs/${encodeURIComponent(input.pubsubId)}/streams/${encodeURIComponent(input.id)}`,
        "PATCH",
        { ...streamUpdateBody(input), ...(input.ticket ? { confirmation: { ticket: input.ticket } } : {}) },
      ),
    ),
};

const previewDeleteStreamCommand: CommandDescriptor<{ pubsubId: string; id: string }, PreviewDeleteStreamPayload> = {
  invalidates: [],
  run: ({ pubsubId, id }) =>
    runCommandMapped(() =>
      apiSend<PreviewDeleteStreamPayload>("/v1/pubsubs/streams/preview-delete", "POST", {
        pubsub_id: pubsubId,
        stream_id: id,
      }),
    ),
};

const deleteStreamCommand: CommandDescriptor<{ pubsubId: string; id: string; ticket?: string }, void> = {
  run: ({ pubsubId, id, ticket }) =>
    runCommandMapped(async () => {
      await apiFetch(
        `/v1/pubsubs/${encodeURIComponent(pubsubId)}/streams/${encodeURIComponent(id)}${ticketQuery(ticket)}`,
        { method: "DELETE", cache: "no-store" },
      );
    }),
};

export const streamsResource = defineResource({
  name: "pubsub-streams",
  getId: (row: StreamRow) => row.id ?? "",
  scope: ["projectId", "pubsubId"],
  collection: clientCollection(fetchStreams),
  commands: {
    create: createStreamCommand,
    previewUpdate: previewUpdateStreamCommand,
    update: updateStreamCommand,
    previewDelete: previewDeleteStreamCommand,
    delete: deleteStreamCommand,
  },
});

export const streamsLookup = collectionQuerySchema({ defaultSize: 200 });

// ---- buckets（bucket store 实例子资源） ----

const subBucketsAll = restFetchAll<SubBucketRow>({
  path: (ctx) => `/v1/bucket-stores/${encodeURIComponent(ctx.bucketStoreId ?? "")}/buckets`,
  envelope: (body) => (body as { buckets?: Array<SubBucketRow | undefined> }).buckets ?? [],
  fetch: authFetch,
});

async function fetchSubBuckets(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<SubBucketRow>> {
  return ctx.bucketStoreId ? subBucketsAll(ctx, signal) : [];
}

export interface CreateSubBucketInput {
  bucketStoreId: string;
  name: string;
  quotaBytes?: number;
}

const createSubBucketCommand: CommandDescriptor<CreateSubBucketInput, { id?: string }> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<{ id?: string }>(`/v1/bucket-stores/${encodeURIComponent(input.bucketStoreId)}/buckets`, "POST", {
        name: input.name,
        quota_bytes: input.quotaBytes || undefined,
      }),
    ),
};

export interface UpdateSubBucketInput {
  bucketStoreId: string;
  id: string;
  quotaBytes?: number | null;
  ticket?: string;
}

const bucketUpdateBody = (input: UpdateSubBucketInput): Record<string, unknown> => ({
  ...(input.quotaBytes !== undefined ? { quota_bytes: input.quotaBytes ?? 0 } : {}),
});

const previewUpdateBucketCommand: CommandDescriptor<UpdateSubBucketInput, PreviewUpdateBucketPayload> = {
  invalidates: [],
  run: (input) =>
    runCommandMapped(() =>
      apiSend<PreviewUpdateBucketPayload>("/v1/bucket-stores/buckets/preview-update", "POST", {
        update: { bucket_store_id: input.bucketStoreId, bucket_id: input.id, ...bucketUpdateBody(input) },
      }),
    ),
};

const updateBucketCommand: CommandDescriptor<UpdateSubBucketInput, { bucket?: SubBucketRow }> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<{ bucket?: SubBucketRow }>(
        `/v1/bucket-stores/${encodeURIComponent(input.bucketStoreId)}/buckets/${encodeURIComponent(input.id)}`,
        "PATCH",
        { ...bucketUpdateBody(input), ...(input.ticket ? { confirmation: { ticket: input.ticket } } : {}) },
      ),
    ),
};

const previewDeleteBucketCommand: CommandDescriptor<{ bucketStoreId: string; id: string }, PreviewDeleteBucketPayload> = {
  invalidates: [],
  run: ({ bucketStoreId, id }) =>
    runCommandMapped(() =>
      apiSend<PreviewDeleteBucketPayload>("/v1/bucket-stores/buckets/preview-delete", "POST", {
        bucket_store_id: bucketStoreId,
        bucket_id: id,
      }),
    ),
};

const deleteSubBucketCommand: CommandDescriptor<{ bucketStoreId: string; id: string; ticket?: string }, void> = {
  run: ({ bucketStoreId, id, ticket }) =>
    runCommandMapped(async () => {
      await apiFetch(
        `/v1/bucket-stores/${encodeURIComponent(bucketStoreId)}/buckets/${encodeURIComponent(id)}${ticketQuery(ticket)}`,
        { method: "DELETE", cache: "no-store" },
      );
    }),
};

export const subBucketsResource = defineResource({
  name: "bucketstore-buckets",
  getId: (row: SubBucketRow) => row.id ?? "",
  scope: ["projectId", "bucketStoreId"],
  collection: clientCollection(fetchSubBuckets),
  commands: {
    create: createSubBucketCommand,
    previewUpdate: previewUpdateBucketCommand,
    update: updateBucketCommand,
    previewDelete: previewDeleteBucketCommand,
    delete: deleteSubBucketCommand,
  },
});

export const subBucketsLookup = collectionQuerySchema({ defaultSize: 200 });

// ticketQuery：DELETE 带票的 query 形态（confirmation.ticket=…；无票 = 空）。
function ticketQuery(ticket?: string): string {
  return ticket ? `?confirmation.ticket=${encodeURIComponent(ticket)}` : "";
}

// splitSubjects：逗号分隔过滤器串 → 数组（空串 = 清空集）。
function splitSubjects(v: string): string[] {
  if (v.trim() === "") return [];
  return v
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part !== "");
}
