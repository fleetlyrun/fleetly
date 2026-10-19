import { createFileRoute } from "@tanstack/react-router";
import { NodesPage } from "@/features/nodes/nodes-list";

// 节点列表：薄壳管路由；页面实体在 features/nodes（node 详情同款分工）。
export const Route = createFileRoute("/_shell/nodes/")({
  component: NodesPage,
});
