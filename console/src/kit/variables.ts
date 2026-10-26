import { clientCollection } from "@lynx-go/uikit-core/adapters";
import {
  type CommandResult,
  defineResource,
  type ResourceScope,
} from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { apiSend } from "@/api/client";
import { runCommandMapped, toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Variables 域资源适配（useCollection 化批 3）：secrets / configs / shared
// variables 三 tab 共享一 schema（过滤均 name 单字段，与旧 fieldsOf 同）。
// REST+protojson 直连与 lib/catalog 同端点同收敛口径；鉴权/401 闭环经
// kit/http 的 apiFetch 单源。
//
// 命令族切片（批 2 写路径命令化）：三资源各自的 put/remove 挂 resource
// commands（kit 集合键自动失效）；API 动词是 PUT——命令名承动词面。存量
// 键失效桥照抄旧写面 invalidate 键：secrets/configs/shared-variables 目
// 当键 + configs 详情键（["resources","config",projectId] 宽前缀覆盖
// ConfigDetailDialog 的 [.., projectId, name] 形）。shared-variables 的
// put/remove 载荷带 affected_apps（redeploy 提示链数据源）。结果两分经
// runCommandMapped 单源；toast 单源在页面 onSettled。

type Secret = structureSchemas["schemas"]["v1Secret"];
type Config = structureSchemas["schemas"]["v1Config"];
type SharedVariable = structureSchemas["schemas"]["v1SharedVariable"];

export type { Config, Secret, SharedVariable };

/** configs 写载荷（PUT 冻结新版本，回 version 号） */
export interface ConfigWriteResult {
  config?: { version?: string };
}

/** shared-variables 写载荷（受影响 App 名单——redeploy 提示链） */
export interface SharedVariableWriteResult {
  affected_apps?: Array<string | undefined>;
}

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
  // 存量键失效桥：值域照抄旧 useApiMutation invalidate
  // （[["resources","secrets",projectId]]）
  legacyKeys: (scope) => [["resources", "secrets", String(scope.projectId ?? "")]],
  commands: {
    put: {
      run: async (input: { name: string; value: string }, ctx: ResourceScope): Promise<CommandResult<null>> =>
        runCommandMapped(async () => {
          await apiSend("/v1/secrets", "PUT", {
            project_id: ctx.projectId,
            name: input.name,
            value: input.value,
          });
          return null;
        }),
    },
    remove: {
      run: async (input: { name: string }, ctx: ResourceScope): Promise<CommandResult<null>> =>
        runCommandMapped(async () => {
          await apiSend(
            `/v1/secrets/${encodeURIComponent(ctx.projectId ?? "")}/${encodeURIComponent(input.name)}`,
            "DELETE",
          );
          return null;
        }),
    },
  },
});

export const configsResource = defineResource({
  name: "configs",
  getId: (row: Config) => row.id ?? row.name ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchConfigs),
  // 存量键失效桥：列表键 + 详情宽前缀（覆盖 ConfigDetailDialog 的
  // ["resources","config",projectId,name] 形——写后回读新版本）
  legacyKeys: (scope) => [
    ["resources", "configs", String(scope.projectId ?? "")],
    ["resources", "config", String(scope.projectId ?? "")],
  ],
  commands: {
    put: {
      run: async (
        input: { name: string; content: string },
        ctx: ResourceScope,
      ): Promise<CommandResult<ConfigWriteResult>> =>
        runCommandMapped(() =>
          apiSend<ConfigWriteResult>("/v1/configs", "PUT", {
            project_id: ctx.projectId,
            name: input.name,
            content: input.content,
          }),
        ),
    },
  },
});

export const sharedVariablesResource = defineResource({
  name: "shared-variables",
  getId: (row: SharedVariable) => row.id ?? row.name ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchSharedVariables),
  // 存量键失效桥：值域照抄旧 useApiMutation invalidate
  // （[["resources","shared-variables",projectId]]）
  legacyKeys: (scope) => [["resources", "shared-variables", String(scope.projectId ?? "")]],
  commands: {
    put: {
      run: async (
        input: { name: string; value: string },
        ctx: ResourceScope,
      ): Promise<CommandResult<SharedVariableWriteResult>> =>
        runCommandMapped(() =>
          apiSend<SharedVariableWriteResult>("/v1/shared-variables", "PUT", {
            project_id: ctx.projectId,
            name: input.name,
            value: input.value,
          }),
        ),
    },
    remove: {
      run: async (
        input: { name: string },
        ctx: ResourceScope,
      ): Promise<CommandResult<SharedVariableWriteResult>> =>
        runCommandMapped(() =>
          apiSend<SharedVariableWriteResult>(
            `/v1/shared-variables/${encodeURIComponent(ctx.projectId ?? "")}/${encodeURIComponent(input.name)}`,
            "DELETE",
          ),
        ),
    },
  },
});

// 命令句柄提升（commands 在 spec 出现 ⇒ 类型非可选，免 !）
export const putSecretCommand = secretsResource.commands.put;
export const removeSecretCommand = secretsResource.commands.remove;
export const putConfigCommand = configsResource.commands.put;
export const putSharedVariableCommand = sharedVariablesResource.commands.put;
export const removeSharedVariableCommand = sharedVariablesResource.commands.remove;

// 三 tab 共用实例：q/分页跨 tab 持久是接受语义（tab 切换函数式 search 保
// 留集合键）。字段集 = 旧 fieldsOf 同款 name 单字段。
export const variablesSearch = collectionQuerySchema({
  text: ["name"],
  defaultSize: 20,
});
