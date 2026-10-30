import { clientCollection, restFetchAll } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import { apiSend } from "@/api/client";
import type { components as runtimeSchemas } from "@/api/runtime";
import { runCommandMapped } from "./errors";
import { authFetch } from "./http";

// 节点域资源适配（2026-10-26 批1 Wave2-A useCollection 化，平台域——无
// scope）：GET /v1/nodes 是平台级读面（服务端门 R08-M1 非平台管理员即
// 403；Console 的 fail-closed 门在页面层，门外不挂 useCollection）。
// REST 端点与收敛口径照抄 lib/catalog.ts 的 useNodes。
// uikit 0.5.0 回流批（M4 整取面）：nodes 直挂 restFetchAll；containers
// 走守卫门 + restFetchAll（nodeId 缺席 = 空段不打请求，signal 透传随之
// 接通——旧 fetcher 未传）。fetchRows/toApiError 样板归零。

export type NodeRow = runtimeSchemas["schemas"]["v1Node"];

export const nodesResource = defineResource({
  name: "nodes",
  getId: (row: NodeRow) => row.platform_id ?? "",
  collection: clientCollection(
    restFetchAll<NodeRow>({
      path: () => "/v1/nodes",
      envelope: (body) => (body as { nodes?: Array<NodeRow | undefined> }).nodes ?? [],
      fetch: authFetch,
    }),
  ),
  // 节点运维动词（批4 收尾挂账清偿：自批2 集成桥的裸 apiSend + 双前缀
  // 失效全套命令化）。失效 = 默认同资源（kit 集合键全 scope 相交）——
  // 桥时代打的 ["nodes"] legacy 键已随 catalog 退役消亡，不再补桥。
  commands: {
    cordon: {
      run: (input: { nodeId: string }) =>
        runCommandMapped(() => apiSend(`/v1/nodes/${encodeURIComponent(input.nodeId)}/cordon`, "POST", {})),
    },
    uncordon: {
      run: (input: { nodeId: string }) =>
        runCommandMapped(() => apiSend(`/v1/nodes/${encodeURIComponent(input.nodeId)}/uncordon`, "POST", {})),
    },
    drain: {
      run: (input: { nodeId: string }) =>
        runCommandMapped(() => apiSend(`/v1/nodes/${encodeURIComponent(input.nodeId)}/drain`, "POST", {})),
    },
    // Relay command（H10 §4.6）：重铸即断旧连接——响应携带该节点个体凭证
    //（脚本是 node-root 等价材料），ok 载荷交页面揭示面。
    issueRelayCommand: {
      run: (input: { nodeId: string }) =>
        runCommandMapped<{ relay_command?: string }>(() =>
          apiSend<{ relay_command?: string }>("/v1/nodes/enroll", "POST", { node_id: input.nodeId }),
        ),
    },
    revokeRelay: {
      run: (input: { nodeId: string }) =>
        runCommandMapped(() => apiSend("/v1/nodes/enroll", "POST", { node_id: input.nodeId, revoke: true })),
    },
  },
});

// URL 搜索 schema：text 快筛 hostname/platform_id/role（与旧页 fieldsOf
// 同字段集）；缺省排序 = available 降序（旧页渲染前排序进 schema
// defaultOrder——URL 无 sort 时 decode 落默认，fetch 保持纯取数）。
export const nodesSearch = collectionQuerySchema({
  text: ["hostname", "platform_id", "role"],
  defaultOrder: [{ field: "available", dir: "desc" }],
  defaultSize: 20,
});

// 目录级大页 schema（overview 节点卡计数等非列表消费方——直接吃 rows，
// 窗口不截断节点全集）。
export const nodesDirectorySearch = collectionQuerySchema({ defaultSize: 200 });

// 节点容器观测（ADR-0067/记录·五十六的 ListNodeContainers 读面）——批4
// catalog 退役迁入：按节点锚定的容器行（10s 轮询同旧拍）。伴读钩子落 kit
// 层（node-detail 页消费），列表化消费（未来）再落 schema。
export type NodeContainerRow = runtimeSchemas["schemas"]["v1NodeContainer"];

const nodeContainersAll = restFetchAll<NodeContainerRow>({
  path: (ctx) => `/v1/nodes/${encodeURIComponent(ctx.nodeId ?? "")}/containers`,
  envelope: (body) => (body as { containers?: Array<NodeContainerRow | undefined> }).containers ?? [],
  fetch: authFetch,
});

// 空 scope 禁查门（旧 fetcher 守卫语义保留）：nodeId 缺席 = 空段不打请求。
async function fetchNodeContainers(ctx: ResourceScope, signal?: AbortSignal): Promise<ReadonlyArray<NodeContainerRow>> {
  return ctx.nodeId ? nodeContainersAll(ctx, signal) : [];
}

export const nodeContainersResource = defineResource({
  name: "node-containers",
  getId: (row: NodeContainerRow) => row.instance ?? "",
  scope: ["nodeId"],
  collection: clientCollection(fetchNodeContainers),
});

// 伴读消费面（2026-10-29 F1 回流批）：useNodeContainers 伴读钩子退役——
// node-detail 直接 useCollectionData(nodeContainersResource,
// { scope: { nodeId }, window: { size: 200 }, refetchInterval: 10_000,
// enabled: nodeId !== "" })（10s 轮询同旧拍）。
