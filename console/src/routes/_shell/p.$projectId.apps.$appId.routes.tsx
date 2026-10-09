import { createFileRoute } from "@tanstack/react-router";
import { AppRoutesTab } from "@/features/apps-tabs/app-routes";

// App 详情 · Routes tab（IA v3 T2）：本 app 的 host 暴露面 + 创建/删除。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/routes")({
  component: AppRoutesRoute,
});

function AppRoutesRoute() {
  const { projectId, appId } = Route.useParams();
  return (
    <div className="mx-auto max-w-7xl px-6 pb-8">
      <AppRoutesTab projectId={projectId} appId={appId} />
    </div>
  );
}
