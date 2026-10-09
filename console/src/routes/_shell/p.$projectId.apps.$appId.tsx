import { Link, Outlet, useLocation, createFileRoute } from "@tanstack/react-router";
import { RocketIcon, ScrollTextIcon, SquareTerminalIcon, TriangleAlertIcon } from "lucide-react";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { useAppDeployments } from "@/features/deployments/hooks";
import { firingAlerts } from "@/features/apps-tabs/alert-badges";
import { CopyButton } from "@/components/domain/copy-button";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import { useState } from "react";
import { useAlertStates, useApps } from "@/lib/catalog";

// App 详情布局（IA v3 T2 + 二期②，Detail 原型）：状态 hero（firing 徽标）+
// 快捷动作 + 8 个实页 tab（Variables 由 GetAppSpec 通路点亮，只读面）。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId")({
  component: AppDetailLayout,
});

function AppDetailLayout() {
  const { projectId, appId } = Route.useParams();
  const apps = useApps(projectId);
  const deployments = useAppDeployments(appId);
  const alerts = useAlertStates();
  const firing = firingAlerts(alerts.data, appId);
  const latest = deployments.data?.[0];
  const app = apps.data?.find((entry) => entry.id === appId);
  const [deployOpen, setDeployOpen] = useState(false);
  const pathname = useLocation({ select: (location) => location.pathname });

  const tabBase = `/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}`;
  const tabs = [
    { label: "Overview", to: tabBase, exact: true },
    { label: "Deployments", to: `${tabBase}/deployments`, exact: false },
    { label: "Metrics", to: `${tabBase}/metrics`, exact: false },
    { label: "Logs", to: `${tabBase}/logs`, exact: false },
    { label: "Terminal", to: `${tabBase}/terminal`, exact: false },
    { label: "Variables", to: `${tabBase}/variables`, exact: false },
    { label: "Routes", to: `${tabBase}/routes`, exact: false },
    { label: "Settings", to: `${tabBase}/settings`, exact: false },
  ];

  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        breadcrumb={
          <span className="text-xs text-muted-foreground">
            <Link to="/p/$projectId" params={{ projectId }} className="hover:text-foreground">
              {app?.name ?? "Project"}
            </Link>
            <span className="mx-1.5">/</span>
            <span className="text-foreground">{appId === app?.id ? app?.name : appId}</span>
          </span>
        }
        title={
          <span className="flex items-center gap-3">
            <ProjectAvatar seed={appId} label={app?.name ?? appId} className="size-8 rounded-xl text-sm" />
            {app?.name ?? appId}
            {latest?.state ? <DeploymentStatusBadge state={latest.state} /> : null}
          </span>
        }
        description={
          <span className="flex items-center gap-1 font-mono text-[11.5px]">
            {appId}
            <CopyButton value={appId} />
          </span>
        }
        actions={
          <>
            {firing.length > 0 ? (
              <Button size="sm" variant="destructive" asChild>
                <a href="/alerts" title={`${firing.length} alert(s) firing on this app`}>
                  <TriangleAlertIcon data-icon-start-inline />
                  {firing.length} firing
                </a>
              </Button>
            ) : null}
            <Button size="sm" onClick={() => setDeployOpen(true)}>
              <RocketIcon data-icon-start-inline />
              Deploy
            </Button>
            <Button size="sm" variant="outline" asChild>
              <a href={`${tabBase}/logs`}>
                <ScrollTextIcon data-icon-start-inline />
                Logs
              </a>
            </Button>
            <Button size="sm" variant="outline" asChild>
              <a href={`${tabBase}/terminal`}>
                <SquareTerminalIcon data-icon-start-inline />
                Terminal
              </a>
            </Button>
          </>
        }
      />

      <div className="mb-4 flex gap-1 border-b">
        {tabs.map((tab) => {
          const active = tab.exact ? pathname === tab.to : pathname.startsWith(tab.to);
          return (
            <a
              key={tab.label}
              href={tab.to}
              className={`-mb-px rounded-t-md border-b-2 px-3.5 py-2 text-[13px] font-medium transition-colors ${
                active
                  ? "border-primary text-primary"
                  : "border-transparent text-muted-foreground hover:bg-muted hover:text-foreground"
              }`}
            >
              {tab.label}
            </a>
          );
        })}
      </div>

      <Outlet />

      <DeploySheet
        open={deployOpen}
        onOpenChange={setDeployOpen}
        apps={apps.data ?? []}
        defaultAppId={appId}
        projectId={projectId}
        onDeployed={(deploymentId) => {
          window.location.assign(`${tabBase}/deployments/${encodeURIComponent(deploymentId)}`);
        }}
      />
    </div>
  );
}
