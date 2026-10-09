import { createFileRoute } from "@tanstack/react-router";
import { DatabasesListPage } from "@/features/databases/databases";

// Databases 一级列表（IA v3 T3）：备份健康度列 + 创建对话框 + 行动作。
// 入口随 T1 导航翻闸接入侧栏；/data 页过渡期保留同数据面。
export const Route = createFileRoute("/_shell/p/$projectId/databases/")({
  component: DatabasesPageRoute,
});

function DatabasesPageRoute() {
  const { projectId } = Route.useParams();
  return <DatabasesListPage projectId={projectId} />;
}
