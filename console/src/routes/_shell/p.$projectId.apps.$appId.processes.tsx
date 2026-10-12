import { createFileRoute } from "@tanstack/react-router";
import { AppProcessesTab } from "@/features/apps-tabs/app-processes";

// App 详情 · Processes tab（ADR-0060）：进程列表 + 实例数管理面。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/processes")({
  component: AppProcessesRoute,
});

function AppProcessesRoute() {
  const { projectId, appId } = Route.useParams();
  return (
    <AppProcessesTab projectId={projectId} appId={appId} />
  );
}
