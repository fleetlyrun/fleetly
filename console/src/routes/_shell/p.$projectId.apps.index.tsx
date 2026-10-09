import { useState } from "react";
import { Link, useNavigate, createFileRoute } from "@tanstack/react-router";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { BoxesIcon, MoreHorizontalIcon, RocketIcon, ScrollTextIcon, SquareTerminalIcon } from "lucide-react";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { useAppDeployments, type Deployment } from "@/features/deployments/hooks";
import { CopyButton } from "@/components/domain/copy-button";
import { DataTable } from "@/components/domain/data-table";
import { EmptyState } from "@/components/domain/empty-state";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useApps, type AppEntry } from "@/lib/catalog";

// Apps 列表（List 原型，UI v2 批 2）：项目语境下的密度升级表——App/状态/
// 最近部署/updated/行操作（Deploy + ⋯）。手贴 project ID 的时代终结：
// 语境来自 URL。
export const Route = createFileRoute("/_shell/p/$projectId/apps/")({
  component: AppsListPage,
});

function AppsListPage() {
  const { projectId } = Route.useParams();
  const navigate = useNavigate();
  const apps = useApps(projectId);
  const rows = apps.data ?? [];
  const [deployOpen, setDeployOpen] = useState(false);
  const [deployAppId, setDeployAppId] = useState("");

  const columns: LegacyColumnDef<AppEntry, any>[] = [
    {
      accessorKey: "name",
      header: "App",
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <ProjectAvatar seed={row.original.id} label={row.original.name} />
          <div className="min-w-0">
            <div className="text-[13px] font-semibold">{row.original.name}</div>
            <div className="flex items-center gap-0.5 font-mono text-[11px] text-muted-foreground">
              {row.original.id.slice(0, 10)}…
              <CopyButton value={row.original.id ?? ""} />
            </div>
          </div>
        </div>
      ),
    },
    {
      id: "status",
      header: "Status",
      cell: ({ row }) => <LatestCell appId={row.original.id} />,
    },
    {
      id: "latest",
      header: "Latest deployment",
      cell: ({ row }) => <LatestDetailCell appId={row.original.id} />,
    },
    {
      id: "updated",
      header: "Updated",
      cell: ({ row }) => <LatestUpdatedCell appId={row.original.id} />,
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <RowActions
          projectId={projectId}
          appId={row.original.id}
          onDeploy={() => {
            setDeployAppId(row.original.id);
            setDeployOpen(true);
          }}
        />
      ),
    },
  ];

  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        title="Apps"
        description="Deployments are tracked per app — pick one to dive in"
        actions={
          <Button
            onClick={() => {
              setDeployAppId(rows[0]?.id ?? "");
              setDeployOpen(true);
            }}
          >
            <RocketIcon data-icon-start-inline />
            Deploy…
          </Button>
        }
      />
      <div className="rounded-xl border bg-card">
        <DataTable
          data={rows}
          columns={columns}
          loading={apps.isPending}
          error={apps.isError ? apps.error : null}
          onRetry={() => void apps.refetch()}
          onRowClick={(app) => {
            void navigate({ to: "/p/$projectId/apps/$appId", params: { projectId, appId: app.id } });
          }}
          empty={
            <EmptyState
              icon={BoxesIcon}
              title="No apps yet"
              description="Create one via Quickstart, or close this and deploy an image — the app appears here."
            />
          }
        />
      </div>

      <DeploySheet
        open={deployOpen}
        onOpenChange={setDeployOpen}
        apps={rows}
        defaultAppId={deployAppId || rows[0]?.id || ""}
        projectId={projectId}
        onDeployed={(deploymentId) => {
          void navigate({
            to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
            params: { projectId, appId: deployAppId || rows[0]?.id || "", deploymentId },
          });
        }}
      />
    </div>
  );
}

// 单元格组件（每行自持最近部署查询——与磁贴/详情页同查询键共享缓存）。
function useLatestDeployment(appId: string): Deployment | undefined {
  const deployments = useAppDeployments(appId);
  return deployments.data?.[0];
}

function LatestCell({ appId }: { appId: string }) {
  const latest = useLatestDeployment(appId);
  if (!latest?.state) return <span className="text-xs text-muted-foreground">—</span>;
  return <DeploymentStatusBadge state={latest.state} />;
}

function LatestDetailCell({ appId }: { appId: string }) {
  const latest = useLatestDeployment(appId);
  if (!latest) return <span className="text-xs text-muted-foreground">no deployments yet</span>;
  return (
    <div className="min-w-0">
      <div className="font-mono text-xs">
        {latest.from_revision ? "R…" : ""}
        {latest.to_revision ? `→ R…${latest.to_revision.slice(-6)}` : "—"}
      </div>
      {latest.error ? (
        <div className="max-w-72 truncate text-[11.5px] text-[var(--status-danger)]" title={latest.error}>
          {latest.error}
        </div>
      ) : null}
    </div>
  );
}

function LatestUpdatedCell({ appId }: { appId: string }) {
  const latest = useLatestDeployment(appId);
  return <RelativeTime value={latest?.updated_at} className="text-xs text-muted-foreground" />;
}

function RowActions({ projectId, appId, onDeploy }: { projectId: string; appId: string; onDeploy: () => void }) {
  return (
    <div className="flex items-center justify-end gap-1" onClick={(event) => event.stopPropagation()}>
      <Button size="sm" onClick={onDeploy}>
        Deploy
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label="More actions">
            <MoreHorizontalIcon className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem asChild>
            <Link to="/p/$projectId/apps/$appId/deployments" params={{ projectId, appId }}>
              <RocketIcon />
              Deployments
            </Link>
          </DropdownMenuItem>
          <DropdownMenuItem asChild>
            <Link to="/logs">
              <ScrollTextIcon />
              Open logs
            </Link>
          </DropdownMenuItem>
          <DropdownMenuItem asChild>
            <Link to="/terminal">
              <SquareTerminalIcon />
              Terminal
            </Link>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem asChild>
            <Link to="/quickstart">
              <BoxesIcon />
              Quickstart
            </Link>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
