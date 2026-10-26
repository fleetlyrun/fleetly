import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Networks 域资源适配（useCollection 化批 3）：networks + peers 双 tab 共
// 享一 schema（text name/id——peers 行无 name 字段，该叶子恒 false 不误
// 配，id 快筛仍生效）。REST+protojson 直连与 lib/catalog 同端点同收敛口
// 径；鉴权/401 闭环经 kit/http 的 apiFetch 单源。

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
});

export const networkPeersResource = defineResource({
  name: "network-peers",
  getId: (row: NetworkPeer) => row.id ?? "",
  scope: ["projectId"],
  collection: clientCollection(fetchNetworkPeers),
});

// 双 tab 共用实例：q/分页跨 tab 持久是接受语义（tab 切换函数式 search 保
// 留集合键）。字段集 = 旧 fieldsOf 并集（networks name/id；peers 行只有
// id 参与）。
export const networksSearch = collectionQuerySchema({
  text: ["name", "id"],
  defaultSize: 20,
});
