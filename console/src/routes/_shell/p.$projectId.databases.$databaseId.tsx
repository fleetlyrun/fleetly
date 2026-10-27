import { createFileRoute } from "@tanstack/react-router";
import { DatabaseDetailPage } from "@/features/databases/databases";

// Database 详情（IA v3 T3，5 tabs）：Overview/Metrics/Backups/Browse/Settings。
// Logs tab 按 T8 结论留二期（StreamLogsRequest 无 database 轴，需 proto + API 换轴）。
export const Route = createFileRoute("/_shell/p/$projectId/databases/$databaseId")({
  component: DatabaseDetailRoute,
});

function DatabaseDetailRoute() {
  const { projectId, databaseId } = Route.useParams();
  // routeId 线程：批4 读面 useCollection 的 from 锚（NetworksPanel 同款）。
  return <DatabaseDetailPage projectId={projectId} databaseId={databaseId} routeId={Route.id} />;
}
