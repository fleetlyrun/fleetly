import { useEffect, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection, useCollectionData, useCommand, useCommandForm } from "@lynx-go/uikit-core";
import type { ApiError } from "@lynx-go/uikit-core/kernel";
import type { components } from "@/api/structure";
import {
  DatabaseIcon,
  MoreHorizontalIcon,
  HardDriveDownloadIcon,
  ExternalLinkIcon,
  EyeIcon,
  RefreshCwIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";
import { apiSend } from "@/api/client";
import { backupHealth } from "@/features/databases/backup-health";
import { DatabaseLogsTab } from "@/features/databases/database-logs";
import { downloadBackupChunks } from "@/api/streams";
import { DATABASE_CARRIER_PRESETS } from "@/features/databases/database-metrics";
import { MetricChart } from "@/features/metrics/metric-chart";
import { useScopedMetricsSeries } from "@/kit/metrics";
import { specIndex } from "@/features/spec/use-app-specs";
import { CopyButton } from "@/components/domain/copy-button";
import { ContentCrumb } from "@/components/domain/content-crumb";
import { EmptyState } from "@/components/domain/empty-state";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { PageFrame } from "@/components/domain/page-frame";
import { PageTabs } from "@/components/domain/page-tabs";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, statusToneClass, type StatusTone } from "@/components/domain/status-badge";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { Form } from "@/components/kit/command/form";
import { SelectField, TextField } from "@/components/kit/command/fields";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { appsResource, appsDirectorySearch } from "@/kit/apps";
import { projectsResource } from "@/kit/projects";
import { appSpecsResource, appSpecsLookup, type AppSpec } from "@/kit/app-specs";
import {
  browseDatabaseCommand,
  databaseBackupListLookup,
  databaseBackupListResource,
  DATABASE_ENGINES,
  databasesLookup,
  databasesResource,
  databasesSearch,
  type DatabaseRow,
} from "@/kit/databases";
import { describeApiError } from "@/kit/errors";
import { toastOnSettled } from "@/kit/toast";

// Databases 一级页族（IA v3 T3，docs/design/2026-10-09-console-ia-v3.md §4.2）：
// 列表（备份健康度列）+ 详情 5-tab（Overview/Metrics/Backups/Browse/Settings）。
// Logs tab 按设计文档 §8 T8 结论留二期（StreamLogsRequest 无 database 轴）；
// Metrics 走载体寻址 preset（T8：docker label + k3s namespace/pod 双臂）。
// 2026-10-26 批1 useCollection 化（仅列表页）：骨架换 uikit 块，快筛/引擎
// 下拉/分页进 URL。批2 写路径命令化：9 位 useMutation → useCommand/
// useCommandForm（kit/databases commands 单源；创建对话框迁 kit form 块，
// 结果通道 rejected/conflict 走结果盒，失败通道 toast describeApiError）。

type DatabaseEntry = DatabaseRow;
type BackupEntry = components["schemas"]["v1Backup"];

// status: pending | running | degraded | stopped（收敛环推进）。
function databaseTone(status: string | undefined): StatusTone {
  switch (status) {
    case "running":
      return "success";
    case "degraded":
      return "warning";
    case "pending":
      return "info";
    default:
      return "neutral";
  }
}

function fieldError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

// ---- 列表页 ----

export function DatabasesListPage({ projectId, from }: { projectId: string; from: string }) {
  const list = useCollection(databasesResource, {
    from,
    search: databasesSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const rows = list.rows;
  // 项目名反查（M1 useCollectionData 伴读，60s 同旧 catalog useProjects 拍）。
  const projectName = useCollectionData(projectsResource, { window: { size: 100 }, refetchInterval: 60_000 }).rows.find(
    (project) => project.id === projectId,
  )?.name;
  const navigate = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);

  const columns: LegacyColumnDef<DatabaseEntry, any>[] = [
    {
      accessorKey: "name",
      header: "Database",
      meta: { sortField: "name" },
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <ProjectAvatar seed={row.original.id ?? ""} label={row.original.name ?? ""} />
          <div className="min-w-0">
            <div className="text-sm font-semibold">{row.original.name}</div>
            <div className="flex items-center gap-0.5 font-mono text-2xs text-muted">
              {(row.original.id ?? "").slice(0, 10)}…
              <CopyButton value={row.original.id ?? ""} />
            </div>
          </div>
        </div>
      ),
    },
    {
      accessorKey: "engine",
      header: "Engine",
      meta: { sortField: "engine" },
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted">
          {row.original.engine}
          {row.original.version ? ` · ${row.original.version}` : ""}
        </span>
      ),
    },
    {
      id: "status",
      header: "Status",
      cell: ({ row }) => (
        <div className="flex items-center gap-1.5">
          <StatusBadge tone={databaseTone(row.original.status)}>{row.original.status ?? "unknown"}</StatusBadge>
          {row.original.restore_from_backup ? (
            <StatusBadge tone="info" pulse>
              restoring
            </StatusBadge>
          ) : null}
        </div>
      ),
    },
    {
      id: "backup-health",
      header: "Backup health",
      cell: ({ row }) => {
        const health = backupHealth(row.original.last_backup_at);
        return (
          <span className={`inline-flex items-center gap-1.5 text-xs font-medium ${statusToneClass(health.tone)}`}>
            <span className="inline-block size-1.5 rounded-full bg-current" />
            {health.label}
          </span>
        );
      },
    },
    {
      id: "endpoint",
      header: "Endpoint",
      cell: ({ row }) =>
        row.original.host ? (
          <span className="flex items-center gap-1 font-mono text-xs text-muted">
            {row.original.host}:{row.original.port ?? "?"}
            <CopyButton value={`${row.original.host}:${row.original.port ?? ""}`} />
          </span>
        ) : (
          <span className="text-xs text-muted">—</span>
        ),
    },
    {
      id: "created",
      header: "Created",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          <RelativeTime value={row.original.created_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <RowActions projectId={projectId} database={row.original} />,
    },
  ];

  // 源空与过滤空两套文案（旧页 filterEmpty 契约）：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;
  const count = list.total ?? rows.length;

  return (
    // 一页一壳（2026-10-28 机制收口）：壳 = PageFrame（衬垫 + 页头三件
    // 单源）；kit ListPage 降为壳内模块配方（kit 件零页面 chrome）。
    <PageFrame
      title="Databases"
      description={
        count > 0
          ? `${count} database${count === 1 ? "" : "s"} in ${projectName ?? "this project"} — scheduled backups with verify & restore (ADR-0039)`
          : "Managed databases — scheduled backups with verify & restore (ADR-0039)"
      }
    >
      <ListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            {/* 引擎下拉由 FilterBar 经 schema filters 渲染（静态值域），页内自持 select 退役 */}
            <FilterBar list={list} schema={databasesSearch} textPlaceholder="Filter databases..." />
            <div className="py-2 pr-3">
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <DatabaseIcon data-icon-start-inline />
                New database
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No databases match the filter" : "No databases"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : "Create a database to get a template-rendered managed service with scheduled backups."
        }
        emptyAction={
          filtered ? undefined : (
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <DatabaseIcon data-icon-start-inline />
              New database
            </Button>
          )
        }
        appendix={
          <>
            <CliEquivalent command={`fleetly databases list --project ${projectId}`} />
            <CreateDatabaseDialog
              projectId={projectId}
              open={createOpen}
              onOpenChange={setCreateOpen}
              onCreated={(databaseId) =>
                navigate({ to: "/p/$projectId/databases/$databaseId", params: { projectId, databaseId } })
              }
            />
          </>
        }
      >
        <DataTable
          list={list}
          columns={columns}
          getRowId={(row) => row.id ?? ""}
          onRowClick={(row) =>
            navigate({ to: "/p/$projectId/databases/$databaseId", params: { projectId, databaseId: row.id ?? "" } })
          }
        />
      </ListPage>
    </PageFrame>
  );
}

function RowActions({ projectId, database }: { projectId: string; database: DatabaseEntry }) {
  const navigate = useNavigate();
  const id = database.id ?? "";
  // 批2：三写位走 kit commands（invalidates 默认同资源 + legacyKeys 桥
  // catalog 键；browse 动作类裸 descriptor 无失效面）。反馈走 toast 单源。
  const backup = useCommand(databasesResource.commands.backup, {
    scope: { projectId },
    onSettled: toastOnSettled(() => toast("Backup triggered")),
  });
  const browse = useCommand(browseDatabaseCommand, {
    onSettled: toastOnSettled((payload) => {
      if (payload.url) window.open(payload.url, "_blank", "noopener");
    }),
  });
  const remove = useCommand(databasesResource.commands.delete, {
    scope: { projectId },
    onSettled: toastOnSettled(() => toast(`Database ${database.name} deleted`)),
  });
  return (
    <div className="flex items-center justify-end gap-1" onClick={(event) => event.stopPropagation()}>
      <Button
        variant="outline"
        size="sm"
        disabled={backup.pending}
        onClick={() => backup.mutate({ id })}
        title="Trigger an on-demand backup"
      >
        Back up
      </Button>
      <Button
        variant="outline"
        size="sm"
        disabled={browse.pending}
        onClick={() => browse.mutate({ id })}
        title="Launch a managed browse session (one-time ticket, 120s)"
      >
        Browse
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="size-8">
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            onClick={() => navigate({ to: "/p/$projectId/databases/$databaseId", params: { projectId, databaseId: id } })}
          >
            <ExternalLinkIcon />
            Open details
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <DropdownMenuItem variant="destructive" onSelect={(event) => event.preventDefault()}>
                <Trash2Icon />
                Delete database…
              </DropdownMenuItem>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Delete database {database.name}?</AlertDialogTitle>
                <AlertDialogDescription>
                  The carrier workload and its deploy state are removed. The volume and the credential secret are kept and must be
                  cleaned up separately. Data is destroyed.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  disabled={remove.pending}
                  onClick={(event) => {
                    event.preventDefault();
                    remove.mutate({ id });
                  }}
                >
                  {remove.pending ? "Deleting…" : "Delete"}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

// New database 对话框（创建交互统一契约 + §5.5 瘦契约）：Name + engine +
// 可选 restore-from-backup；引擎参数配置属二期。批2 迁 kit form/fields 块
// （静态字段三枚）：rejected/conflict 走 Form 块结果盒（message-first），
// 失败通道 toast describeApiError；ok 后原句 toast + 关窗 + 重置 + 跳详情。
export function CreateDatabaseDialog({
  projectId,
  open,
  onOpenChange,
  onCreated,
}: {
  projectId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated?: (databaseId: string) => void;
}) {
  const { form, submitting, result, command } = useCommandForm(databasesResource.commands.create, {
    defaultValues: { name: "", engine: "postgres", restoreFrom: "" },
    scope: { projectId },
    // ok 载荷从 settled.result 自取（reveal 是渲染帧值——onSettled 在重渲
    // 前触发，闭包里恒陈旧）
    onSettled: (settled, failure) => {
      if (settled?.status === "ok") {
        // restore 分句在提交值上判（reset 前读取，与旧文案口径一致）
        const restored = form.state.values.restoreFrom !== "";
        toast(restored ? "Restore queued — the new database converges from the backup" : "Database created");
        onOpenChange(false);
        form.reset();
        if (settled.payload.id) onCreated?.(settled.payload.id);
      } else if (settled == null && failure != null) {
        // 失败通道（网络/401/5xx）：结果盒只吃结果两分，失败走 toast
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
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!submitting) onOpenChange(next);
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New database</DialogTitle>
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
          <TextField form={form} name="name" label="Name" placeholder="orders-db" required />
          <SelectField
            form={form}
            name="engine"
            label="Engine"
            options={DATABASE_ENGINES.map((engine) => ({ value: engine, label: engine }))}
          />
          <TextField form={form} name="restoreFrom" label="Restore from backup (optional)" placeholder="bk_01j…" />
          <p className="text-2xs text-muted">
            Restore creates a new database seeded from the backup — the source is never overwritten.
          </p>
        </Form>
      </DialogContent>
    </Dialog>
  );
}

// ---- 详情页（5 tabs） ----

export function DatabaseDetailPage({ projectId, databaseId, routeId }: { projectId: string; databaseId: string; routeId: string }) {
  // 批4（2026-10-27）读面收编：库行查找/目录/spec 反查三读面迁 kit
  // useCollection——大页 lookup 不截断行集，拍同旧 catalog 常量；Used-by
  // 反查逻辑不动。metrics 与 projects 反查仍走 lib/catalog（R2/R1 域）。
  const databases = useCollection(databasesResource, {
    from: routeId,
    search: databasesLookup,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const apps = useCollection(appsResource, {
    from: routeId,
    search: appsDirectorySearch,
    scope: { projectId },
    query: { refetchInterval: 60_000 },
  });
  const specsList = useCollection(appSpecsResource, {
    from: routeId,
    search: appSpecsLookup,
    scope: { projectId },
    query: { refetchInterval: 60_000 },
  });
  const specs = new Map(specsList.rows.map((row) => [row.appId, row.spec]));
  const database = databases.rows.find((entry) => entry.id === databaseId);
  const navigate = useNavigate();
  const [tab, setTab] = useState<"overview" | "metrics" | "logs" | "backups" | "browse" | "settings">("overview");
  // 批2：页角备份/开窗两写位走 kit commands（与 RowActions 同失效面）
  const backup = useCommand(databasesResource.commands.backup, {
    scope: { projectId },
    onSettled: toastOnSettled(() => toast("Backup triggered")),
  });
  const browse = useCommand(browseDatabaseCommand, {
    onSettled: toastOnSettled((payload) => {
      if (payload.url) window.open(payload.url, "_blank", "noopener");
    }),
  });
  return (
    <PageFrame
      breadcrumb={
        <ContentCrumb
          projectId={projectId}
          section={{ label: "Databases", to: "/p/$projectId/databases" }}
          current={database?.name ?? databaseId}
        />
      }
    >
      {/* 英雄卡对齐 App Detail（2026-10-12 走查：裸标题 + 页角动作钮与 Apps
          形态脱队——头像/名称/徽标/可读 id/动作组统一收卡。 */}
      <section className="mb-5 flex flex-wrap items-center gap-4 rounded-xl border bg-panel p-5">
        <ProjectAvatar seed={databaseId} label={database?.name ?? databaseId} className="size-11 rounded-xl text-base" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="font-heading text-xl font-semibold tracking-tight">{database?.name ?? databaseId}</h1>
            <StatusBadge tone={databaseTone(database?.status)}>{database?.status ?? "unknown"}</StatusBadge>
            {database?.restore_from_backup ? (
              <StatusBadge tone="info" pulse>
                restoring
              </StatusBadge>
            ) : null}
          </div>
          <div className="flex items-center gap-1 font-mono text-2xs text-muted">
            {databaseId}
            <CopyButton value={databaseId} />
          </div>
        </div>
        <div className="ml-auto flex flex-none items-center gap-2">
          <Button size="sm" disabled={backup.pending} onClick={() => backup.mutate({ id: databaseId })}>
            <HardDriveDownloadIcon data-icon-start-inline />
            Back up now
          </Button>
          <Button size="sm" variant="outline" disabled={browse.pending} onClick={() => browse.mutate({ id: databaseId })}>
            <EyeIcon data-icon-start-inline />
            Browse
          </Button>
        </div>
      </section>

      <PageTabs
        tabs={[
          { value: "overview", label: "Overview" },
          { value: "metrics", label: "Metrics" },
          { value: "logs", label: "Logs" },
          { value: "backups", label: "Backups" },
          { value: "browse", label: "Browse" },
          { value: "settings", label: "Settings" },
        ]}
        current={tab}
        onChange={setTab}
      />
      {tab === "overview" ? <DatabaseOverview database={database} projectApps={apps.rows} specs={specs} /> : null}
      {tab === "metrics" ? <DatabaseMetrics databaseId={databaseId} /> : null}
      {tab === "logs" ? <DatabaseLogsTab databaseId={databaseId} /> : null}
      {tab === "backups" ? (
        <DatabaseBackups database={database} projectId={projectId} databaseId={databaseId} routeId={routeId} />
      ) : null}
      {tab === "browse" ? (
        <DatabaseBrowsePanel
          browse={() => browse.mutate({ id: databaseId })}
          pending={browse.pending}
          failure={browse.failure}
          engine={database?.engine}
        />
      ) : null}
      {tab === "settings" ? (
        <DatabaseSettings
          database={database}
          projectId={projectId}
          projectApps={apps.rows}
          specs={specs}
          onDeleted={() => navigate({ to: "/p/$projectId/databases", params: { projectId } })}
        />
      ) : null}
    </PageFrame>
  );
}

function DatabaseOverview({
  database,
  projectApps,
  specs,
}: {
  database: DatabaseEntry | undefined;
  projectApps: ReadonlyArray<{ id: string; name: string }>;
  specs: Map<string, AppSpec>;
}) {
  if (database == null) {
    return <EmptyState icon={DatabaseIcon} title="Database not found" description="It may have been deleted." />;
  }
  // Used-by 反查（IA v3 二期②）：扫描项目内 App 冻结 Spec 的 secret_refs，
  // 锚 = 库凭证 Secret 名（credentials_ref）——值永不进 Spec，引用即锚。
  const usedBy = database.credentials_ref
    ? (specIndex(specs, (spec) => (spec.processes ?? []).flatMap((process) => process.secret_refs ?? [])).get(database.credentials_ref) ?? [])
        .map((appId) => projectApps.find((app) => app.id === appId)?.name ?? appId)
    : [];
  const health = backupHealth(database.last_backup_at);
  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Connection</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted">host</dt>
          <dd className="flex items-center gap-1 font-mono">
            {database.host ?? "—"}
            {database.host ? <CopyButton value={database.host} /> : null}
          </dd>
          <dt className="text-muted">port</dt>
          <dd className="font-mono">{database.port ?? "—"}</dd>
          <dt className="text-muted">credential</dt>
          <dd className="flex items-center gap-1 font-mono">
            {database.credentials_ref ?? "—"}
            {database.credentials_ref ? <CopyButton value={database.credentials_ref} /> : null}
          </dd>
        </dl>
        <p className="mt-3 text-2xs text-muted">
          The full connection URL lives in the project secret and is never displayed. Apps receive it via secret_refs.
        </p>
      </section>
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Engine</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted">engine</dt>
          <dd className="font-mono">{database.engine ?? "—"}</dd>
          <dt className="text-muted">version</dt>
          <dd className="font-mono">{database.version ?? "pinned"}</dd>
          <dt className="text-muted">created</dt>
          <dd>
            <RelativeTime value={database.created_at} />
          </dd>
          <dt className="text-muted">updated</dt>
          <dd>
            <RelativeTime value={database.updated_at} />
          </dd>
        </dl>
      </section>
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Used by</h3>
        {usedBy.length === 0 ? (
          <p className="text-xs text-muted">
            No app references this database yet — apps attach via secret_refs on the credential secret{" "}
            <span className="font-mono">{database.credentials_ref ?? "—"}</span>.
          </p>
        ) : (
          <div className="flex flex-col gap-2 text-xs">
            {usedBy.map((appName) => (
              <span key={appName} className="flex items-center gap-2">
                <span className="font-mono">{appName}</span>
                <span className="rounded-full border border-info/30 bg-info/10 px-2 py-0.5 text-2xs text-info">
                  {database.credentials_ref}
                </span>
              </span>
            ))}
          </div>
        )}
        <p className="mt-3 text-2xs text-muted">
          Resolved from frozen app specs (secret_refs scan) — live after the next deploy.
        </p>
      </section>
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Backup</h3>
        <div className="mb-2 flex items-center gap-2">
          <span className={`inline-flex items-center gap-1.5 text-xs font-medium ${statusToneClass(health.tone)}`}>
            <span className="inline-block size-1.5 rounded-full bg-current" />
            {health.label}
          </span>
        </div>
        <p className="text-xs text-muted">
          Last successful backup:{" "}
          {database.last_backup_at ? <RelativeTime value={database.last_backup_at} /> : "never"}
        </p>
        {database.restore_from_backup ? (
          <div className="mt-3 rounded-md border border-info/30 bg-info/10 p-2.5 text-xs">
            <p className="font-medium">Restore in progress</p>
            <p className="mt-1 font-mono text-2xs text-muted">from {database.restore_from_backup}</p>
            {database.restore_error ? (
              <p className="mt-1 text-2xs text-err">last error: {database.restore_error}</p>
            ) : null}
          </div>
        ) : null}
      </section>
    </div>
  );
}

function DatabaseMetrics({ databaseId }: { databaseId: string }) {
  const memory = DATABASE_CARRIER_PRESETS.find((preset) => preset.key === "memory")!;
  const cpu = DATABASE_CARRIER_PRESETS.find((preset) => preset.key === "cpu_cores")!;
  const memorySeries = useScopedMetricsSeries({ kind: "database", id: databaseId }, memory.metric, undefined, "1h");
  const cpuSeries = useScopedMetricsSeries({ kind: "database", id: databaseId }, cpu.metric, undefined, "1h");
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 lg:grid-cols-2">
        <MetricCard title="Memory working set" unit="bytes" series={memorySeries} />
        <MetricCard title="CPU (cores)" unit="plain" series={cpuSeries} />
      </div>
      <p className="text-2xs text-muted">
        Scoped metric templates: the server resolves this database's project and builds the docker/k3s arms — the
        console never sends PromQL.
      </p>
    </div>
  );
}

function MetricCard({
  title,
  unit,
  series,
}: {
  title: string;
  unit: "bytes" | "plain";
  series: ReturnType<typeof useScopedMetricsSeries>;
}) {
  const seriesList = series.data?.series ?? [];
  return (
    <section className="rounded-xl border bg-panel p-4">
      <div className="mb-2 flex items-baseline justify-between">
        <h3 className="text-sm font-semibold">{title}</h3>
        <span className="text-2xs text-muted">window 1h · refreshed 30s</span>
      </div>
      {series.isPending ? (
        <p className="py-10 text-center text-xs text-muted">Loading series…</p>
      ) : series.isError ? (
        <p className="py-10 text-center text-xs text-err">{fieldError(series.error)}</p>
      ) : seriesList.length === 0 ? (
        <p className="py-10 text-center text-xs text-muted">No series — the carrier may be stopped.</p>
      ) : (
        <MetricChart
          seriesList={seriesList}
          unit={unit}
          truncated={series.data?.truncated}
          seriesTotal={series.data?.seriesTotal}
        />
      )}
    </section>
  );
}

function DatabaseBackups({
  database,
  projectId,
  databaseId,
  routeId,
}: {
  database: DatabaseEntry | undefined;
  projectId: string;
  databaseId: string;
  routeId: string;
}) {
  // 批4：单库备份读面迁 kit databaseBackupListResource（15s 轮询同旧拍）。
  const backups = useCollection(databaseBackupListResource, {
    from: routeId,
    search: databaseBackupListLookup,
    scope: { projectId, databaseId },
    query: { refetchInterval: 15_000 },
  });
  const rows = backups.rows;
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-3 sm:grid-cols-3">
        <InfoTile label="Retention" value="rolling (platform)" />
        <InfoTile label="Backend" value="local object store" hint="same-node — not disaster recovery" />
        <InfoTile label="Schedule" value="platform backup window" />
      </div>
      <div className="overflow-hidden rounded-xl border">
        <table className="w-full text-sm">
          <thead className="border-b bg-panel-2/40 text-left text-2xs font-semibold tracking-wide text-muted uppercase">
            <tr>
              <th className="px-3 py-2">Backup</th>
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2">Size</th>
              <th className="px-3 py-2">Digest</th>
              <th className="px-3 py-2">Finished</th>
              <th className="px-3 py-2 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {backups.status === "loading" ? (
              <tr>
                <td colSpan={6} className="px-3 py-6 text-center text-xs text-muted">
                  Loading backups…
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr>
                <td colSpan={6} className="px-3 py-6 text-center text-xs text-muted">
                  No backups yet — trigger one with “Back up now”.
                </td>
              </tr>
            ) : (
              rows.map((backup) => <BackupRow key={backup.id} backup={backup} projectId={projectId} engine={database?.engine ?? ""} />)
            )}
          </tbody>
        </table>
      </div>
      <p className="text-2xs text-muted">
        Restore always creates a new database — the source is never overwritten (ADR-0039).
      </p>
    </div>
  );
}

function InfoTile({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-xl border bg-panel p-3.5">
      <div className="text-2xs font-medium tracking-wide text-muted uppercase">{label}</div>
      <div className="mt-1 text-sm font-semibold">{value}</div>
      {hint ? <div className="mt-0.5 text-2xs text-muted">{hint}</div> : null}
    </div>
  );
}

function BackupRow({
  backup,
  projectId,
  engine,
}: {
  backup: BackupEntry;
  projectId: string;
  engine: string;
}) {
  const [verifying, setVerifying] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [verifyResult, setVerifyResult] = useState<{ ok: boolean; detail: string } | null>(null);
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [restoreName, setRestoreName] = useState("");
  // restore = create 命令的 restore_from_backup 形（ADR-0039：恒新建库）；
  // 行内面无结果盒，结果/失败通道都走 toast 单源
  const restore = useCommand(databasesResource.commands.create, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      toast("Restore queued — the new database converges from the backup");
      setRestoreOpen(false);
      setRestoreName("");
    }),
  });
  async function runDownload() {
    setDownloading(true);
    try {
      const blob = await downloadBackupChunks(backup.project_id ?? projectId, backup.id ?? "", new AbortController().signal);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `${backup.id ?? "backup"}.backup`;
      anchor.click();
      URL.revokeObjectURL(url);
      toast(`Backup downloaded (${(blob.size / 1048576).toFixed(2)} MiB)`);
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setDownloading(false);
    }
  }

  async function runVerify() {
    setVerifying(true);
    setVerifyResult(null);
    try {
      const res = await apiSend<{ ok?: boolean; digest?: string; error?: string }>(
        `/v1/backups/${encodeURIComponent(backup.id ?? "")}/verify`,
        "POST",
        {},
      );
      setVerifyResult({ ok: res.ok === true, detail: res.ok === true ? `digest ${res.digest?.slice(0, 16)}…` : res.error ?? "verification error" });
    } catch (cause) {
      setVerifyResult({ ok: false, detail: fieldError(cause) });
    } finally {
      setVerifying(false);
    }
  }
  return (
    <tr className="border-b last:border-b-0">
      <td className="px-3 py-2 font-mono text-xs">{backup.id}</td>
      <td className="px-3 py-2">
        <StatusBadge tone={backup.status === "succeeded" ? "success" : backup.status === "failed" ? "danger" : "info"} pulse={backup.status === "running"}>
          {backup.status ?? "unknown"}
        </StatusBadge>
        {backup.error ? <div className="mt-1 max-w-56 truncate text-2xs text-err" title={backup.error}>{backup.error}</div> : null}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-muted">
        {backup.size_bytes ? `${(Number(backup.size_bytes) / 1048576).toFixed(2)} MiB` : "—"}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-muted" title={backup.digest}>
        {backup.digest ? `${backup.digest.slice(0, 16)}…` : "—"}
      </td>
      <td className="px-3 py-2 text-xs text-muted">
        <RelativeTime value={backup.finished_at ?? backup.created_at} />
      </td>
      <td className="px-3 py-2">
        <div className="flex items-center justify-end gap-1.5">
          {verifyResult != null ? (
            <span className={`text-2xs ${verifyResult.ok ? "text-emerald-600 dark:text-emerald-400" : "text-err"}`}>
              {verifyResult.ok ? `ok — ${verifyResult.detail}` : `failed — ${verifyResult.detail}`}
            </span>
          ) : null}
          <Button variant="outline" size="sm" disabled={verifying || downloading || backup.status !== "succeeded"} onClick={() => void runDownload()} title="Stream the backup object to a local file">
            {downloading ? "Downloading…" : "Download"}
          </Button>
          <Button variant="outline" size="sm" disabled={verifying} onClick={() => void runVerify()} title="Recompute the digest against the stored object (read-only)">
            {verifying ? "Verifying…" : "Verify"}
          </Button>
          <Button variant="outline" size="sm" onClick={() => setRestoreOpen((prev) => !prev)} title="Create a new database restored from this backup">
            Restore…
          </Button>
        </div>
        {restoreOpen ? (
          <form
            className="mt-2 flex items-end justify-end gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              restore.mutate({ name: restoreName, engine, restoreFrom: backup.id ?? "" });
            }}
          >
            <div className="flex flex-col gap-1">
              <Label htmlFor={`restore-name-${backup.id}`} className="text-2xs">
                New {engine} database name
              </Label>
              <Input
                id={`restore-name-${backup.id}`}
                className="h-8 w-52 font-mono text-xs"
                value={restoreName}
                onChange={(event) => setRestoreName(event.target.value)}
                placeholder="restored-copy"
                autoFocus
              />
            </div>
            <Button type="submit" size="sm" disabled={restore.pending || restoreName === ""}>
              {restore.pending ? "Creating…" : "Restore"}
            </Button>
          </form>
        ) : null}
      </td>
    </tr>
  );
}

function DatabaseBrowsePanel({
  browse,
  pending,
  failure,
  engine,
}: {
  browse: () => void;
  pending: boolean;
  failure: ApiError | null;
  engine: string | undefined;
}) {
  return (
    <div className="max-w-xl rounded-xl border bg-panel p-5">
      <h3 className="mb-2 flex items-center gap-2 text-sm font-semibold">
        <EyeIcon className="size-4 text-muted" />
        Browse session
      </h3>
      <p className="mb-4 text-xs text-muted">
        Launches a managed web console for {engine ?? "this engine"}. The session runs as a throwaway workload behind a
        one-time launcher ticket (120s); the session itself is capped at 30 minutes (ADR-0051).
      </p>
      <div className="flex items-center gap-3">
        <Button size="sm" disabled={pending} onClick={browse}>
          <ExternalLinkIcon data-icon-start-inline />
          {pending ? "Launching…" : "Launch session"}
        </Button>
        <span className="text-2xs text-muted">read-only dialect</span>
      </div>
      {failure != null ? <p className="mt-3 text-xs text-err">{describeApiError(failure)}</p> : null}
    </div>
  );
}

function DatabaseSettings({
  database,
  projectId,
  projectApps,
  specs,
  onDeleted,
}: {
  database: DatabaseEntry | undefined;
  projectId: string;
  projectApps: ReadonlyArray<{ id: string; name: string }>;
  specs: Map<string, AppSpec>;
  onDeleted: () => void;
}) {
  const id = database?.id ?? "";
  // 级联披露数据源（与 Overview Used-by 同锚）：credentials_ref 扫项目内
  // App 冻结 Spec 的 secret_refs——确认页必须点名引用方（IA v3 §4.2）。
  const usedBy = database?.credentials_ref
    ? (specIndex(specs, (spec) => (spec.processes ?? []).flatMap((process) => process.secret_refs ?? [])).get(database.credentials_ref) ?? [])
        .map((appId) => projectApps.find((app) => app.id === appId)?.name ?? appId)
    : [];
  const ready = database?.status === "running" || database?.status === "degraded";
  // 批2：配置面 delete/rotate 两写位走 kit commands（AlertDialog 壳不动，
  // 反馈 toast 单源）
  const remove = useCommand(databasesResource.commands.delete, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      toast(`Database ${database?.name} deleted`);
      onDeleted();
    }),
  });
  const [rotateOpen, setRotateOpen] = useState(false);
  const rotate = useCommand(databasesResource.commands.rotate, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      setRotateOpen(false); // W-4：成功即关确认框（数据已换、引用方披露已确认）
      toast("Credential rotated — redeploy referencing apps to pick up the new value");
    }),
  });
  return (
    <div className="flex flex-col gap-4">
      <section className="rounded-xl border bg-panel p-5">
        <h3 className="mb-2 text-sm font-semibold">Credential</h3>
        <p className="mb-4 text-xs text-muted">
          The single credential lives in the project secret{" "}
          <span className="font-mono">{database?.credentials_ref ?? "—"}</span> (full connection URL, never displayed). Rotation
          mints a new password; referencing apps must be redeployed to pick it up.
        </p>
        <AlertDialog open={rotateOpen} onOpenChange={setRotateOpen}>
          <AlertDialogTrigger asChild>
            <Button size="sm" disabled={database == null || !ready || rotate.pending}>
              <RefreshCwIcon data-icon-start-inline />
              {rotate.pending ? "Rotating…" : "Rotate password…"}
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Rotate the credential for {database?.name}?</AlertDialogTitle>
              <AlertDialogDescription>
                A new password is minted and the connection URL in the credential secret is rewritten; the old password stops
                working. Apps referencing this database keep the old value until redeployed
                {usedBy.length > 0 ? `: ${usedBy.join(", ")}` : " (none found in this project right now)"}. Carriers re-roll as
                the new materials are published, so expect a brief connection blip. The new URL is never displayed — apps receive
                it via their secret refs.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                disabled={rotate.pending}
                onClick={(event) => {
                  event.preventDefault();
                  rotate.mutate({ id });
                }}
              >
                {rotate.pending ? "Rotating…" : "Rotate"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
        {!ready ? (
          <p className="mt-3 text-2xs text-muted">
            Rotation needs a running database — the data-plane change requires a live target.
          </p>
        ) : null}
      </section>
      <section className="rounded-xl border border-err/30 bg-panel p-5">
        <h3 className="mb-2 text-sm font-semibold text-err">Danger zone</h3>
        <p className="mb-4 text-xs text-muted">
          Deleting <span className="font-semibold text-fg">{database?.name}</span> removes the carrier workload and its
          deploy state. The volume and the credential secret{" "}
          <span className="font-mono">{database?.credentials_ref ?? "—"}</span> are kept and must be cleaned up separately. Data
          is destroyed.
        </p>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="destructive" size="sm" disabled={database == null}>
              <Trash2Icon data-icon-start-inline />
              Delete database…
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete database {database?.name}?</AlertDialogTitle>
              <AlertDialogDescription>
                This permanently destroys the database data. The volume and credential secret are kept for post-mortem cleanup.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                disabled={remove.pending}
                onClick={(event) => {
                  event.preventDefault();
                  remove.mutate({ id });
                }}
              >
                {remove.pending ? "Deleting…" : "Delete"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </section>
    </div>
  );
}
