import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowUpRightIcon, BoxesIcon } from "lucide-react";
import { useAppDeployments } from "@/features/deployments/hooks";
import { ErrorState } from "@/components/domain/error-state";
import { EmptyState } from "@/components/domain/empty-state";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

// App 总览（Detail 原型的 Overview tab，UI v2 批 2）：当前状态卡 + 近期
// 部署迷你表。Routes/进程策略在批 4/后续深化。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/")({
  component: AppOverviewTab,
});

function AppOverviewTab() {
  const { projectId, appId } = Route.useParams();
  const deployments = useAppDeployments(appId);
  const rows = deployments.data ?? [];

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Current status</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-[13px]">
          {deployments.isPending ? (
            <Skeleton className="h-20 w-full" />
          ) : deployments.isError ? (
            <ErrorState error={deployments.error} onRetry={() => void deployments.refetch()} />
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
              <dt className="text-muted-foreground">latest deploy</dt>
              <dd className="font-mono text-xs">{rows[0]?.id?.slice(0, 12)}…</dd>
              <dt className="text-muted-foreground">updated</dt>
              <dd className="text-xs"><RelativeTime value={rows[0]?.updated_at} /></dd>
            </dl>
          )}
        </CardContent>
      </Card>

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
                  <TableHead>Generation</TableHead>
                  <TableHead>Updated</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.slice(0, 5).map((item) => (
                  <TableRow key={item.id}>
                    <TableCell><DeploymentStatusBadge state={item.state} /></TableCell>
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
