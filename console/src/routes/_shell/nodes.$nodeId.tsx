import { createFileRoute } from "@tanstack/react-router";
import { NodeDetailPage } from "@/features/nodes/node-detail";

// Node 详情（在跑容器观测）：薄壳管参数，页面实体在 features/nodes
//（task/database 详情同款分工）。
export const Route = createFileRoute("/_shell/nodes/$nodeId")({
  component: NodeDetailRoute,
});

function NodeDetailRoute() {
  const { nodeId } = Route.useParams();
  return <NodeDetailPage nodeId={nodeId} />;
}
