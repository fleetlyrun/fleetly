import { useNavigate, createFileRoute } from "@tanstack/react-router";
import { AppSettingsTab } from "@/features/apps-tabs/app-settings";
import { useApps } from "@/lib/catalog";

// App 详情 · Settings tab（IA v3 T2）：生命周期与凭证（General / Git deploy
// hook / Danger zone）；Variables 与 Build/Processes 卡挂二期 spec 读取通路。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/settings")({
  component: AppSettingsRoute,
});

function AppSettingsRoute() {
  const { projectId, appId } = Route.useParams();
  const navigate = useNavigate();
  const apps = useApps(projectId);
  const app = (apps.data ?? []).find((entry) => entry.id === appId);
  return (
    <div className="mx-auto max-w-7xl px-6 pb-8">
      <AppSettingsTab
        projectId={projectId}
        appId={appId}
        appName={app?.name ?? appId}
        createdAt={app?.created_at}
        onDeleted={() => navigate({ to: "/p/$projectId/apps", params: { projectId } })}
      />
    </div>
  );
}
