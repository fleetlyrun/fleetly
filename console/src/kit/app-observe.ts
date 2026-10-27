import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, scoped, type ApiError, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions, recordQueryOptions } from "@lynx-go/uikit-core/react";
import { apiFetch, ApiError as FetchApiError } from "@/api/client";
import type { components as Delivery } from "@/api/delivery";
import type { components as Workloads } from "@/api/workloads";
import { toApiError } from "./errors";
import { fetchRows } from "./http";

// App 子面观测资源族（2026-10-27 收尾批：观测面手写 useQuery 的收编——
// 批4 收官时的合法例外清偿）。全部是读面：record/collection 资源 + kit
// 层伴读钩子，消费面（features/routes）免于裸 useQuery（守卫契约）。
// 轮询拍子逐一照抄各旧查询；404 → null 的空态语义（未部署 = 无冻结
// Spec，W-3）在 record 内收敛。

export type WorkloadView = Delivery["schemas"]["v1Workload"];
export type WorkloadObservation = Workloads["schemas"]["v1WorkloadObservation"];

export interface AppWorkloadsView {
  revision?: { seq?: string; digest?: string; created_at?: string; source_kind?: string };
  workloads: WorkloadView[];
}

// ① Workload 投影（平台会下发什么）：record id = seq（"0" = 当前生效寻址）。
export const appWorkloadsResource = defineResource({
  name: "app-workloads",
  scope: ["projectId", "appId"],
  record: (id, ctx, signal) =>
    apiFetch<{ revision?: AppWorkloadsView["revision"]; workloads?: WorkloadView[] }>(
      `/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/workloads${id !== "0" ? `?seq=${id}` : ""}`,
      { signal },
    )
      .then((res) => ({ revision: res.revision, workloads: res.workloads ?? [] }))
      .catch((err: unknown) => {
        throw toApiError(err);
      }),
});

export function useAppWorkloads(projectId: string, appId: string, seq: number) {
  return useQuery<AppWorkloadsView, ApiError>({
    ...recordQueryOptions(scoped(appWorkloadsResource, { projectId, appId }), String(seq || 0), {}),
    enabled: appId !== "",
  });
}

// ③ 在服观测（集群真跑着什么，含真实 wire-gen）：10s 轮询同旧拍。
export const appWorkloadsLiveResource = defineResource({
  name: "app-workloads-live",
  scope: ["projectId", "appId"],
  record: (_id, ctx, signal) =>
    apiFetch<{ observations?: WorkloadObservation[] }>(
      `/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/workloads/live`,
      { signal },
    )
      .then((res) => res.observations ?? [])
      .catch((err: unknown) => {
        throw toApiError(err);
      }),
});

export function useAppWorkloadsLive(projectId: string, appId: string, options?: { refetchInterval?: number }) {
  return useQuery<WorkloadObservation[], ApiError>({
    ...recordQueryOptions(scoped(appWorkloadsLiveResource, { projectId, appId }), "live", {}),
    refetchInterval: options?.refetchInterval ?? 10_000,
    enabled: appId !== "",
  });
}

// 冻结全文按 Revision 寻址（Spec tab ① 层）：record id = seq。
export const appRevisionSpecResource = defineResource({
  name: "app-revision-spec",
  scope: ["projectId", "appId"],
  record: (id, ctx, signal) =>
    apiFetch<{ spec?: unknown; revision?: { source_text?: string } }>(
      `/v1/revisions/${encodeURIComponent(ctx.appId ?? "")}/${id}`,
      { signal },
    )
      .then((res) => ({ spec: res.spec, revision: res.revision }))
      .catch((err: unknown) => {
        throw toApiError(err);
      }),
});

export function useAppRevisionSpec(projectId: string, appId: string, seq: number | undefined) {
  return useQuery<{ spec?: unknown; revision?: { source_text?: string } }, ApiError>({
    ...recordQueryOptions(scoped(appRevisionSpecResource, { projectId, appId }), String(seq ?? -1), {}),
    enabled: seq != null && seq > 0,
  });
}

// 最新冻结 Spec（GetAppSpec，404 → null 空态面）：变量页 60s / 进程页与
// DeploySheet 预填无轮询——拍子经 options 传。
export interface AppSpecSheet {
  spec: unknown;
  revision_seq?: string;
}

export const appSpecSheetResource = defineResource({
  name: "app-spec-sheet",
  scope: ["projectId", "appId"],
  // 存量键桥：appTabCommands.redeploy 的旧失效键（["resources","app-spec",
  // projectId, appId]）等值承接——写后冻结体即时刷新不断链。
  legacyKeys: (scope) => [["resources", "app-spec", String(scope.projectId ?? ""), String(scope.appId ?? "")]],
  record: (_id, ctx, signal) =>
    apiFetch<{ spec?: unknown; revision_seq?: string }>(
      `/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/spec`,
      { signal },
    )
      .then((res): AppSpecSheet | null => ({ spec: res.spec ?? null, revision_seq: res.revision_seq }))
      .catch((err: unknown) => {
        if (err instanceof FetchApiError && err.status === 404) return null;
        throw toApiError(err);
      }),
});

export function useAppSpecSheet(
  projectId: string,
  appId: string,
  options?: { refetchInterval?: number; enabled?: boolean },
) {
  return useQuery<AppSpecSheet | null, ApiError>({
    ...recordQueryOptions(scoped(appSpecSheetResource, { projectId, appId }), "current", {}),
    refetchInterval: options?.refetchInterval,
    enabled: (options?.enabled ?? true) && appId !== "",
  });
}

// Git deploy hook（ADR-0072 所有权面，404 → null 未托管）：行形复用
// kit/app-tabs 的 GitHook（SetGitHook upsert 与读面同一形状单源）。
export type AppGitHook = import("./app-tabs").GitHook;

export const appHookResource = defineResource({
  name: "app-hook",
  scope: ["projectId", "appId"],
  record: (_id, ctx, signal) =>
    apiFetch<{ hook?: AppGitHook }>(`/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/hook`, { signal })
      .then((res) => res?.hook ?? null)
      .catch((err: unknown) => {
        if (err instanceof FetchApiError && err.status === 404) return null;
        throw toApiError(err);
      }),
});

export function useAppHook(projectId: string, appId: string, options?: { enabled?: boolean }) {
  return useQuery<AppGitHook | null, ApiError>({
    ...recordQueryOptions(scoped(appHookResource, { projectId, appId }), "current", {}),
    enabled: (options?.enabled ?? true) && appId !== "",
  });
}

// App 变量 store 面（Apply-then-deploy 的基线行集；limit 200 上限同旧）。
// 行形照旧页局部类型（响应信封宽松形收敛）。
export interface AppVariableRow {
  id?: string;
  name: string;
  value: string;
  updated_at?: string;
}

async function fetchAppVariables(ctx: ResourceScope, signal?: AbortSignal): Promise<AppVariableRow[]> {
  try {
    return await fetchRows<AppVariableRow>(
      `/v1/app-variables?app_id=${encodeURIComponent(ctx.appId ?? "")}&limit=200`,
      (res) => (res as { variables?: Array<AppVariableRow | undefined> }).variables,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

export const appVariablesStoreResource = defineResource({
  name: "app-variables",
  getId: (row: AppVariableRow) => row.name ?? "",
  scope: ["projectId", "appId"],
  collection: clientCollection(fetchAppVariables),
});

export function useAppVariablesStore(projectId: string, appId: string) {
  return useQuery({
    ...collectionQueryOptions(scoped(appVariablesStoreResource, { projectId, appId }), { window: { size: 200 } }, {}),
    enabled: appId !== "",
  });
}
