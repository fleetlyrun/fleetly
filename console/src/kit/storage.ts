import { clientCollection } from "@lynx-go/uikit-core/adapters";
import {
  type CommandResult,
  defineResource,
  type ResourceScope,
} from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as deliverySchemas } from "@/api/delivery";
import type { components as structureSchemas } from "@/api/structure";
import { apiSend } from "@/api/client";
import { runCommandMapped, toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Storage 域资源适配（useCollection 化批 3）：volumes + uploads 双 tab 共
// 享一 schema（text 并集 name/id/digest——uploads 有 digest、volumes 没有，
// 缺字段谓词恒 false 不误配）。REST+protojson 直连与 lib/catalog 同端点同
// 收敛口径（rowsOf 非空行过滤），鉴权/401 闭环经 kit/http 的 apiFetch 单源。
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

export async function fetchVolumes(ctx: ResourceScope, signal?: AbortSignal): Promise<Volume[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  try {
    return await fetchRows<Volume>(
      `/v1/volumes${qs({ project_id: projectId })}`,
      (res) => (res as { volumes?: Array<Volume | undefined> }).volumes,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（states 块错误分诊契约）
    throw toApiError(err);
  }
}

export async function fetchUploads(ctx: ResourceScope, signal?: AbortSignal): Promise<Upload[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  try {
    return await fetchRows<Upload>(
      `/v1/uploads${qs({ project_id: projectId })}`,
      (res) => (res as { uploads?: Array<Upload | undefined> }).uploads,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

export const volumesResource = defineResource({
  name: "volumes",
  getId: (row: Volume) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchVolumes),
  // 存量键失效桥：值域照抄旧 useApiMutation invalidate
  // （[["resources","volumes",projectId]]）
  legacyKeys: (scope) => [["resources", "volumes", String(scope.projectId ?? "")]],
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
