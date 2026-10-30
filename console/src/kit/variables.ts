import { clientCollection, restFetchAll, restRecord } from "@lynx-go/uikit-core/adapters";
import {
  type CommandResult,
  defineResource,
  type ResourceScope,
} from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { apiSend } from "@/api/client";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";

// Variables 域资源适配（useCollection 化批 3）：secrets / configs / shared
// variables 三 tab 共享一 schema（过滤均 name 单字段，与旧 fieldsOf 同）。
// REST+protojson 直连与 lib/catalog 同端点同收敛口径；鉴权/401 闭环经
// authFetch（uikit 0.5.0 回流批 M4 整取面——守卫门 + restFetchAll，
// fetchRows/qs/toApiError 样板归零）。
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

const secretsAll = restFetchAll<Secret>({
  path: () => "/v1/secrets",
  params: (ctx) => ({ project_id: ctx.projectId }),
  envelope: (body) => (body as { secrets?: Array<Secret | undefined> }).secrets ?? [],
  fetch: authFetch,
});

const configsAll = restFetchAll<Config>({
  path: () => "/v1/configs",
  params: (ctx) => ({ project_id: ctx.projectId }),
  envelope: (body) => (body as { configs?: Array<Config | undefined> }).configs ?? [],
  fetch: authFetch,
});

const sharedVariablesAll = restFetchAll<SharedVariable>({
  path: () => "/v1/shared-variables",
  params: (ctx) => ({ project_id: ctx.projectId }),
  envelope: (body) => (body as { variables?: Array<SharedVariable | undefined> }).variables ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（旧 fetcher 守卫语义保留）：projectId 缺席 = 空段不打请求。
async function fetchSecrets(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<Secret>> {
  return ctx.projectId ? secretsAll(ctx, signal) : [];
}

async function fetchConfigs(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<Config>> {
  return ctx.projectId ? configsAll(ctx, signal) : [];
}

async function fetchSharedVariables(
  ctx: ResourceScope,
  signal?: AbortSignal,
): Promise<ReadonlyArray<SharedVariable>> {
  return ctx.projectId ? sharedVariablesAll(ctx, signal) : [];
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

// F1 回流批 D 组（M4 rest 工厂试点域 3/3）：config 详情回读走 restRecord
// ——信封缺席（config 键不在）经 envelope 投影为 null 空态；fetchRows +
// rows[0] ?? null + toApiError 样板归零。
export const configDetailResource = defineResource({
  name: "config-detail",
  scope: ["projectId"],
  record: restRecord<ConfigDetail | null>({
    path: (name, ctx) =>
      `/v1/configs/${encodeURIComponent(ctx.projectId ?? "")}/${encodeURIComponent(name)}`,
    envelope: (body) => (body as { config?: ConfigDetail | undefined }).config ?? null,
    fetch: authFetch,
  }),
});

// 伴读消费面（2026-10-29 F1 回流批）：useConfigDetail 伴读钩子退役——
// panels 直接 useRecord(configDetailResource, { id: name || undefined,
// scope: { projectId } })（空 name 停查 = undefined id 门）。
