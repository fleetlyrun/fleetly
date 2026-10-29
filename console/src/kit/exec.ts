import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { listExecInstances, type ExecInstance } from "@/api/execstream";

// 进程在跑实例集观测（ADR-0067 快照语义：直读编排器任务/pod 列表，空集 =
// 观测行非错误）。批4 读面收尾自 lib/catalog 迁入——Terminal/Logs 副本下拉、
// Processes 页 Running 列共享同一资源键（App→Process→Instance 钻取面，
// 2026-10-12 层级定案）。

async function fetchExecInstances(ctx: ResourceScope): Promise<ExecInstance[]> {
  const appId = ctx.appId ?? "";
  const process = ctx.process ?? "";
  if (appId === "" || process === "") return [];
  const res = await listExecInstances(appId, process);
  return res.instances ?? [];
}

export const execInstancesResource = defineResource({
  name: "exec-instances",
  getId: (row: ExecInstance) => row.instance ?? "",
  scope: ["appId", "process"],
  collection: clientCollection(fetchExecInstances),
  legacyKeys: (scope) => [["exec-instances", String(scope.appId ?? "")]],
});

// 伴读消费面（2026-10-29 F1 回流批）：useProcessInstances 伴读钩子退役——
// 三消费面（Logs/Terminal 副本下拉、Processes Running 列）直接
// useCollectionData(execInstancesResource, { scope: { appId, process },
// window: { size: 200 }, refetchInterval: 10_000,
// enabled: appId !== "" && process !== "" })（10s 轮询与空 process 禁查
// 承接旧钩子语义；整集窗 size 200 不截断）。
