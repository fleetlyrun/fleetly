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
      // 详情 record（config-detail）随列表一并失效——PUT 同名冻结新版本，
      // 详情弹窗回读即时换版（收尾批测试实咬：record 键不受 legacy 桥覆盖）。
      invalidates: [{ resource: "configs" }, { resource: "config-detail" }],
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

// 三列表各持 namespace 实例（F1 回流批 M1：secrets.q/configs.q/variables.q
// 并存互不携带，「q 跨 tab 持久」妥协退役；字段集同旧 name 单字段）。
export const secretsSearch = collectionQuerySchema({ namespace: "secrets", text: ["name"], defaultSize: 20 });
export const configsSearch = collectionQuerySchema({ namespace: "configs", text: ["name"], defaultSize: 20 });
export const sharedVarsSearch = collectionQuerySchema({ namespace: "variables", text: ["name"], defaultSize: 20 });

// Config 详情回读（ConfigDetailDialog 的 GetConfig 单记录；PUT 同名冻结
// 新版本后经 put 的 config-detail selector 失效——M2 投影匹配命中 kit
// record 键，legacy 桥零读者退役）。
export interface ConfigDetail {
  version?: string;
  created_at?: string;
  content?: string;
}

export const configDetailResource = defineResource({
  name: "config-detail",
  scope: ["projectId"],
  record: (name: string, ctx: ResourceScope, signal?: AbortSignal) =>
    fetchRows<ConfigDetail>(
      `/v1/configs/${encodeURIComponent(ctx.projectId ?? "")}/${encodeURIComponent(name)}`,
      (res) => {
        const config = (res as { config?: ConfigDetail | undefined }).config;
        return config != null ? [config] : [];
      },
      signal,
    )
      .then((rows) => rows[0] ?? null)
      .catch((err: unknown) => {
        throw toApiError(err);
      }),
});

// 伴读消费面（2026-10-29 F1 回流批）：useConfigDetail 伴读钩子退役——
// panels 直接 useRecord(configDetailResource, { id: name || undefined,
// scope: { projectId } })（空 name 停查 = undefined id 门）。
