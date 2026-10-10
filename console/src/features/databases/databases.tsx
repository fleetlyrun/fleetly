import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
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
import { useDatabases, useDatabaseBackups, useMetricsSeries, useApps, useProjectName } from "@/lib/catalog";
import { useAppSpecs, specIndex, type AppSpec } from "@/features/spec/use-app-specs";
import { CopyButton } from "@/components/domain/copy-button";
import { ContentCrumb } from "@/components/domain/content-crumb";
import { DataTable } from "@/components/domain/data-table";
import { EmptyState } from "@/components/domain/empty-state";
import { CliEquivalent, ListToolbar, useListFilter } from "@/components/domain/list-toolbar";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, statusToneClass, type StatusTone } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
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

// Databases 一级页族（IA v3 T3，docs/design/2026-10-09-console-ia-v3.md §4.2）：
// 列表（备份健康度列）+ 详情 5-tab（Overview/Metrics/Backups/Browse/Settings）。
// Logs tab 按设计文档 §8 T8 结论留二期（StreamLogsRequest 无 database 轴）；
// Metrics 走载体寻址 preset（T8：docker label + k3s namespace/pod 双臂）。

export type DatabaseEntry = components["schemas"]["v1Database"];
type BackupEntry = components["schemas"]["v1Backup"];

const ENGINES = ["postgres", "pgvector", "redis", "mysql", "mongo"] as const;

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

function invalidateDatabases(queryClient: ReturnType<typeof useQueryClient>) {
  void queryClient.invalidateQueries({ queryKey: ["resources", "databases"] });
}

// ---- 列表页 ----

export function DatabasesListPage({ projectId }: { projectId: string }) {
  const databases = useDatabases(projectId);
  const rows = databases.data ?? [];
  const projectName = useProjectName(projectId);
  const navigate = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [engine, setEngine] = useState("all");
  const engines = Array.from(new Set(rows.map((row) => row.engine ?? "").filter(Boolean))).sort();
  const filtered = useListFilter(rows, query, (row: DatabaseEntry) => [row.name ?? "", row.engine ?? "", row.id ?? ""]).filter(
    (row) => engine === "all" || row.engine === engine,
  );

  const columns: LegacyColumnDef<DatabaseEntry, any>[] = [
    {
      accessorKey: "name",
      header: "Database",
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <ProjectAvatar seed={row.original.id ?? ""} label={row.original.name ?? ""} />
          <div className="min-w-0">
            <div className="text-[13px] font-semibold">{row.original.name}</div>
            <div className="flex items-center gap-0.5 font-mono text-[11px] text-muted-foreground">
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
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
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
          <span className="flex items-center gap-1 font-mono text-xs text-muted-foreground">
            {row.original.host}:{row.original.port ?? "?"}
            <CopyButton value={`${row.original.host}:${row.original.port ?? ""}`} />
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        ),
    },
    {
      id: "created",
      header: "Created",
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
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

  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        title="Databases"
        description={
          rows.length > 0
            ? `${rows.length} database${rows.length === 1 ? "" : "s"} in ${projectName ?? "this project"} — scheduled backups with verify & restore (ADR-0039)`
            : "Managed databases — scheduled backups with verify & restore (ADR-0039)"
        }
        actions={
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <DatabaseIcon data-icon-start-inline />
            New database
          </Button>
        }
      />
      <div className="rounded-xl border bg-card">
        <div className="px-3 pt-3">
          <ListToolbar
            label="databases"
            value={query}
            onChange={setQuery}
            placeholder="Filter databases..."
            total={rows.length}
            shown={filtered.length}
          >
            <select
              value={engine}
              onChange={(event) => setEngine(event.target.value)}
              aria-label="Filter by engine"
              className="h-8 rounded-md border bg-muted/40 px-2 text-xs text-foreground"
            >
              <option value="all">All engines</option>
              {engines.map((entry) => (
                <option key={entry} value={entry}>
                  {entry}
                </option>
              ))}
            </select>
          </ListToolbar>
        </div>
      <DataTable
        data={filtered}
        columns={columns}
        loading={databases.isPending}
        error={databases.error}
        onRetry={() => void databases.refetch()}
        onRowClick={(row) => navigate({ to: "/p/$projectId/databases/$databaseId", params: { projectId, databaseId: row.id ?? "" } })}
        empty={
          <EmptyState
            icon={DatabaseIcon}
            title="No databases"
            description="Create a database to get a template-rendered managed service with scheduled backups."
            actionLabel="New database"
            onAction={() => setCreateOpen(true)}
          />
        }
      />
      </div>
      <CliEquivalent command={`fleetly databases list --project ${projectId}`} />

      <CreateDatabaseDialog
        projectId={projectId}
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(databaseId) =>
          navigate({ to: "/p/$projectId/databases/$databaseId", params: { projectId, databaseId } })
        }
      />
    </div>
  );
}

function RowActions({ projectId, database }: { projectId: string; database: DatabaseEntry }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const id = database.id ?? "";
  const backup = useMutation({
    mutationFn: async () => apiSend(`/v1/databases/${encodeURIComponent(id)}/backups`, "POST", {}),
    onSuccess: () => {
      invalidateDatabases(queryClient);
      toast("Backup triggered");
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  const browse = useMutation({
    mutationFn: async () => apiSend<{ url?: string }>(`/v1/databases/${encodeURIComponent(id)}/browse`, "POST", {}),
    onSuccess: (resp) => {
      if (resp?.url) window.open(resp.url, "_blank", "noopener");
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  const remove = useMutation({
    mutationFn: async () => apiSend(`/v1/databases/${encodeURIComponent(id)}`, "DELETE"),
    onSuccess: () => {
      invalidateDatabases(queryClient);
      toast(`Database ${database.name} deleted`);
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <div className="flex items-center justify-end gap-1" onClick={(event) => event.stopPropagation()}>
      <Button variant="outline" size="sm" disabled={backup.isPending} onClick={() => backup.mutate()} title="Trigger an on-demand backup">
        Back up
      </Button>
      <Button
        variant="outline"
        size="sm"
        disabled={browse.isPending}
        onClick={() => browse.mutate()}
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
                  disabled={remove.isPending}
                  onClick={(event) => {
                    event.preventDefault();
                    remove.mutate(undefined, { onSuccess: () => undefined });
                  }}
                >
                  {remove.isPending ? "Deleting…" : "Delete"}
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
// 可选 restore-from-backup；引擎参数配置属二期。
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
  const queryClient = useQueryClient();
  const [form, setForm] = useState({ name: "", engine: "postgres", restoreFrom: "" });
  const create = useMutation({
    mutationFn: async () =>
      apiSend<{ id?: string }>("/v1/databases", "POST", {
        project_id: projectId,
        name: form.name,
        engine: form.engine,
        restore_from_backup: form.restoreFrom || undefined,
      }),
    onSuccess: (resp) => {
      invalidateDatabases(queryClient);
      toast(form.restoreFrom ? "Restore queued — the new database converges from the backup" : "Database created");
      onOpenChange(false);
      setForm({ name: "", engine: "postgres", restoreFrom: "" });
      if (resp?.id) onCreated?.(resp.id);
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!create.isPending) onOpenChange(next);
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New database</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            create.mutate();
          }}
        >
          <div className="flex flex-col gap-2">
            <Label htmlFor="db-name">Name</Label>
            <Input
              id="db-name"
              className="font-mono"
              value={form.name}
              onChange={(event) => setForm({ ...form, name: event.target.value })}
              placeholder="orders-db"
              autoFocus
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="db-engine">Engine</Label>
            <select
              id="db-engine"
              className="h-9 rounded-md border bg-transparent px-3 text-sm shadow-xs"
              value={form.engine}
              onChange={(event) => setForm({ ...form, engine: event.target.value })}
            >
              {ENGINES.map((engine) => (
                <option key={engine} value={engine}>
                  {engine}
                </option>
              ))}
            </select>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="db-restore">Restore from backup (optional)</Label>
            <Input
              id="db-restore"
              className="font-mono"
              value={form.restoreFrom}
              onChange={(event) => setForm({ ...form, restoreFrom: event.target.value })}
              placeholder="bk_01j…"
            />
            <p className="text-[11.5px] text-muted-foreground">
              Restore creates a new database seeded from the backup — the source is never overwritten.
            </p>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending || form.name === ""}>
              {create.isPending ? "Creating…" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---- 详情页（5 tabs） ----

export function DatabaseDetailPage({ projectId, databaseId }: { projectId: string; databaseId: string }) {
  const databases = useDatabases(projectId);
  const apps = useApps(projectId);
  const { specs } = useAppSpecs(projectId, apps.data ?? []);
  const database = (databases.data ?? []).find((entry) => entry.id === databaseId);
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const backup = useMutation({
    mutationFn: async () => apiSend(`/v1/databases/${encodeURIComponent(databaseId)}/backups`, "POST", {}),
    onSuccess: () => {
      invalidateDatabases(queryClient);
      toast("Backup triggered");
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  const browse = useMutation({
    mutationFn: async () => apiSend<{ url?: string }>(`/v1/databases/${encodeURIComponent(databaseId)}/browse`, "POST", {}),
    onSuccess: (resp) => {
      if (resp?.url) window.open(resp.url, "_blank", "noopener");
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        breadcrumb={
          <ContentCrumb
            projectId={projectId}
            section={{ label: "Databases", to: "/p/$projectId/databases" }}
            current={database?.name ?? databaseId}
          />
        }
        title={
          <span className="flex items-center gap-3">
            <ProjectAvatar seed={databaseId} label={database?.name ?? databaseId} className="size-8 rounded-xl text-sm" />
            {database?.name ?? databaseId}
            <StatusBadge tone={databaseTone(database?.status)}>{database?.status ?? "unknown"}</StatusBadge>
            {database?.restore_from_backup ? (
              <StatusBadge tone="info" pulse>
                restoring
              </StatusBadge>
            ) : null}
          </span>
        }
        description={
          <span className="flex items-center gap-1 font-mono text-[11.5px]">
            {databaseId}
            <CopyButton value={databaseId} />
          </span>
        }
        actions={
          <>
            <Button size="sm" disabled={backup.isPending} onClick={() => backup.mutate()}>
              <HardDriveDownloadIcon data-icon-start-inline />
              Back up now
            </Button>
            <Button size="sm" variant="outline" disabled={browse.isPending} onClick={() => browse.mutate()}>
              <EyeIcon data-icon-start-inline />
              Browse
            </Button>
          </>
        }
      />

      <Tabs defaultValue="overview">
        <TabsList className="mb-4">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="metrics">Metrics</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
          <TabsTrigger value="backups">Backups</TabsTrigger>
          <TabsTrigger value="browse">Browse</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="overview">
          <DatabaseOverview database={database} projectApps={apps.data ?? []} specs={specs} />
        </TabsContent>
        <TabsContent value="metrics">
          <DatabaseMetrics databaseId={databaseId} projectId={projectId} />
        </TabsContent>
        <TabsContent value="logs">
          <DatabaseLogsTab databaseId={databaseId} />
        </TabsContent>
        <TabsContent value="backups">
          <DatabaseBackups database={database} projectId={projectId} databaseId={databaseId} />
        </TabsContent>
        <TabsContent value="browse">
          <DatabaseBrowsePanel browse={() => browse.mutate()} pending={browse.isPending} error={browse.error} engine={database?.engine} />
        </TabsContent>
        <TabsContent value="settings">
          <DatabaseSettings
            database={database}
            projectApps={apps.data ?? []}
            specs={specs}
            onDeleted={() => navigate({ to: "/p/$projectId/databases", params: { projectId } })}
          />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function DatabaseOverview({
  database,
  projectApps,
  specs,
}: {
  database: DatabaseEntry | undefined;
  projectApps: Array<{ id: string; name: string }>;
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
      <section className="rounded-xl border bg-card p-4">
        <h3 className="mb-3 text-[13px] font-semibold">Connection</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted-foreground">host</dt>
          <dd className="flex items-center gap-1 font-mono">
            {database.host ?? "—"}
            {database.host ? <CopyButton value={database.host} /> : null}
          </dd>
          <dt className="text-muted-foreground">port</dt>
          <dd className="font-mono">{database.port ?? "—"}</dd>
          <dt className="text-muted-foreground">credential</dt>
          <dd className="flex items-center gap-1 font-mono">
            {database.credentials_ref ?? "—"}
            {database.credentials_ref ? <CopyButton value={database.credentials_ref} /> : null}
          </dd>
        </dl>
        <p className="mt-3 text-[11.5px] text-muted-foreground">
          The full connection URL lives in the project secret and is never displayed. Apps receive it via secret_refs.
        </p>
      </section>
      <section className="rounded-xl border bg-card p-4">
        <h3 className="mb-3 text-[13px] font-semibold">Engine</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted-foreground">engine</dt>
          <dd className="font-mono">{database.engine ?? "—"}</dd>
          <dt className="text-muted-foreground">version</dt>
          <dd className="font-mono">{database.version ?? "pinned"}</dd>
          <dt className="text-muted-foreground">created</dt>
          <dd>
            <RelativeTime value={database.created_at} />
          </dd>
          <dt className="text-muted-foreground">updated</dt>
          <dd>
            <RelativeTime value={database.updated_at} />
          </dd>
        </dl>
      </section>
      <section className="rounded-xl border bg-card p-4">
        <h3 className="mb-3 text-[13px] font-semibold">Used by</h3>
        {usedBy.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            No app references this database yet — apps attach via secret_refs on the credential secret{" "}
            <span className="font-mono">{database.credentials_ref ?? "—"}</span>.
          </p>
        ) : (
          <div className="flex flex-col gap-2 text-xs">
            {usedBy.map((appName) => (
              <span key={appName} className="flex items-center gap-2">
                <span className="font-mono">{appName}</span>
                <span className="rounded-full border border-info/30 bg-info/10 px-2 py-0.5 text-[10.5px] text-info">
                  {database.credentials_ref}
                </span>
              </span>
            ))}
          </div>
        )}
        <p className="mt-3 text-[11.5px] text-muted-foreground">
          Resolved from frozen app specs (secret_refs scan) — live after the next deploy.
        </p>
      </section>
      <section className="rounded-xl border bg-card p-4">
        <h3 className="mb-3 text-[13px] font-semibold">Backup</h3>
        <div className="mb-2 flex items-center gap-2">
          <span className={`inline-flex items-center gap-1.5 text-xs font-medium ${statusToneClass(health.tone)}`}>
            <span className="inline-block size-1.5 rounded-full bg-current" />
            {health.label}
          </span>
        </div>
        <p className="text-xs text-muted-foreground">
          Last successful backup:{" "}
          {database.last_backup_at ? <RelativeTime value={database.last_backup_at} /> : "never"}
        </p>
        {database.restore_from_backup ? (
          <div className="mt-3 rounded-md border border-info/30 bg-info/10 p-2.5 text-xs">
            <p className="font-medium">Restore in progress</p>
            <p className="mt-1 font-mono text-[11px] text-muted-foreground">from {database.restore_from_backup}</p>
            {database.restore_error ? (
              <p className="mt-1 text-[11px] text-destructive">last error: {database.restore_error}</p>
            ) : null}
          </div>
        ) : null}
      </section>
    </div>
  );
}

function DatabaseMetrics({ databaseId, projectId }: { databaseId: string; projectId: string }) {
  const memory = DATABASE_CARRIER_PRESETS.find((preset) => preset.key === "memory")!;
  const cpu = DATABASE_CARRIER_PRESETS.find((preset) => preset.key === "cpu_cores")!;
  const memorySeries = useMetricsSeries(memory.build(databaseId, projectId), "1h");
  const cpuSeries = useMetricsSeries(cpu.build(databaseId, projectId), "1h");
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 lg:grid-cols-2">
        <MetricCard title="Memory working set" unit="bytes" series={memorySeries} />
        <MetricCard title="CPU (cores)" unit="plain" series={cpuSeries} />
      </div>
      <p className="text-[11.5px] text-muted-foreground">
        Carrier addressing (IA v3 T8): docker label <span className="font-mono">fleetly_workload_id</span> with a k3s
        namespace/pod fallback arm — both runtimes covered by one query.
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
  series: ReturnType<typeof useMetricsSeries>;
}) {
  return (
    <section className="rounded-xl border bg-card p-4">
      <div className="mb-2 flex items-baseline justify-between">
        <h3 className="text-[13px] font-semibold">{title}</h3>
        <span className="text-[11.5px] text-muted-foreground">window 1h · refreshed 30s</span>
      </div>
      {series.isPending ? (
        <p className="py-10 text-center text-xs text-muted-foreground">Loading series…</p>
      ) : series.isError ? (
        <p className="py-10 text-center text-xs text-destructive">{fieldError(series.error)}</p>
      ) : (series.data ?? []).length === 0 ? (
        <p className="py-10 text-center text-xs text-muted-foreground">No series — the carrier may be stopped.</p>
      ) : (
        <MetricChart seriesList={series.data ?? []} unit={unit} />
      )}
    </section>
  );
}

function DatabaseBackups({
  database,
  projectId,
  databaseId,
}: {
  database: DatabaseEntry | undefined;
  projectId: string;
  databaseId: string;
}) {
  const backups = useDatabaseBackups(databaseId);
  const queryClient = useQueryClient();
  const rows = backups.data ?? [];
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-3 sm:grid-cols-3">
        <InfoTile label="Retention" value="rolling (platform)" />
        <InfoTile label="Backend" value="local object store" hint="same-node — not disaster recovery" />
        <InfoTile label="Schedule" value="platform backup window" />
      </div>
      <div className="overflow-hidden rounded-xl border">
        <table className="w-full text-sm">
          <thead className="border-b bg-muted/40 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
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
            {backups.isPending ? (
              <tr>
                <td colSpan={6} className="px-3 py-6 text-center text-xs text-muted-foreground">
                  Loading backups…
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr>
                <td colSpan={6} className="px-3 py-6 text-center text-xs text-muted-foreground">
                  No backups yet — trigger one with “Back up now”.
                </td>
              </tr>
            ) : (
              rows.map((backup) => <BackupRow key={backup.id} backup={backup} projectId={projectId} engine={database?.engine ?? ""} queryClient={queryClient} />)
            )}
          </tbody>
        </table>
      </div>
      <p className="text-[11.5px] text-muted-foreground">
        Restore always creates a new database — the source is never overwritten (ADR-0039).
      </p>
    </div>
  );
}

function InfoTile({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-xl border bg-card p-3.5">
      <div className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">{label}</div>
      <div className="mt-1 text-sm font-semibold">{value}</div>
      {hint ? <div className="mt-0.5 text-[11px] text-muted-foreground">{hint}</div> : null}
    </div>
  );
}

function BackupRow({
  backup,
  projectId,
  engine,
  queryClient,
}: {
  backup: BackupEntry;
  projectId: string;
  engine: string;
  queryClient: ReturnType<typeof useQueryClient>;
}) {
  const [verifying, setVerifying] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [verifyResult, setVerifyResult] = useState<{ ok: boolean; detail: string } | null>(null);
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [restoreName, setRestoreName] = useState("");
  const restore = useMutation({
    mutationFn: async () =>
      apiSend("/v1/databases", "POST", {
        project_id: projectId,
        name: restoreName,
        engine,
        restore_from_backup: backup.id,
      }),
    onSuccess: () => {
      invalidateDatabases(queryClient);
      toast("Restore queued — the new database converges from the backup");
      setRestoreOpen(false);
      setRestoreName("");
    },
    onError: (cause) => toast.error(fieldError(cause)),
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
        {backup.error ? <div className="mt-1 max-w-56 truncate text-[11px] text-destructive" title={backup.error}>{backup.error}</div> : null}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
        {backup.size_bytes ? `${(Number(backup.size_bytes) / 1048576).toFixed(2)} MiB` : "—"}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground" title={backup.digest}>
        {backup.digest ? `${backup.digest.slice(0, 16)}…` : "—"}
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">
        <RelativeTime value={backup.finished_at ?? backup.created_at} />
      </td>
      <td className="px-3 py-2">
        <div className="flex items-center justify-end gap-1.5">
          {verifyResult != null ? (
            <span className={`text-[11px] ${verifyResult.ok ? "text-emerald-600 dark:text-emerald-400" : "text-destructive"}`}>
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
              restore.mutate();
            }}
          >
            <div className="flex flex-col gap-1">
              <Label htmlFor={`restore-name-${backup.id}`} className="text-[11px]">
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
            <Button type="submit" size="sm" disabled={restore.isPending || restoreName === ""}>
              {restore.isPending ? "Creating…" : "Restore"}
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
  error,
  engine,
}: {
  browse: () => void;
  pending: boolean;
  error: unknown;
  engine: string | undefined;
}) {
  return (
    <div className="max-w-xl rounded-xl border bg-card p-5">
      <h3 className="mb-2 flex items-center gap-2 text-[13px] font-semibold">
        <EyeIcon className="size-4 text-muted-foreground" />
        Browse session
      </h3>
      <p className="mb-4 text-xs text-muted-foreground">
        Launches a managed web console for {engine ?? "this engine"}. The session runs as a throwaway workload behind a
        one-time launcher ticket (120s); the session itself is capped at 30 minutes (ADR-0051).
      </p>
      <div className="flex items-center gap-3">
        <Button size="sm" disabled={pending} onClick={browse}>
          <ExternalLinkIcon data-icon-start-inline />
          {pending ? "Launching…" : "Launch session"}
        </Button>
        <span className="text-[11.5px] text-muted-foreground">read-only dialect</span>
      </div>
      {error != null ? <p className="mt-3 text-xs text-destructive">{fieldError(error)}</p> : null}
    </div>
  );
}

function DatabaseSettings({
  database,
  projectApps,
  specs,
  onDeleted,
}: {
  database: DatabaseEntry | undefined;
  projectApps: Array<{ id: string; name: string }>;
  specs: Map<string, AppSpec>;
  onDeleted: () => void;
}) {
  const queryClient = useQueryClient();
  const id = database?.id ?? "";
  // 级联披露数据源（与 Overview Used-by 同锚）：credentials_ref 扫项目内
  // App 冻结 Spec 的 secret_refs——确认页必须点名引用方（IA v3 §4.2）。
  const usedBy = database?.credentials_ref
    ? (specIndex(specs, (spec) => (spec.processes ?? []).flatMap((process) => process.secret_refs ?? [])).get(database.credentials_ref) ?? [])
        .map((appId) => projectApps.find((app) => app.id === appId)?.name ?? appId)
    : [];
  const ready = database?.status === "running" || database?.status === "degraded";
  const remove = useMutation({
    mutationFn: async () => apiSend(`/v1/databases/${encodeURIComponent(id)}`, "DELETE"),
    onSuccess: () => {
      invalidateDatabases(queryClient);
      toast(`Database ${database?.name} deleted`);
      onDeleted();
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  const [rotateOpen, setRotateOpen] = useState(false);
  const rotate = useMutation({
    mutationFn: async () => apiSend(`/v1/databases/${encodeURIComponent(id)}/rotate-password`, "POST", {}),
    onSuccess: () => {
      invalidateDatabases(queryClient);
      setRotateOpen(false); // W-4：成功即关确认框（数据已换、引用方披露已确认）
      toast("Credential rotated — redeploy referencing apps to pick up the new value");
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <div className="flex flex-col gap-4">
      <section className="max-w-2xl rounded-xl border bg-card p-5">
        <h3 className="mb-2 text-[13px] font-semibold">Credential</h3>
        <p className="mb-4 text-xs text-muted-foreground">
          The single credential lives in the project secret{" "}
          <span className="font-mono">{database?.credentials_ref ?? "—"}</span> (full connection URL, never displayed). Rotation
          mints a new password; referencing apps must be redeployed to pick it up.
        </p>
        <AlertDialog open={rotateOpen} onOpenChange={setRotateOpen}>
          <AlertDialogTrigger asChild>
            <Button size="sm" disabled={database == null || !ready || rotate.isPending}>
              <RefreshCwIcon data-icon-start-inline />
              {rotate.isPending ? "Rotating…" : "Rotate password…"}
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
                disabled={rotate.isPending}
                onClick={(event) => {
                  event.preventDefault();
                  rotate.mutate();
                }}
              >
                {rotate.isPending ? "Rotating…" : "Rotate"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
        {!ready ? (
          <p className="mt-3 text-[11.5px] text-muted-foreground">
            Rotation needs a running database — the data-plane change requires a live target.
          </p>
        ) : null}
      </section>
      <section className="max-w-2xl rounded-xl border border-destructive/30 bg-card p-5">
        <h3 className="mb-2 text-[13px] font-semibold text-destructive">Danger zone</h3>
        <p className="mb-4 text-xs text-muted-foreground">
          Deleting <span className="font-semibold text-foreground">{database?.name}</span> removes the carrier workload and its
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
                disabled={remove.isPending}
                onClick={(event) => {
                  event.preventDefault();
                  remove.mutate();
                }}
              >
                {remove.isPending ? "Deleting…" : "Delete"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </section>
    </div>
  );
}
