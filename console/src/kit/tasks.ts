import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as automationSchemas } from "@/api/automation";
import { fetchRows, qs } from "./http";

// 自动化域资源适配（2026-10-26 批1 useCollection 化）：tasks/schedules 双
// 资源共享同一 search schema 实例（双 tab 双列表——q/分页跨 tab 持久是
// 接受的语义，text 字段取两表过滤字段的并集）。REST 端点与收敛口径照抄
// lib/catalog.ts 的 useTasks/useSchedules（rowsOf 同款 undefined 行过滤）。

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
});

export const schedulesResource = defineResource({
  name: "schedules",
  getId: (row: ScheduleEntry) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchSchedules),
});

// 双 tab 共享实例：text = Tasks（name/id/form/state）∪ Schedules（name/cron/image）。
export const tasksSearch = collectionQuerySchema({
  text: ["name", "id", "form", "state", "cron", "image"],
  defaultSize: 20,
});
