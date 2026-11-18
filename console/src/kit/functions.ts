import { clientCollection, restFetchAll } from "@lynxland/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynxland/uikit-core/kernel";
import { collectionQuerySchema } from "@lynxland/uikit-core/schema";
import type { components as functionsSchemas } from "@/api/functions";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";

// functions 域资源适配（ADR-0102 任务⑥，设计 v3 §11 Console 面）：kit/
// pubsubs 同构——REST 端点与命令面对齐 CLI 动词族（create 即首部署/deploy
// 全量替换/scale/exposure/rollback/delete）；invoke 不入 Console 命令面
//（客户端直发是 CLI 语义，浏览器面以 URL CopyButton 承载）。

export type FunctionRow = functionsSchemas["schemas"]["v1Function"];
export type FunctionRevisionRow = functionsSchemas["schemas"]["v1FunctionRevision"];

const functionsAll = restFetchAll<FunctionRow>({
  path: () => "/v1/functions",
  params: (ctx) => ({ project_id: ctx.projectId }),
  envelope: (body) => (body as { functions?: Array<FunctionRow | undefined> }).functions ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（databases/pubsubs 同款）。
async function fetchFunctions(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<FunctionRow>> {
  return ctx.projectId ? functionsAll(ctx, signal) : [];
}

// ---- 写路径命令（对齐 CLI 动词面） ----

export interface CreateFunctionInput {
  name: string;
  image: string;
  port: string;
  command?: string;
  env?: string;
  secretRefs?: string;
  cpuMillis?: string;
  memoryMb?: string;
  minReplicas?: string;
  maxReplicas?: string;
  exposure: string;
  host?: string;
}

const createFunctionCommand: CommandDescriptor<CreateFunctionInput, { function?: FunctionRow }> = {
  run: (input, ctx) =>
    runCommandMapped(() =>
      apiSend<{ function?: FunctionRow }>("/v1/functions", "POST", {
        project_id: ctx.projectId,
        name: input.name,
        image: input.image,
        port: Number(input.port || "8080"),
        command: splitCommand(input.command),
        env: kvPairs(input.env),
        secret_refs: lines(input.secretRefs),
        resources: resourcesPayload(input.cpuMillis, input.memoryMb),
        min_replicas: positiveNumber(input.minReplicas),
        max_replicas: positiveNumber(input.maxReplicas),
        exposure: input.exposure === "public" ? "public" : undefined,
        host: input.exposure === "public" ? input.host || undefined : undefined,
      }),
    ),
};

export interface DeployFunctionInput {
  id: string;
  image: string;
  port: string;
  command?: string;
  env?: string;
  secretRefs?: string;
  cpuMillis?: string;
  memoryMb?: string;
}

const deployFunctionCommand: CommandDescriptor<DeployFunctionInput, { function?: FunctionRow }> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<{ function?: FunctionRow }>(`/v1/functions/${encodeURIComponent(input.id)}/deploy`, "POST", {
        image: input.image,
        port: Number(input.port || "8080"),
        command: splitCommand(input.command),
        env: kvPairs(input.env),
        secret_refs: lines(input.secretRefs),
        resources: resourcesPayload(input.cpuMillis, input.memoryMb),
      }),
    ),
};

const scaleFunctionCommand: CommandDescriptor<{ id: string; replicas: string }, { function?: FunctionRow }> = {
  run: ({ id, replicas }) =>
    runCommandMapped(() =>
      apiSend<{ function?: FunctionRow }>(`/v1/functions/${encodeURIComponent(id)}/scale`, "POST", {
        replicas: positiveNumber(replicas) ?? 1,
      }),
    ),
};

export interface SetExposureInput {
  id: string;
  exposure: "public" | "private";
  host?: string;
}

const setExposureCommand: CommandDescriptor<SetExposureInput, { function?: FunctionRow }> = {
  run: ({ id, exposure, host }) =>
    runCommandMapped(() =>
      apiSend<{ function?: FunctionRow }>(`/v1/functions/${encodeURIComponent(id)}/exposure`, "POST", {
        exposure,
        host: exposure === "public" ? host || undefined : undefined,
      }),
    ),
};

const rollbackFunctionCommand: CommandDescriptor<{ id: string }, { function?: FunctionRow }> = {
  run: ({ id }) =>
    runCommandMapped(() =>
      apiSend<{ function?: FunctionRow }>(`/v1/functions/${encodeURIComponent(id)}/rollback`, "POST", {}),
    ),
};

const deleteFunctionCommand: CommandDescriptor<{ id: string }, void> = {
  run: ({ id }) => runCommandMapped(() => apiSend<void>(`/v1/functions/${encodeURIComponent(id)}`, "DELETE")),
};

export const functionsResource = defineResource({
  name: "functions",
  getId: (row: FunctionRow) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchFunctions),
  commands: {
    create: createFunctionCommand,
    deploy: deployFunctionCommand,
    scale: scaleFunctionCommand,
    exposure: setExposureCommand,
    rollback: rollbackFunctionCommand,
    delete: deleteFunctionCommand,
  },
});

// URL 搜索 schema：text 快筛 name/status；exposure 下拉（private|public）。
const FUNCTIONS_QUERY_TEXT = ["name", "status"] as const;

export const functionsSearch = collectionQuerySchema({
  text: FUNCTIONS_QUERY_TEXT,
  filters: [
    {
      field: "exposure",
      label: "Exposure",
      options: [
        { value: "private", label: "Private" },
        { value: "public", label: "Public" },
      ],
    },
  ],
  defaultSize: 20,
});

// 行查找读面大页 schema（详情页项目全集）。
export const functionsLookup = collectionQuerySchema({ defaultSize: 200 });

// ---- per-Function 冻结体列表（详情页 Revisions 段，ADR-0101 保留窗内） ----

const functionRevisionsAll = restFetchAll<FunctionRevisionRow>({
  path: (ctx) => `/v1/functions/${encodeURIComponent(ctx.functionId ?? "")}/revisions`,
  envelope: (body) => (body as { revisions?: Array<FunctionRevisionRow | undefined> }).revisions ?? [],
  fetch: authFetch,
});

async function fetchFunctionRevisions(
  ctx: ResourceScope,
  signal?: AbortSignal,
): Promise<ReadonlyArray<FunctionRevisionRow>> {
  return ctx.functionId ? functionRevisionsAll(ctx, signal) : [];
}

export const functionRevisionsResource = defineResource({
  name: "function-revisions",
  getId: (row: FunctionRevisionRow) => String(row.seq ?? ""),
  scope: ["projectId", "functionId"],
  collection: clientCollection(fetchFunctionRevisions),
});

export const functionRevisionsLookup = collectionQuerySchema({ defaultSize: 200 });

// ---- 表单文本域帮手（tasks 面板 kvLines 同源语义） ----

// kvPairs：K=V 多行 → env map（非 K=V 行忽略——表单占位注释面）。
function kvPairs(text?: string): Record<string, string> | undefined {
  if (!text) return undefined;
  const out: Record<string, string> = {};
  for (const line of text.split("\n").map((l) => l.trim())) {
    if (line === "" || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    out[line.slice(0, eq)] = line.slice(eq + 1);
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

// lines：非空行数组（secret 引用名单）。
function lines(text?: string): string[] | undefined {
  if (!text) return undefined;
  const out = text.split("\n").map((l) => l.trim()).filter((l) => l !== "" && !l.startsWith("#"));
  return out.length > 0 ? out : undefined;
}

// splitCommand：逗号 command 形（CLI 同参——逗号或换行皆分隔）。
function splitCommand(text?: string): string[] | undefined {
  if (!text) return undefined;
  const out = text
    .split(/[,\n]/)
    .map((part) => part.trim())
    .filter((part) => part !== "");
  return out.length > 0 ? out : undefined;
}

// positiveNumber：数字旗标的 proto 形（空/非正 = undefined 走服务端缺省）。
function positiveNumber(text?: string): number | undefined {
  if (!text) return undefined;
  const value = Number(text);
  return Number.isFinite(value) && value > 0 ? value : undefined;
}

// resourcesPayload：cpu/memory 两轴（0 = 不限 = 缺席——databases 同口径）。
function resourcesPayload(cpuMillis?: string, memoryMb?: string): { cpu_millis: number; memory_mb: number } | undefined {
  const cpu = positiveNumber(cpuMillis);
  const memory = positiveNumber(memoryMb);
  if (cpu === undefined && memory === undefined) return undefined;
  return { cpu_millis: cpu ?? 0, memory_mb: memory ?? 0 };
}
