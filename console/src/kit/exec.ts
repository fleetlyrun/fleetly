import { clientCollection } from "@lynx-go/uikit-core/adapters";
import {
  defineResource,
  scoped,
  type ApiError,
  type CollectionQuery,
  type ResourceScope,
  type Segment,
} from "@lynx-go/uikit-core/kernel";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core/react";
import { listExecInstances, type ExecInstance } from "@/api/execstream";

// 进程在跑实例集观测（ADR-0067 快照语义：直读编排器任务/pod 列表，空集 =
// 观测行非错误）。批4 读面收尾自 lib/catalog 迁入——Terminal/Logs 副本下拉、
// Processes 页 Running 列共享同一资源键（App→Process→Instance 钻取面，
// 2026-10-12 层级定案）。伴读消费形态 = collectionQueryOptions(scoped(...))
// （下拉/计数不进 URL——useCollection 的共享 search 键会误收，壳层伴读
// 先例同 projectsAllQuery 注）。

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

// 伴读固定窗（实例集个位数~两位数，200 封顶不截断）。
export const execInstancesQuery: CollectionQuery = { window: { size: 200 } };

// useProcessInstances 是三消费面（Logs 副本下拉/Terminal 副本下拉/Processes
// Running 列）的共享伴读钩子——10s 轮询与旧 catalog 钩子同拍；空 process
// 不发请求（enabled 门承接旧语义）。钩子落 kit 层让消费面免于裸 useQuery；
// error 显式注入 kernel ApiError 封闭集（消费面 describeApiError）。
export function useProcessInstances(appId: string, process: string) {
  return useQuery<Segment<ExecInstance>, ApiError>({
    ...collectionQueryOptions(
      scoped(execInstancesResource, { appId, process }),
      execInstancesQuery,
      { refetchInterval: 10_000 },
    ),
    enabled: appId !== "" && process !== "",
  });
}
