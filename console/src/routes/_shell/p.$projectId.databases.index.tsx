import { createFileRoute } from "@tanstack/react-router";
import { DatabasesListPage } from "@/features/databases/databases";

// Databases 一级列表（IA v3 T3）：备份健康度列 + 创建对话框 + 行动作。
export const Route = createFileRoute("/_shell/p/$projectId/databases/")({
  component: DatabasesPageRoute,
});

function DatabasesPageRoute() {
  const { projectId } = Route.useParams();
  return <DatabasesListPage projectId={projectId} />;
}
