import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useStore } from "@tanstack/react-form";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection, useCollectionData, useCommand, useCommandForm, useRecord } from "@lynxland/uikit-core";
import { ArchiveIcon, ExternalLinkIcon, MoreHorizontalIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";
import { KnobFields, ResourcesFields } from "@/features/databases/knob-fields";
import { BucketStoreLogsTab } from "@/features/buckets/bucket-logs";
import { BucketStoreSettingsEditor } from "@/features/buckets/bucket-settings";
import { BUCKET_STORE_CARRIER_PRESETS } from "@/features/buckets/bucket-metrics";
import { MetricChart } from "@/features/metrics/metric-chart";
import { useScopedMetricsSeries } from "@/kit/metrics";
import { specIndex } from "@/features/spec/use-app-specs";
import { CopyButton } from "@/components/domain/copy-button";
import { ContentCrumb } from "@/components/domain/content-crumb";
import { EmptyState } from "@/components/domain/empty-state";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { PageFrame } from "@/components/domain/page-frame";
import { PageTabs } from "@/components/domain/page-tabs";
import { BucketSubResourcesTab } from "@/features/buckets/bucket-subresources";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, type StatusTone } from "@/components/domain/status-badge";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { Form } from "@/components/kit/command/form";
import { TextField } from "@/components/kit/command/fields";
import { Select as NativeSelect } from "@/components/kit/ui/native-select";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Label } from "@/components/ui/label";
import { appsResource, appsDirectorySearch } from "@/kit/apps";
import { projectsResource } from "@/kit/projects";
import { appSpecsResource, appSpecsLookup, type AppSpec } from "@/kit/app-specs";
import {
  dataEnginesResource,
  DATA_ENGINES_RECORD_ID,
  DATA_ENGINES_STALE_MS,
  defaultVersionOf,
  versionOptionLabel,
} from "@/kit/data-engines";
import {
  bucketStoresFilterSchema,
  bucketStoresLookup,
  bucketStoresResource,
  bucketStoresSearch,
  type BucketStoreRow,
  type CreateBucketStoreInput,
} from "@/kit/buckets";
import { describeApiError } from "@/kit/errors";
import { toastOnSettled } from "@/kit/toast";

// Buckets 一级页族（B5 姐妹页，ADR-0096 决策 8 / design §6.2「databases 姐妹
// 页，ListPage/详情英雄卡+tab 同形态——一页一壳律」）：列表（NAME/ID/ENGINE·
// VERSION/STATUS/ENDPOINT/CREATED + 行动作 Delete）+ 详情 4-tab（Overview/
// Metrics/Logs/Settings——**无 Backups tab**：silo 转储挂账，目录
// capabilities.backup=false 如实；无 Browse：轻量 S3 web 工具挂账）。创建对
// 话框与 Settings 编辑全目录驱动（Engine Catalog 已含 bucketstore 族；silo
// v1 零旋钮——Settings 面只剩 resources，EngineSettingsEditor 直接复用）；
// Settings 保存走 Update 确认面（PreviewUpdateBucketStore）。

type BucketStoreEntry = BucketStoreRow;

// 删实例后果文案（DeleteBucketStore 语义单源——行删与 Settings Danger zone
// 两确认面共用；DELETE_PUBSUB_CONSEQUENCES 同构）：数据不毁、卷保留（同名
// 重建撞异源卷标记 = 受理 E_CONFLICT，卷源门），凭证 Secret 不随删。
const DELETE_BUCKET_STORE_CONSEQUENCES =
  "Kept: the data volume and the credential secret — nothing on disk is deleted. Recreating a bucket store with the same name hits the volume origin gate (the retained volume still carries the previous instance marker) — clean the runtime-side volume or recreate under a new name.";

// status: pending | running | degraded | stopped（收敛环推进；三族同词面）。
function bucketStoreTone(status: string | undefined): StatusTone {
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

export function BucketsListPage({ projectId, from }: { projectId: string; from: string }) {
  const list = useCollection(bucketStoresResource, {
    from,
    search: bucketStoresSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const rows = list.rows;
  const projectName = useCollectionData(projectsResource, { window: { size: 100 }, refetchInterval: 60_000 }).rows.find(
    (project) => project.id === projectId,
  )?.name;
  const navigate = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);
  // 引擎下拉值域取自 Engine Catalog 的 bucketstore 族（B5 起在册）——
  // FilterBar 用同形 schema 实例，URL 编解码仍走 bucketStoresSearch 常量。
  const catalog = useRecord(dataEnginesResource, {
    id: DATA_ENGINES_RECORD_ID,
    query: { staleTime: DATA_ENGINES_STALE_MS },
  });
  const engineKey = (catalog.data ?? [])
    .filter((entry) => entry.family === "bucketstore")
    .map((entry) => entry.engine ?? "")
    .filter(Boolean)
    .join(",");
  const filterSchema = useMemo(
    () => bucketStoresFilterSchema(engineKey === "" ? [] : engineKey.split(",")),
    [engineKey],
  );

  const columns: LegacyColumnDef<BucketStoreEntry, any>[] = [
    {
      accessorKey: "name",
      header: "Bucket store",
      meta: { sortField: "name" },
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <ProjectAvatar seed={row.original.id ?? ""} label={row.original.name ?? ""} />
          <div className="min-w-0">
            <div className="flex items-center gap-0.5">
              <span className="text-sm font-semibold">{row.original.name}</span>
              <CopyButton value={row.original.name ?? ""} />
            </div>
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
        <StatusBadge tone={bucketStoreTone(row.original.status)}>{row.original.status ?? "unknown"}</StatusBadge>
      ),
    },
    {
      id: "endpoint",
      header: "S3 endpoint",
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
      cell: ({ row }) => <RowActions projectId={projectId} bucketStore={row.original} />,
    },
  ];

  const filtered = list.query.predicate != null;
  const count = list.total ?? rows.length;

  return (
    // 一页一壳律：壳 = PageFrame（衬垫 + 页头三件单源）；kit ListPage 是壳内
    // 模块配方（kit 件零页面 chrome）。
    <PageFrame
      title="Buckets"
      description={
        count > 0
          ? `${count} bucket store${count === 1 ? "" : "s"} in ${projectName ?? "this project"} — S3-compatible bucket stores (no backups; external sync is the durability story)`
          : "Managed S3-compatible bucket stores — no backups; external sync is the durability story"
      }
    >
      <ListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FilterBar list={list} schema={filterSchema} textPlaceholder="Filter bucket stores..." />
            <div className="py-2 pr-3">
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <ArchiveIcon data-icon-start-inline />
                New bucket store
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No bucket stores match the filter" : "No bucket stores"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : "Create a bucket store instance to get a template-rendered S3-compatible storage service."
        }
        emptyAction={
          filtered ? undefined : (
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <ArchiveIcon data-icon-start-inline />
              New bucket store
            </Button>
          )
        }
        appendix={
          <>
            <CliEquivalent command={`fleetly buckets list --project ${projectId}`} />
            <CreateBucketStoreDialog
              projectId={projectId}
              open={createOpen}
              onOpenChange={setCreateOpen}
              onCreated={(bucketStoreId) =>
                navigate({ to: "/p/$projectId/buckets/$bucketId", params: { projectId, bucketId: bucketStoreId } })
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
            navigate({ to: "/p/$projectId/buckets/$bucketId", params: { projectId, bucketId: row.id ?? "" } })
          }
        />
      </ListPage>
    </PageFrame>
  );
}

function RowActions({ projectId, bucketStore }: { projectId: string; bucketStore: BucketStoreEntry }) {
  const navigate = useNavigate();
  const id = bucketStore.id ?? "";
  const [deleting, setDeleting] = useState(false);
  // 写位走 kit commands（invalidates 默认同资源）；反馈走 toast 单源。
  const remove = useCommand(bucketStoresResource.commands.delete, {
    scope: { projectId },
    onSettled: toastOnSettled(() => toast(`Bucket store ${bucketStore.name} deleted`)),
  });
  return (
    <div className="flex items-center justify-end gap-1" onClick={(event) => event.stopPropagation()}>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="size-8">
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            onClick={() => navigate({ to: "/p/$projectId/buckets/$bucketId", params: { projectId, bucketId: id } })}
          >
            <ExternalLinkIcon />
            Open details
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          {/* 删实例 = 级联影响面（数据语义）→ wide；后果段单源
              DELETE_BUCKET_STORE_CONSEQUENCES。 */}
          <DropdownMenuItem
            variant="destructive"
            onSelect={(event) => {
              event.preventDefault();
              setDeleting(true);
            }}
          >
            <Trash2Icon />
            Delete bucket store…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete bucket store ${bucketStore.name}?`}
        description="The carrier workload and its deploy state are removed."
        confirmLabel="Delete"
        pendingLabel="Deleting…"
        risk={{ tier: "wide", consequences: DELETE_BUCKET_STORE_CONSEQUENCES }}
        onConfirm={() => remove.mutateAsync({ id })}
      />
    </div>
  );
}

// New bucket store 对话框（CreatePubSubDialog 同构）：Name + 引擎（目录
// bucketstore 族）+ 版本（Default 置顶）+ 旋钮区（KnobFields 复用——silo
// 零旋钮即空态）+ 资源。零硬编码引擎页面（目录驱动）；无 restore 面
// （无备份族）。

// 引擎必填校验器（模块级单例——TanStack Field validators 变化重置字段状态
// 的 kit fields 教训同款）。
const requireBucketEngine = ({ value }: { value: unknown }): string | undefined =>
  typeof value === "string" && value !== "" ? undefined : "Pick an engine from the engine catalog";

export function CreateBucketStoreDialog({
  projectId,
  open,
  onOpenChange,
  onCreated,
}: {
  projectId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated?: (bucketStoreId: string) => void;
}) {
  const catalog = useRecord(dataEnginesResource, {
    id: DATA_ENGINES_RECORD_ID,
    query: { staleTime: DATA_ENGINES_STALE_MS },
  });
  // 目录 bucketstore 族（B5 起在册：silo；未来 S3 兼容实现同 dialect 互示）。
  const engines = (catalog.data ?? []).filter((entry) => entry.family === "bucketstore");
  const defaults: CreateBucketStoreInput = {
    name: "",
    engine: "",
    version: "",
    settings: {},
    cpuMillis: "",
    memoryMb: "",
  };
  const { form, submitting, result, command } = useCommandForm(bucketStoresResource.commands.create, {
    defaultValues: defaults,
    scope: { projectId },
    onSettled: (settled, failure) => {
      if (settled?.status === "ok") {
        toast("Bucket store created");
        onOpenChange(false);
        form.reset();
        if (settled.payload.id) onCreated?.(settled.payload.id);
      } else if (settled == null && failure != null) {
        toast.error(describeApiError(failure));
      }
    },
  });
  const engineName = useStore(form.store, (state) => state.values.engine);
  const selected = engines.find((entry) => entry.engine === engineName);
  // 版本下拉新→旧（目录声明序旧→新；Default 置顶即默认位——silo 单版）。
  const versions = [...(selected?.versions ?? [])].reverse();
  const deprecated = versions.filter((version) => version.deprecated);
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
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>New bucket store</DialogTitle>
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
          <TextField form={form} name="name" label="Name" placeholder="assets" required />
          <form.Field name="engine" validators={{ onChange: requireBucketEngine, onSubmit: requireBucketEngine }}>
            {(field) => (
              <div className="space-y-1">
                <Label htmlFor="create-bucket-engine" className="text-xs font-medium text-muted">
                  Engine
                </Label>
                <NativeSelect
                  id="create-bucket-engine"
                  value={field.state.value}
                  disabled={catalog.status !== "success"}
                  onChange={(event) => {
                    const next = engines.find((entry) => entry.engine === event.target.value);
                    field.handleChange(event.target.value);
                    form.setFieldValue("version", defaultVersionOf(next));
                    form.setFieldValue("settings", {});
                  }}
                >
                  <option value="" disabled>
                    {catalog.status === "loading" ? "Loading the engine catalog…" : "Select an engine…"}
                  </option>
                  {engines.map((entry) => (
                    <option key={entry.engine} value={entry.engine}>
                      {entry.engine}
                      {entry.dialect && entry.dialect !== entry.engine ? ` (${entry.dialect} dialect)` : ""}
                    </option>
                  ))}
                </NativeSelect>
                {catalog.status === "error" ? (
                  <p className="text-[11px] text-err" role="alert">
                    The engine catalog is unavailable{catalog.error ? ` — ${describeApiError(catalog.error)}` : ""}.
                  </p>
                ) : null}
                {[...new Set(field.state.meta.errors.map((error) => String(error)))].map((message) => (
                  <p key={message} className="text-[11px] text-err" role="alert">
                    {message}
                  </p>
                ))}
                {selected?.license_note ? <p className="text-2xs text-muted">License: {selected.license_note}</p> : null}
              </div>
            )}
          </form.Field>
          {selected != null ? (
            <form.Field name="version">
              {(field) => (
                <div className="space-y-1">
                  <Label htmlFor="create-bucket-version" className="text-xs font-medium text-muted">
                    Version
                  </Label>
                  <NativeSelect
                    id="create-bucket-version"
                    value={field.state.value ?? ""}
                    onChange={(event) => field.handleChange(event.target.value)}
                  >
                    {versions.map((version) => (
                      <option key={version.id} value={version.id} disabled={Boolean(version.deprecated)}>
                        {versionOptionLabel(version)}
                      </option>
                    ))}
                  </NativeSelect>
                  {deprecated.map((version) => (
                    <p key={version.id} className="text-2xs text-muted">
                      {version.id} is deprecated{version.note ? ` — ${version.note}` : ""}. New instances cannot use it.
                    </p>
                  ))}
                </div>
              )}
            </form.Field>
          ) : null}
          {selected != null && (selected.knobs ?? []).length > 0 ? (
            <div className="space-y-1.5">
              <span className="text-xs font-medium text-muted">Engine knobs (optional)</span>
              <div className="max-h-72 overflow-y-auto pr-1">
                <form.Field name="settings">
                  {(field) => (
                    <KnobFields
                      knobs={selected.knobs}
                      values={field.state.value ?? {}}
                      onChange={(next) => field.handleChange(next)}
                    />
                  )}
                </form.Field>
              </div>
            </div>
          ) : null}
          {selected != null ? (
            <div className="space-y-1.5">
              <span className="text-xs font-medium text-muted">Resources (optional)</span>
              <form.Field name="cpuMillis">
                {(cpuField) => (
                  <form.Field name="memoryMb">
                    {(memoryField) => (
                      <ResourcesFields
                        cpuMillis={cpuField.state.value ?? ""}
                        memoryMb={memoryField.state.value ?? ""}
                        onChange={(next) => {
                          cpuField.handleChange(next.cpuMillis);
                          memoryField.handleChange(next.memoryMb);
                        }}
                      />
                    )}
                  </form.Field>
                )}
              </form.Field>
            </div>
          ) : null}
          <p className="text-2xs text-muted">
            No backup face for this family — the engine catalog lists capabilities.backup=false; durability is an external
            sync concern.
          </p>
        </Form>
      </DialogContent>
    </Dialog>
  );
}

// ---- 详情页（4 tabs：Overview/Metrics/Logs/Settings——无 Backups/Browse） ----

export function BucketStoreDetailPage({
  projectId,
  bucketId,
  routeId,
}: {
  projectId: string;
  bucketId: string;
  routeId: string;
}) {
  const bucketStores = useCollection(bucketStoresResource, {
    from: routeId,
    search: bucketStoresLookup,
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
  const bucketStore = bucketStores.rows.find((entry) => entry.id === bucketId);
  const navigate = useNavigate();
  const [tab, setTab] = useState<"overview" | "buckets" | "metrics" | "logs" | "settings">("overview");
  return (
    <PageFrame
      breadcrumb={
        <ContentCrumb
          projectId={projectId}
          section={{ label: "Buckets", to: "/p/$projectId/buckets" }}
          current={bucketStore?.name ?? bucketId}
        />
      }
    >
      {/* 英雄卡（PubSub Detail 同形态：头像/名称/徽标/可读 id）。 */}
      <section className="mb-5 flex flex-wrap items-center gap-4 rounded-xl border bg-panel p-5">
        <ProjectAvatar seed={bucketId} label={bucketStore?.name ?? bucketId} className="size-11 rounded-xl text-base" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="font-heading text-xl font-semibold tracking-tight">{bucketStore?.name ?? bucketId}</h1>
            <StatusBadge tone={bucketStoreTone(bucketStore?.status)}>{bucketStore?.status ?? "unknown"}</StatusBadge>
          </div>
          <div className="flex items-center gap-1 font-mono text-2xs text-muted">
            {bucketId}
            <CopyButton value={bucketId} />
          </div>
        </div>
      </section>

      <PageTabs
        tabs={[
          { value: "overview", label: "Overview" },
          // 子资源 tab（B8，ADR-0096 决策 10）：Bucket 声明行 + 收敛状态。
          { value: "buckets", label: "Buckets" },
          { value: "metrics", label: "Metrics" },
          { value: "logs", label: "Logs" },
          { value: "settings", label: "Settings" },
        ]}
        current={tab}
        onChange={setTab}
      />
      {tab === "overview" ? <BucketStoreOverview bucketStore={bucketStore} projectApps={apps.rows} specs={specs} /> : null}
      {tab === "buckets" ? <BucketSubResourcesTab projectId={projectId} bucketStoreId={bucketId} routeId={routeId} /> : null}
      {tab === "metrics" ? <BucketStoreMetrics bucketStoreId={bucketId} /> : null}
      {tab === "logs" ? <BucketStoreLogsTab bucketStoreId={bucketId} /> : null}
      {tab === "settings" ? (
        <BucketStoreSettings
          bucketStore={bucketStore}
          projectId={projectId}
          projectApps={apps.rows}
          specs={specs}
          onDeleted={() => navigate({ to: "/p/$projectId/buckets", params: { projectId } })}
        />
      ) : null}
    </PageFrame>
  );
}

// usedByApps：反查引用方（双锚）：bucketstore_refs 按名引用（ADR-0096 决策
// 5）+ secret_refs 文件注入通道（值 = k=v 连接材料方言串直用）。锚 = 实例
// 名 / 凭证 Secret 名。
function usedByApps(
  bucketStore: BucketStoreEntry | undefined,
  projectApps: ReadonlyArray<{ id: string; name: string }>,
  specs: Map<string, AppSpec>,
): string[] {
  if (bucketStore == null) return [];
  const byRefs = specIndex(specs, (spec) =>
    (spec.processes ?? []).flatMap((process) => process.bucketstore_refs ?? []),
  ).get(bucketStore.name ?? "");
  const bySecretRefs = specIndex(specs, (spec) => (spec.processes ?? []).flatMap((process) => process.secret_refs ?? [])).get(
    bucketStore.credentials_ref ?? "",
  );
  const appIds = new Set([...(byRefs ?? []), ...(bySecretRefs ?? [])]);
  return [...appIds].map((appId) => projectApps.find((app) => app.id === appId)?.name ?? appId);
}

function BucketStoreOverview({
  bucketStore,
  projectApps,
  specs,
}: {
  bucketStore: BucketStoreEntry | undefined;
  projectApps: ReadonlyArray<{ id: string; name: string }>;
  specs: Map<string, AppSpec>;
}) {
  if (bucketStore == null) {
    return <EmptyState icon={ArchiveIcon} title="Bucket store not found" description="It may have been deleted." />;
  }
  const usedBy = usedByApps(bucketStore, projectApps, specs);
  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Connection</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted">endpoint</dt>
          <dd className="flex items-center gap-1 font-mono">
            {bucketStore.host ? `${bucketStore.host}:${bucketStore.port ?? "?"}` : "—"}
            {bucketStore.host ? <CopyButton value={`${bucketStore.host}:${bucketStore.port ?? ""}`} /> : null}
          </dd>
          <dt className="text-muted">credentials</dt>
          <dd className="flex items-center gap-1 font-mono">
            {bucketStore.credentials_ref ?? "—"}
            {bucketStore.credentials_ref ? <CopyButton value={bucketStore.credentials_ref} /> : null}
          </dd>
        </dl>
        <p className="mt-3 text-2xs text-muted">
          The access/secret key pair lives in the project secret and is never displayed. Apps receive it via
          bucketstore_refs (FLEETLY_BKT_* env) or secret_refs (file mount).
        </p>
      </section>
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Engine</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted">engine</dt>
          <dd className="font-mono">{bucketStore.engine ?? "—"}</dd>
          <dt className="text-muted">version</dt>
          <dd className="font-mono">{bucketStore.version ?? "pinned"}</dd>
          <dt className="text-muted">created</dt>
          <dd>
            <RelativeTime value={bucketStore.created_at} />
          </dd>
          <dt className="text-muted">updated</dt>
          <dd>
            <RelativeTime value={bucketStore.updated_at} />
          </dd>
        </dl>
      </section>
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Used by</h3>
        {usedBy.length === 0 ? (
          <p className="text-xs text-muted">
            No app references this bucket store yet — apps attach via bucketstore_refs (FLEETLY_BKT_* env) on the instance
            name <span className="font-mono">{bucketStore.name ?? "—"}</span>.
          </p>
        ) : (
          <div className="flex flex-col gap-2 text-xs">
            {usedBy.map((appName) => (
              <span key={appName} className="flex items-center gap-2">
                <span className="font-mono">{appName}</span>
                <span className="rounded-full border border-info/30 bg-info/10 px-2 py-0.5 text-2xs text-info">
                  {bucketStore.name}
                </span>
              </span>
            ))}
          </div>
        )}
        <p className="mt-3 text-2xs text-muted">
          Resolved from frozen app specs (bucketstore_refs + secret_refs scan) — live after the next deploy.
        </p>
      </section>
    </div>
  );
}

function BucketStoreMetrics({ bucketStoreId }: { bucketStoreId: string }) {
  const memory = BUCKET_STORE_CARRIER_PRESETS.find((preset) => preset.key === "memory")!;
  const cpu = BUCKET_STORE_CARRIER_PRESETS.find((preset) => preset.key === "cpu_cores")!;
  const memorySeries = useScopedMetricsSeries({ kind: "bucketstore", id: bucketStoreId }, memory.metric, undefined, "1h");
  const cpuSeries = useScopedMetricsSeries({ kind: "bucketstore", id: bucketStoreId }, cpu.metric, undefined, "1h");
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 lg:grid-cols-2">
        <MetricCard title="Memory working set" unit="bytes" series={memorySeries} />
        <MetricCard title="CPU (cores)" unit="plain" series={cpuSeries} />
      </div>
      <p className="text-2xs text-muted">
        Scoped metric templates: the server resolves this bucket store's project and builds the docker/k3s arms — the
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

function BucketStoreSettings({
  bucketStore,
  projectId,
  projectApps,
  specs,
  onDeleted,
}: {
  bucketStore: BucketStoreEntry | undefined;
  projectId: string;
  projectApps: ReadonlyArray<{ id: string; name: string }>;
  specs: Map<string, AppSpec>;
  onDeleted: () => void;
}) {
  const id = bucketStore?.id ?? "";
  const usedBy = usedByApps(bucketStore, projectApps, specs);
  const ready = bucketStore?.status === "running" || bucketStore?.status === "degraded";
  const remove = useCommand(bucketStoresResource.commands.delete, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      toast(`Bucket store ${bucketStore?.name} deleted`);
      onDeleted();
    }),
  });
  const [deleting, setDeleting] = useState(false);
  const [rotateOpen, setRotateOpen] = useState(false);
  const rotate = useCommand(bucketStoresResource.commands.rotate, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      setRotateOpen(false);
      toast("Credential rotated — the carrier rolls with the new key; redeploy referencing apps");
    }),
  });
  return (
    <div className="flex flex-col gap-4">
      {/* 目录驱动资源面（Update 确认面：PreviewUpdateBucketStore——silo v1
          零旋钮，表单只剩 resources；EngineSettingsEditor 三族共用体直接复用）。 */}
      {bucketStore != null ? <BucketStoreSettingsEditor bucketStore={bucketStore} projectId={projectId} /> : null}
      <section className="rounded-xl border bg-panel p-5">
        <h3 className="mb-2 text-sm font-semibold">Credential</h3>
        <p className="mb-4 text-xs text-muted">
          The root access/secret key pair lives in the project secret{" "}
          <span className="font-mono">{bucketStore?.credentials_ref ?? "—"}</span> (values never displayed). Rotation is
          declarative: a new secret key is minted into the credential secret and the carrier rolls to pick it up;
          referencing apps must be redeployed to receive the new key.
        </p>
        <Button size="sm" disabled={bucketStore == null || !ready || rotate.pending} onClick={() => setRotateOpen(true)}>
          <RefreshCwIcon data-icon-start-inline />
          {rotate.pending ? "Rotating…" : "Rotate secret key…"}
        </Button>
        <ConfirmDialog
          open={rotateOpen}
          onOpenChange={setRotateOpen}
          title={`Rotate the credential for ${bucketStore?.name}?`}
          description={
            <>
              A new secret key is minted into the credential secret and the carrier rolls to pick it up, so expect a brief
              connection blip. Apps referencing this bucket store keep the old key until redeployed
              {usedBy.length > 0 ? `: ${usedBy.join(", ")}` : " (none found in this project right now)"}. The new key is
              never displayed — apps receive it via their refs.
            </>
          }
          confirmLabel="Rotate"
          pendingLabel="Rotating…"
          destructive={false}
          risk={{ tier: "standard" }}
          onConfirm={() => rotate.mutateAsync({ id })}
        />
        {!ready ? (
          <p className="mt-3 text-2xs text-muted">Rotation needs a running bucket store — the roll needs a live target.</p>
        ) : null}
      </section>
      <section className="rounded-xl border border-err/30 bg-panel p-5">
        <h3 className="mb-2 text-sm font-semibold text-err">Danger zone</h3>
        <p className="mb-4 text-xs text-muted">
          Deleting <span className="font-semibold text-fg">{bucketStore?.name}</span> removes the carrier workload and its
          deploy state. The volume and the credential secret{" "}
          <span className="font-mono">{bucketStore?.credentials_ref ?? "—"}</span> are kept — nothing on disk is deleted.
          Same-name recreation hits the volume origin gate; clean both up separately to discard the data.
        </p>
        <Button variant="destructive" size="sm" disabled={bucketStore == null} onClick={() => setDeleting(true)}>
          <Trash2Icon data-icon-start-inline />
          Delete bucket store…
        </Button>
        <ConfirmDialog
          open={deleting}
          onOpenChange={setDeleting}
          title={`Delete bucket store ${bucketStore?.name}?`}
          description="This removes the carrier workload and its deploy state."
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          risk={{ tier: "wide", consequences: DELETE_BUCKET_STORE_CONSEQUENCES }}
          onConfirm={() => remove.mutateAsync({ id })}
        />
      </section>
    </div>
  );
}
