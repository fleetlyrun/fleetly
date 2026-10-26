import { useState } from "react";
import { Outlet, useLocation, createFileRoute } from "@tanstack/react-router";
import { useCommand } from "@lynx-go/uikit-core";
import { MoreHorizontalIcon, RefreshCwIcon, RocketIcon, ScrollTextIcon, SquareTerminalIcon, TriangleAlertIcon, Undo2Icon } from "lucide-react";
import { toast } from "sonner";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { useAppDeployments } from "@/features/deployments/hooks";
import { firingAlerts } from "@/features/apps-tabs/alert-badges";
import { ContentCrumb } from "@/components/domain/content-crumb";
import { PageFrame } from "@/components/domain/page-frame";
import { CopyButton } from "@/components/domain/copy-button";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { deploymentsResource } from "@/kit/deployments";
import { toastOnSettled } from "@/kit/toast";
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
import { appNameOf, useAlertStates, useApps } from "@/lib/catalog";

// App 详情布局（Detail 原型 + IA v3 原型 screen-appdetail 对齐批）：hero
// 卡（状态徽标 + firing + 右侧动作组含 Rollback）+ 8 tab。内容区三段
// crumb（项目 / Apps / app）是项目域唯一面包屑——topbar 对 /p/ 域不再
// 重复渲染（原型顶栏无 crumb）。
// 批2（2026-10-26）：Rollback/Redeploy 两写位迁 deployments resource
// commands（AlertDialog 壳不动，反馈 toast 单源，成功跳详情保留）。
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
  // W-1 修复（2026-10-12 走查）：目录加载窗内 apps.data 为 undefined——
  // 裸 `?? appId` 让 hero/面包屑闪原始 id；appNameOf 的截断回落接住窗口
  //（`01M4QA9B56…` 形态），目录落定即换真名。
  const appLabel = appNameOf(apps.data, appId);
  const [deployOpen, setDeployOpen] = useState(false);
  const pathname = useLocation({ select: (location) => location.pathname });

  const tabBase = `/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}`;

  const rollback = useCommand(deploymentsResource.commands.rollback, {
    scope: { projectId },
    onSettled: toastOnSettled((payload) => {
      toast("Rollback requested — a new deployment is rolling the previous baseline forward");
      // 成功跳 deployment 详情（现状保留：整页导航）
      if (payload.deploymentId) window.location.assign(`${tabBase}/deployments/${encodeURIComponent(payload.deploymentId)}`);
    }),
  });

  // Redeploy（ADR-0069 批）：重发冻结 Spec 走 spec_file 第四源——共享变量
  // /secret 合成拾取走这条；语义如实披露（ADR-0064 内容寻址）：内容未变的
  // 进程零滚动，排障重启走 Processes 页的行级 Restart（RestartProcess）。
  const redeploy = useCommand(deploymentsResource.commands.redeploy, {
    scope: { projectId },
    onSettled: toastOnSettled((payload) => {
      toast("Redeploy queued — the frozen spec is submitted as a new deployment");
      if (payload.deploymentId) window.location.assign(`${tabBase}/deployments/${encodeURIComponent(payload.deploymentId)}`);
    }),
  });

  const tabs = [
    { label: "Overview", to: tabBase, exact: true },
    // Processes（ADR-0060）：进程列表 + 实例数管理——结构面紧随 Overview。
    { label: "Processes", to: `${tabBase}/processes`, exact: false },
    { label: "Deployments", to: `${tabBase}/deployments`, exact: false },
    // Spec（ADR-0071 配置可观测面）：冻结全文 / Workload 投影 / 在服观测。
    { label: "Spec", to: `${tabBase}/spec`, exact: false },
    { label: "Metrics", to: `${tabBase}/metrics`, exact: false },
    { label: "Logs", to: `${tabBase}/logs`, exact: false },
    { label: "Terminal", to: `${tabBase}/terminal`, exact: false },
    { label: "Variables", to: `${tabBase}/variables`, exact: false },
    { label: "Routes", to: `${tabBase}/routes`, exact: false },
    { label: "Settings", to: `${tabBase}/settings`, exact: false },
  ];

  return (
    <PageFrame
      breadcrumb={
        <ContentCrumb
          projectId={projectId}
          section={{ label: "Apps", to: `${tabBase.split("/apps")[0]}/apps` }}
          current={appLabel}
        />
      }
    >
      <section className="mb-5 flex flex-wrap items-center gap-4 rounded-xl border bg-card p-5">
        <ProjectAvatar seed={appId} label={appLabel} className="size-11 rounded-xl text-base" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="font-heading text-xl font-semibold tracking-tight">{appLabel}</h1>
            {latest?.state ? <DeploymentStatusBadge state={latest.state} /> : null}
            {firing.length > 0 ? (
              <a
                href="/alerts"
                title={`${firing.length} alert(s) firing on this app`}
                className="inline-flex items-center gap-1 rounded-full border border-destructive/40 bg-destructive/10 px-2 py-0.5 text-2xs font-semibold text-destructive"
              >
                <TriangleAlertIcon className="size-3" />
                {firing.length} alert firing
              </a>
            ) : null}
          </div>
          <div className="flex items-center gap-1 font-mono text-2xs text-muted-foreground">
            {appId}
            <CopyButton value={appId} />
          </div>
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Button size="sm" onClick={() => setDeployOpen(true)}>
            <RocketIcon data-icon-start-inline />
            Deploy
          </Button>
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button size="sm" variant="outline" disabled={redeploy.pending || latest == null}>
                <RefreshCwIcon data-icon-start-inline />
                Redeploy
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Redeploy {appLabel}?</AlertDialogTitle>
                <AlertDialogDescription>
                  Submits the frozen spec as a new deployment — picks up the latest shared variables and secrets.
                  Unchanged processes are not restarted (content-addressed rollout); to force-restart a single
                  process, use Restart on the Processes tab.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  disabled={redeploy.pending}
                  onClick={(event) => {
                    event.preventDefault();
                    redeploy.mutate({ appId });
                  }}
                >
                  {redeploy.pending ? "Redeploying…" : "Redeploy"}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
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
              <Button size="sm" variant="outline" disabled={rollback.pending || latest == null}>
                <Undo2Icon data-icon-start-inline />
                Rollback
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Roll back {appLabel}?</AlertDialogTitle>
                <AlertDialogDescription>
                  Re-deploys the last successful baseline as a new deployment — the current generation keeps serving until the
                  rollout completes.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  disabled={rollback.pending}
                  onClick={(event) => {
                    event.preventDefault();
                    rollback.mutate({ appId });
                  }}
                >
                  {rollback.pending ? "Rolling back…" : "Roll back"}
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
              className={`-mb-px rounded-t-md border-b-2 px-3.5 py-2 text-sm font-medium transition-colors ${
                active
                  ? "border-primary text-primary"
                  : "border-transparent text-muted-foreground hover:bg-panel-2 hover:text-foreground"
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
    </PageFrame>
  );
}
