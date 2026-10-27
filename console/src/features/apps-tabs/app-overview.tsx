import { Link } from "@tanstack/react-router";
import { useCollection } from "@lynx-go/uikit-core";
import { ArrowUpRightIcon, BoxesIcon, GlobeIcon, LayersIcon } from "lucide-react";
import { firingAlerts } from "@/features/apps-tabs/alert-badges";
import { lastPointValue } from "@/features/fleet/freshness";
import { useScopedMetricsSeries } from "@/kit/metrics";
import { formatBytes, revisionLabel } from "@/lib/format";
import { alertStatesResource, alertStatesLookup } from "@/kit/alerts";
import { appSpecsResource, appSpecsLookup } from "@/kit/app-specs";
import { appDeploymentsResource, appDeploymentsLookup } from "@/kit/deployments";
import { revisionsResource, revisionsLookup } from "@/kit/revisions";
import { routesResource, routesLookup } from "@/kit/routes";
import { describeApiError } from "@/kit/errors";
import { RunningCount } from "@/features/apps-tabs/running-instances";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

// App 总览（Detail 原型 + IA v3 原型 screen-appdetail 对齐批）：四指标卡
//（Replicas/CPU/Memory/Alerts）+ Current status + Processes 摘要 + Routes
// 卡 + Recent deployments（R 序号——v1Revision.seq 与部署 to_revision 对
// 表）。Processes 摘要卡是层级定案（2026-10-12）：Overview 回答"整个单元
// 健康吗"——App 汇总之下铺 per-process 行（声明 Replicas × live Running），
// 不进 Instance（钻取点在 Processes 页）。组件自路由文件外挪，与其余
// tab 同构（features/apps-tabs）。
// 批4（2026-10-27）读面收编：apps/deployments/revisions/routes/alert
// states/app specs 六读面迁 kit useCollection（routeId 由宿主路由注入，
// NetworksPanel 同款线程）；join 读面一律大页 lookup schema（窗口不截断
// 行集），轮询拍照抄旧 catalog 常量。metrics 仍走 lib/catalog 的 scoped
// 模板查询（telemetry 面属 R2 域）。
export function AppOverviewTab({ projectId, appId, routeId }: { projectId: string; appId: string; routeId: string }) {
  const deployments = useCollection(appDeploymentsResource, {
    from: routeId,
    search: appDeploymentsLookup,
    scope: { projectId, appId },
    query: { refetchInterval: 5_000 },
  });
  const revisions = useCollection(revisionsResource, {
    from: routeId,
    search: revisionsLookup,
    scope: { projectId, appId },
    query: { refetchInterval: 15_000 },
  });
  const routes = useCollection(routesResource, {
    from: routeId,
    search: routesLookup,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const alerts = useCollection(alertStatesResource, {
    from: routeId,
    search: alertStatesLookup,
    query: { refetchInterval: 15_000 },
  });
  const specsList = useCollection(appSpecsResource, {
    from: routeId,
    search: appSpecsLookup,
    scope: { projectId },
    query: { refetchInterval: 60_000 },
  });
  const specs = new Map(specsList.rows.map((row) => [row.appId, row.spec]));
  const rows = deployments.rows;
  const firing = firingAlerts([...alerts.rows], appId);
  const spec = specs.get(appId);
  const specProcesses = spec?.processes ?? [];
  const replicas = specProcesses.reduce((sum, process) => sum + Number(process.replicas ?? 0), 0);
  // app 域指标（服务端 scoped 模板；最近点取值——工作台预设同源查询）
  const cpu = useScopedMetricsSeries({ kind: "app", id: appId }, "cpu_cores", undefined, "1h");
  const mem = useScopedMetricsSeries({ kind: "app", id: appId }, "memory_working_set_bytes", undefined, "1h");
  const cpuValue = lastPointValue(cpu.data?.series);
  const memValue = lastPointValue(mem.data?.series);
  // R 序号对表：revision id → seq
  const seqByRevision = new Map(revisions.rows.map((rev) => [rev.id ?? "", rev.seq ?? ""]));
  const appRoutes = routes.rows.filter((route) => route.app_id === appId);
  const deploymentsPending = deployments.status === "loading";

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
          <CardContent className="flex flex-col gap-2 text-sm">
            {deploymentsPending ? (
              <Skeleton className="h-20 w-full" />
            ) : deployments.error != null ? (
              <p className="text-xs text-err">{describeApiError(deployments.error)}</p>
            ) : rows.length === 0 ? (
              <EmptyState
                icon={BoxesIcon}
                title="No deployments yet"
                description="Hit Deploy in the header to create the first one."
              />
            ) : (
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
                <dt className="text-muted">state</dt>
                <dd><DeploymentStatusBadge state={rows[0]?.state} /></dd>
                <dt className="text-muted">generation</dt>
                <dd className="font-mono text-xs">{rows[0]?.from_generation && rows[0]?.from_generation !== "0" ? `g${rows[0]?.from_generation} → ` : ""}g{rows[0]?.generation ?? "?"}</dd>
                <dt className="text-muted">latest revision</dt>
                <dd className="font-mono text-xs">{revisionLabel(seqByRevision.get(rows[0]?.to_revision ?? ""), rows[0]?.to_revision)}</dd>
                <dt className="text-muted">updated</dt>
                <dd className="text-xs"><RelativeTime value={rows[0]?.updated_at} /></dd>
              </dl>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex-row items-center">
            <CardTitle className="flex items-center gap-1.5 text-sm">
              <GlobeIcon className="size-3.5 text-muted" />
              Routes
            </CardTitle>
            <Link
              to="/p/$projectId/apps/$appId/routes"
              params={{ projectId, appId }}
              className="ml-auto flex items-center gap-1 text-xs font-medium text-accent hover:underline"
            >
              Manage
              <ArrowUpRightIcon className="size-3" />
            </Link>
          </CardHeader>
          <CardContent className="flex flex-col gap-1.5 text-xs">
            {appRoutes.length === 0 ? (
              <p className="text-muted">No public routes — declare one from the Routes tab.</p>
            ) : (
              appRoutes.map((route) => (
                <div key={route.id} className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate font-mono">
                    {route.host}
                    <span className="text-muted"> → {route.process ?? "web"}:{route.port ?? "?"}</span>
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
            <p className="text-2xs text-muted">TLS auto (ACME) · managed Traefik</p>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <LayersIcon className="size-3.5 text-muted" />
            Processes
          </CardTitle>
          <Link
            to="/p/$projectId/apps/$appId/processes"
            params={{ projectId, appId }}
            className="ml-auto flex items-center gap-1 text-xs font-medium text-accent hover:underline"
          >
            Manage
            <ArrowUpRightIcon className="size-3" />
          </Link>
        </CardHeader>
        <CardContent>
          {spec == null ? (
            <p className="text-xs text-muted">No frozen spec yet — processes appear after the first deploy.</p>
          ) : specProcesses.length === 0 ? (
            <p className="text-xs text-muted">The frozen spec carries no processes.</p>
          ) : (
            <Table className="text-xs">
              <TableHeader>
                <TableRow>
                  <TableHead>Process</TableHead>
                  <TableHead>Replicas</TableHead>
                  <TableHead>Running</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {specProcesses.map((process) => (
                  <TableRow key={process.name ?? ""}>
                    <TableCell className="font-mono">{process.name}</TableCell>
                    <TableCell className="font-mono text-muted">{String(process.replicas ?? 0)}</TableCell>
                    <TableCell className="font-mono">
                      <RunningCount appId={appId} processName={process.name ?? ""} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
          <p className="mt-2 text-2xs text-muted">
            Replicas are declared in the frozen spec; Running is the live instance count — expand the Processes tab for instance
            details.
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="text-sm">Recent deployments</CardTitle>
          <Link
            to="/p/$projectId/apps/$appId/deployments"
            params={{ projectId, appId }}
            className="ml-auto flex items-center gap-1 text-xs font-medium text-accent hover:underline"
          >
            View all
            <ArrowUpRightIcon className="size-3" />
          </Link>
        </CardHeader>
        <CardContent>
          {deploymentsPending ? (
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
                    <TableCell className="font-mono text-2xs">{revisionLabel(seqByRevision.get(item.to_revision ?? ""), item.to_revision)}</TableCell>
                    <TableCell className="font-mono text-2xs">g{item.generation ?? "?"}</TableCell>
                    <TableCell className="text-muted"><RelativeTime value={item.updated_at} /></TableCell>
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

// seqLabel 呈现 revision 序号——已收编 lib/format.revisionLabel 单源
//（W-2 修复：Deployments 列表同口径，本地复制体退役）。

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
        <span className="text-2xs font-semibold tracking-wide text-muted uppercase">{label}</span>
        {pending ? (
          <Skeleton className="h-7 w-16" />
        ) : (
          <span className={`font-heading text-2xl font-bold ${danger ? "text-err" : ""}`}>{value ?? "—"}</span>
        )}
        {hint ? <span className="truncate text-2xs text-muted">{hint}</span> : null}
      </CardContent>
    </Card>
  );
}
