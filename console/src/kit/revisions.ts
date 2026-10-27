import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as deliverySchemas } from "@/api/delivery";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Revision 域资源适配（2026-10-27 批4 读面收编）：App 冻结历史（GET
// /v1/revisions?app_id=…）——R 序号对表、进程下拉、部署 diff 的只读面。
// REST 端点与收敛口径照抄 lib/catalog.ts 的 useRevisions（rowsOf 同款
// undefined 行过滤）；消费方经 query.refetchInterval 注入 15s 轮询（旧
// RESOURCE_MS 同拍）。

export type RevisionRow = deliverySchemas["schemas"]["v1Revision"];

const REVISIONS_FETCH_LIMIT = 200;

export async function fetchRevisions(ctx: ResourceScope, signal?: AbortSignal): Promise<RevisionRow[]> {
  const appId = ctx.appId;
  if (appId === undefined || appId === "") return [];
  try {
    return await fetchRows<RevisionRow>(
      `/v1/revisions${qs({ app_id: appId, limit: REVISIONS_FETCH_LIMIT })}`,
      (res) => (res as { revisions?: Array<RevisionRow | undefined> }).revisions,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（states 块错误分诊契约）
    throw toApiError(err);
  }
}

export const revisionsResource = defineResource({
  name: "revisions",
  getId: (row: RevisionRow) => row.id ?? "",
  scope: ["projectId", "appId"],
  collection: clientCollection(fetchRevisions),
  // 存量键失效桥：catalog useRevisions 的 ["resources","revisions",appId] 前缀
  legacyKeys: (scope) => [["resources", "revisions", String(scope.appId ?? "")]],
});

// join 读面用大页 schema（历史下拉/R 序号对表要全集——窗口不截断行集；
// 只读消费不写 URL，与未来列表页的 search schema 分立）。
export const revisionsLookup = collectionQuerySchema({ defaultSize: REVISIONS_FETCH_LIMIT });
