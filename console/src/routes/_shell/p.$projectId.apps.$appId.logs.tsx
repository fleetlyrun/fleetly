import { createFileRoute } from "@tanstack/react-router";
import { AppLogsTab } from "@/features/apps-tabs/app-logs";

// App 详情 · Logs tab（IA v3 T2）：日志工作台的 app 域变体（协议零改动）。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/logs")({
  component: AppLogsRoute,
});

function AppLogsRoute() {
  const { appId } = Route.useParams();
  return (
    <div className="mx-auto max-w-7xl px-6 pb-8">
      <AppLogsTab appId={appId} />
    </div>
  );
}
