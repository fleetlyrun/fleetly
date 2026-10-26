import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as deliverySchemas } from "@/api/delivery";
import { apiFetch } from "@/api/client";
import { fetchApps } from "./apps";

// 部署域资源适配（2026-10-26 批1 useCollection 化，项目级聚合页）：API
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

export const deploymentsResource = defineResource({
  name: "deployments",
  getId: (row: DeploymentRow) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchProjectDeployments),
});

// URL 搜索 schema：text 快筛 id/app_id/state/to_revision（与旧页 fieldsOf
// 同字段集）；缺省排序 = fetch 的 updated_at 倒序（不设 defaultOrder，点
// 列头排序是新增 URL 能力）。
export const deploymentsSearch = collectionQuerySchema({
  text: ["id", "app_id", "state", "to_revision"],
  defaultSize: 20,
});
