import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, scoped, type ApiError, type ResourceScope, type Segment } from "@lynx-go/uikit-core/kernel";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core/react";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as deliverySchemas } from "@/api/delivery";
import { digestByRevisionId, latestDeploymentPerApp } from "@/features/registry/registry-model";
import { fetchApps } from "./apps";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Registry 域资源适配（useCollection 化批 3）：Apps tab 的"当前运行内容"
// 列表 = apps → deployments → revisions 三级合并的单条集合查询（复刻
// registry-view 旧三级 fan-out 的取数口径：apps 60s / deployments 15s /
// revisions 30s 并轨为 deployment 轴 15s 轮询）。registry-model 纯派生
// （latestDeploymentPerApp / digestByRevisionId）继续单源。

type Deployment = deliverySchemas["schemas"]["v1Deployment"];
type Revision = deliverySchemas["schemas"]["v1Revision"];

// 行形态：App 字段平铺到行首（快筛 name/id 直达行字段——客户端求值谓词
// 不走嵌套路径），最新部署与已解析的 revision digest 随行携带。
export interface RegistryRow {
  id: string;
  name: string;
  created_at: string;
  deployment?: Deployment;
  digest?: string;
}

const APP_FAN_OUT_LIMIT = 50;
const DEPLOYMENTS_LIMIT = 20;
const REVISIONS_LIMIT = 200;

// deployments fan-out：口径复刻 features/deployments/hooks.ts 的
// useProjectDeployments（per-App /v1/deployments、失败收敛空段、id 行
// 过滤、updated_at 倒序）——fetch 面无 hook 语境，此处是同口径直打。
async function fetchProjectDeployments(appIds: string[], signal?: AbortSignal): Promise<Deployment[]> {
  const responses = await Promise.all(
    appIds.map((appId) =>
      fetchRows<Deployment>(
        `/v1/deployments${qs({ app_id: appId, limit: DEPLOYMENTS_LIMIT })}`,
        (res) => (res as { deployments?: Array<Deployment | undefined> }).deployments,
        signal,
      ).catch(() => [] as Deployment[]),
    ),
  );
  const merged = responses.flatMap((rows) => rows.filter((row) => row.id != null && row.id !== ""));
  return merged.sort((a, b) => (b.updated_at ?? "").localeCompare(a.updated_at ?? ""));
}

// revisions fan-out：口径复刻 registry-view 旧 inline 查询（per-App
// /v1/revisions limit=200；单 App 失败 = 该 App digest 缺席，列表仍可用
// ——旧 useQueries 失败行同语义）。直打 REST，不与 catalog useRevisions
// 的 react-query 键相撞。
async function fetchProjectRevisions(appIds: string[], signal?: AbortSignal): Promise<Array<Revision[]>> {
  return Promise.all(
    appIds.map((appId) =>
      fetchRows<Revision>(
        `/v1/revisions${qs({ app_id: appId, limit: REVISIONS_LIMIT })}`,
        (res) => (res as { revisions?: Array<Revision | undefined> }).revisions,
        signal,
      ).catch(() => [] as Revision[]),
    ),
  );
}

export async function fetchRegistryRows(ctx: ResourceScope, signal?: AbortSignal): Promise<RegistryRow[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  try {
    const apps = await fetchApps(ctx, signal);
    const appIds = apps.map((app) => app.id).slice(0, APP_FAN_OUT_LIMIT);
    const [deployments, revisionLists] = await Promise.all([
      fetchProjectDeployments(appIds, signal),
      fetchProjectRevisions(appIds, signal),
    ]);
    const latest = latestDeploymentPerApp(deployments);
    const digests = digestByRevisionId(revisionLists);
    return apps.map((app) => {
      const deployment = latest.get(app.id);
      const digest = deployment?.to_revision != null ? digests.get(deployment.to_revision) : undefined;
      return {
        id: app.id,
        name: app.name,
        created_at: app.created_at,
        ...(deployment != null ? { deployment } : {}),
        ...(digest != null ? { digest } : {}),
      };
    });
  } catch (err) {
    // apps 轴失败即整列表失败（旧页 apps.isError 错误态同口径）；子轴失
    // 败已在 fan-out 内收敛为缺席。收敛为 kernel ApiError（states 块契约）。
    throw toApiError(err);
  }
}

export const registryResource = defineResource({
  name: "registry-apps",
  getId: (row: RegistryRow) => row.id,
  scope: ["projectId"],
  collection: clientCollection(fetchRegistryRows),
});

// 过滤面 = 旧 fieldsOf 同款 app.name/app.id（行平铺后直达）。
export const registrySearch = collectionQuerySchema({
  text: ["name", "id"],
  defaultSize: 20,
});

// Image catalog 台账（Registry Catalog tab 的手风琴主表）：repo 名单 60s
// retry:false 同旧；打开手风琴后的 tag 子表按 repo 锚定（打开态归页面）。
async function fetchRegistryCatalog(ctx: ResourceScope, signal?: AbortSignal): Promise<string[]> {
  try {
    return await fetchRows<string>(
      `/v1/registry/catalog?project_id=${encodeURIComponent(ctx.projectId ?? "")}`,
      (res) => (res as { repositories?: Array<string | undefined> }).repositories,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

export const registryCatalogResource = defineResource({
  name: "registry-catalog",
  getId: (repo: string) => repo,
  scope: ["projectId"],
  collection: clientCollection(fetchRegistryCatalog),
});

export function useRegistryCatalog(projectId: string) {
  return useQuery({
    ...collectionQueryOptions(scoped(registryCatalogResource, { projectId }), { window: { size: 200 } }, {
      refetchInterval: 60_000,
    }),
    enabled: projectId !== "",
    retry: false,
  });
}

export interface RegistryTagRow {
  name?: string;
  digest?: string;
  size_bytes?: string | number;
  pushed_at?: string;
}

async function fetchRegistryTags(ctx: ResourceScope, signal?: AbortSignal): Promise<RegistryTagRow[]> {
  try {
    return await fetchRows<RegistryTagRow>(
      `/v1/registry/${encodeURIComponent(ctx.repo ?? "")}/tags?project_id=${encodeURIComponent(ctx.projectId ?? "")}`,
      (res) => (res as { tags?: Array<RegistryTagRow | undefined> }).tags,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

export const registryTagsResource = defineResource({
  name: "registry-tags",
  getId: (row: RegistryTagRow) => row.name ?? "",
  scope: ["projectId", "repo"],
  collection: clientCollection(fetchRegistryTags),
});

export function useRegistryTags(projectId: string, repo: string, options?: { enabled?: boolean }) {
  return useQuery<Segment<RegistryTagRow>, ApiError>({
    ...collectionQueryOptions(scoped(registryTagsResource, { projectId, repo }), { window: { size: 200 } }, {}),
    enabled: (options?.enabled ?? true) && repo !== "",
    retry: false,
  });
}
