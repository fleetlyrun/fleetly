import { clientCollection, restFetchAll } from "@lynx-go/uikit-core/adapters";
import {
  type CommandResult,
  defineResource,
  type ResourceScope,
} from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as deliverySchemas } from "@/api/delivery";
import type { components as structureSchemas } from "@/api/structure";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";

// Storage 域资源适配（useCollection 化批 3）：volumes + uploads 双 tab 共
// 享一 schema（text 并集 name/id/digest——uploads 有 digest、volumes 没有，
// 缺字段谓词恒 false 不误配）。REST+protojson 直连与 lib/catalog 同端点
// 同收敛口径；鉴权/401 闭环经 authFetch（uikit 0.5.0 回流批 M4 整取面
// ——守卫门 + restFetchAll，fetchRows/qs/toApiError 样板归零）。
//
// 命令族切片（批 2 写路径命令化）：volumes create/remove 挂 volumesResource
// commands（kit 集合键自动失效）；uploads 是上传流（tar 构建 + 原始字节
// 体），不命令化保留原流。存量键失效桥照抄旧 useApiMutation invalidate 键
// [["resources","volumes",projectId]]。create 表单展示形（pinnedNode 空串
// = 不钉住）在 run 内收敛；结果两分经 runCommandMapped 单源，toast 单源
// 在页面 onSettled（volume 删除成功 toast 原文案保留）。

type Volume = structureSchemas["schemas"]["v1Volume"];
type Upload = deliverySchemas["schemas"]["v1Upload"];

export type { Upload, Volume };

const volumesAll = restFetchAll<Volume>({
  path: () => "/v1/volumes",
  params: (ctx) => ({ project_id: ctx.projectId }),
  envelope: (body) => (body as { volumes?: Array<Volume | undefined> }).volumes ?? [],
  fetch: authFetch,
});

const uploadsAll = restFetchAll<Upload>({
  path: () => "/v1/uploads",
  params: (ctx) => ({ project_id: ctx.projectId }),
  envelope: (body) => (body as { uploads?: Array<Upload | undefined> }).uploads ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（旧 fetcher 守卫语义保留；deploy-sheet 的 uploads 消费
// 以此为 enabled 门——「fetch 对空 projectId 自守为空段」）：projectId
// 缺席 = 空段不打请求。
async function fetchVolumes(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<Volume>> {
  return ctx.projectId ? volumesAll(ctx, signal) : [];
}

async function fetchUploads(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<Upload>> {
  return ctx.projectId ? uploadsAll(ctx, signal) : [];
}

export const volumesResource = defineResource({
  name: "volumes",
  getId: (row: Volume) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchVolumes),
  // 存量键失效桥：值域照抄旧 useApiMutation invalidate
  // （[["resources","volumes",projectId]]）
  commands: {
    create: {
      run: async (
        input: { name: string; pinnedNode: string },
        ctx: ResourceScope,
      ): Promise<CommandResult<null>> =>
        runCommandMapped(async () => {
          await apiSend("/v1/volumes", "POST", {
            project_id: ctx.projectId,
            name: input.name,
            pinned_node_id: input.pinnedNode || undefined,
          });
          return null;
        }),
    },
    remove: {
      run: async (input: { volumeId: string }): Promise<CommandResult<null>> =>
        runCommandMapped(async () => {
          await apiSend(`/v1/volumes/${encodeURIComponent(input.volumeId)}`, "DELETE");
          return null;
        }),
    },
  },
});

// 命令句柄提升（commands 在 spec 出现 ⇒ 类型非可选，免 !）
export const createVolumeCommand = volumesResource.commands.create;
export const removeVolumeCommand = volumesResource.commands.remove;

export const uploadsResource = defineResource({
  name: "uploads",
  getId: (row: Upload) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchUploads),
});

// 双列表各持 namespace 实例（F1 回流批 M1：volumes.q 与 uploads.q 并存互
// 不携带，「q 跨 tab 持久」妥协退役；字段集保持旧并集——volumes name/id +
// uploads id/digest）。
export const volumesSearch = collectionQuerySchema({
  namespace: "volumes",
  text: ["name", "id", "digest"],
  defaultSize: 20,
});
export const uploadsSearch = collectionQuerySchema({
  namespace: "uploads",
  text: ["name", "id", "digest"],
  defaultSize: 20,
});

// ---- join 读面大页 schema（2026-10-27 批4）：DeploySheet 的 upload 产物
// 下拉要目录全集——窗口不截断行集；无 text 字段（宿主路由的 q 是别的集合
// 的快筛，不串台）；只读消费不写 URL，与 storageSearch 分立。 ----
export const uploadsLookup = collectionQuerySchema({ defaultSize: 200 });
