import { clientCollection } from "@lynx-go/uikit-core/adapters";
import {
  type CommandResult,
  defineResource,
  type ResourceScope,
} from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as proxySchemas } from "@/api/proxy";
import { apiSend } from "@/api/client";
import { runCommandMapped, toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Routes 域资源适配（useCollection 化批 3，最标准单列表形态）：host →
// app/process/port 的受管代理路由表。REST+protojson 直连与 lib/catalog
// 同端点同收敛口径；鉴权/401 闭环经 kit/http 的 apiFetch 单源。
//
// 命令族切片（批 2 写路径命令化）：create/remove 挂 routesResource
// commands（kit 集合键自动失效）；存量键失效桥照抄旧 useApiMutation
// invalidate 键 [["resources","routes",projectId]]（apps 域 useRoutes 等
// 在册消费面新鲜度）。create 的表单展示形（port 字符串等）在 run 内收
// 敛为提交形；结果两分经 runCommandMapped 单源，toast 单源在页面
// onSettled。

type Route = proxySchemas["schemas"]["v1Route"];

export type { Route };

/** New route 表单展示形（port 为字符串输入，提交形在 run 内收敛） */
export interface RouteCreateForm {
  host: string;
  path: string;
  app: string;
  process: string;
  port: string;
  protocol: string;
  tls: string;
}

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
  // 存量键失效桥：值域照抄旧 useApiMutation invalidate
  // （[["resources","routes",projectId]]——apps 域 useRoutes 消费面）
  commands: {
    create: {
      run: async (input: RouteCreateForm, ctx: ResourceScope): Promise<CommandResult<null>> =>
        runCommandMapped(async () => {
          await apiSend("/v1/routes", "POST", {
            project_id: ctx.projectId,
            host: input.host,
            path: input.path || undefined,
            app_id: input.app,
            process: input.process,
            port: Number(input.port),
            protocol: input.protocol,
            tls_mode: input.tls,
          });
          return null;
        }),
    },
    remove: {
      run: async (input: { routeId: string }): Promise<CommandResult<null>> =>
        runCommandMapped(async () => {
          await apiSend(`/v1/routes/${encodeURIComponent(input.routeId)}`, "DELETE");
          return null;
        }),
    },
  },
});

// 命令句柄提升（commands 在 spec 出现 ⇒ 类型非可选，免 !）
export const createRouteCommand = routesResource.commands.create;
export const removeRouteCommand = routesResource.commands.remove;

// host/app_id/process 三字段与旧 fieldsOf 同（路由表快筛面）。
export const routesSearch = collectionQuerySchema({
  text: ["host", "app_id", "process"],
  defaultSize: 20,
});

// ---- join 读面大页 schema（2026-10-27 批4）：app 域反查（Overview 路由卡
// /Processes 归因/apps 列表路由计数）要项目路由全集——窗口不截断行集；
// 只读消费不写 URL，与列表页 routesSearch 分立。 ----
export const routesLookup = collectionQuerySchema({ defaultSize: 200 });
