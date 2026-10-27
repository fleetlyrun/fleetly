import { useNavigate, createFileRoute } from "@tanstack/react-router";
import { useCollection } from "@lynx-go/uikit-core";
import { AppSettingsTab } from "@/features/apps-tabs/app-settings";
import { appsResource, appsDirectorySearch } from "@/kit/apps";

// App 详情 · Settings tab（IA v3 T2）：生命周期与凭证（General / Git deploy
// hook / Danger zone）；Variables 与 Build/Processes 卡挂二期 spec 读取通路。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/settings")({
  component: AppSettingsRoute,
});

function AppSettingsRoute() {
  // 批4（2026-10-27）：目录读面迁 kit useCollection（大页 lookup 承接旧
  // useApps 的整取上限 200；60s 轮询同旧拍）。
  const { projectId, appId } = Route.useParams();
  const navigate = useNavigate();
  const apps = useCollection(appsResource, {
    from: Route.id,
    search: appsDirectorySearch,
    scope: { projectId },
    query: { refetchInterval: 60_000 },
  });
  const app = apps.rows.find((entry) => entry.id === appId);
  return (
    <AppSettingsTab
      projectId={projectId}
      appId={appId}
      appName={app?.name ?? appId}
      createdAt={app?.created_at}
      onDeleted={() => navigate({ to: "/p/$projectId/apps", params: { projectId } })}
    />
  );
}
