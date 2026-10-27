import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, scoped, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core/react";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as runtimeSchemas } from "@/api/runtime";
import { toApiError } from "./errors";
import { fetchRows } from "./http";

// 节点域资源适配（2026-10-26 批1 Wave2-A useCollection 化，平台域——无
// scope）：GET /v1/nodes 是平台级读面（服务端门 R08-M1 非平台管理员即
// 403；Console 的 fail-closed 门在页面层，门外不挂 useCollection）。
// REST 端点与收敛口径照抄 lib/catalog.ts 的 useNodes（rowsOf 同款
// undefined 行过滤）。

export type NodeRow = runtimeSchemas["schemas"]["v1Node"];

export async function fetchNodes(_ctx: ResourceScope, signal?: AbortSignal): Promise<NodeRow[]> {
  try {
    return await fetchRows<NodeRow>(
      "/v1/nodes",
      (res) => (res as { nodes?: Array<NodeRow | undefined> }).nodes,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（states 块错误分诊契约）
    throw toApiError(err);
  }
}

export const nodesResource = defineResource({
  name: "nodes",
  getId: (row: NodeRow) => row.platform_id ?? "",
  collection: clientCollection(fetchNodes),
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

async function fetchNodeContainers(ctx: ResourceScope): Promise<NodeContainerRow[]> {
  const nodeId = ctx.nodeId ?? "";
  if (nodeId === "") return [];
  return fetchRows<NodeContainerRow>(
    `/v1/nodes/${encodeURIComponent(nodeId)}/containers`,
    (res) => (res as { containers?: Array<NodeContainerRow | undefined> }).containers,
  );
}

export const nodeContainersResource = defineResource({
  name: "node-containers",
  getId: (row: NodeContainerRow) => row.instance ?? "",
  scope: ["nodeId"],
  collection: clientCollection(fetchNodeContainers),
  legacyKeys: (scope) => [["node-containers", String(scope.nodeId ?? "")]],
});

export function useNodeContainers(nodeId: string) {
  return useQuery({
    ...collectionQueryOptions(scoped(nodeContainersResource, { nodeId }), { window: { size: 200 } }, {
      refetchInterval: 10_000,
    }),
    enabled: nodeId !== "",
  });
}
