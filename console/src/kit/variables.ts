import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Variables 域资源适配（useCollection 化批 3）：secrets / configs / shared
// variables 三 tab 共享一 schema（过滤均 name 单字段，与旧 fieldsOf 同）。
// REST+protojson 直连与 lib/catalog 同端点同收敛口径；鉴权/401 闭环经
// kit/http 的 apiFetch 单源。

type Secret = structureSchemas["schemas"]["v1Secret"];
type Config = structureSchemas["schemas"]["v1Config"];
type SharedVariable = structureSchemas["schemas"]["v1SharedVariable"];

export type { Config, Secret, SharedVariable };

export async function fetchSecrets(ctx: ResourceScope, signal?: AbortSignal): Promise<Secret[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  try {
    return await fetchRows<Secret>(
      `/v1/secrets${qs({ project_id: projectId })}`,
      (res) => (res as { secrets?: Array<Secret | undefined> }).secrets,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（states 块错误分诊契约）
    throw toApiError(err);
  }
}

export async function fetchConfigs(ctx: ResourceScope, signal?: AbortSignal): Promise<Config[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  try {
    return await fetchRows<Config>(
      `/v1/configs${qs({ project_id: projectId })}`,
      (res) => (res as { configs?: Array<Config | undefined> }).configs,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

export async function fetchSharedVariables(
  ctx: ResourceScope,
  signal?: AbortSignal,
): Promise<SharedVariable[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  try {
    return await fetchRows<SharedVariable>(
      `/v1/shared-variables${qs({ project_id: projectId })}`,
      (res) => (res as { variables?: Array<SharedVariable | undefined> }).variables,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

export const secretsResource = defineResource({
  name: "secrets",
  getId: (row: Secret) => row.id ?? row.name ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchSecrets),
});

export const configsResource = defineResource({
  name: "configs",
  getId: (row: Config) => row.id ?? row.name ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchConfigs),
});

export const sharedVariablesResource = defineResource({
  name: "shared-variables",
  getId: (row: SharedVariable) => row.id ?? row.name ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchSharedVariables),
});

// 三 tab 共用实例：q/分页跨 tab 持久是接受语义（tab 切换函数式 search 保
// 留集合键）。字段集 = 旧 fieldsOf 同款 name 单字段。
export const variablesSearch = collectionQuerySchema({
  text: ["name"],
  defaultSize: 20,
});
