import { useState } from "react";
import { useNavigate, createFileRoute } from "@tanstack/react-router";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { RocketIcon } from "lucide-react";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { useAppDeployments, type Deployment } from "@/features/deployments/hooks";
import { DataTable } from "@/components/domain/data-table";
import { EmptyState } from "@/components/domain/empty-state";
import { ListToolbar, useListFilter } from "@/components/domain/list-toolbar";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import { useApps, useRevisions } from "@/lib/catalog";
import { revisionLabel } from "@/lib/format";

// 部署列表（List 原型，UI v2 批 2）：per-App 轴（API 契约，ADR-0044）+
// 5s 轮询；Deploy Sheet 为项目语境下的主操作；行点击进叙事详情。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/deployments/")({
  component: AppDeploymentsPage,
});

function AppDeploymentsPage() {
  const { projectId, appId } = Route.useParams();
  const navigate = useNavigate();
  const deployments = useAppDeployments(appId);
  const apps = useApps(projectId);
  // R 序号对表（W-2 修复）：revision id → seq，未命中退化为 R…hash 短形式
  //（revisionLabel 单源，与 Overview 同口径）。
  const revisions = useRevisions(appId);
  const seqByRevision = new Map((revisions.data ?? []).map((rev) => [rev.id ?? "", rev.seq ?? ""]));
  const [deployOpen, setDeployOpen] = useState(false);
  const [query, setQuery] = useState("");
  const filtered = useListFilter(deployments.data ?? [], query, (d: { id?: string; state?: string; to_revision?: string }) => [d.id ?? "", d.state ?? "", d.to_revision ?? ""]);

  const columns: LegacyColumnDef<Deployment, any>[] = [
    {
      accessorKey: "state",
      header: "State",
      cell: ({ row }) => <DeploymentStatusBadge state={row.original.state} />,
    },
    {
      accessorKey: "to_revision",
      header: "Revision",
      cell: ({ row }) => {
        const from = row.original.from_revision
          ? `${revisionLabel(seqByRevision.get(row.original.from_revision), row.original.from_revision)} → `
          : "";
        return (
          <span className="font-mono text-xs">
            {from}
            {revisionLabel(seqByRevision.get(row.original.to_revision ?? ""), row.original.to_revision)}
          </span>
        );
      },
    },
    {
      accessorKey: "generation",
      header: "Generation",
      cell: ({ row }) => {
        const hasBaseline = row.original.from_generation != null && row.original.from_generation !== "0";
        return (
          <span className="font-mono text-xs text-muted-foreground">
            {hasBaseline ? `g${row.original.from_generation} → ` : ""}
            g{row.original.generation ?? "?"}
          </span>
        );
      },
    },
    {
      id: "updated",
      header: "Updated",
      cell: ({ row }) => <RelativeTime value={row.original.updated_at} className="text-xs text-muted-foreground" />,
    },
    {
      id: "error",
      header: "Error",
      cell: ({ row }) =>
        row.original.error ? (
          <div className="max-w-64 truncate font-mono text-2xs text-[var(--status-danger)]" title={row.original.error}>
            {row.original.error}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        ),
    },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-xl border bg-card">
        <div className="px-3 pt-3">
          <ListToolbar
            label="deployments"
            value={query}
            onChange={setQuery}
            placeholder="Filter deployments..."
            total={(deployments.data ?? []).length}
            shown={filtered.length}
            actions={
              <Button size="sm" onClick={() => setDeployOpen(true)}>
                <RocketIcon data-icon-start-inline />
                Deploy…
              </Button>
            }
          />
        </div>
        <DataTable
          data={filtered}
          columns={columns}
          loading={deployments.isPending}
          error={deployments.isError ? deployments.error : null}
          onRetry={() => void deployments.refetch()}
          onRowClick={(deployment) => {
            void navigate({
              to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
              params: { projectId, appId, deploymentId: deployment.id ?? "" },
            });
          }}
          empty={
            <EmptyState
              icon={RocketIcon}
              title="No deployments yet"
              description="Deploy an image, compose file, spec or uploaded source — the timeline shows up here."
              actionLabel="Deploy…"
              onAction={() => setDeployOpen(true)}
            />
          }
        />
      </div>

      <DeploySheet
        open={deployOpen}
        onOpenChange={setDeployOpen}
        apps={apps.data ?? []}
        defaultAppId={appId}
        projectId={projectId}
        onDeployed={(deploymentId) => {
          void navigate({
            to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
            params: { projectId, appId, deploymentId },
          });
        }}
      />
    </div>
  );
}
