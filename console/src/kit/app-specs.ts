import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import { ApiError, apiFetch } from "@/api/client";
import type { components as structureSchemas } from "@/api/structure";
import { fetchApps } from "./apps";

// App 冻结 Spec 反查面（2026-10-27 批4 读面收编）：项目内全部 App 的
// GetAppSpec fan-out——资源反查三面（App Variables / Database Used-by /
// Storage 挂载与上传反查，IA v3 二期②）与 App Overview 进程摘要共用。
// fan-out 逻辑复刻 features/spec/use-app-specs.ts（≤50 App 上界同 deployments
// fan-out；apps 整取复用 kit/apps fetchApps——与 catalog useApps 同端点同
// 上限 200）；未部署 App（无冻结 Revision）映射为缺席而非错误（404→空段）。
// 行形态 = appId + spec 载荷：反查索引以 appId 为键，spec 整行随行携带。

export type AppSpec = structureSchemas["schemas"]["v1AppSpec"];

// 行形态：appId + spec 载荷（specIndex 反查锚的挂点）。
export interface AppSpecRow {
  appId: string;
  spec: AppSpec;
}

const FAN_OUT_APP_LIMIT = 50;

export async function fetchAppSpecs(ctx: ResourceScope, signal?: AbortSignal): Promise<AppSpecRow[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  const apps = await fetchApps(ctx, signal);
  const appIds = apps.map((app) => app.id).slice(0, FAN_OUT_APP_LIMIT);
  if (appIds.length === 0) return [];
  const responses = await Promise.all(
    appIds.map(async (appId): Promise<AppSpecRow[]> => {
      try {
        const res = await apiFetch<{ spec?: AppSpec }>(`/v1/apps/${encodeURIComponent(appId)}/spec`, { signal });
        return res?.spec ? [{ appId, spec: res.spec }] : [];
      } catch (err) {
        // 未部署 = 无冻结 Spec（缺席非错误；其余失败如实上抛）
        if (err instanceof ApiError && err.status === 404) return [];
        throw err;
      }
    }),
  );
  return responses.flat();
}

export const appSpecsResource = defineResource({
  name: "app-specs",
  getId: (row: AppSpecRow) => row.appId,
  scope: ["projectId"],
  collection: clientCollection(fetchAppSpecs),
  // 存量键失效桥：catalog/局部 useQuery 的 ["resources","app-spec",projectId,appId]
  // 前缀（appTabCommands.redeploy 的桥键仍打在这族键上）
  legacyKeys: (scope) => [["resources", "app-spec", String(scope.projectId ?? "")]],
});

// join 读面用大页 schema（反查行集 = 项目 App 全集 ≤50，窗口不截断）。
export const appSpecsLookup = collectionQuerySchema({ defaultSize: 200 });
