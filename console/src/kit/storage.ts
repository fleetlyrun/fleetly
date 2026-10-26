import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as deliverySchemas } from "@/api/delivery";
import type { components as structureSchemas } from "@/api/structure";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Storage 域资源适配（useCollection 化批 3）：volumes + uploads 双 tab 共
// 享一 schema（text 并集 name/id/digest——uploads 有 digest、volumes 没有，
// 缺字段谓词恒 false 不误配）。REST+protojson 直连与 lib/catalog 同端点同
// 收敛口径（rowsOf 非空行过滤），鉴权/401 闭环经 kit/http 的 apiFetch 单源。

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
});

export const uploadsResource = defineResource({
  name: "uploads",
  getId: (row: Upload) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchUploads),
});

// 双 tab 共用实例：q/分页跨 tab 持久是接受语义（tab 切换函数式 search 保
// 留集合键）。字段集 = 旧 fieldsOf 并集（volumes name/id + uploads id/digest）。
export const storageSearch = collectionQuerySchema({
  text: ["name", "id", "digest"],
  defaultSize: 20,
});
