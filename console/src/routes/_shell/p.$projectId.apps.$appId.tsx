import { useState } from "react";
import { Outlet, useLocation, createFileRoute } from "@tanstack/react-router";
import { useMutation } from "@tanstack/react-query";
import { MoreHorizontalIcon, RocketIcon, ScrollTextIcon, SquareTerminalIcon, TriangleAlertIcon, Undo2Icon } from "lucide-react";
import { toast } from "sonner";
import { apiSend } from "@/api/client";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { useAppDeployments } from "@/features/deployments/hooks";
import { firingAlerts } from "@/features/apps-tabs/alert-badges";
import { ContentCrumb } from "@/components/domain/content-crumb";
import { CopyButton } from "@/components/domain/copy-button";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useAlertStates, useApps } from "@/lib/catalog";

// App 详情布局（Detail 原型 + IA v3 原型 screen-appdetail 对齐批）：hero
// 卡（状态徽标 + firing + 右侧动作组含 Rollback）+ 8 tab。内容区三段
// crumb（项目 / Apps / app）是项目域唯一面包屑——topbar 对 /p/ 域不再
// 重复渲染（原型顶栏无 crumb）。
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

  const rollback = useMutation({
    mutationFn: async () =>
      apiSend<{ deployment?: { id?: string } }>(`/v1/apps/${encodeURIComponent(appId)}/rollback`, "POST", {}),
    onSuccess: (resp) => {
      toast("Rollback requested — a new deployment is rolling the previous baseline forward");
      const id = resp?.deployment?.id;
      if (id) window.location.assign(`${tabBase}/deployments/${encodeURIComponent(id)}`);
    },
    onError: (cause) => toast.error(cause instanceof Error ? cause.message : String(cause)),
  });

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
      <ContentCrumb projectId={projectId} section={{ label: "Apps", to: `${tabBase.split("/apps")[0]}/apps` }} current={app?.name ?? appId} />

      <section className="mb-5 flex flex-wrap items-center gap-4 rounded-xl border bg-card p-4">
        <ProjectAvatar seed={appId} label={app?.name ?? appId} className="size-11 rounded-xl text-base" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="font-heading text-lg font-bold tracking-tight">{app?.name ?? appId}</h1>
            {latest?.state ? <DeploymentStatusBadge state={latest.state} /> : null}
            {firing.length > 0 ? (
              <a
                href="/alerts"
                title={`${firing.length} alert(s) firing on this app`}
                className="inline-flex items-center gap-1 rounded-full border border-destructive/40 bg-destructive/10 px-2 py-0.5 text-[11px] font-semibold text-destructive"
              >
                <TriangleAlertIcon className="size-3" />
                {firing.length} alert firing
              </a>
            ) : null}
          </div>
          <div className="flex items-center gap-1 font-mono text-[11.5px] text-muted-foreground">
            {appId}
            <CopyButton value={appId} />
          </div>
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-2">
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
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button size="sm" variant="outline" disabled={rollback.isPending || latest == null}>
                <Undo2Icon data-icon-start-inline />
                Rollback
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Roll back {app?.name ?? appId}?</AlertDialogTitle>
                <AlertDialogDescription>
                  Re-deploys the last successful baseline as a new deployment — the current generation keeps serving until the
                  rollout completes.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  disabled={rollback.isPending}
                  onClick={(event) => {
                    event.preventDefault();
                    rollback.mutate();
                  }}
                >
                  {rollback.isPending ? "Rolling back…" : "Roll back"}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="sm" variant="ghost" aria-label="More actions">
                <MoreHorizontalIcon className="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem asChild>
                <a href={`${tabBase}/settings`}>Settings</a>
              </DropdownMenuItem>
              <DropdownMenuItem asChild>
                <a href={`${tabBase}/deployments`}>All deployments</a>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </section>

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
