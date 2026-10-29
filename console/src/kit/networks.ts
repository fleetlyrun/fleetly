import { clientCollection } from "@lynx-go/uikit-core/adapters";
import {
  type CommandDescriptor,
  type CommandResult,
  defineResource,
  type ResourceScope,
} from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import { apiSend } from "@/api/client";
import type { components as structureSchemas } from "@/api/structure";
import { runCommandMapped, toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Networks 域资源适配（useCollection 化批 3）：networks + peers 双 tab 共
// 享一 schema（text name/id——peers 行无 name 字段，该叶子恒 false 不误
// 配，id 快筛仍生效）。REST+protojson 直连与 lib/catalog 同端点同收敛口
// 径；鉴权/401 闭环经 kit/http 的 apiFetch 单源。
//
// 命令族切片（批 2 写路径命令化）：create 挂 networksResource commands
// （kit 集合键自动失效）；rebuild/approve/revoke/declare 是子资源/动作类
// 动词——裸 descriptor（invalidates 显式声明目标资源）。存量键失效桥：
// 旧 useApiMutation 的 invalidate 键清单照抄（networks/network-peers 目录
// 键，在册消费面——DeclarePeerForm 的 useNetworks 等——新鲜度）。结果两
// 分映射经 runCommandMapped 单源（400→rejected 保留 apperr code、409→
// conflict、其余 throw kernel ApiError）；toast 单源在页面 onSettled。

type Network = structureSchemas["schemas"]["v1Network"];
type NetworkPeer = structureSchemas["schemas"]["v1NetworkPeer"];

export type { Network, NetworkPeer };

export async function fetchNetworks(ctx: ResourceScope, signal?: AbortSignal): Promise<Network[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  try {
    return await fetchRows<Network>(
      `/v1/networks${qs({ project_id: projectId })}`,
      (res) => (res as { networks?: Array<Network | undefined> }).networks,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（states 块错误分诊契约）
    throw toApiError(err);
  }
}

export async function fetchNetworkPeers(
  ctx: ResourceScope,
  signal?: AbortSignal,
): Promise<NetworkPeer[]> {
  const projectId = ctx.projectId;
  if (projectId === undefined || projectId === "") return [];
  try {
    return await fetchRows<NetworkPeer>(
      `/v1/networks/peers${qs({ project_id: projectId })}`,
      (res) => (res as { peers?: Array<NetworkPeer | undefined> }).peers,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

export const networksResource = defineResource({
  name: "networks",
  getId: (row: Network) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchNetworks),
  // 存量键失效桥：命令 ok 后除 kit 集合键外失效旧 catalog 目录键（值域
  // 照抄旧 useApiMutation invalidate：[["resources","networks",projectId]]）
  legacyKeys: (scope) => [["resources", "networks", String(scope.projectId ?? "")]],
  commands: {
    // 建网：表单展示形（egressNone 布尔）在 run 内收敛为提交形
    // （egress_none 未勾选 = 不发送）
    create: {
      run: async (
        input: { name: string; egressNone: boolean },
        ctx: ResourceScope,
      ): Promise<CommandResult<null>> =>
        runCommandMapped(async () => {
          await apiSend("/v1/networks", "POST", {
            project_id: ctx.projectId,
            name: input.name,
            egress_none: input.egressNone || undefined,
          });
          return null;
        }),
    },
  },
});

export const networkPeersResource = defineResource({
  name: "network-peers",
  getId: (row: NetworkPeer) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchNetworkPeers),
});

// 命令句柄提升（commands 在 spec 出现 ⇒ 类型非可选，免 !）
export const createNetworkCommand = networksResource.commands.create;

// —— 裸 descriptor（子资源/动作类动词）——

// rebuild：重建 carrier 网络（minute-scale 行内钮）；目标资源 = networks
export const rebuildNetworkDescriptor: CommandDescriptor<{ networkId: string }, null> = {
  run: async (input) =>
    runCommandMapped(async () => {
      await apiSend("/v1/networks/rebuild", "POST", { network_id: input.networkId });
      return null;
    }),
  invalidates: [{ resource: networksResource.name }],
};

// approve/revoke：peer 审批面（行内钮/ConfirmDialog）；目标资源 = network-peers
export const approvePeerDescriptor: CommandDescriptor<{ peerId: string }, null> = {
  run: async (input) =>
    runCommandMapped(async () => {
      await apiSend(`/v1/networks/peers/${encodeURIComponent(input.peerId)}/approve`, "POST");
      return null;
    }),
  invalidates: [{ resource: networkPeersResource.name }],
};

export const revokePeerDescriptor: CommandDescriptor<{ peerId: string }, null> = {
  run: async (input) =>
    runCommandMapped(async () => {
      await apiSend(`/v1/networks/peers/${encodeURIComponent(input.peerId)}/revoke`, "POST");
      return null;
    }),
  invalidates: [{ resource: networkPeersResource.name }],
};

// declare：跨项目挂靠声明（本项目为 attaching 方）；旧 invalidate 键
// [["resources","network-peers",projectId]] 在 legacyKeys 桥
export const declarePeerDescriptor: CommandDescriptor<
  { networkId: string; peerProjectId: string },
  null
> = {
  run: async (input, ctx) =>
    runCommandMapped(async () => {
      await apiSend("/v1/networks/peers", "POST", {
        network_id: input.networkId,
        project_id: ctx.projectId,
        peer_project_id: input.peerProjectId,
      });
      return null;
    }),
  invalidates: [{ resource: networkPeersResource.name }],
  legacyKeys: (scope) => [["resources", "network-peers", String(scope.projectId ?? "")]],
};

// 双列表各持 namespace 实例（F1 回流批 M1：networks.q 与 peers.q 并存互
// 不携带，「q 跨 tab 持久」妥协退役；字段集保持旧并集——networks name/id、
// peers 行只有 id 参与）。
export const networksSearch = collectionQuerySchema({
  namespace: "networks",
  text: ["name", "id"],
  defaultSize: 20,
});
export const peersSearch = collectionQuerySchema({
  namespace: "peers",
  text: ["name", "id"],
  defaultSize: 20,
});
