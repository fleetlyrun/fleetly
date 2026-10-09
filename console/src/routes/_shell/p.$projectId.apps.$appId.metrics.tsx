import { createFileRoute } from "@tanstack/react-router";
import { AppMetricsTab } from "@/features/apps-tabs/app-metrics";

// App 详情 · Metrics tab（IA v3 T2）：工作台预设的 app 域变体。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/metrics")({
  component: AppMetricsRoute,
});

function AppMetricsRoute() {
  const { appId } = Route.useParams();
  return (
    <div className="mx-auto max-w-7xl px-6 pb-8">
      <AppMetricsTab appId={appId} />
    </div>
  );
}
