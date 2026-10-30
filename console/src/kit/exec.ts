import { clientCollection, restFetchAll } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import type { ExecInstance } from "@/api/execstream";
import { authFetch } from "./http";

// 进程在跑实例集观测（ADR-0067 快照语义：直读编排器任务/pod 列表，空集 =
// 观测行非错误）。批4 读面收尾自 lib/catalog 迁入——Terminal/Logs 副本下拉、
// Processes 页 Running 列共享同一资源键（App→Process→Instance 钻取面，
// 2026-10-12 层级定案）。uikit 0.5.0 回流批（M4 整取面）：守卫门 +
// restFetchAll（api/execstream 的 listExecInstances 帮手退役——signal
// 透传随换装接通）。

const execInstancesAll = restFetchAll<ExecInstance>({
  path: (ctx) => `/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/exec/instances`,
  params: (ctx) => ({ process: ctx.process }),
  envelope: (body) => (body as { instances?: Array<ExecInstance | undefined> }).instances ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（旧 fetcher 守卫语义保留；消费面 enabled 同款）：appId
// 或 process 缺席 = 空段不打请求。
async function fetchExecInstances(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<ExecInstance>> {
  return ctx.appId && ctx.process ? execInstancesAll(ctx, signal) : [];
}

export const execInstancesResource = defineResource({
  name: "exec-instances",
  getId: (row: ExecInstance) => row.instance ?? "",
  scope: ["appId", "process"],
  collection: clientCollection(fetchExecInstances),
});

// 伴读消费面（2026-10-29 F1 回流批）：useProcessInstances 伴读钩子退役——
// 三消费面（Logs/Terminal 副本下拉、Processes Running 列）直接
// useCollectionData(execInstancesResource, { scope: { appId, process },
// window: { size: 200 }, refetchInterval: 10_000,
// enabled: appId !== "" && process !== "" })（10s 轮询与空 process 禁查
// 承接旧钩子语义；整集窗 size 200 不截断）。
