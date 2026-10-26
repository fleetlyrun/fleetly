import { createFileRoute } from "@tanstack/react-router";
import { DatabasesListPage } from "@/features/databases/databases";
import { databasesSearch } from "@/kit/databases";

// Databases 一级列表（IA v3 T3）：备份健康度列 + 创建对话框 + 行动作。
// 2026-10-26 批1 useCollection 化：快筛/引擎下拉/分页进 URL（databasesSearch）。
export const Route = createFileRoute("/_shell/p/$projectId/databases/")({
  validateSearch: databasesSearch.toSearchSchema(),
  component: DatabasesPageRoute,
});

function DatabasesPageRoute() {
  const { projectId } = Route.useParams();
  return <DatabasesListPage projectId={projectId} from={Route.id} />;
}
