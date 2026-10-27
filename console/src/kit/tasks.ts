import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import { apiSend } from "@/api/client";
import type { components as automationSchemas } from "@/api/automation";
import { runCommandMapped, toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// 自动化域资源适配（2026-10-26 批1 useCollection 化；批2 写路径命令化）：
// tasks/schedules 双资源共享同一 search schema 实例（双 tab 双列表——q/分
// 页跨 tab 持久是接受的语义，text 字段取两表过滤字段的并集）。REST 端点
// 与收敛口径照抄 lib/catalog.ts 的 useTasks/useSchedules（rowsOf 同款
// undefined 行过滤）。批2：实体动词挂 defineResource commands（run 内
// runCommandMapped 结果两分），失效 = kit selector 缺省（同资源、scope 相
// 交）+ legacyKeys 桥（存量 ["resources", …] catalog 键前缀失效）。

export type TaskEntry = automationSchemas["schemas"]["v1Task"];
export type ScheduleEntry = automationSchemas["schemas"]["v1Schedule"];

export async function fetchTasks(ctx: ResourceScope, signal?: AbortSignal): Promise<TaskEntry[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  return fetchRows<TaskEntry>(
    `/v1/tasks${qs({ project_id: projectId })}`,
    (res) => (res as { tasks?: Array<TaskEntry | undefined> }).tasks,
    signal,
  );
}

export async function fetchSchedules(ctx: ResourceScope, signal?: AbortSignal): Promise<ScheduleEntry[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  return fetchRows<ScheduleEntry>(
    `/v1/schedules${qs({ project_id: projectId })}`,
    (res) => (res as { schedules?: Array<ScheduleEntry | undefined> }).schedules,
    signal,
  );
}

export const tasksResource = defineResource({
  name: "tasks",
  getId: (row: TaskEntry) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchTasks),
  // 存量键失效桥：对齐旧 useApiMutation 的 invalidate 键（task 详情页
  // useTasks 等仍读 ["resources","tasks",projectId]——批2 迁期双轨）。
  legacyKeys: (scope) => [["resources", "tasks", scope.projectId]],
  commands: {
    // create 的 project_id 走命令 ctx（ResourceScope 线程），输入不重复携带。
    create: {
      run: (input: CreateTaskInput, ctx: ResourceScope) =>
        runCommandMapped(() =>
          apiSend<{ task?: TaskEntry }>("/v1/tasks", "POST", { project_id: ctx.projectId, ...input }),
        ),
    },
    scale: {
      // scale/stop 改变池与 Run 面：除默认同资源失效外，桥面追加 runs 前缀
      // 键（runs 无 kit 资源、legacyKeys 只收 scope——按行 id 的精确键表达
      // 不了，取全 tasks 共享的 ["resources","runs"] 前缀，与旧 RunRow stop
      // 的失效口径一致）。
      run: (input: { id: string; desired_concurrency: number }, _ctx: ResourceScope) =>
        runCommandMapped(() =>
          apiSend<{ task?: TaskEntry }>(
            `/v1/tasks/${encodeURIComponent(input.id)}/scale`,
            "POST",
            { desired_concurrency: input.desired_concurrency },
          ),
        ),
      legacyKeys: (scope: ResourceScope) => [["resources", "tasks", scope.projectId], ["resources", "runs"]],
    },
    stop: {
      run: (input: { id: string; force?: boolean }, _ctx: ResourceScope) =>
        runCommandMapped(() =>
          apiSend<{ task?: TaskEntry }>(`/v1/tasks/${encodeURIComponent(input.id)}/stop`, "POST", {
            force: input.force ?? false,
          }),
        ),
      legacyKeys: (scope: ResourceScope) => [["resources", "tasks", scope.projectId], ["resources", "runs"]],
    },
    renew: {
      run: (input: { id: string }, _ctx: ResourceScope) =>
        runCommandMapped(() =>
          apiSend<{ task?: TaskEntry }>(`/v1/tasks/${encodeURIComponent(input.id)}/renew`, "POST", {}),
        ),
    },
    delete: {
      // 幂等裁决保持 throw：服务端已删行返回 200（engine 幂等），404 = id
      // 从未存在——CLI delete 对错误原样上抛，不吞；此处对齐（无 treatAs）。
      run: (input: { id: string }, _ctx: ResourceScope) =>
        runCommandMapped(() =>
          apiSend<Record<string, never>>(`/v1/tasks/${encodeURIComponent(input.id)}`, "DELETE", {}),
        ),
    },
  },
});

export const schedulesResource = defineResource({
  name: "schedules",
  getId: (row: ScheduleEntry) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchSchedules),
  legacyKeys: (scope) => [["resources", "schedules", scope.projectId]],
  commands: {
    create: {
      run: (input: CreateScheduleInput, ctx: ResourceScope) =>
        runCommandMapped(() =>
          apiSend<{ schedule?: ScheduleEntry }>("/v1/schedules", "POST", {
            project_id: ctx.projectId,
            ...input,
          }),
        ),
    },
    // trigger 立即铸一拍 Task（RunNow 语义）：失效跨 tasks + schedules 双面
    //（selector 声明 + 桥键），旧版只靠 15s 轮询兜底。
    trigger: {
      run: (input: { id: string }, _ctx: ResourceScope) =>
        runCommandMapped(() =>
          apiSend<{ task?: TaskEntry }>(
            `/v1/schedules/${encodeURIComponent(input.id)}/trigger`,
            "POST",
            {},
          ),
        ),
      invalidates: [{ resource: "tasks" }, { resource: "schedules" }],
      legacyKeys: (scope: ResourceScope) => [["resources", "tasks", scope.projectId], ["resources", "schedules", scope.projectId]],
    },
    delete: {
      // 幂等裁决保持 throw：服务端终态行返回 200（tombstone 幂等），404 =
      // id 从未存在——CLI 对齐（无 treatAs）。
      run: (input: { id: string }, _ctx: ResourceScope) =>
        runCommandMapped(() =>
          apiSend<Record<string, never>>(`/v1/schedules/${encodeURIComponent(input.id)}`, "DELETE", {}),
        ),
    },
  },
});

// runs 集合资源（2026-10-27 批4 读面收编：catalog useRuns 退役）——stop
// 仍是裸 descriptor：无 __resource 缺省失效，桥键显式声明 runs 前缀（与旧
// panels RunRow stop 的 invalidate 口径一致）。
export const stopRunCommand: CommandDescriptor<{ id: string }, { run?: RunEntry }> = {
  run: (input, _ctx) =>
    runCommandMapped(() =>
      apiSend<{ run?: RunEntry }>(`/v1/runs/${encodeURIComponent(input.id)}/stop`, "POST", {}),
    ),
  legacyKeys: () => [["resources", "runs"]],
};

export async function fetchRuns(ctx: ResourceScope, signal?: AbortSignal): Promise<RunEntry[]> {
  const taskId = ctx.taskId;
  if (taskId === undefined || taskId === "") return [];
  try {
    return await fetchRows<RunEntry>(
      `/v1/runs${qs({ task_id: taskId })}`,
      (res) => (res as { runs?: Array<RunEntry | undefined> }).runs,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（states 块错误分诊契约）
    throw toApiError(err);
  }
}

// runs 资源（scope projectId+taskId）：展开行/详情 Runs tab 的按需观测面，
// 轮询由消费方 query.refetchInterval 注入（旧 RESOURCE_MS 15s 同拍）。
export const runsResource = defineResource({
  name: "runs",
  getId: (row: RunEntry) => row.id ?? "",
  scope: ["projectId", "taskId"],
  collection: clientCollection(fetchRuns),
  // 存量键失效桥：catalog useRuns 的 ["resources","runs",taskId] 前缀
  legacyKeys: (scope) => [["resources", "runs", String(scope.taskId ?? "")]],
});

// runs 读面用大页 schema（Run 观测要全集——窗口不截断行集；只读消费不写
// URL，与列表页 search schema 分立）。
export const runsLookup = collectionQuerySchema({ defaultSize: 200 });

// create 输入面：wire 形（protojson snake_case）——页表单值在调用点归一，
// project_id 由命令 ctx 注入。desired_concurrency 是 proto 真名（spec.proto
// desired_concurrency = 7；旧面板误用 "concurrency" 字段，服务端不识别，
// 批2 迁移时修正）。
export interface CreateTaskInput {
  name?: string;
  form?: string;
  image: string;
  command?: string[];
  env?: Record<string, string>;
  secret_refs?: string[];
  cpu_millis?: number;
  memory_mb?: number;
  ttl_seconds?: number;
  desired_concurrency?: number;
  network_group?: string;
}

export interface CreateScheduleInput {
  name?: string;
  cron: string;
  timezone?: string;
  image: string;
  command?: string[];
  env?: Record<string, string>;
  ttl_seconds?: number;
}

export type RunEntry = automationSchemas["schemas"]["v1Run"];

// 双 tab 共享实例：text = Tasks（name/id/form/state）∪ Schedules（name/cron/image）。
export const tasksSearch = collectionQuerySchema({
  text: ["name", "id", "form", "state", "cron", "image"],
  defaultSize: 20,
});

// ---- 行查找/join 读面大页 schema（2026-10-27 批4）：任务详情的行查找与
// Schedule 反链要项目全集——窗口不截断行集；无 text 字段（列表页的 q 是
// 双 tab 快筛，不串台）；只读消费不写 URL，与 tasksSearch 分立。 ----
export const tasksLookup = collectionQuerySchema({ defaultSize: 200 });
export const schedulesLookup = collectionQuerySchema({ defaultSize: 200 });
