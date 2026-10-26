import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as deliverySchemas } from "@/api/delivery";
import { apiFetch, apiSend } from "@/api/client";
import { buildDeployPayload, type DeployFormState } from "@/lib/deployPayload";
import { fetchFrozenSpec } from "@/lib/spec-deploy";
import { fetchApps } from "./apps";
import { runCommandMapped, toApiError } from "./errors";

// 部署域资源适配（2026-10-26 批1 useCollection 化，项目级聚合页；批2 写
// 路径命令化）：API
// 契约 per-App 轴（ListDeployments app_id 必填，ADR-0044/0057），项目级
// 时间线 = 客户端 fan-out（apps 整取 → 逐 app deployments 合并 →
// updated_at 倒序）。fan-out 逻辑复刻 features/deployments/hooks.ts 的
// useProjectDeployments（hooks 保留不删——registry 页与 per-App 查询仍在
// 用；hooks 的 queryFn 内联不可分离，故此处抄逻辑同口径：≤50 App 上界、
// per-app limit 20、单 app 失败吞为空）。apps 段复用 kit/apps 的 fetchApps
// （与 catalog useApps 同端点同上限）。

export type DeploymentRow = deliverySchemas["schemas"]["v1Deployment"];

const FAN_OUT_APP_LIMIT = 50;
const FAN_OUT_PER_APP_LIMIT = 20;

interface DeploymentsEnvelope {
  deployments?: Array<DeploymentRow | undefined>;
}

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
    appIds.map((appId) =>
      apiFetch<DeploymentsEnvelope>(
        `/v1/deployments?app_id=${encodeURIComponent(appId)}&limit=${FAN_OUT_PER_APP_LIMIT}`,
        { signal },
      ).catch(() => ({ deployments: [] as Array<DeploymentRow | undefined> })),
    ),
  );
  // 无 id 行过滤与 hooks rowsOf 同口径；全项目时间线按 updated_at 倒序收敛
  const merged = responses.flatMap((res) => (res.deployments ?? []).flatMap((item) => (item?.id ? [item] : [])));
  return merged.sort((a, b) => (b.updated_at ?? "").localeCompare(a.updated_at ?? ""));
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
  // 存量键失效桥：对齐旧 Sheet 的 invalidate 键（["deployments"] 前缀——
  // 覆盖 features/deployments/hooks.ts 的 ["deployments", appId] 与
  // ["deployments", "project-fanout", …] 双形态）。
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
