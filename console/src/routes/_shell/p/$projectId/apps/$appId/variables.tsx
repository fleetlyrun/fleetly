import { createFileRoute } from "@tanstack/react-router";
import { AppVariablesTab } from "@/features/apps-tabs/app-variables";

// App 详情 · Variables tab（IA v3 二期②）：冻结 Spec 只读读面。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/variables")({
  component: AppVariablesRoute,
});

function AppVariablesRoute() {
  const { projectId, appId } = Route.useParams();
  return (
    <AppVariablesTab projectId={projectId} appId={appId} />
  );
}
