import { clientCollection, restFetchAll } from "@lynx-go/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as deliverySchemas } from "@/api/delivery";
import { apiFetch, apiSend } from "@/api/client";
import { buildDeployPayload, type DeployFormState } from "@/lib/deployPayload";
import { fetchFrozenSpec } from "@/lib/spec-deploy";
import { fetchApps } from "./apps";
import { runCommandMapped, toApiError } from "./errors";
import { authFetch } from "./http";

// 部署域资源适配（2026-10-26 批1 useCollection 化，项目级聚合页；批2 写
// 路径命令化）：API
// 契约 per-App 轴（ListDeployments app_id 必填，ADR-0044/0057），项目级
// 时间线 = 客户端 fan-out（apps 整取 → 逐 app deployments 合并 →
// updated_at 倒序）。fan-out 逻辑复刻 features/deployments/hooks.ts 的
// useProjectDeployments（hooks 保留不删——registry 页与 per-App 查询仍在
// 用；hooks 的 queryFn 内联不可分离，故此处抄逻辑同口径：≤50 App 上界、
// per-app limit 20、单 app 失败吞为空）。apps 段复用 kit/apps 的 fetchApps
// （与 catalog useApps 同端点同上限）。uikit 0.5.0 回流批（M4 整取面）：
// fan-out 内件与 per-App 集合走 restFetchAll（fetchRows/qs/apiFetch 样板
// 归零；错误收敛工厂内置）。

export type DeploymentRow = deliverySchemas["schemas"]["v1Deployment"];

const FAN_OUT_APP_LIMIT = 50;
const FAN_OUT_PER_APP_LIMIT = 20;

interface DeploymentsEnvelope {
  deployments?: Array<DeploymentRow | undefined>;
}

// 无 id 行过滤与 hooks rowsOf 同口径（envelope 内展平）。
const deploymentsOf = (body: unknown): DeploymentRow[] =>
  (body as DeploymentsEnvelope).deployments?.flatMap((item) => (item?.id ? [item] : [])) ?? [];

// fan-out 内件（per-app limit 20，ctx.appId 逐 App 喂入；单 App 失败在
// 调用点 .catch 吞为空段）。
const appDeploymentsFanOut = restFetchAll<DeploymentRow>({
  path: () => "/v1/deployments",
  params: (ctx) => ({ app_id: ctx.appId, limit: FAN_OUT_PER_APP_LIMIT }),
  envelope: deploymentsOf,
  fetch: authFetch,
});

export async function fetchProjectDeployments(
  ctx: ResourceScope,
  signal?: AbortSignal,
): Promise<DeploymentRow[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  const apps = await fetchApps(ctx, signal);
  const appIds = apps.map((app) => app.id).slice(0, FAN_OUT_APP_LIMIT);
  if (appIds.length === 0) return [];
  const responses = await Promise.all(
    appIds.map((appId) => appDeploymentsFanOut({ appId }, signal).catch(() => [] as const)),
  );
  // 全项目时间线按 updated_at 倒序收敛
  const merged = responses.flat();
  return [...merged].sort((a, b) => (b.updated_at ?? "").localeCompare(a.updated_at ?? ""));
}

// ---- 写路径命令（批2） ----

// deploy = DeploySheet 五源提交面（image/compose/spec_file=spec|current/
// upload）的实体动词：载荷构造单源仍是 lib/deployPayload（纯函数已测），
// 命令只负责"表单状态 → 诚实载荷 → 结果两分"。spec_file 链的 OCC 面
// （AppSpec.restart_seq，ADR-0083）：序数随冻结体预填/用户编辑原样携带；
// 陈旧序数的服务端拒绝（CAS 冲突，409）经 runCommandMapped 落 conflict
// 分支——结果盒 message-first 如实呈现，不复现为 ok。
export interface DeployOutcome {
  deployment?: deliverySchemas["schemas"]["v1Deployment"];
  admission?: deliverySchemas["schemas"]["v1Admission"];
}

const deployCommand: CommandDescriptor<DeployFormState, DeployOutcome> = {
  run: (state) =>
    runCommandMapped(() => apiSend<DeployOutcome>("/v1/deployments", "POST", buildDeployPayload(state))),
};

// redeploy = 重发冻结 Spec（ADR-0069/0072）：回读 GetAppSpec → 原样走
// spec_file 第四源。未部署 = 无冻结体 → 诚实 rejected（hero 侧按钮已按
// latest 部署禁用，此处是深度防御）；apiSend 的 400/409 照常两分。
const redeployCommand: CommandDescriptor<{ appId: string }, { deploymentId?: string }> = {
  run: async ({ appId }) => {
    try {
      const spec = await fetchFrozenSpec(appId);
      if (!spec) {
        return { status: "rejected", message: "the app has no frozen spec yet — deploy first" };
      }
      return await runCommandMapped(async () => {
        const res = await apiSend<{ deployment?: { id?: string } }>("/v1/deployments", "POST", {
          app_id: appId,
          spec_file: JSON.stringify(spec),
        });
        return { deploymentId: res?.deployment?.id };
      });
    } catch (err) {
      // fetchFrozenSpec 自身的失败（网络/5xx）不在 runCommandMapped 内——
      // 统一收敛 kernel ApiError 出界走失败通道
      throw toApiError(err);
    }
  },
};

// rollback = App 级动作产出新 deployment（POST /v1/apps/{id}/rollback）：
// 效果是部署时间线新行，挂 deployments 命令族共享失效面。
const rollbackCommand: CommandDescriptor<{ appId: string }, { deploymentId?: string }> = {
  run: ({ appId }) =>
    runCommandMapped(async () => {
      const res = await apiSend<{ deployment?: { id?: string } }>(
        `/v1/apps/${encodeURIComponent(appId)}/rollback`,
        "POST",
        {},
      );
      return { deploymentId: res?.deployment?.id };
    }),
};

// hookRefresh = hook 托管 App 的 Deploy now（RefreshGitHook）：不依赖
// webhook 现拉 HEAD；admission 同链 deduplicated 语义透传（Sheet 的
// 成功 toast 后缀消费）。
export interface HookRefreshOutcome {
  deploymentId?: string;
  commit?: string;
  outcome?: string;
}

const hookRefreshCommand: CommandDescriptor<{ appId: string }, HookRefreshOutcome> = {
  run: ({ appId }) =>
    runCommandMapped(async () => {
      const res = await apiSend<{ deployment_id?: string; commit?: string; admission?: { outcome?: string } }>(
        `/v1/apps/${encodeURIComponent(appId)}/hook/refresh`,
        "POST",
        {},
      );
      return { deploymentId: res?.deployment_id, commit: res?.commit, outcome: res?.admission?.outcome };
    }),
};

export const deploymentsResource = defineResource({
  name: "deployments",
  getId: (row: DeploymentRow) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchProjectDeployments),
  // 存量键桥（F1 回流批审计后唯一存活族）：["deployments"] 前缀服务
  // project-overview AppTile 的裸键（["deployments","latest",appId]）。
  legacyKeys: () => [["deployments"]],
  commands: {
    deploy: deployCommand,
    redeploy: redeployCommand,
    rollback: rollbackCommand,
    hookRefresh: hookRefreshCommand,
  },
});

// URL 搜索 schema：text 快筛 id/app_id/state/to_revision（与旧页 fieldsOf
// 同字段集）；缺省排序 = fetch 的 updated_at 倒序（不设 defaultOrder，点
// 列头排序是新增 URL 能力）。
export const deploymentsSearch = collectionQuerySchema({
  text: ["id", "app_id", "state", "to_revision"],
  defaultSize: 20,
});

// ---- per-App 部署集合（2026-10-27 批4 读面收编：features/deployments/
// hooks.ts 的 useAppDeployments 退役） ----

// 独立资源而不复用项目 fan-out 集合的原因：hero/行级/概览的 per-App 消费
// 按 (projectId, appId) 键共享缓存；走扇出集合会让每行挂载放大成全项目
// 取数（apps + ≤50 deployments 请求）×行数。端点与收敛照抄 hooks 的
// useAppDeployments（per-app limit 50、id 缺行过滤同口径）；5s 轮询由消费
// 方 query.refetchInterval 注入（旧 APP_POLL_MS 同拍）。
const APP_DEPLOYMENTS_LIMIT = 50;

const appDeploymentsAll = restFetchAll<DeploymentRow>({
  path: () => "/v1/deployments",
  params: (ctx) => ({ app_id: ctx.appId, limit: APP_DEPLOYMENTS_LIMIT }),
  envelope: deploymentsOf,
  fetch: authFetch,
});

// 空 scope 禁查门（旧 fetcher 守卫语义保留）：appId 缺席 = 空段不打请求。
async function fetchAppDeployments(
  ctx: ResourceScope,
  signal?: AbortSignal,
): Promise<ReadonlyArray<DeploymentRow>> {
  return ctx.appId ? appDeploymentsAll(ctx, signal) : [];
}

export const appDeploymentsResource = defineResource({
  name: "app-deployments",
  getId: (row: DeploymentRow) => row.id ?? "",
  scope: ["projectId", "appId"],
  collection: clientCollection(fetchAppDeployments),
  // 存量键失效桥：对齐 hooks 的 ["deployments", appId] 前缀（Sheet/命令族
  // 的 ["deployments"] 桥键已覆盖该前缀，双向兼容）
  legacyKeys: () => [["deployments"]],
});

// join 读面用大页 schema（hero 的 recent 卡与 per-App 列表同源全集——窗口
// 不截断行集；per-App 时间线的列表页若成形态再立 search schema）。
export const appDeploymentsLookup = collectionQuerySchema({ defaultSize: 200 });

// 单部署查证（deployment-detail 的 stalled/缺流轮询兜底——wait 流是主径，
// 此为 S2 裁决保留的 stalled+轮询组合的轮询半）。app 轴 list 里按 id 拾取。
export const deploymentLookupResource = defineResource({
  name: "deployment-lookup",
  scope: ["projectId", "appId"],
  record: (id, ctx, signal) =>
    apiFetch<{ deployments?: Array<DeploymentRow | undefined> }>(
      `/v1/deployments?app_id=${encodeURIComponent(ctx.appId ?? "")}&limit=50`,
      { signal },
    )
      .then((res) => (res.deployments ?? []).find((item) => item?.id === id) ?? null)
      .catch((err: unknown) => {
        throw toApiError(err);
      }),
});

// 伴读消费面（2026-10-29 F1 回流批）：useDeploymentLookup 伴读钩子退役——
// deployment-detail 直接 useRecord(deploymentLookupResource, { id:
// deploymentId || undefined, scope, query: { refetchInterval?, enabled? } })。
