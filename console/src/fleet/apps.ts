import { clientCollection, createHttp } from "@fleetly/fleet-ui-kit/adapters";
import { defineResource, type ResourceScope } from "@fleetly/fleet-ui-kit/kernel";
import { collectionQuerySchema } from "@fleetly/fleet-ui-kit/schema";
import { notifyUnauthorized } from "@/api/client";
import type { AppEntry } from "@/lib/catalog";
import { getToken } from "@/lib/token";

// fleet-ui 适配器族（试点切片：apps 目录是第一个资源，后续 12 上下文分批
// 落此目录）。REST+protojson 直连 /v1/apps——int64 全 string（生成链钉死，
// fleet-ui design §5.2 string 侧）；列表方言 = 整取 + 客户端求值
// （clientCollection 本地求值谓词/排序/窗口并伪造游标，fleetly 方言）。

const http = createHttp({ onUnauthorized: notifyUnauthorized });

const APPS_FETCH_LIMIT = 200;

// protojson 行收敛（undefined 行/缺字段行过滤——与 lib/catalog 的 rowsOf
// 口径一致，AppEntry 非空形态在此钉死）。
function toAppEntries(
  apps:
    | Array<
        | {
            id?: string;
            name?: string;
            project_id?: string;
            created_at?: string;
          }
        | undefined
      >
    | undefined,
): AppEntry[] {
  return (apps ?? []).flatMap((app) =>
    app?.id && app.name
      ? [{ id: app.id, project_id: app.project_id ?? "", name: app.name, created_at: app.created_at ?? "" }]
      : [],
  );
}

export async function fetchApps(ctx: ResourceScope, signal?: AbortSignal): Promise<AppEntry[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  const token = getToken();
  const res = await http.get<{ apps?: Parameters<typeof toAppEntries>[0] }>("/v1/apps", {
    params: { project_id: projectId, limit: APPS_FETCH_LIMIT },
    headers: token !== "" ? { Authorization: `Bearer ${token}` } : undefined,
    signal,
  });
  return toAppEntries(res.apps);
}

export const appsResource = defineResource({
  name: "apps",
  getId: (row: AppEntry) => row.id,
  scope: ["projectId"],
  collection: clientCollection(fetchApps),
});

// URL 搜索 schema（路由与绑定共用同一实例）：文本快筛 name/id（与旧页
// fieldsOf 同字段集）；默认页大小 20（与旧 ListCard 同）；默认排序缺省 =
// 服务端序（与旧页一致——排序是本切片新增的 URL 能力，缺省不改变行为）。
export const appsSearch = collectionQuerySchema({
  text: ["name", "id"],
  defaultSize: 20,
});
