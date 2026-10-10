import { useState } from "react";
import { useNavigate, createFileRoute } from "@tanstack/react-router";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { RocketIcon } from "lucide-react";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { useProjectDeployments, type Deployment } from "@/features/deployments/hooks";
import { DataTable } from "@/components/domain/data-table";
import { EmptyState } from "@/components/domain/empty-state";
import { ListToolbar, useListFilter } from "@/components/domain/list-toolbar";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import { useApps } from "@/lib/catalog";

// 项目级部署流（List 原型，UI v2 批 2）：跨 App 聚合走客户端 fan-out
//（有界 50 App、15s 档——服务端 per-App 轴契约不动，聚合是 Console 侧的
// 诚实替代，ADR-0057 不变量）。
export const Route = createFileRoute("/_shell/p/$projectId/deployments/")({
  component: ProjectDeploymentsPage,
});

function ProjectDeploymentsPage() {
  const { projectId } = Route.useParams();
  const navigate = useNavigate();
  const apps = useApps(projectId);
  const deployments = useProjectDeployments((apps.data ?? []).map((app) => app.id));
  const [deployOpen, setDeployOpen] = useState(false);

  const columns: LegacyColumnDef<Deployment, any>[] = [
    {
      accessorKey: "state",
      header: "State",
      cell: ({ row }) => <DeploymentStatusBadge state={row.original.state} />,
    },
    {
      id: "app",
      header: "App",
      cell: ({ row }) => {
        const app = (apps.data ?? []).find((entry) => entry.id === row.original.app_id);
        return (
          <div className="flex items-center gap-2">
            <ProjectAvatar seed={row.original.app_id} label={app?.name ?? "?"} className="size-5 rounded-md text-[10px]" />
            <span className="text-[13px] font-medium">{app?.name ?? row.original.app_id?.slice(0, 10) + "…"}</span>
          </div>
        );
      },
    },
    {
      id: "revision",
      header: "Revision",
      cell: ({ row }) => (
        <span className="font-mono text-xs">
          {row.original.from_revision ? `R…${row.original.from_revision.slice(-6)} → ` : ""}
          R…{row.original.to_revision?.slice(-6) ?? "—"}
        </span>
      ),
    },
    {
      id: "generation",
      header: "Generation",
      cell: ({ row }) => {
        const hasBaseline = row.original.from_generation != null && row.original.from_generation !== "0";
        return (
          <span className="font-mono text-xs text-muted-foreground">
            g{row.original.generation ?? "?"}
            {hasBaseline ? ` ← g${row.original.from_generation}` : ""}
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
          <div className="max-w-56 truncate font-mono text-[11px] text-[var(--status-danger)]" title={row.original.error}>
            {row.original.error}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        ),
    },
  ];

  const [query, setQuery] = useState("");
  const filtered = useListFilter(deployments.data ?? [], query, (d: { id?: string; app_id?: string; state?: string; to_revision?: string }) => [d.id ?? "", d.app_id ?? "", d.state ?? "", d.to_revision ?? ""]);
  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        title="Deployments"
        description="All apps in this project — client-side fan-out, refreshed every 15s"
        actions={
          <Button size="sm" onClick={() => setDeployOpen(true)}>
            <RocketIcon data-icon-start-inline />
            Deploy…
          </Button>
        }
      />
      <div className="rounded-xl border bg-card">
        <div className="px-3 pt-3">
          <ListToolbar label="deployments" value={query} onChange={setQuery} placeholder="Filter deployments..." total={(deployments.data ?? []).length} shown={filtered.length} />
        </div>
        <DataTable
          data={filtered}
          columns={columns}
          loading={deployments.isPending}
          error={deployments.isError ? deployments.error : null}
          onRetry={() => void deployments.refetch()}
          skeletonRows={8}
          onRowClick={(deployment) => {
            const appId = deployment.app_id ?? "";
            if (appId === "") return;
            void navigate({
              to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
              params: { projectId, appId, deploymentId: deployment.id ?? "" },
            });
          }}
          empty={
            <EmptyState
              icon={RocketIcon}
              title="No deployments yet"
              description="Deploy from an app's page, or pick an app above and hit Deploy."
            />
          }
        />
      </div>

      <DeploySheet
        open={deployOpen}
        onOpenChange={setDeployOpen}
        apps={apps.data ?? []}
        defaultAppId={(apps.data ?? [])[0]?.id ?? ""}
        projectId={projectId}
        onDeployed={(deploymentId) => {
          const appId = (apps.data ?? [])[0]?.id ?? "";
          if (appId === "") return;
          void navigate({
            to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
            params: { projectId, appId, deploymentId },
          });
        }}
      />
    </div>
  );
}
