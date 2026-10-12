import { createFileRoute } from "@tanstack/react-router";
import { AppOverviewTab } from "@/features/apps-tabs/app-overview";

// App 详情 · Overview tab：四指标卡 + Current status + Processes 摘要 +
// Routes + Recent deployments。组件宿主在 features/apps-tabs（与其余 tab
// 同构；2026-10-12 层级定案批外挪），路由只挂参。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/")({
  component: AppOverviewRoute,
});

function AppOverviewRoute() {
  const { projectId, appId } = Route.useParams();
  return <AppOverviewTab projectId={projectId} appId={appId} />;
}
