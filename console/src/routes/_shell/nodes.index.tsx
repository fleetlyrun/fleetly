import { createFileRoute } from "@tanstack/react-router";
import { NodesPage } from "@/features/nodes/nodes-list";
import { nodesSearch } from "@/kit/nodes";

// 节点列表：薄壳管路由；页面实体在 features/nodes（node 详情同款分工）。
// 2026-10-26 批1 Wave2-A useCollection 化：validateSearch 落 nodesSearch
//（q/分页/available 降序默认排序进 URL），from=Route.id 经 props 注入
// 页面（feature 不反查路由，避免 route ↔ feature 循环引用）。
export const Route = createFileRoute("/_shell/nodes/")({
  validateSearch: nodesSearch.toSearchSchema(),
  component: NodesRoute,
});

function NodesRoute() {
  return <NodesPage from={Route.id} />;
}
