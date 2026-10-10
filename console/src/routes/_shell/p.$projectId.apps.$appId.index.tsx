import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowUpRightIcon, BoxesIcon, GlobeIcon } from "lucide-react";
import { useAppDeployments } from "@/features/deployments/hooks";
import { useAppSpecs } from "@/features/spec/use-app-specs";
import { METRIC_PRESETS } from "@/features/metrics/metric-presets";
import { firingAlerts } from "@/features/apps-tabs/alert-badges";
import { lastPointValue } from "@/features/fleet/freshness";
import { useAlertStates, useApps, useMetricsSeries, useRevisions, useRoutes } from "@/lib/catalog";
import { formatBytes } from "@/lib/format";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

// App 总览（Detail 原型 + IA v3 原型 screen-appdetail 对齐批）：四指标卡
//（Replicas/CPU/Memory/Alerts）+ Current status + Routes 卡 + Recent
// deployments（R 序号——v1Revision.seq 与部署 to_revision 对表）。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/")({
  component: AppOverviewTab,
});

function AppOverviewTab() {
  const { projectId, appId } = Route.useParams();
  const apps = useApps(projectId);
  const deployments = useAppDeployments(appId);
  const revisions = useRevisions(appId);
  const routes = useRoutes(projectId);
  const alerts = useAlertStates();
  const { specs } = useAppSpecs(projectId, apps.data ?? []);
  const rows = deployments.data ?? [];
  const firing = firingAlerts(alerts.data, appId);
  const spec = specs.get(appId);
  const replicas = (spec?.processes ?? []).reduce((sum, process) => sum + Number(process.replicas ?? 0), 0);
  // app 域指标（cadvisor ns 标签过滤；最近点取值——工作台预设同款查询）
  const cpuQuery = METRIC_PRESETS.find((preset) => preset.key === "cpu_cores")?.build(appId.toLowerCase()) ?? "";
  const memQuery = METRIC_PRESETS.find((preset) => preset.key === "memory")?.build(appId.toLowerCase()) ?? "";
  const cpu = useMetricsSeries(cpuQuery, "1h");
  const mem = useMetricsSeries(memQuery, "1h");
  const cpuValue = lastPointValue(cpu.data);
  const memValue = lastPointValue(mem.data);
  // R 序号对表：revision id → seq
  const seqByRevision = new Map((revisions.data ?? []).map((rev) => [rev.id ?? "", rev.seq ?? ""]));
  const appRoutes = (routes.data ?? []).filter((route) => route.app_id === appId);

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-4 xl:grid-cols-4">
        <MetricCard label="Replicas" value={spec ? String(replicas) : undefined} hint={spec ? "frozen spec" : "no frozen spec"} />
        <MetricCard
          label="CPU"
          value={cpu.isPending ? undefined : cpuValue != null ? `${cpuValue.toFixed(2)} cores` : "n/a"}
          hint={cpuValue != null ? "all processes" : "metrics face offline or idle"}
          pending={cpu.isPending}
        />
        <MetricCard
          label="Memory"
          value={mem.isPending ? undefined : memValue != null ? formatBytes(memValue) : "n/a"}
          hint={memValue != null ? "working set" : "metrics face offline or idle"}
          pending={mem.isPending}
        />
        <MetricCard label="Alerts" value={String(firing.length)} hint={firing.length > 0 ? "firing — open Alerts" : "all quiet"} danger={firing.length > 0} />
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Current status</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2 text-[13px]">
            {deployments.isPending ? (
              <Skeleton className="h-20 w-full" />
            ) : deployments.isError ? (
              <p className="text-xs text-destructive">{deployments.error instanceof Error ? deployments.error.message : String(deployments.error)}</p>
            ) : rows.length === 0 ? (
              <EmptyState
                icon={BoxesIcon}
                title="No deployments yet"
                description="Hit Deploy in the header to create the first one."
              />
            ) : (
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
                <dt className="text-muted-foreground">state</dt>
                <dd><DeploymentStatusBadge state={rows[0]?.state} /></dd>
                <dt className="text-muted-foreground">generation</dt>
                <dd className="font-mono text-xs">g{rows[0]?.generation ?? "?"}{rows[0]?.from_generation && rows[0]?.from_generation !== "0" ? ` ← g${rows[0]?.from_generation}` : ""}</dd>
                <dt className="text-muted-foreground">latest revision</dt>
                <dd className="font-mono text-xs">{seqLabel(seqByRevision.get(rows[0]?.to_revision ?? ""), rows[0]?.to_revision)}</dd>
                <dt className="text-muted-foreground">updated</dt>
                <dd className="text-xs"><RelativeTime value={rows[0]?.updated_at} /></dd>
              </dl>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex-row items-center">
            <CardTitle className="flex items-center gap-1.5 text-sm">
              <GlobeIcon className="size-3.5 text-muted-foreground" />
              Routes
            </CardTitle>
            <Link
              to="/p/$projectId/apps/$appId/routes"
              params={{ projectId, appId }}
              className="ml-auto flex items-center gap-1 text-xs font-medium text-primary hover:underline"
            >
              Manage
              <ArrowUpRightIcon className="size-3" />
            </Link>
          </CardHeader>
          <CardContent className="flex flex-col gap-1.5 text-xs">
            {appRoutes.length === 0 ? (
              <p className="text-muted-foreground">No public routes — declare one from the Routes tab.</p>
            ) : (
              appRoutes.map((route) => (
                <div key={route.id} className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate font-mono">
                    {route.host}
                    <span className="text-muted-foreground"> → {route.process ?? "web"}:{route.port ?? "?"}</span>
                  </span>
                  {route.host ? <CopyButton value={route.host ?? ""} /> : null}
                  <a
                    href={`https://${route.host}`}
                    target="_blank"
                    rel="noreferrer"
                    className="text-info underline-offset-2 hover:underline"
                    title="Open route"
                  >
                    ↗
                  </a>
                </div>
              ))
            )}
            <p className="text-[11px] text-muted-foreground">TLS auto (ACME) · managed Traefik</p>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="text-sm">Recent deployments</CardTitle>
          <Link
            to="/p/$projectId/apps/$appId/deployments"
            params={{ projectId, appId }}
            className="ml-auto flex items-center gap-1 text-xs font-medium text-primary hover:underline"
          >
            View all
            <ArrowUpRightIcon className="size-3" />
          </Link>
        </CardHeader>
        <CardContent>
          {deployments.isPending ? (
            <Skeleton className="h-20 w-full" />
          ) : (
            <Table className="text-xs">
              <TableHeader>
                <TableRow>
                  <TableHead>State</TableHead>
                  <TableHead>Revision</TableHead>
                  <TableHead>Generation</TableHead>
                  <TableHead>Updated</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.slice(0, 5).map((item) => (
                  <TableRow key={item.id}>
                    <TableCell><DeploymentStatusBadge state={item.state} /></TableCell>
                    <TableCell className="font-mono text-[11px]">{seqLabel(seqByRevision.get(item.to_revision ?? ""), item.to_revision)}</TableCell>
                    <TableCell className="font-mono text-[11px]">g{item.generation ?? "?"}</TableCell>
                    <TableCell className="text-muted-foreground"><RelativeTime value={item.updated_at} /></TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// seqLabel 呈现 revision 序号（R12；对表未命中退化为 id 短形式——诚实降级）。
function seqLabel(seq: string | undefined, revisionId: string | undefined): string {
  if (seq) return `R${seq}`;
  if (revisionId) return `R…${revisionId.slice(-6)}`;
  return "—";
}

function MetricCard({
  label,
  value,
  hint,
  pending,
  danger,
}: {
  label: string;
  value: string | undefined;
  hint?: string;
  pending?: boolean;
  danger?: boolean;
}) {
  return (
    <Card>
      <CardContent className="flex flex-col gap-1 p-4">
        <span className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">{label}</span>
        {pending ? (
          <Skeleton className="h-7 w-16" />
        ) : (
          <span className={`font-heading text-2xl font-bold ${danger ? "text-destructive" : ""}`}>{value ?? "—"}</span>
        )}
        {hint ? <span className="truncate text-[11px] text-muted-foreground">{hint}</span> : null}
      </CardContent>
    </Card>
  );
}
