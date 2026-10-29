import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core";
import { Link, useNavigate, createFileRoute } from "@tanstack/react-router";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { MoreHorizontalIcon, PlusIcon, RocketIcon, ScrollTextIcon, SquareTerminalIcon } from "lucide-react";
import { toast } from "sonner";
import { useCollection, useCommandForm } from "@lynx-go/uikit-core";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage as FleetListPage } from "@/components/kit/query/list-page";
import { PageFrame } from "@/components/domain/page-frame";
import { Pagination } from "@/components/kit/query/pagination";
import { Form } from "@/components/kit/command/form";
import { TextField } from "@/components/kit/command/fields";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { CopyButton } from "@/components/domain/copy-button";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { describeApiError } from "@/kit/errors";
import { appsResource, appsSearch, type AppEntry } from "@/kit/apps";
import { appDeploymentsResource, appDeploymentsLookup, type DeploymentRow } from "@/kit/deployments";
import { routesResource, routesLookup } from "@/kit/routes";
import { projectsAllQuery, projectsResource } from "@/kit/projects";

// Apps 列表（fleet-ui 试点切片，2026-10-24）：骨架换 fleet 块（ListPage ×
// FilterBar × DataTable × Pagination），快筛/分页/排序首次进 URL（q/size/
// page/sort——旧页是 useState 本地态，无存量参数兼容面，纯增量）；业务件
// （单元格/创建对话框/DeploySheet/CLI 行）保持本仓自持。语境 projectId 经
// useCollection 的 scope 注入数据层（query key 嵌入 scope 段）。
// 批2（2026-10-26）：New app 写位迁 useCommandForm + kit form 块（apps
// resource create 命令单源）。
export const Route = createFileRoute("/_shell/p/$projectId/apps/")({
  validateSearch: appsSearch.toSearchSchema(),
  component: AppsListPage,
});

// 行级最近部署读面（批4：hooks.ts 的 useAppDeployments 退役——per-App
// 资源按 (projectId, appId) 键共享缓存，5s 轮询同旧拍；单元格挂载语义
// 原样保留）。
function useLatestDeployment(projectId: string, appId: string, from: string): DeploymentRow | undefined {
  const deployments = useCollection(appDeploymentsResource, {
    from,
    search: appDeploymentsLookup,
    scope: { projectId, appId },
    query: { refetchInterval: 5_000 },
  });
  return deployments.rows[0];
}

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
  // 批4（2026-10-27）：路由归簇读面迁 kit useCollection（大页 lookup 不截
  // 断行集；15s 轮询同旧拍）。
  const routes = useCollection(routesResource, {
    from: Route.id,
    search: routesLookup,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  // 项目名反查（R1 的 kit/projects 伴读形：collectionQueryOptions + 固定窗，
  // 不经 URL——旧 catalog useProjects 60s 拍）。
  const projectName = useQuery({
    ...collectionQueryOptions(projectsResource, projectsAllQuery, { refetchInterval: 60_000 }),
    select: (segment) => segment.rows.find((project) => project.id === projectId)?.name,
  }).data;
  const rows = list.rows;
  const [createOpen, setCreateOpen] = useState(false);
  const [deployOpen, setDeployOpen] = useState(false);
  const [deployAppId, setDeployAppId] = useState("");

  // ROUTES 列数据源：项目路由按 app 归簇（host 计数；链接进 app routes tab）。
  const routeCountByApp = new Map<string, number>();
  for (const route of routes.rows) {
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
            <div className="flex items-center gap-0.5 font-mono text-2xs text-muted">
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
      cell: ({ row }) => <LatestCell projectId={projectId} appId={row.original.id} from={Route.id} />,
    },
    {
      id: "latest",
      header: "Latest deployment",
      cell: ({ row }) => <LatestDetailCell projectId={projectId} appId={row.original.id} from={Route.id} />,
    },
    {
      id: "routes",
      header: "Routes",
      cell: ({ row }) => {
        const count = routeCountByApp.get(row.original.id ?? "") ?? 0;
        if (count === 0) return <span className="text-xs text-muted">0</span>;
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
      cell: ({ row }) => <LatestUpdatedCell projectId={projectId} appId={row.original.id} from={Route.id} />,
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
    // 一页一壳（2026-10-28 机制收口）：壳 = PageFrame（衬垫 + 页头三件
    // 单源）；FleetListPage 降为壳内模块配方（kit 件零页面 chrome）。
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
              from={Route.id}
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
// configure，进程/端口/变量等初始值建后到各 tab/Deploy 承接。批2 迁 kit
// form 块（单静态字段）：rejected/conflict 走结果盒，失败通道 toast。
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
  const { form, submitting, result, command } = useCommandForm(appsResource.commands.create, {
    defaultValues: { name: "" },
    scope: { projectId },
    // ok 载荷从 settled.result 自取（reveal 是渲染帧值——onSettled 在重渲
    // 前触发，闭包里恒陈旧）
    onSettled: (settled, failure) => {
      if (settled?.status === "ok") {
        toast(`App ${form.state.values.name.trim()} created — deploy to freeze its first spec`);
        onOpenChange(false);
        form.reset();
        if (settled.payload.appId) onCreated(settled.payload.appId);
      } else if (settled == null && failure != null) {
        toast.error(describeApiError(failure));
      }
    },
  });
  // 重开清上一轮结果/失败面（结果盒不跨会话残留）
  const resetCommand = command.reset;
  useEffect(() => {
    if (open) resetCommand();
  }, [open, resetCommand]);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New app</DialogTitle>
        </DialogHeader>
        <Form
          form={form}
          submitting={submitting}
          result={result}
          submitLabel="Create"
          submittingLabel="Creating…"
          cancelLabel="Cancel"
          onCancel={() => onOpenChange(false)}
        >
          <TextField form={form} name="name" label="Name" required />
          <p className="text-2xs text-muted">
            Source and configuration land on the app's tabs after creation — deploy to freeze the first spec.
          </p>
        </Form>
      </DialogContent>
    </Dialog>
  );
}

function LatestCell({ projectId, appId, from }: { projectId: string; appId: string; from: string }) {
  const latest = useLatestDeployment(projectId, appId, from);
  if (!latest?.state) return <span className="text-xs text-muted">—</span>;
  return <DeploymentStatusBadge state={latest.state} />;
}

function LatestDetailCell({ projectId, appId, from }: { projectId: string; appId: string; from: string }) {
  const latest = useLatestDeployment(projectId, appId, from);
  if (!latest) return <span className="text-xs text-muted">no deployments yet</span>;
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

function LatestUpdatedCell({ projectId, appId, from }: { projectId: string; appId: string; from: string }) {
  const latest = useLatestDeployment(projectId, appId, from);
  return <RelativeTime value={latest?.updated_at} className="text-xs text-muted" />;
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
