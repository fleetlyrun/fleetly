import { createFileRoute } from "@tanstack/react-router";
import { AppDetailLayout } from "@/features/apps-tabs/app-hero";

// App 详情路由（薄壳）：hero 布局宿主在 features/apps-tabs/app-hero（与其
// 余 tab 同构——Route.useParams 只在壳层解一次）；内容区三段 crumb 与
// tab 骨架的语义注解见组件侧。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId")({
  component: AppDetailRoute,
});

function AppDetailRoute() {
  const { projectId, appId } = Route.useParams();
  return <AppDetailLayout projectId={projectId} appId={appId} routeId={Route.id} />;
}
