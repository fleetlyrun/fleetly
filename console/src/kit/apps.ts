import { clientCollection, createHttp } from "@lynx-go/uikit-core/adapters";
import { defineResource, scoped, type CommandDescriptor, type ResourceScope, type Segment } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core/react";
import { apiSend, notifyUnauthorized } from "@/api/client";
import { getToken } from "@/lib/token";
import { runCommandMapped } from "./errors";

// App 目录行（批4 catalog 退役：类型自 lib/catalog 上收至此——id/name 双
// 全非空形态，protojson 行收敛在此钉死）。
export interface AppEntry {
  id: string;
  project_id: string;
  name: string;
  created_at: string;
}

// uikit 适配器族（试点切片：apps 目录是第一个资源，后续 12 上下文分批
// 落此目录）。REST+protojson 直连 /v1/apps——int64 全 string（生成链钉死，
// uikit design §5.2 string 侧）；列表方言 = 整取 + 客户端求值
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


// useAppsDirectory 是 App 目录伴读钩子（表单/名字反查消费；60s 与旧
// catalog useApps 同拍，空 projectId 禁查同旧 enabled 门）——落 kit 层
// 让消费面免于裸 useQuery。
export function useAppsDirectory(projectId: string, options?: { refetchInterval?: number }) {
  return useQuery<Segment<AppEntry>>({
    ...collectionQueryOptions(scoped(appsResource, { projectId }), appsDirectorySearch.defaults, {
      refetchInterval: options?.refetchInterval ?? 60_000,
    }),
    enabled: projectId !== "",
  });
}
