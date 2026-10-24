import { useState } from "react";
import { Link, useNavigate, createFileRoute } from "@tanstack/react-router";
import { useMutation } from "@tanstack/react-query";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { MoreHorizontalIcon, PlusIcon, RocketIcon, ScrollTextIcon, SquareTerminalIcon } from "lucide-react";
import { toast } from "sonner";
import { useCollection } from "@lynx-go/ui-kit";
import { apiSend } from "@/api/client";
import { DataTable } from "@/components/fleet/query/data-table";
import { FilterBar } from "@/components/fleet/query/filter-bar";
import { ListPage as FleetListPage } from "@/components/fleet/query/list-page";
import { Pagination } from "@/components/fleet/query/pagination";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { useAppDeployments, type Deployment } from "@/features/deployments/hooks";
import { CopyButton } from "@/components/domain/copy-button";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { PageFrame } from "@/components/domain/page-frame";
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
import { useProjectName, useRoutes, type AppEntry } from "@/lib/catalog";
import { appsResource, appsSearch } from "@/fleet/apps";

// Apps 列表（fleet-ui 试点切片，2026-10-24）：骨架换 fleet 块（ListPage ×
// FilterBar × DataTable × Pagination），快筛/分页/排序首次进 URL（q/size/
// page/sort——旧页是 useState 本地态，无存量参数兼容面，纯增量）；业务件
// （单元格/创建对话框/DeploySheet/CLI 行）保持本仓自持。语境 projectId 经
// useCollection 的 scope 注入数据层（query key 嵌入 scope 段）。
export const Route = createFileRoute("/_shell/p/$projectId/apps/")({
  validateSearch: appsSearch.toSearchSchema(),
  component: AppsListPage,
});

function AppsListPage() {
  const { projectId } = Route.useParams();
  const navigate = useNavigate();
  const list = useCollection(appsResource, {
    // from 传 Route.id（不是 fullPath）：_shell 无路径布局下 fullPath 与
    // 路由 id 不同形，useSearch/useNavigate 按 id 找活跃 match（试点实咬，
    // 记 design.md §12.1 痛点清单）
    from: Route.id,
    search: appsSearch,
    scope: { projectId },
    query: { refetchInterval: 60_000 },
  });
  const routes = useRoutes(projectId);
  const projectName = useProjectName(projectId);
  const rows = list.rows;
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
      meta: { sortField: "name" },
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <ProjectAvatar seed={row.original.id} label={row.original.name} />
          <div className="min-w-0">
            <div className="text-sm font-semibold">{row.original.name}</div>
            <div className="flex items-center gap-0.5 font-mono text-2xs text-muted-foreground">
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

  // 源空与过滤空两套文案（旧页 filterEmpty 契约）：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;
  const count = list.total ?? rows.length;

  return (
    <PageFrame
      title="Apps"
      description={
        count > 0
          ? `${count} app${count === 1 ? "" : "s"} in ${projectName ?? "this project"} — deployments are tracked per app`
          : "Deployments are tracked per app — create one to get started"
      }
    >
      <FleetListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FilterBar list={list} schema={appsSearch} textPlaceholder="Filter apps..." />
            {/* 列表主动作钉工具栏行最右（本仓对齐批 5 裁决——不吃 fleet 块的页头钮位） */}
            <div className="py-2 pr-3">
              <Button
                size="sm"
                onClick={() => {
                  setCreateOpen(true);
                }}
              >
                <PlusIcon data-icon-start-inline />
                New app
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No apps match the filter" : "No apps yet"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : "Create one with New app — or close this and deploy an image, the app appears here."
        }
        appendix={
          <>
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
              apps={[...rows]}
              defaultAppId={deployAppId || rows[0]?.id || ""}
              projectId={projectId}
              onDeployed={(deploymentId) => {
                void navigate({
                  to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
                  params: { projectId, appId: deployAppId || rows[0]?.id || "", deploymentId },
                });
              }}
            />
          </>
        }
      >
        <DataTable
          list={list}
          columns={columns}
          getRowId={(row) => row.id}
          onRowClick={(app) => {
            void navigate({ to: "/p/$projectId/apps/$appId", params: { projectId, appId: app.id } });
          }}
        />
      </FleetListPage>
    </PageFrame>
  )
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
            <span className="text-2xs font-normal text-muted-foreground">
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
        <div className="max-w-72 truncate text-2xs text-[var(--status-danger)]" title={latest.error}>
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
