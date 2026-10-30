import { clientCollection, restFetchAll, restRecord } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as deliverySchemas } from "@/api/delivery";
import { authFetch } from "./http";

// Revision 域资源适配（2026-10-27 批4 读面收编）：App 冻结历史（GET
// /v1/revisions?app_id=…）——R 序号对表、进程下拉、部署 diff 的只读面。
// REST 端点与收敛口径照抄 lib/catalog.ts 的 useRevisions；消费方经
// query.refetchInterval 注入 15s 轮询（旧 RESOURCE_MS 同拍）。
// uikit 0.5.0 回流批（M4 整取面）：集合走守卫门 + restFetchAll；diff
// record 换 restRecord（from:to 拆分在 path 回调，信封过滤在 envelope
// ——apiFetch/toApiError 链归零）。

export type RevisionRow = deliverySchemas["schemas"]["v1Revision"];

const REVISIONS_FETCH_LIMIT = 200;

const revisionsAll = restFetchAll<RevisionRow>({
  path: () => "/v1/revisions",
  params: (ctx) => ({ app_id: ctx.appId, limit: REVISIONS_FETCH_LIMIT }),
  envelope: (body) => (body as { revisions?: Array<RevisionRow | undefined> }).revisions ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（旧 fetcher 守卫语义保留）：appId 缺席 = 空段不打请求。
async function fetchRevisions(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<RevisionRow>> {
  return ctx.appId ? revisionsAll(ctx, signal) : [];
}

export const revisionsResource = defineResource({
  name: "revisions",
  getId: (row: RevisionRow) => row.id ?? "",
  scope: ["projectId", "appId"],
  collection: clientCollection(fetchRevisions),
  // 存量键失效桥：catalog useRevisions 的 ["resources","revisions",appId] 前缀
});

// join 读面用大页 schema（历史下拉/R 序号对表要全集——窗口不截断行集；
// 只读消费不写 URL，与未来列表页的 search schema 分立）。
export const revisionsLookup = collectionQuerySchema({ defaultSize: REVISIONS_FETCH_LIMIT });

// Revision diff（deployment-detail 的 from→to 对照面）：record id =
// "from:to"；行形 = DiffEntry 信封展平（旧查询同口径）。
export type RevisionDiffEntry = {
  path?: string;
  old_value?: string;
  new_value?: string;
};

export const revisionDiffResource = defineResource({
  name: "revision-diff",
  scope: ["projectId", "appId"],
  record: restRecord<RevisionDiffEntry[]>({
    path: (id, ctx) => {
      const [fromSeq, toSeq] = id.split(":");
      return `/v1/revisions/diff?app_id=${encodeURIComponent(ctx.appId ?? "")}&from_seq=${fromSeq}&to_seq=${toSeq}`;
    },
    envelope: (body) =>
      (body as { entries?: Array<RevisionDiffEntry | undefined> }).entries?.flatMap((entry) =>
        entry != null ? [entry] : [],
      ) ?? [],
    fetch: authFetch,
  }),
});

// 伴读消费面（2026-10-29 F1 回流批）：useRevisionDiff 伴读钩子退役——
// deployment-detail 直接 useRecord(revisionDiffResource, { id: seq 未选齐
// 或相同 ? undefined : `${fromSeq}:${toSeq}`, scope })（undefined id 门 =
// 旧 enabled 语义）。
