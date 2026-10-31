import { createFileRoute } from "@tanstack/react-router";
import { AppDiagnosisTab } from "@/features/apps-tabs/app-diagnosis";

// App 详情 · Diagnosis tab（B2a W4）：条目卡四层形态 + unreadable 诚实横幅
// + 空态 = 健康。挂位对照 Processes/Overview（features/apps-tabs 宿主，
// 路由只挂参）。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/diagnosis")({
  component: AppDiagnosisRoute,
});

function AppDiagnosisRoute() {
  const { projectId, appId } = Route.useParams();
  return <AppDiagnosisTab projectId={projectId} appId={appId} routeId={Route.id} />;
}
