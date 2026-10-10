import { useState } from "react";
import { Link, useNavigate, createFileRoute } from "@tanstack/react-router";
import { useMutation } from "@tanstack/react-query";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { BoxesIcon, MoreHorizontalIcon, PlusIcon, RocketIcon, ScrollTextIcon, SquareTerminalIcon } from "lucide-react";
import { toast } from "sonner";
import { apiSend } from "@/api/client";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { useAppDeployments, type Deployment } from "@/features/deployments/hooks";
import { CopyButton } from "@/components/domain/copy-button";
import { CliEquivalent, ListToolbar, useListFilter } from "@/components/domain/list-toolbar";
import { DataTable } from "@/components/domain/data-table";
import { EmptyState } from "@/components/domain/empty-state";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useApps, useProjectName, useRoutes, type AppEntry } from "@/lib/catalog";

// Apps 列表（List 原型 + IA v3 原型 screen-apps 对齐批）：filter 工具栏 +
// 计数 + ROUTES 列 + 行尾动词 + CLI equivalent 行；创建入口 "+ New app"
// （瘦对话框契约 §5.5：只承载 Name——create-then-configure，初始源在
// 详情页 Deploy 承接）。语境来自 URL，手贴 project ID 的时代终结。
export const Route = createFileRoute("/_shell/p/$projectId/apps/")({
  component: AppsListPage,
});

function AppsListPage() {
  const { projectId } = Route.useParams();
  const navigate = useNavigate();
  const apps = useApps(projectId);
  const routes = useRoutes(projectId);
  const projectName = useProjectName(projectId);
  const rows = apps.data ?? [];
  const [query, setQuery] = useState("");
  const filtered = useListFilter(
    rows,
    query,
    (app: AppEntry) => [app.name, app.id],
  );
  const [createOpen, setCreateOpen] = useState(false);
  const [deployOpen, setDeployOpen] = useState(false);
  const [deployAppId, setDeployAppId] = useState("");

  // ROUTES 列数据源：项目路由按 app 归簇（host 计数；链接进 app routes tab）。
  const routeCountByApp = new Map<string, number>();
  for (const route of routes.data ?? []) {
    if (route.app_id == null) continue;
    routeCountByApp.set(route.app_id, (routeCountByApp.get(route.app_id) ?? 0) + 1);
  }

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
      id: "routes",
      header: "Routes",
      cell: ({ row }) => {
        const count = routeCountByApp.get(row.original.id ?? "") ?? 0;
        if (count === 0) return <span className="text-xs text-muted-foreground">0</span>;
        return (
          <Link
            to="/p/$projectId/apps/$appId/routes"
            params={{ projectId, appId: row.original.id ?? "" }}
            className="text-xs font-semibold text-info underline-offset-2 hover:underline"
          >
            {count}
          </Link>
        );
      },
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
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader
        title="Apps"
        description={
          rows.length > 0
            ? `${rows.length} app${rows.length === 1 ? "" : "s"} in ${projectName ?? "this project"} — deployments are tracked per app`
            : "Deployments are tracked per app — create one to get started"
        }
      />
      <div className="rounded-xl border bg-card">
        <div className="px-3 pt-3">
          <ListToolbar
            label="apps"
            value={query}
            onChange={setQuery}
            placeholder="Filter apps..."
            total={rows.length}
            shown={filtered.length}
            actions={
              <Button
                size="sm"
                onClick={() => {
                  setCreateOpen(true);
                }}
              >
                <PlusIcon data-icon-start-inline />
                New app
              </Button>
            }
          />
        </div>
        <DataTable
          data={filtered}
          columns={columns}
          loading={apps.isPending}
          error={apps.isError ? apps.error : null}
          onRetry={() => void apps.refetch()}
          onRowClick={(app) => {
            void navigate({ to: "/p/$projectId/apps/$appId", params: { projectId, appId: app.id } });
          }}
          empty={
            query.trim() !== "" ? (
              <EmptyState
                icon={BoxesIcon}
                title="No apps match the filter"
                description={`No app name or id matches "${query.trim()}".`}
              />
            ) : (
              <EmptyState
                icon={BoxesIcon}
                title="No apps yet"
                description="Create one with New app — or close this and deploy an image, the app appears here."
              />
            )
          }
        />
      </div>
      <CliEquivalent command={`fleetly apps list --project ${projectId}`} />

      <NewAppDialog
        projectId={projectId}
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(appId) => {
          void navigate({ to: "/p/$projectId/apps/$appId", params: { projectId, appId } });
        }}
      />

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

// NewAppDialog 是创建瘦对话框（§5.5 契约）：只承载 Name——create-then-
// configure，进程/端口/变量等初始值建后到各 tab/Deploy 承接。
function NewAppDialog({
  projectId,
  open,
  onOpenChange,
  onCreated,
}: {
  projectId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (appId: string) => void;
}) {
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: async () =>
      apiSend<{ app?: { id?: string } }>("/v1/apps", "POST", { project_id: projectId, name: name.trim() }),
    onSuccess: (resp) => {
      toast(`App ${name.trim()} created — deploy to freeze its first spec`);
      setName("");
      onOpenChange(false);
      if (resp?.app?.id) onCreated(resp.app.id);
    },
    onError: (cause) => toast.error(cause instanceof Error ? cause.message : String(cause)),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New app</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            if (name.trim() === "") return;
            create.mutate();
          }}
        >
          <Label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">Name</span>
            <Input value={name} onChange={(event) => setName(event.target.value)} autoFocus />
            <span className="text-[11px] font-normal text-muted-foreground">
              Source and configuration land on the app's tabs after creation — deploy to freeze the first spec.
            </span>
          </Label>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending || name.trim() === ""}>
              {create.isPending ? "Creating…" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
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
        <RocketIcon data-icon-start-inline />
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
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
