import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as proxySchemas } from "@/api/proxy";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Routes 域资源适配（useCollection 化批 3，最标准单列表形态）：host →
// app/process/port 的受管代理路由表。REST+protojson 直连与 lib/catalog
// 同端点同收敛口径；鉴权/401 闭环经 kit/http 的 apiFetch 单源。

type Route = proxySchemas["schemas"]["v1Route"];

export type { Route };

export async function fetchRoutes(ctx: ResourceScope, signal?: AbortSignal): Promise<Route[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  try {
    return await fetchRows<Route>(
      `/v1/routes${qs({ project_id: projectId })}`,
      (res) => (res as { routes?: Array<Route | undefined> }).routes,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（states 块错误分诊契约）
    throw toApiError(err);
  }
}

export const routesResource = defineResource({
  name: "routes",
  getId: (row: Route) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchRoutes),
});

// host/app_id/process 三字段与旧 fieldsOf 同（路由表快筛面）。
export const routesSearch = collectionQuerySchema({
  text: ["host", "app_id", "process"],
  defaultSize: 20,
});
