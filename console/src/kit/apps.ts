import { clientCollection, restFetchAll } from "@lynx-go/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";

// App 目录行（批4 catalog 退役：类型自 lib/catalog 上收至此——id/name 双
// 全非空形态，protojson 行收敛在此钉死）。
export interface AppEntry {
  id: string;
  project_id: string;
  name: string;
  created_at: string;
}

// uikit 适配器族（试点切片：apps 目录是第一个资源）。REST+protojson 直连
// /v1/apps——int64 全 string（生成链钉死，uikit design §5.2 string 侧）；
// 列表方言 = 整取 + 客户端求值（clientCollection 本地求值谓词/排序/窗口
// 并伪造游标，fleetly 方言）。uikit 0.5.0 回流批（M4 整取面）：restFetchAll
// 直产 fetchApps（createHttp 手写鉴权管线退役——Bearer/401 闭环归
// authFetch，错误收敛归工厂 Response→ApiError 单源）。

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

const appsAll = restFetchAll<AppEntry>({
  path: () => "/v1/apps",
  params: (ctx) => ({ project_id: ctx.projectId, limit: APPS_FETCH_LIMIT }),
  envelope: (body) => toAppEntries((body as { apps?: Parameters<typeof toAppEntries>[0] }).apps),
  fetch: authFetch,
});

// 空 scope 禁查门（ListApps project_id 必填——旧 enabled 语义，deployments/
// app-specs/registry 三处 fan-out 复用）：projectId 缺席 = 空段不打请求。
export async function fetchApps(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<AppEntry>> {
  return ctx.projectId ? appsAll(ctx, signal) : [];
}

// ---- 写路径命令（批2） ----

// create = New app 瘦对话框（§5.5 契约）：只承载 Name——create-then-
// configure，进程/端口/变量等初始值建后到各 tab/Deploy 承接。projectId
// 经 ctx scope 线程注入（资源声明 scope: ["projectId"]）。
export interface CreateAppInput {
  name: string;
}

const createAppCommand: CommandDescriptor<CreateAppInput, { appId?: string }> = {
  run: (input, ctx) =>
    runCommandMapped(async () => {
      const res = await apiSend<{ app?: { id?: string } }>("/v1/apps", "POST", {
        project_id: ctx.projectId,
        name: input.name.trim(),
      });
      return { appId: res?.app?.id };
    }),
};

export const appsResource = defineResource({
  name: "apps",
  getId: (row: AppEntry) => row.id,
  scope: ["projectId"],
  collection: clientCollection(fetchApps),
  commands: {
    create: createAppCommand,
  },
});

// URL 搜索 schema（路由与绑定共用同一实例）：文本快筛 name/id（与旧页
// fieldsOf 同字段集）；默认页大小 20（与旧 ListCard 同）；默认排序缺省 =
// 服务端序（与旧页一致——排序是本切片新增的 URL 能力，缺省不改变行为）。
export const appsSearch = collectionQuerySchema({
  text: ["name", "id"],
  defaultSize: 20,
});

// 目录级大页 schema（非列表消费方——overview 磁贴计数、metrics/terminal
// 选择器——直接吃 rows；取数封顶 200，窗口 200 不截断目录）。
export const appsDirectorySearch = collectionQuerySchema({ defaultSize: 200 });

// ---- 批4 追加导出（既有内容不动） ----

// 目录行类型随资源出口（消费面从 catalog 的 type import 迁此单源）。

// 目录名解析单源（kit 侧出口）：ID→名；目录未达时 undefined 入参回退截断
// 短 id——裸 ID 不该出现在面包屑/hero（W-1/W-2 走查教训）。lib/catalog 的
// 同名帮手随消费面迁移退役，口径逐字相同。
export function appNameOf(apps: ReadonlyArray<AppEntry> | undefined, appId: string | undefined): string {
  if (!appId) return "—";
  const hit = apps?.find((app) => app.id === appId);
  return hit ? hit.name : `${appId.slice(0, 10)}…`;
}

// 目录伴读消费面（2026-10-29 F1 回流批）：useAppsDirectory 伴读钩子退役——
// 消费点直接 useCollectionData(appsResource, { scope: { projectId },
// window: { size: 200 }, refetchInterval: 60_000, enabled: projectId !== "" })
//（60s 与旧 catalog useApps 同拍；size 200 同 appsDirectorySearch 大页）。
