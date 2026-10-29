import { restCollection, restRecord } from "@lynx-go/uikit-core/adapters";
import { defineResource } from "@lynx-go/uikit-core/kernel";
import type { components as Delivery } from "@/api/delivery";
import type { components as Workloads } from "@/api/workloads";
import { authFetch } from "./http";

// App 子面观测资源族（2026-10-27 收尾批：观测面手写 useQuery 的收编——
// 批4 收官时的合法例外清偿）。全部是读面：record/collection 资源，消费面
// （features/routes）经 useRecord/useCollectionData 直消费（F1 回流批 A 组）。
// 轮询拍子逐一照抄各旧查询；404 → null 的空态语义（未部署 = 无冻结
// Spec，W-3）经 restRecord notFound:"null" 收敛。
// F1 回流批 D 组（M4 rest 工厂试点域 2/3）：六资源全走 restRecord/
// restCollection——组 URL/信封投影/错误收敛归工厂内置（apiFetch 手写
// then/catch 链与 toApiError 样板在本域归零）。

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
  record: restRecord<AppWorkloadsView>({
    path: (id, ctx) =>
      `/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/workloads${id !== "0" ? `?seq=${id}` : ""}`,
    envelope: (body) => {
      const res = body as { revision?: AppWorkloadsView["revision"]; workloads?: WorkloadView[] };
      return { revision: res.revision, workloads: res.workloads ?? [] };
    },
    fetch: authFetch,
  }),
});

// ③ 在服观测（集群真跑着什么，含真实 wire-gen）：10s 轮询同旧拍。
export const appWorkloadsLiveResource = defineResource({
  name: "app-workloads-live",
  scope: ["projectId", "appId"],
  record: restRecord<WorkloadObservation[]>({
    path: (_id, ctx) => `/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/workloads/live`,
    envelope: (body) => (body as { observations?: WorkloadObservation[] }).observations ?? [],
    fetch: authFetch,
  }),
});

// 冻结全文按 Revision 寻址（Spec tab ① 层）：record id = seq。
export const appRevisionSpecResource = defineResource({
  name: "app-revision-spec",
  scope: ["projectId", "appId"],
  record: restRecord<{ spec?: unknown; revision?: { source_text?: string } }>({
    path: (id, ctx) => `/v1/revisions/${encodeURIComponent(ctx.appId ?? "")}/${id}`,
    envelope: (body) => body as { spec?: unknown; revision?: { source_text?: string } },
    fetch: authFetch,
  }),
});

// 最新冻结 Spec（GetAppSpec，404 → null 空态面）：变量页 60s / 进程页与
// DeploySheet 预填无轮询——拍子经 options 传。
export interface AppSpecSheet {
  spec: unknown;
  revision_seq?: string;
}

// 404 → null（未部署 = 无冻结 Spec，W-3）经 notFound:"null" 收敛——
// 手写 404 分支随 F1 回流批 D 组退役。
export const appSpecSheetResource = defineResource({
  name: "app-spec-sheet",
  scope: ["projectId", "appId"],
  record: restRecord<AppSpecSheet | null>({
    path: (_id, ctx) => `/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/spec`,
    envelope: (body): AppSpecSheet => {
      const res = body as { spec?: unknown; revision_seq?: string };
      return { spec: res.spec ?? null, revision_seq: res.revision_seq };
    },
    notFound: "null",
    fetch: authFetch,
  }),
});

// Git deploy hook（ADR-0072 所有权面，404 → null 未托管）：行形复用
// kit/app-tabs 的 GitHook（SetGitHook upsert 与读面同一形状单源）。
export type AppGitHook = import("./app-tabs").GitHook;

// 404 → null（未托管）经 notFound:"null" 收敛。
export const appHookResource = defineResource({
  name: "app-hook",
  scope: ["projectId", "appId"],
  record: restRecord<AppGitHook | null>({
    path: (_id, ctx) => `/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/hook`,
    envelope: (body) => (body as { hook?: AppGitHook }).hook ?? null,
    notFound: "null",
    fetch: authFetch,
  }),
});

// App 变量 store 面（Apply-then-deploy 的基线行集；limit 200 上限同旧）。
// 行形照旧页局部类型（响应信封宽松形收敛）。
export interface AppVariableRow {
  id?: string;
  name: string;
  value: string;
  updated_at?: string;
}

// store 面是纯 data 层消费（useCollectionData 固定窗）——restCollection
// 直挂（params 回调组 app_id/limit 查询串，limit 200 上限同旧）。
export const appVariablesStoreResource = defineResource({
  name: "app-variables",
  getId: (row: AppVariableRow) => row.name ?? "",
  scope: ["projectId", "appId"],
  collection: restCollection<AppVariableRow>({
    path: () => "/v1/app-variables",
    envelope: (body) => (body as { variables?: Array<AppVariableRow | undefined> }).variables ?? [],
    params: (_query, ctx) => ({ app_id: ctx.appId, limit: 200 }),
    fetch: authFetch,
  }),
});

// 伴读消费面（2026-10-29 F1 回流批）：本文件六个伴读钩子（useAppWorkloads/
// useAppWorkloadsLive/useAppRevisionSpec/useAppSpecSheet/useAppHook/
// useAppVariablesStore）退役——消费点直接 useRecord(resource, { id, scope,
// query: { refetchInterval?, enabled? } }) / useCollectionData（app-variables
// 的 store 面，size 200）。record id 约定：workloads=String(seq)（"0" 当前
// 生效）、live="live"、spec-sheet/hook="current"、revision-spec=String(seq)
//（缺席传 undefined 即自动停查）。
