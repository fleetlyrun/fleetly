import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useStore } from "@tanstack/react-form";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection, useCollectionData, useCommand, useCommandForm, useRecord } from "@lynxland/uikit-core";
import type { components } from "@/api/structure";
import { MoreHorizontalIcon, HardDriveDownloadIcon, ExternalLinkIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";
import { apiSend } from "@/api/client";
import { downloadPubSubBackupChunks } from "@/api/streams";
import { backupHealth } from "@/features/databases/backup-health";
import { KnobFields, ResourcesFields } from "@/features/databases/knob-fields";
import { PubSubLogsTab } from "@/features/pubsubs/pubsub-logs";
import { PubSubSubResourcesTab, subresourceKindOf } from "@/features/pubsubs/pubsub-subresources";
import { PubSubSettingsEditor } from "@/features/pubsubs/pubsub-settings";
import { PUBSUB_CARRIER_PRESETS } from "@/features/pubsubs/pubsub-metrics";
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
import { Input } from "@/components/ui/input";
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
  pubsubBackupListLookup,
  pubsubBackupListResource,
  pubsubsFilterSchema,
  pubsubsLookup,
  pubsubsResource,
  pubsubsSearch,
  type CreatePubSubInput,
  type PubSubRow,
} from "@/kit/pubsubs";
import { describeApiError } from "@/kit/errors";
import { toastOnSettled } from "@/kit/toast";

// PubSubs 一级页族（B4c 姐妹页，ADR-0096 决策 8 / design §6.2「databases
// 姐妹页，ListPage/详情英雄卡+tab 同形态——一页一壳律」）：列表（NAME/ID/
// ENGINE·VERSION/STATUS/ENDPOINT/CREATED + 行动作 Backup/Delete）+ 详情
// 5-tab（Overview/Metrics/Logs/Backups/Settings——无 Browse：nats 无轻量
// web 工具，ADR-0096 决策 4 挂账）。创建对话框与 Settings 编辑全目录驱动
// （Engine Catalog 已含 pubsub 族）；Settings 保存走 Update 确认面
// （PreviewUpdatePubSub——本族首批旋钮全 static，恒「将触发滚动重启」披露）。

type PubSubEntry = PubSubRow;
type BackupEntry = components["schemas"]["v1Backup"];

// 删实例后果文案（DeletePubSub 语义单源——行删与 Settings Danger zone 两
// 确认面共用；DELETE_DATABASE_CONSEQUENCES 同构）：数据不毁、卷保留（同名
// 重建撞异源卷标记 = 受理 E_CONFLICT，B4c 卷源门），凭证 Secret 不随删。
const DELETE_PUBSUB_CONSEQUENCES =
  "Kept: the data volume and the credential secret — nothing on disk is deleted. Recreating a pubsub with the same name hits the volume origin gate (the retained volume still carries the previous instance marker) — clean the runtime-side volume or recreate under a new name (optionally restoring from a backup).";

// status: pending | running | degraded | stopped（收敛环推进；database 族同词面）。
function pubsubTone(status: string | undefined): StatusTone {
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

export function PubSubsListPage({ projectId, from }: { projectId: string; from: string }) {
  const list = useCollection(pubsubsResource, {
    from,
    search: pubsubsSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const rows = list.rows;
  const projectName = useCollectionData(projectsResource, { window: { size: 100 }, refetchInterval: 60_000 }).rows.find(
    (project) => project.id === projectId,
  )?.name;
  const navigate = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);
  // 引擎下拉值域取自 Engine Catalog 的 pubsub 族（B4b 起在册）——FilterBar
  // 用同形 schema 实例，URL 编解码仍走 pubsubsSearch 常量。
  const catalog = useRecord(dataEnginesResource, {
    id: DATA_ENGINES_RECORD_ID,
    query: { staleTime: DATA_ENGINES_STALE_MS },
  });
  const engineKey = (catalog.data ?? [])
    .filter((entry) => entry.kind === "pubsub")
    .map((entry) => entry.engine ?? "")
    .filter(Boolean)
    .join(",");
  const filterSchema = useMemo(() => pubsubsFilterSchema(engineKey === "" ? [] : engineKey.split(",")), [engineKey]);

  const columns: LegacyColumnDef<PubSubEntry, any>[] = [
    {
      accessorKey: "name",
      header: "PubSub",
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
        <div className="flex items-center gap-1.5">
          <StatusBadge tone={pubsubTone(row.original.status)}>{row.original.status ?? "unknown"}</StatusBadge>
          {row.original.restore_from_backup ? (
            <StatusBadge tone="info" pulse>
              restoring
            </StatusBadge>
          ) : null}
        </div>
      ),
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
      cell: ({ row }) => <RowActions projectId={projectId} pubsub={row.original} />,
    },
  ];

  const filtered = list.query.predicate != null;
  const count = list.total ?? rows.length;

  return (
    // 一页一壳律：壳 = PageFrame（衬垫 + 页头三件单源）；kit ListPage 是壳内
    // 模块配方（kit 件零页面 chrome）。
    <PageFrame
      title="Pub/Sub"
      description={
        count > 0
          ? `${count} pubsub instance${count === 1 ? "" : "s"} in ${projectName ?? "this project"} — per-stream backups with verify & restore`
          : "Managed pub/sub instances — per-stream backups with verify & restore"
      }
    >
      <ListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FilterBar list={list} schema={filterSchema} textPlaceholder="Filter pubsubs..." />
            <div className="py-2 pr-3">
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <HardDriveDownloadIcon data-icon-start-inline />
                New pubsub
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No pubsubs match the filter" : "No pubsubs"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : "Create a pubsub instance to get a template-rendered managed messaging service with per-stream backups."
        }
        emptyAction={
          filtered ? undefined : (
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <HardDriveDownloadIcon data-icon-start-inline />
              New pubsub
            </Button>
          )
        }
        appendix={
          <>
            <CliEquivalent command={`fleetly pubsubs list --project ${projectId}`} />
            <CreatePubSubDialog
              projectId={projectId}
              open={createOpen}
              onOpenChange={setCreateOpen}
              onCreated={(pubsubId) =>
                navigate({ to: "/p/$projectId/pubsubs/$pubsubId", params: { projectId, pubsubId } })
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
            navigate({ to: "/p/$projectId/pubsubs/$pubsubId", params: { projectId, pubsubId: row.id ?? "" } })
          }
        />
      </ListPage>
    </PageFrame>
  );
}

function RowActions({ projectId, pubsub }: { projectId: string; pubsub: PubSubEntry }) {
  const navigate = useNavigate();
  const id = pubsub.id ?? "";
  const [deleting, setDeleting] = useState(false);
  // 三写位走 kit commands（invalidates 默认同资源）；反馈走 toast 单源。
  const backup = useCommand(pubsubsResource.commands.backup, {
    scope: { projectId },
    onSettled: toastOnSettled(() => toast("Backup triggered")),
  });
  const remove = useCommand(pubsubsResource.commands.delete, {
    scope: { projectId },
    onSettled: toastOnSettled(() => toast(`PubSub ${pubsub.name} deleted`)),
  });
  return (
    <div className="flex items-center justify-end gap-1" onClick={(event) => event.stopPropagation()}>
      <Button
        variant="outline"
        size="sm"
        disabled={backup.pending}
        onClick={() => backup.mutate({ id })}
        title="Trigger an on-demand per-stream backup"
      >
        Back up
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="size-8">
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            onClick={() => navigate({ to: "/p/$projectId/pubsubs/$pubsubId", params: { projectId, pubsubId: id } })}
          >
            <ExternalLinkIcon />
            Open details
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          {/* 删实例 = 级联影响面（数据语义）→ wide；后果段单源
              DELETE_PUBSUB_CONSEQUENCES。 */}
          <DropdownMenuItem
            variant="destructive"
            onSelect={(event) => {
              event.preventDefault();
              setDeleting(true);
            }}
          >
            <Trash2Icon />
            Delete pubsub…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete pubsub ${pubsub.name}?`}
        description="The carrier workload and its deploy state are removed."
        confirmLabel="Delete"
        pendingLabel="Deleting…"
        risk={{ tier: "wide", consequences: DELETE_PUBSUB_CONSEQUENCES }}
        onConfirm={() => remove.mutateAsync({ id })}
      />
    </div>
  );
}

// New pubsub 对话框（CreateDatabaseDialog 同构）：Name + 引擎（目录 pubsub
// 族）+ 版本（Default 置顶）+ 旋钮区（KnobFields 复用）+ 资源 + 可选
// restore-from-backup。零硬编码引擎页面（目录驱动）。

// 引擎必填校验器（模块级单例——TanStack Field validators 变化重置字段状态
// 的 kit fields 教训同款）。
const requirePubSubEngine = ({ value }: { value: unknown }): string | undefined =>
  typeof value === "string" && value !== "" ? undefined : "Pick an engine from the engine catalog";

export function CreatePubSubDialog({
  projectId,
  open,
  onOpenChange,
  onCreated,
}: {
  projectId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated?: (pubsubId: string) => void;
}) {
  const catalog = useRecord(dataEnginesResource, {
    id: DATA_ENGINES_RECORD_ID,
    query: { staleTime: DATA_ENGINES_STALE_MS },
  });
  // 目录 pubsub 族（B4b 起在册：nats；kafka/redpanda 随 B6）。
  const engines = (catalog.data ?? []).filter((entry) => entry.kind === "pubsub");
  const defaults: CreatePubSubInput = {
    name: "",
    engine: "",
    version: "",
    restoreFrom: "",
    settings: {},
    cpuMillis: "",
    memoryMb: "",
  };
  const { form, submitting, result, command } = useCommandForm(pubsubsResource.commands.create, {
    defaultValues: defaults,
    scope: { projectId },
    onSettled: (settled, failure) => {
      if (settled?.status === "ok") {
        const restored = (form.state.values.restoreFrom ?? "") !== "";
        toast(restored ? "Restore queued — the new pubsub converges from the backup" : "PubSub created");
        onOpenChange(false);
        form.reset();
        if (settled.payload.id) onCreated?.(settled.payload.id);
      } else if (settled == null && failure != null) {
        toast.error(describeApiError(failure));
      }
    },
  });
  const engineName = useStore(form.store, (state) => state.values.engine);
  const restoreFrom = useStore(form.store, (state) => state.values.restoreFrom ?? "");
  const selected = engines.find((entry) => entry.engine === engineName);
  // 版本下拉新→旧（目录声明序旧→新；Default 置顶即默认位）。
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
          <DialogTitle>New pubsub</DialogTitle>
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
          <TextField form={form} name="name" label="Name" placeholder="events-bus" required />
          <form.Field name="engine" validators={{ onChange: requirePubSubEngine, onSubmit: requirePubSubEngine }}>
            {(field) => (
              <div className="space-y-1">
                <Label htmlFor="create-pubsub-engine" className="text-xs font-medium text-muted">
                  Engine
                </Label>
                <NativeSelect
                  id="create-pubsub-engine"
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
                  <Label htmlFor="create-pubsub-version" className="text-xs font-medium text-muted">
                    Version
                  </Label>
                  <NativeSelect
                    id="create-pubsub-version"
                    value={field.state.value ?? ""}
                    onChange={(event) => field.handleChange(event.target.value)}
                  >
                    {versions.map((version) => (
                      <option
                        key={version.id}
                        value={version.id}
                        disabled={Boolean(version.deprecated) && restoreFrom === ""}
                      >
                        {versionOptionLabel(version)}
                      </option>
                    ))}
                  </NativeSelect>
                  {deprecated.map((version) => (
                    <p key={version.id} className="text-2xs text-muted">
                      {version.id} is deprecated{version.note ? ` — ${version.note}` : ""}. New instances cannot use it;
                      a same-version restore from a backup still can.
                    </p>
                  ))}
                </div>
              )}
            </form.Field>
          ) : null}
          {selected != null ? (
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
          <TextField form={form} name="restoreFrom" label="Restore from backup (optional)" placeholder="bk_01j…" />
          <p className="text-2xs text-muted">
            Restore creates a new pubsub seeded from the backup's streams — the source is never overwritten (memory-storage
            streams are refused at backup time).
          </p>
        </Form>
      </DialogContent>
    </Dialog>
  );
}

// ---- 详情页（6 tabs：Overview/Topics|Streams/Metrics/Logs/Backups/Settings——
// 无 Browse；B8 子资源 tab 按 engine 方言分面） ----

export function PubSubDetailPage({
  projectId,
  pubsubId,
  routeId,
}: {
  projectId: string;
  pubsubId: string;
  routeId: string;
}) {
  const pubsubs = useCollection(pubsubsResource, {
    from: routeId,
    search: pubsubsLookup,
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
  const pubsub = pubsubs.rows.find((entry) => entry.id === pubsubId);
  const navigate = useNavigate();
  const [tab, setTab] = useState<"overview" | "subresources" | "metrics" | "logs" | "backups" | "settings">("overview");
  const backup = useCommand(pubsubsResource.commands.backup, {
    scope: { projectId },
    onSettled: toastOnSettled(() => toast("Backup triggered")),
  });
  return (
    <PageFrame
      breadcrumb={
        <ContentCrumb
          projectId={projectId}
          section={{ label: "Pub/Sub", to: "/p/$projectId/pubsubs" }}
          current={pubsub?.name ?? pubsubId}
        />
      }
    >
      {/* 英雄卡（Database Detail 同形态：头像/名称/徽标/可读 id/动作组）。 */}
      <section className="mb-5 flex flex-wrap items-center gap-4 rounded-xl border bg-panel p-5">
        <ProjectAvatar seed={pubsubId} label={pubsub?.name ?? pubsubId} className="size-11 rounded-xl text-base" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="font-heading text-xl font-semibold tracking-tight">{pubsub?.name ?? pubsubId}</h1>
            <StatusBadge tone={pubsubTone(pubsub?.status)}>{pubsub?.status ?? "unknown"}</StatusBadge>
            {pubsub?.restore_from_backup ? (
              <StatusBadge tone="info" pulse>
                restoring
              </StatusBadge>
            ) : null}
          </div>
          <div className="flex items-center gap-1 font-mono text-2xs text-muted">
            {pubsubId}
            <CopyButton value={pubsubId} />
          </div>
        </div>
        <div className="ml-auto flex flex-none items-center gap-2">
          <Button size="sm" disabled={backup.pending} onClick={() => backup.mutate({ id: pubsubId })}>
            <HardDriveDownloadIcon data-icon-start-inline />
            Back up now
          </Button>
        </div>
      </section>

      <PageTabs
        tabs={[
          { value: "overview", label: "Overview" },
          // 子资源 tab（B8，ADR-0096 决策 10）：engine 决定面——kafka 协议族
          // → Topics；nats → Streams（声明行 + 收敛状态）。
          { value: "subresources", label: subresourceKindOf(pubsub?.engine) === "stream" ? "Streams" : "Topics" },
          { value: "metrics", label: "Metrics" },
          { value: "logs", label: "Logs" },
          { value: "backups", label: "Backups" },
          { value: "settings", label: "Settings" },
        ]}
        current={tab}
        onChange={setTab}
      />
      {tab === "overview" ? <PubSubOverview pubsub={pubsub} projectApps={apps.rows} specs={specs} /> : null}
      {tab === "subresources" ? (
        <PubSubSubResourcesTab projectId={projectId} pubsubId={pubsubId} engine={pubsub?.engine} routeId={routeId} />
      ) : null}
      {tab === "metrics" ? <PubSubMetrics pubsubId={pubsubId} /> : null}
      {tab === "logs" ? <PubSubLogsTab pubsubId={pubsubId} /> : null}
      {tab === "backups" ? (
        <PubSubBackups pubsub={pubsub} projectId={projectId} pubsubId={pubsubId} routeId={routeId} />
      ) : null}
      {tab === "settings" ? (
        <PubSubSettings
          pubsub={pubsub}
          projectId={projectId}
          projectApps={apps.rows}
          specs={specs}
          onDeleted={() => navigate({ to: "/p/$projectId/pubsubs", params: { projectId } })}
        />
      ) : null}
    </PageFrame>
  );
}

// usedByApps：反查引用方（双锚）：pubsub_refs 按名引用（ADR-0096 决策 5）+
// secret_refs 文件注入通道（值 = 连接材料方言串直用）。锚 = 实例名 / 凭证
// Secret 名。
function usedByApps(
  pubsub: PubSubEntry | undefined,
  projectApps: ReadonlyArray<{ id: string; name: string }>,
  specs: Map<string, AppSpec>,
): string[] {
  if (pubsub == null) return [];
  const byPubSubRefs = specIndex(specs, (spec) => (spec.processes ?? []).flatMap((process) => process.pubsub_refs ?? [])).get(
    pubsub.name ?? "",
  );
  const bySecretRefs = specIndex(specs, (spec) => (spec.processes ?? []).flatMap((process) => process.secret_refs ?? [])).get(
    pubsub.credentials_ref ?? "",
  );
  const appIds = new Set([...(byPubSubRefs ?? []), ...(bySecretRefs ?? [])]);
  return [...appIds].map((appId) => projectApps.find((app) => app.id === appId)?.name ?? appId);
}

function PubSubOverview({
  pubsub,
  projectApps,
  specs,
}: {
  pubsub: PubSubEntry | undefined;
  projectApps: ReadonlyArray<{ id: string; name: string }>;
  specs: Map<string, AppSpec>;
}) {
  if (pubsub == null) {
    return <EmptyState icon={HardDriveDownloadIcon} title="PubSub not found" description="It may have been deleted." />;
  }
  const usedBy = usedByApps(pubsub, projectApps, specs);
  const health = backupHealth(pubsub.last_backup_at);
  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Connection</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted">host</dt>
          <dd className="flex items-center gap-1 font-mono">
            {pubsub.host ?? "—"}
            {pubsub.host ? <CopyButton value={pubsub.host} /> : null}
          </dd>
          <dt className="text-muted">port</dt>
          <dd className="font-mono">{pubsub.port ?? "—"}</dd>
          <dt className="text-muted">credential</dt>
          <dd className="flex items-center gap-1 font-mono">
            {pubsub.credentials_ref ?? "—"}
            {pubsub.credentials_ref ? <CopyButton value={pubsub.credentials_ref} /> : null}
          </dd>
        </dl>
        <p className="mt-3 text-2xs text-muted">
          The connection URL lives in the project secret and is never displayed. Apps receive it via pubsub_refs
          (FLEETLY_PUB_* env) or secret_refs (file mount).
        </p>
      </section>
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Engine</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted">engine</dt>
          <dd className="font-mono">{pubsub.engine ?? "—"}</dd>
          <dt className="text-muted">version</dt>
          <dd className="font-mono">{pubsub.version ?? "pinned"}</dd>
          <dt className="text-muted">created</dt>
          <dd>
            <RelativeTime value={pubsub.created_at} />
          </dd>
          <dt className="text-muted">updated</dt>
          <dd>
            <RelativeTime value={pubsub.updated_at} />
          </dd>
        </dl>
      </section>
      <section className="rounded-xl border bg-panel p-4">
        <h3 className="mb-3 text-sm font-semibold">Used by</h3>
        {usedBy.length === 0 ? (
          <p className="text-xs text-muted">
            No app references this pubsub yet — apps attach via pubsub_refs (FLEETLY_PUB_* env) on the instance name{" "}
            <span className="font-mono">{pubsub.name ?? "—"}</span>.
          </p>
        ) : (
          <div className="flex flex-col gap-2 text-xs">
            {usedBy.map((appName) => (
              <span key={appName} className="flex items-center gap-2">
                <span className="font-mono">{appName}</span>
                <span className="rounded-full border border-info/30 bg-info/10 px-2 py-0.5 text-2xs text-info">
                  {pubsub.name}
                </span>
              </span>
            ))}
          </div>
        )}
        <p className="mt-3 text-2xs text-muted">
          Resolved from frozen app specs (pubsub_refs + secret_refs scan) — live after the next deploy.
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
          Last successful backup: {pubsub.last_backup_at ? <RelativeTime value={pubsub.last_backup_at} /> : "never"}
        </p>
        {pubsub.restore_from_backup ? (
          <div className="mt-3 rounded-md border border-info/30 bg-info/10 p-2.5 text-xs">
            <p className="font-medium">Restore in progress</p>
            <p className="mt-1 font-mono text-2xs text-muted">from {pubsub.restore_from_backup}</p>
            {pubsub.restore_error ? <p className="mt-1 text-2xs text-err">last error: {pubsub.restore_error}</p> : null}
          </div>
        ) : null}
      </section>
    </div>
  );
}

function PubSubMetrics({ pubsubId }: { pubsubId: string }) {
  const memory = PUBSUB_CARRIER_PRESETS.find((preset) => preset.key === "memory")!;
  const cpu = PUBSUB_CARRIER_PRESETS.find((preset) => preset.key === "cpu_cores")!;
  const memorySeries = useScopedMetricsSeries({ kind: "pubsub", id: pubsubId }, memory.metric, undefined, "1h");
  const cpuSeries = useScopedMetricsSeries({ kind: "pubsub", id: pubsubId }, cpu.metric, undefined, "1h");
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 lg:grid-cols-2">
        <MetricCard title="Memory working set" unit="bytes" series={memorySeries} />
        <MetricCard title="CPU (cores)" unit="plain" series={cpuSeries} />
      </div>
      <p className="text-2xs text-muted">
        Scoped metric templates: the server resolves this pubsub's project and builds the docker/k3s arms — the console
        never sends PromQL.
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

function PubSubBackups({
  pubsub,
  projectId,
  pubsubId,
  routeId,
}: {
  pubsub: PubSubEntry | undefined;
  projectId: string;
  pubsubId: string;
  routeId: string;
}) {
  const backups = useCollection(pubsubBackupListResource, {
    from: routeId,
    search: pubsubBackupListLookup,
    scope: { projectId, pubsubId },
    query: { refetchInterval: 15_000 },
  });
  const rows = backups.rows;
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-3 sm:grid-cols-3">
        <InfoTile label="Retention" value="rolling (platform)" />
        <InfoTile label="Backend" value="local object store" hint="same-node — not disaster recovery" />
        <InfoTile label="Shape" value="per-stream members" hint="manifest-anchored object set; memory-storage streams are refused" />
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
                  No backups yet — trigger one with "Back up now".
                </td>
              </tr>
            ) : (
              rows.map((backup) => (
                <PubSubBackupRow
                  key={backup.id}
                  backup={backup}
                  projectId={projectId}
                  pubsubId={pubsubId}
                  engine={pubsub?.engine ?? ""}
                />
              ))
            )}
          </tbody>
        </table>
      </div>
      <p className="text-2xs text-muted">
        Restore always creates a new pubsub — the source is never overwritten; streams are restored one by one once the
        new instance is running.
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

function PubSubBackupRow({
  backup,
  projectId,
  pubsubId,
  engine,
}: {
  backup: BackupEntry;
  projectId: string;
  pubsubId: string;
  engine: string;
}) {
  const [verifying, setVerifying] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [verifyResult, setVerifyResult] = useState<{ ok: boolean; detail: string } | null>(null);
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [restoreName, setRestoreName] = useState("");
  // restore = create 命令的 restore_from_backup 形（恒新建实例；逐流恢复）。
  const restore = useCommand(pubsubsResource.commands.create, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      toast("Restore queued — the new pubsub converges from the backup");
      setRestoreOpen(false);
      setRestoreName("");
    }),
  });
  async function runDownload() {
    setDownloading(true);
    try {
      const blob = await downloadPubSubBackupChunks(pubsubId, backup.id ?? "", new AbortController().signal);
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
          <Button variant="outline" size="sm" disabled={verifying || downloading || backup.status !== "succeeded"} onClick={() => void runDownload()} title="Stream the backup object set to a local file (members in manifest order)">
            {downloading ? "Downloading…" : "Download"}
          </Button>
          <Button variant="outline" size="sm" disabled={verifying} onClick={() => void runVerify()} title="Recompute the manifest digests against the stored objects (read-only)">
            {verifying ? "Verifying…" : "Verify"}
          </Button>
          <Button variant="outline" size="sm" onClick={() => setRestoreOpen((prev) => !prev)} title="Create a new pubsub restored from this backup">
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
              <Label htmlFor={`pubsub-restore-name-${backup.id}`} className="text-2xs">
                New {engine} pubsub name
              </Label>
              <Input
                id={`pubsub-restore-name-${backup.id}`}
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

function PubSubSettings({
  pubsub,
  projectId,
  projectApps,
  specs,
  onDeleted,
}: {
  pubsub: PubSubEntry | undefined;
  projectId: string;
  projectApps: ReadonlyArray<{ id: string; name: string }>;
  specs: Map<string, AppSpec>;
  onDeleted: () => void;
}) {
  const id = pubsub?.id ?? "";
  const usedBy = usedByApps(pubsub, projectApps, specs);
  const ready = pubsub?.status === "running" || pubsub?.status === "degraded";
  const remove = useCommand(pubsubsResource.commands.delete, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      toast(`PubSub ${pubsub?.name} deleted`);
      onDeleted();
    }),
  });
  const [deleting, setDeleting] = useState(false);
  const [rotateOpen, setRotateOpen] = useState(false);
  const rotate = useCommand(pubsubsResource.commands.rotate, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      setRotateOpen(false);
      toast("Credential rotated — redeploy referencing apps to pick up the new value");
    }),
  });
  return (
    <div className="flex flex-col gap-4">
      {/* 目录驱动旋钮/资源面（Update 确认面：PreviewUpdatePubSub——本族首批
          旋钮全 static，确认框恒披露「将触发滚动重启」）。 */}
      {pubsub != null ? <PubSubSettingsEditor pubsub={pubsub} projectId={projectId} /> : null}
      <section className="rounded-xl border bg-panel p-5">
        <h3 className="mb-2 text-sm font-semibold">Credential</h3>
        <p className="mb-4 text-xs text-muted">
          The single platform credential lives in the project secret{" "}
          <span className="font-mono">{pubsub?.credentials_ref ?? "—"}</span> (connection URL, never displayed). Rotation
          is declarative: the secret is rewritten and the instance restarts with the new password; referencing apps must
          be redeployed to pick it up.
        </p>
        <Button size="sm" disabled={pubsub == null || !ready || rotate.pending} onClick={() => setRotateOpen(true)}>
          <RefreshCwIcon data-icon-start-inline />
          {rotate.pending ? "Rotating…" : "Rotate password…"}
        </Button>
        <ConfirmDialog
          open={rotateOpen}
          onOpenChange={setRotateOpen}
          title={`Rotate the credential for ${pubsub?.name}?`}
          description={
            <>
              A new password is minted and the connection URL in the credential secret is rewritten; the old password
              stops working. Apps referencing this pubsub keep the old value until redeployed
              {usedBy.length > 0 ? `: ${usedBy.join(", ")}` : " (none found in this project right now)"}. The instance
              rolls as the new materials are published, so expect a brief connection blip. The new URL is never
              displayed — apps receive it via their refs.
            </>
          }
          confirmLabel="Rotate"
          pendingLabel="Rotating…"
          destructive={false}
          risk={{ tier: "standard" }}
          onConfirm={() => rotate.mutateAsync({ id })}
        />
        {!ready ? (
          <p className="mt-3 text-2xs text-muted">Rotation needs a running pubsub — the declarative restart needs a live instance.</p>
        ) : null}
      </section>
      <section className="rounded-xl border border-err/30 bg-panel p-5">
        <h3 className="mb-2 text-sm font-semibold text-err">Danger zone</h3>
        <p className="mb-4 text-xs text-muted">
          Deleting <span className="font-semibold text-fg">{pubsub?.name}</span> removes the carrier workload and its
          deploy state. The volume and the credential secret{" "}
          <span className="font-mono">{pubsub?.credentials_ref ?? "—"}</span> are kept — nothing on disk is deleted.
          Same-name recreation hits the volume origin gate; clean both up separately to discard the data.
        </p>
        <Button variant="destructive" size="sm" disabled={pubsub == null} onClick={() => setDeleting(true)}>
          <Trash2Icon data-icon-start-inline />
          Delete pubsub…
        </Button>
        <ConfirmDialog
          open={deleting}
          onOpenChange={setDeleting}
          title={`Delete pubsub ${pubsub?.name}?`}
          description="This removes the carrier workload and its deploy state."
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          risk={{ tier: "wide", consequences: DELETE_PUBSUB_CONSEQUENCES }}
          onConfirm={() => remove.mutateAsync({ id })}
        />
      </section>
    </div>
  );
}
