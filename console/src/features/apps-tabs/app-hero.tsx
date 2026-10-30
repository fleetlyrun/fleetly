import { useState } from "react";
import { Outlet, useLocation } from "@tanstack/react-router";
import { useCollection, useRecord } from "@lynx-go/uikit-core";
import { MoreHorizontalIcon, RefreshCwIcon, RocketIcon, ScrollTextIcon, SquareTerminalIcon, TriangleAlertIcon } from "lucide-react";
import { toast } from "sonner";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { RollbackButton } from "@/features/deployments/rollback-confirm";
import { firingAlerts } from "@/features/apps-tabs/alert-badges";
import { ConfirmSkipDialog } from "@/components/domain/confirm-skip-dialog";
import { ContentCrumb } from "@/components/domain/content-crumb";
import { PageFrame } from "@/components/domain/page-frame";
import { CopyButton } from "@/components/domain/copy-button";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { alertStatesResource, alertStatesLookup } from "@/kit/alerts";
import { appNameOf, appsResource, appsDirectorySearch } from "@/kit/apps";
import { appDeploymentsResource, appDeploymentsLookup, deploymentsResource } from "@/kit/deployments";
import { appWorkloadsResource } from "@/kit/app-observe";
import { sourceKindLabel } from "@/lib/format";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

// App 详情布局（Detail 原型 + IA v3 原型 screen-appdetail 对齐批）：hero
// 卡（状态徽标 + firing + 右侧动作组含 Rollback）+ 8 tab。内容区三段
// crumb（项目 / Apps / app）是项目域唯一面包屑——topbar 对 /p/ 域不再
// 重复渲染（原型顶栏无 crumb）。
// 批2（2026-10-26）：Rollback/Redeploy 两写位迁 deployments resource
// commands（反馈 toast 单源，成功跳详情保留）；B1 W4 遗留直发面收口
// （2026-10-31）：Redeploy 的裸 AlertDialog 直发退役，两写位同走双击
// 确认共享件（RollbackButton / ConfirmSkipDialog）。
// 批4（2026-10-27）读面收编：目录/部署/告警三读面迁 kit useCollection——
// 目录 60s、per-App 部署 5s、告警态 15s（旧 catalog 拍）；join/下拉读面
// 一律大页 lookup schema（窗口不截断行集）。
// 组件宿主在 features/apps-tabs（与 Overview tab 同构先例外挪，路由只挂
// 参——$appId.tsx 薄壳）。
export function AppDetailLayout({
  projectId,
  appId,
  routeId,
}: {
  projectId: string;
  appId: string;
  /** 宿主路由 id（useCollection from 锚——薄壳路由注入 Route.id） */
  routeId: string;
}) {
  const apps = useCollection(appsResource, {
    from: routeId,
    search: appsDirectorySearch,
    scope: { projectId },
    query: { refetchInterval: 60_000 },
  });
  const deployments = useCollection(appDeploymentsResource, {
    from: routeId,
    search: appDeploymentsLookup,
    scope: { projectId, appId },
    query: { refetchInterval: 5_000 },
  });
  const alerts = useCollection(alertStatesResource, {
    from: routeId,
    search: alertStatesLookup,
    query: { refetchInterval: 15_000 },
  });
  const firing = firingAlerts([...alerts.rows], appId);
  const latest = deployments.rows[0];
  // 部署身份徽标（走查反馈 2026-10-27）：hero 第一眼面亮 source kind——
  // 生效锚经 workloads 单查（无轮询，record 一次取）；compose 细分留
  // Settings/Spec（hero 只给粗粒度枚举词）。
  const identity = useRecord(appWorkloadsResource, {
    id: "0",
    scope: { projectId, appId },
    query: { enabled: appId !== "" },
  });
  const sourceBadge = identity.data?.revision
    ? sourceKindLabel(identity.data.revision.source_kind)
    : undefined;
  // W-1 修复（2026-10-12 走查）：目录加载窗内 apps 行集为空——
  // 裸 `?? appId` 让 hero/面包屑闪原始 id；appNameOf 的截断回落接住窗口
  //（`01M4QA9B56…` 形态），目录落定即换真名。
  const appLabel = appNameOf(apps.rows, appId);
  const [deployOpen, setDeployOpen] = useState(false);
  const pathname = useLocation({ select: (location) => location.pathname });

  const tabBase = `/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}`;

  const rollbackDeployed = (deploymentId: string) => {
    // 成功跳 deployment 详情（现状保留：整页导航）
    window.location.assign(`${tabBase}/deployments/${encodeURIComponent(deploymentId)}`);
  };

  // Rollback（W4 B1 确认闭环）：双击确认模式收编进 RollbackButton 共享件
  //（features/deployments/rollback-confirm——无 Preview RPC 的动词走「首击
  // REQUIRED → 二击 skip=true」最简形态，设计稿 §1.6）。

  // Redeploy（ADR-0069 批）：重发冻结 Spec 走 spec_file 第四源——共享变量
  // /secret 合成拾取走这条；语义如实披露（ADR-0064 内容寻址）：内容未变的
  // 进程零滚动，排障重启走 Processes 页的行级 Restart（RestartProcess）。
  // B1 W4 遗留直发面收口（2026-10-31）：裸 AlertDialog 直发在强制确认下
  // 发不出去（428）——接 ConfirmSkipDialog 双击 skip 形态（同 Rollback 机
  // 制）；预览化（PreviewDeploy 铸票复用 DeploySheet 面板）留 B2a。

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
      <section className="mb-5 flex flex-wrap items-center gap-4 rounded-xl border bg-panel p-5">
        <ProjectAvatar seed={appId} label={appLabel} className="size-11 rounded-xl text-base" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="font-heading text-xl font-semibold tracking-tight">{appLabel}</h1>
            {latest?.state ? <DeploymentStatusBadge state={latest.state} /> : null}
            {sourceBadge && sourceBadge !== "—" ? (
              <span
                className="rounded-full border bg-panel-2/60 px-2 py-0.5 font-mono text-2xs text-muted"
                title="Deployment source — details on the Settings and Spec tabs"
              >
                {sourceBadge}
              </span>
            ) : null}
            {firing.length > 0 ? (
              <a
                href="/alerts"
                title={`${firing.length} alert(s) firing on this app`}
                className="inline-flex items-center gap-1 rounded-full border border-err/40 bg-err/10 px-2 py-0.5 text-2xs font-semibold text-err"
              >
                <TriangleAlertIcon className="size-3" />
                {firing.length} alert firing
              </a>
            ) : null}
          </div>
          <div className="flex items-center gap-1 font-mono text-2xs text-muted">
            {appId}
            <CopyButton value={appId} />
          </div>
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Button size="sm" onClick={() => setDeployOpen(true)}>
            <RocketIcon data-icon-start-inline />
            Deploy
          </Button>
          <ConfirmSkipDialog
            label="Redeploy"
            icon={<RefreshCwIcon data-icon-start-inline />}
            disabled={latest == null}
            title={`Redeploy ${appLabel}?`}
            description={
              "Submits the frozen spec as a new deployment — picks up the latest shared variables and secrets. Unchanged processes are not restarted (content-addressed rollout); to force-restart a single process, use Restart on the Processes tab."
            }
            confirmLabel="Redeploy"
            confirmAgainLabel="Confirm redeploy"
            pendingLabel="Redeploying…"
            askedSubject="Redeploying"
            askedNote="the frozen spec is resubmitted without a preview"
            descriptor={deploymentsResource.commands.redeploy}
            scope={{ projectId }}
            buildInput={(skip) => ({ appId, skip: skip || undefined })}
            onOk={(payload) => {
              toast("Redeploy queued — the frozen spec is submitted as a new deployment");
              if (payload.deploymentId) window.location.assign(`${tabBase}/deployments/${encodeURIComponent(payload.deploymentId)}`);
            }}
          />
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
          <RollbackButton
            projectId={projectId}
            appId={appId}
            appLabel={appLabel}
            disabled={latest == null}
            onDeployed={rollbackDeployed}
          />
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
                  ? "border-accent text-accent"
                  : "border-transparent text-muted hover:bg-panel-2 hover:text-fg"
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
        apps={apps.rows}
        defaultAppId={appId}
        projectId={projectId}
        from={routeId}
        onDeployed={(deploymentId) => {
          window.location.assign(`${tabBase}/deployments/${encodeURIComponent(deploymentId)}`);
        }}
      />
    </PageFrame>
  );
}
