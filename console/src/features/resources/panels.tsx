import { useState, useEffect, useRef } from "react";
import { LinkIcon, PencilIcon, PlusIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { toast } from "sonner";
import { useCollection, useCollectionData, useCommand, useCommandForm, useRecord } from "@lynx-go/uikit-core";

import type { ApiError, CommandResult } from "@lynx-go/uikit-core/kernel";
import { appsResource } from "@/kit/apps";
import { databasesResource } from "@/kit/databases";
import { projectsResource } from "@/kit/projects";
import { configDetailResource } from "@/kit/variables";
import { apiSendRaw } from "@/api/client";
import { describeApiError, toApiError } from "@/kit/errors";
import { appSpecsResource, appSpecsLookup, type AppSpecRow } from "@/kit/app-specs";
import { shortId } from "@/lib/format";
import { specIndex } from "@/features/spec/use-app-specs";
import { buildTar, rootPrefixOf } from "@/lib/tar";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { Form } from "@/components/kit/command/form";
import { SelectField, TextField } from "@/components/kit/command/fields";
import {
  approvePeerDescriptor,
  createNetworkCommand,
  declarePeerDescriptor,
  networkPeersResource,
  networksResource,
  networksSearch,
  peersSearch,
  rebuildNetworkDescriptor,
  revokePeerDescriptor,
  type Network,
  type NetworkPeer,
} from "@/kit/networks";
import {
  createRouteCommand,
  removeRouteCommand,
  routesResource,
  routesSearch,
  type Route,
} from "@/kit/routes";
import {
  createVolumeCommand,
  removeVolumeCommand,
  uploadsResource,
  uploadsSearch,
  volumesResource,
  volumesSearch,
  type Upload,
  type Volume,
} from "@/kit/storage";
import {
  configsResource,
  putConfigCommand,
  putSecretCommand,
  putSharedVariableCommand,
  removeSecretCommand,
  removeSharedVariableCommand,
  secretsResource,
  secretsSearch,
  configsSearch,
  sharedVarsSearch,
  sharedVariablesResource,
  type Config,
  type Secret,
  type SharedVariable,
} from "@/kit/variables";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { CopyButton } from "@/components/domain/copy-button";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { RelativeTime } from "@/components/domain/relative-time";
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
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { describeError } from "@/lib/api-errors";

// 资源面板族（UI v2 批 4 → 模板化收编 ListCard → useCollection 化批 3
// 收编 kit 块）：自旧 Resources.tsx 八 tab 搬迁——走查过的 mutation 语义
// 零漂移（创建/变更/删除/peer 审批/目录 tar 上传）。列表读面走 kit 资源
// （useCollection + ListPage × FilterBar × DataTable × Pagination，快筛/
// 分页/排序进 URL）；写面批 2 命令化（kit/<域> commands + useCommand/
// useCommandForm；简单静态表单收编 kit command 块——submit 进 Form 内芯，
// 复杂/动态壳保留只换命令接线；toast 单源 onSettled：ok→成功句、
// rejected/conflict→表单结果盒或行钮 toast、failure→describeApiError）。
// Mounted by / Referenced by 反查列的异步占位是块外槽（页面列 cell 自绘）。
// DatabasesPanel 已随 Databases 一级页（features/databases）消亡删除。

// FormError 是弹窗表单的失败通道错误面（danger 信封盒，describeError 单
// 源）——承载查询面错误（Config 详情回读）与上传流错误；命令结果通道
// （rejected/conflict）走 CmdResultError/kit Form 结果盒。
function FormError({ error }: { error: unknown }) {
  return (
    <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 font-mono text-2xs break-all text-muted">
      {describeError(error).detail}
    </div>
  );
}

// CmdResultError 是命令结果通道（rejected/conflict）的错误盒——保留原壳
// 的表单（PutForm/Config 编辑/declare）接替 FormError 的位置：400 的
// apperr code 保留机器判别面前缀，失败通道（网络/5xx）不走这里（toast）。
function CmdResultError({ result }: { result: Extract<CommandResult<unknown>, { status: "rejected" | "conflict" }> }) {
  return (
    <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 font-mono text-2xs break-all text-muted">
      {result.status === "rejected" && result.code ? `[${result.code}] ` : ""}
      {result.message}
    </div>
  );
}

// ---- networks ----

// NetworksPanel：网络表 + rebuild 行内动词（kit 化：快筛/分页/排序进
// URL，15s 轮询沿用）。
export function NetworksPanel({ projectId, routeId }: { projectId: string; routeId: string }) {
  return <NetworksTab projectId={projectId} routeId={routeId} />;
}

// NewNetworkButton 是 Networks 工具栏主动作（弹窗壳 + kit command 表单
// 内芯：submit 进 Form 块，rejected/conflict 走结果盒，failure 走 toast）。
export function NewNetworkButton({ projectId }: { projectId: string }) {
  const [createOpen, setCreateOpen] = useState(false);
  const create = useCommandForm(createNetworkCommand, {
    defaultValues: { name: "", egressNone: false },
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setCreateOpen(false);
      } else if (failure) {
        toast.error(describeApiError(failure));
      }
    },
  });
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          create.form.reset();
          create.command.reset();
          setCreateOpen(true);
        }}
      >
        <PlusIcon data-icon-start-inline />
        New network…
      </Button>
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>New network</DialogTitle>
          </DialogHeader>
          <Form
            form={create.form}
            submitting={create.submitting}
            result={create.result}
            submitLabel="Create"
            submittingLabel="Creating…"
            cancelLabel="Cancel"
            onCancel={() => setCreateOpen(false)}
          >
            <TextField form={create.form} name="name" label="Name" required />
            <create.form.Field name="egressNone">
              {(field: { state: { value: boolean }; handleChange(value: boolean): void }) => (
                <label className="flex items-center gap-2 text-xs text-muted">
                  <input
                    type="checkbox"
                    checked={field.state.value}
                    onChange={(event) => field.handleChange(event.target.checked)}
                  />
                  Egress none (isolated network)
                </label>
              )}
            </create.form.Field>
          </Form>
        </DialogContent>
      </Dialog>
    </>
  );
}

function NetworksTab({ projectId, routeId }: { projectId: string; routeId: string }) {
  const list = useCollection(networksResource, {
    from: routeId,
    search: networksSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });

  const columns: LegacyColumnDef<Network, any>[] = [
    { accessorKey: "name", header: "Name", meta: { sortField: "name" }, cell: ({ row }) => <span className="font-medium">{row.original.name}</span> },
    {
      accessorKey: "id",
      header: "ID",
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted" title={row.original.id}>
          {shortId(row.original.id)}
        </span>
      ),
    },
    {
      accessorKey: "egress_none",
      header: "Egress",
      cell: ({ row }) => <span className="text-xs text-muted">{row.original.egress_none ? "none (isolated)" : "open"}</span>,
    },
    {
      accessorKey: "created_at",
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
      cell: ({ row }) => <NetworkActionsCell projectId={projectId} networkId={row.original.id} />,
    },
  ];

  // 源空与过滤空两套文案（旧页 filterEmpty 契约）：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <ListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={networksSearch} textPlaceholder="Filter networks..." />
          {/* 列表主动作钉工具栏行最右（对齐批 5 裁决） */}
          <div className="py-2 pr-3">
            <NewNetworkButton projectId={projectId} />
          </div>
        </div>
      }
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No networks match the filter" : "No networks yet"}
      emptyDescription={
        filtered ? "Adjust or clear the filter to see rows again." : "Create one with New network… to attach apps across hosts."
      }
      appendix={<CliEquivalent command={`fleetly networks list --project ${projectId}`} />}
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
    </ListPage>
  );
}

// NetworkActionsCell 是行内 rebuild 动词（无容器行钮：rejected/conflict 与
// 失败通道走 toast；成功原无 toast——零漂移不新造）。
function NetworkActionsCell({ projectId, networkId }: { projectId: string; networkId?: string }) {
  const rebuild = useCommand(rebuildNetworkDescriptor, {
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result && result.status !== "ok") toast.error(result.message);
      else if (failure) toast.error(describeApiError(failure));
    },
  });
  return (
    <div className="text-right">
      <Button
        variant="outline"
        size="sm"
        disabled={rebuild.pending}
        onClick={() => rebuild.mutate({ networkId: networkId ?? "" })}
        title="Rebuild the carrier network (minute-scale)"
      >
        rebuild
      </Button>
    </div>
  );
}

// PeersPanel 是 Networks 页 Peers tab（跨项目挂靠面：审批表 + 声明表单）。
// 本批收编 kit 列表（resource networkPeers + DataTable，原自绘表退役）；
// approve/revoke 动词与 DeclarePeerForm 写面不动。
export function PeersPanel({ projectId, routeId }: { projectId: string; routeId: string }) {
  const list = useCollection(networkPeersResource, {
    from: routeId,
    search: peersSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  // DeclarePeerForm 的网络下拉数据（M1 useCollectionData 伴读——下拉不进 URL）。
  const networks = useCollectionData(networksResource, {
    scope: { projectId },
    window: { size: 20 },
    refetchInterval: 15_000,
    enabled: projectId !== "",
  });

  const columns: LegacyColumnDef<NetworkPeer, any>[] = [
    {
      id: "network",
      header: "Network",
      cell: ({ row }) => (
        <span className="font-mono text-xs" title={row.original.network_id}>
          {shortId(row.original.network_id)}
        </span>
      ),
    },
    {
      id: "peer_project",
      header: "Peer project",
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted" title={row.original.peer_project_id}>
          {shortId(row.original.peer_project_id)}
        </span>
      ),
    },
    {
      accessorKey: "state",
      header: "State",
      cell: ({ row }) => <span className="text-xs text-muted">{row.original.state}</span>,
    },
    {
      id: "id",
      header: "ID",
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted" title={row.original.id}>
          {shortId(row.original.id)}
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <PeerActionsCell projectId={projectId} peer={row.original} />,
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <section className="flex flex-col gap-3">
      <p className="text-xs text-muted">
        Cross-project network attachment: the attaching project declares, the network owner approves.
      </p>
      <ListPage
        list={list}
        toolbar={<FilterBar list={list} schema={peersSearch} textPlaceholder="Filter peers..." />}
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No peers match the filter" : "No peers declared"}
        emptyDescription={
          filtered ? "Adjust or clear the filter to see rows again." : "Declare a cross-project attachment below."
        }
      >
        <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
      </ListPage>
      <DeclarePeerForm projectId={projectId} networks={networks.rows} />
    </section>
  );
}

// PeerActionsCell：行内 approve/revoke（无容器行钮：rejected/conflict 与失
// 败通道走 toast，成功原无 toast——零漂移；revoke 经 ConfirmDialog 确认）。
function PeerActionsCell({ projectId, peer }: { projectId: string; peer: NetworkPeer }) {
  const settle = (result: CommandResult<null> | null, failure: ApiError | null) => {
    if (result && result.status !== "ok") toast.error(result.message);
    else if (failure) toast.error(describeApiError(failure));
  };
  const approve = useCommand(approvePeerDescriptor, { scope: { projectId }, onSettled: settle });
  const revoke = useCommand(revokePeerDescriptor, { scope: { projectId }, onSettled: settle });
  const [revoking, setRevoking] = useState(false);
  return (
    <div className="flex flex-col items-end">
      <div className="flex justify-end gap-1">
        <Button
          variant="outline"
          size="sm"
          disabled={approve.pending}
          onClick={() => approve.mutate({ peerId: peer.id ?? "" })}
        >
          approve
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="text-[var(--status-danger)]"
          disabled={revoke.pending}
          onClick={() => setRevoking(true)}
        >
          revoke
        </Button>
      </div>
      <ConfirmDialog
        open={revoking}
        onOpenChange={setRevoking}
        title="Revoke this peer?"
        description="The cross-project attachment is detached immediately."
        confirmLabel="Revoke"
        onConfirm={() => revoke.mutateAsync({ peerId: peer.id ?? "" })}
      />
    </div>
  );
}

// DeclarePeerForm：跨项目挂靠声明（inline 工具栏表单——壳保留：shadcn
// Select 双下拉与行内提交钮形态不变，只换命令接线；结果盒/Working… 占位
// 原位，成功占位保持瞬时态（reset 后即退场，零漂移））。
function DeclarePeerForm({ projectId, networks }: { projectId: string; networks: ReadonlyArray<{ id?: string; name?: string }> }) {
  const projects = useCollectionData(projectsResource, { window: { size: 100 }, refetchInterval: 60_000 });
  const [networkId, setNetworkId] = useState("");
  const [peerProject, setPeerProject] = useState("");
  const declare = useCommand(declarePeerDescriptor, {
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setNetworkId("");
        setPeerProject("");
        declare.reset();
      } else if (failure) {
        toast.error(describeApiError(failure));
      }
      // rejected/conflict → 结果盒（有表单容器，不 toast）
    },
  });
  return (
    <form
      className="flex flex-wrap items-end gap-2 rounded-lg border px-3 py-2"
      onSubmit={(event) => {
        event.preventDefault();
        declare.mutate({ networkId, peerProjectId: peerProject });
      }}
    >
      <Label className="flex flex-col gap-1 text-xs">
        <span className="font-medium text-fg">Declare peer — network</span>
        <Select value={networkId} onValueChange={setNetworkId}>
          <SelectTrigger className="h-8 w-44">
            <SelectValue placeholder="select…" />
          </SelectTrigger>
          <SelectContent>
            {networks.map((network) => (
              <SelectItem key={network.id} value={network.id ?? ""}>
                {network.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Label>
      <Label className="flex flex-col gap-1 text-xs">
        <span className="font-medium text-fg">Peer project (owner of the network you want to attach)</span>
        <Select value={peerProject} onValueChange={setPeerProject}>
          <SelectTrigger className="h-8 w-44">
            <SelectValue placeholder="select…" />
          </SelectTrigger>
          <SelectContent>
            {projects.rows
              .filter((project) => project.id !== projectId)
              .map((project) => (
                <SelectItem key={project.id} value={project.id}>
                  {project.name}
                </SelectItem>
              ))}
          </SelectContent>
        </Select>
      </Label>
      <Button type="submit" size="sm" disabled={declare.pending || networkId === "" || peerProject === ""}>
        declare
      </Button>
      {declare.pending ? <span className="text-xs text-muted">Working…</span> : null}
      {declare.result != null && declare.result.status !== "ok" ? <CmdResultError result={declare.result} /> : null}
      {declare.result?.status === "ok" ? <span className="text-xs text-[var(--status-success)]">peer declared</span> : null}
    </form>
  );
}

// ---- routes ----

// RoutesPanel：host → app/process/port 路由表 + 创建（kit 化：快筛/分页/
// 排序进 URL，15s 轮询沿用——最标准单列表形态）。
export function RoutesPanel({
  projectId,
  routeId,
  apps,
}: {
  projectId: string;
  routeId: string;
  apps: Array<{ id: string; name: string }>;
}) {
  return <RoutesTab projectId={projectId} routeId={routeId} apps={apps} />;
}

// NewRouteButton 是 Routes 工具栏主动作（弹窗壳 + kit command 表单内芯；
// host/path/port 带 hint/inputMode 的字段在 Form 块内手接线保文案零漂移，
// 纯下拉走 kit SelectField）；项目尚无 App 时诚实禁用。
export function NewRouteButton({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
  const [createOpen, setCreateOpen] = useState(false);
  const defaults = () => ({
    host: "",
    path: "",
    app: apps[0]?.id ?? "",
    process: "web",
    port: "8080",
    protocol: "http",
    tls: "auto",
  });
  const create = useCommandForm(createRouteCommand, {
    defaultValues: defaults(),
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setCreateOpen(false);
      } else if (failure) {
        toast.error(describeApiError(failure));
      }
    },
  });
  const form = create.form;
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          form.reset(defaults());
          create.command.reset();
          setCreateOpen(true);
        }}
        disabled={apps.length === 0}
      >
        <PlusIcon data-icon-start-inline />
        New route…
      </Button>
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>New route</DialogTitle>
          </DialogHeader>
          <Form
            form={form}
            submitting={create.submitting}
            result={create.result}
            submitLabel="Create"
            submittingLabel="Creating…"
            cancelLabel="Cancel"
            onCancel={() => setCreateOpen(false)}
          >
            <form.Field name="host">
              {(field: { state: { value: unknown }; handleChange(value: unknown): void }) => (
                <label className="block space-y-1">
                  <span className="text-xs font-medium text-muted">Host</span>
                  <Input
                    value={String(field.state.value ?? "")}
                    onChange={(e) => field.handleChange(e.target.value)}
                    autoFocus
                  />
                  <span className="text-[11px] text-muted">e.g. shop.203.0.113.10.sslip.io</span>
                </label>
              )}
            </form.Field>
            <div className="grid grid-cols-2 gap-3">
              <form.Field name="path">
                {(field: { state: { value: unknown }; handleChange(value: unknown): void }) => (
                  <label className="block space-y-1">
                    <span className="text-xs font-medium text-muted">Path</span>
                    <Input
                      value={String(field.state.value ?? "")}
                      onChange={(e) => field.handleChange(e.target.value)}
                      placeholder="/"
                    />
                    <span className="text-[11px] text-muted">optional prefix</span>
                  </label>
                )}
              </form.Field>
              <SelectField
                form={form}
                name="tls"
                label="TLS"
                options={[
                  { value: "auto", label: "auto" },
                  { value: "none", label: "none" },
                ]}
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <SelectField
                form={form}
                name="app"
                label="App"
                required
                options={apps.map((app) => ({ value: app.id, label: app.name }))}
              />
              <TextField form={form} name="process" label="Process" />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <form.Field name="port">
                {(field: { state: { value: unknown }; handleChange(value: unknown): void }) => (
                  <label className="block space-y-1">
                    <span className="text-xs font-medium text-muted">Port</span>
                    <Input
                      value={String(field.state.value ?? "")}
                      onChange={(e) => field.handleChange(e.target.value)}
                      inputMode="numeric"
                    />
                  </label>
                )}
              </form.Field>
              <SelectField
                form={form}
                name="protocol"
                label="Protocol"
                options={[
                  { value: "http", label: "http" },
                  { value: "h2c", label: "h2c" },
                  { value: "tcp", label: "tcp" },
                ]}
              />
            </div>
          </Form>
        </DialogContent>
      </Dialog>
    </>
  );
}

function RoutesTab({
  projectId,
  routeId,
  apps,
}: {
  projectId: string;
  routeId: string;
  apps: Array<{ id: string; name: string }>;
}) {
  const list = useCollection(routesResource, {
    from: routeId,
    search: routesSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });

  const columns: LegacyColumnDef<Route, any>[] = [
    {
      accessorKey: "host",
      header: "Host",
      meta: { sortField: "host" },
      cell: ({ row }) => <span className="font-mono">{row.original.host}</span>,
    },
    {
      accessorKey: "path",
      header: "Path",
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.path || "—"}</span>,
    },
    {
      id: "target",
      header: "Target",
      cell: ({ row }) => {
        const appName = apps.find((app) => app.id === row.original.app_id)?.name ?? shortId(row.original.app_id);
        return (
          <span className="text-xs">
            {appName} / {row.original.process}
          </span>
        );
      },
    },
    {
      accessorKey: "port",
      header: "Port",
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted">
          {row.original.port} {row.original.protocol ? `(${row.original.protocol})` : ""}
        </span>
      ),
    },
    {
      accessorKey: "tls_mode",
      header: "TLS",
      cell: ({ row }) => <span className="text-xs text-muted">{row.original.tls_mode}</span>,
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <RouteActionsCell routeId={row.original.id} host={row.original.host} projectId={projectId} />,
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <ListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={routesSearch} textPlaceholder="Filter routes..." />
          <div className="py-2 pr-3">
            <NewRouteButton projectId={projectId} apps={apps} />
          </div>
        </div>
      }
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No routes match the filter" : "No routes yet"}
      emptyDescription={
        filtered ? "Adjust or clear the filter to see rows again." : "Create one with New route… to serve traffic on a host."
      }
      appendix={<CliEquivalent command={`fleetly routes list --project ${projectId}`} />}
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
    </ListPage>
  );
}

function RouteActionsCell({ routeId, host, projectId }: { routeId?: string; host?: string; projectId: string }) {
  const [deleting, setDeleting] = useState(false);
  const del = useCommand(removeRouteCommand, {
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result && result.status !== "ok") toast.error(result.message);
      else if (failure) toast.error(describeApiError(failure));
    },
  });
  return (
    <div className="text-right">
      <Button
        variant="outline"
        size="sm"
        className="text-[var(--status-danger)]"
        disabled={del.pending}
        onClick={() => setDeleting(true)}
      >
        delete
      </Button>
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete route ${host ?? ""}?`}
        description="Traffic to this host stops being served by the managed proxy."
        confirmLabel="Delete route"
        onConfirm={() => del.mutateAsync({ routeId: routeId ?? "" })}
      />
    </div>
  );
}

// ---- volumes（IA v3 二期⑤b v2 形态：Dialog 创建 + AlertDialog 删除
// 确认 + token 类；mutation 语义自批 4 零漂移；本批 kit 化列表读面，
// 15s 轮询沿用） ----

export function VolumesPanel({ projectId, routeId }: { projectId: string; routeId: string }) {
  return <VolumesTab projectId={projectId} routeId={routeId} />;
}

function VolumesTab({ projectId, routeId }: { projectId: string; routeId: string }) {
  const list = useCollection(volumesResource, {
    from: routeId,
    search: volumesSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  // 目录伴读（M1 useCollectionData——下拉/反查不进 URL，大页窗同 lookup）。
  const apps = useCollectionData(appsResource, {
    scope: { projectId },
    window: { size: 200 },
    refetchInterval: 60_000,
    enabled: projectId !== "",
  });
  const databases = useCollectionData(databasesResource, {
    scope: { projectId },
    window: { size: 200 },
    refetchInterval: 15_000,
    enabled: projectId !== "",
  });
  // 批4（2026-10-27）：冻结 Spec 反查迁 kit appSpecsResource（fan-out 内联
  // 于 fetch——apps 整取不再由本页喂；大页 lookup 不截断；60s 轮询同旧拍；
  // Mounted by 反查逻辑不动）。
  const specsList = useCollection(appSpecsResource, {
    from: routeId,
    search: appSpecsLookup,
    scope: { projectId },
    query: { refetchInterval: 60_000 },
  });
  const specs = new Map<string, AppSpecRow["spec"]>(specsList.rows.map((row) => [row.appId, row.spec]));
  const specsPending = specsList.status === "loading";
  const [createOpen, setCreateOpen] = useState(false);
  const create = useCommandForm(createVolumeCommand, {
    defaultValues: { name: "", pinnedNode: "" },
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setCreateOpen(false);
      } else if (failure) {
        toast.error(describeApiError(failure));
      }
    },
  });
  // 挂载判据（IA v3 二期⑤b）：卷锚 = 平台卷名（App 冻结 Spec 的卷附件
  // volume_id 与 Database 挂靠卷名公式都按 Name——引擎同口径；W-7 对齐）。
  const volumeIndex = specIndex(specs, (spec) =>
    (spec.processes ?? []).flatMap((process) => (process.volumes ?? []).map((volume) => volume.volume_id ?? "")),
  );
  const appName = (id: string) => apps.rows.find((app) => app.id === id)?.name ?? id;
  const mountOf = (volume: { name?: string }): { carrier: boolean; users: string[] } => {
    const users = (volumeIndex.get(volume.name ?? "") ?? []).map(appName);
    const carried = databases.rows.some((database) => database.name === volume.name);
    return { carrier: carried, users: carried ? [] : users };
  };
  const openCreate = () => {
    create.form.reset();
    create.command.reset();
    setCreateOpen(true);
  };

  const columns: LegacyColumnDef<Volume, any>[] = [
    {
      accessorKey: "name",
      header: "Name",
      meta: { sortField: "name" },
      cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
    },
    {
      accessorKey: "pinned_node_id",
      header: "Pinned node",
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.pinned_node_id || "any"}</span>,
    },
    {
      id: "mounted_by",
      header: "Mounted by",
      cell: ({ row }) => {
        // 异步反查占位是块外槽（页面列 cell 自绘——kit 块无此槽）
        if (specsPending) return <span className="text-xs text-muted">…</span>;
        const mount = mountOf(row.original);
        if (mount.carrier) {
          return (
            <span className="flex items-center gap-1 text-xs text-muted">
              <LinkIcon className="size-3" />
              database {row.original.name} (carrier data volume)
            </span>
          );
        }
        if (mount.users.length > 0) {
          return (
            <span className="flex items-center gap-1 text-xs text-muted">
              <LinkIcon className="size-3" />
              {mount.users.join(", ")}
            </span>
          );
        }
        return <span className="text-xs text-muted">—</span>;
      },
    },
    {
      accessorKey: "created_at",
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
      cell: ({ row }) => {
        const mount = mountOf(row.original);
        return (
          <VolumeActionsCell
            volume={row.original}
            mounted={mount.carrier || mount.users.length > 0}
            projectId={projectId}
          />
        );
      },
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <ListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={volumesSearch} textPlaceholder="Filter volumes..." />
          <div className="py-2 pr-3">
            <Button size="sm" onClick={openCreate}>
              <PlusIcon data-icon-start-inline />
              New volume…
            </Button>
          </div>
        </div>
      }
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No volumes match the filter" : "No volumes"}
      emptyDescription={
        filtered ? "Adjust or clear the filter to see rows again." : "Create one to attach it from an app's Variables tab."
      }
      emptyAction={
        <Button size="sm" onClick={openCreate}>
          <PlusIcon data-icon-start-inline />
          New volume…
        </Button>
      }
      appendix={
        <>
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogContent className="sm:max-w-md">
              <DialogHeader>
                <DialogTitle>New volume</DialogTitle>
              </DialogHeader>
              <Form
                form={create.form}
                submitting={create.submitting}
                result={create.result}
                submitLabel="Create"
                submittingLabel="Creating…"
                cancelLabel="Cancel"
                onCancel={() => setCreateOpen(false)}
              >
                <TextField form={create.form} name="name" label="Name" required />
                <create.form.Field name="pinnedNode">
                  {(field: { state: { value: unknown }; handleChange(value: unknown): void }) => (
                    <label className="block space-y-1">
                      <span className="text-xs font-medium text-muted">Pinned node</span>
                      <Input
                        value={String(field.state.value ?? "")}
                        onChange={(e) => field.handleChange(e.target.value)}
                      />
                      <span className="text-[11px] text-muted">optional platform node id — volume stays on that node</span>
                    </label>
                  )}
                </create.form.Field>
              </Form>
            </DialogContent>
          </Dialog>
          <p className="text-xs text-muted">
            Mounts resolve from frozen app specs — new references go live on the app's next deploy.
          </p>
        </>
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
    </ListPage>
  );
}

// VolumeActionsCell：未挂载才可删（挂载判据由服务端 E_CONFLICT 兜底——
// 客户端隐藏是前置体验面；挂载方信息由 Mounted by 列承载）；删除仅收口
// 平台行，底层 runtime 卷留存（数据兜底永不级联），AlertDialog 确认文案
// 明示。
function VolumeActionsCell({
  volume,
  mounted,
  projectId,
}: {
  volume: { id?: string; name?: string };
  mounted: boolean;
  projectId: string;
}) {
  const del = useCommand(removeVolumeCommand, {
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        toast.success(`Volume ${volume.name} deleted`);
      } else if (result) {
        toast.error(result.message);
      } else if (failure) {
        toast.error(describeApiError(failure));
      }
    },
  });
  if (mounted) {
    return null;
  }
  return (
    <div className="text-right">
      <AlertDialog>
        <AlertDialogTrigger asChild>
          <Button variant="ghost" size="sm" className="text-err hover:text-err" disabled={del.pending}>
            delete
          </Button>
        </AlertDialogTrigger>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete volume {volume.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              The platform row is removed; the runtime-side volume stays (data is not reclaimed automatically). A volume still
              mounted by an app or carried by a database is refused with the referencing list.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={del.pending}
              onClick={(event) => {
                event.preventDefault();
                del.mutate({ volumeId: volume.id ?? "" });
              }}
            >
              {del.pending ? "Deleting…" : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ---- secrets / configs / shared variables（put 型写面；列表读面本批
// kit 化，三 tab 各持 namespace schema（F1 回流批），均 15s 轮询沿用） ----

// PutForm 是 Add 型写面的统一弹窗（secret/config/shared variable 共用，
// API 动词是 PUT——组件名承动词面，按钮文案统一 Add *，用户裁决 2026-10-11）：
// 名称 + 值（或 multiline 内容）。壳保留（批 2 只换命令接线）：onSubmit 返
// 回 CommandResult——rejected/conflict 走结果盒，失败通道 toast，ok 关窗。
function PutForm({
  title,
  nameLabel,
  valueLabel,
  valueHint,
  multiline,
  onSubmit,
}: {
  title: string;
  nameLabel: string;
  valueLabel: string;
  valueHint?: string;
  multiline?: boolean;
  onSubmit: (name: string, value: string) => Promise<CommandResult<unknown>>;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [result, setResult] = useState<Extract<CommandResult<unknown>, { status: "rejected" | "conflict" }> | null>(null);
  const [pending, setPending] = useState(false);
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          setName("");
          setValue("");
          setResult(null);
          setOpen(true);
        }}
      >
        <PlusIcon data-icon-start-inline />
        {title}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
          </DialogHeader>
          <form
            className="flex flex-col gap-3"
            onSubmit={async (event) => {
              event.preventDefault();
              setPending(true);
              setResult(null);
              try {
                const settled = await onSubmit(name, value);
                if (settled.status === "ok") {
                  setOpen(false);
                } else {
                  setResult(settled);
                }
              } catch (cause) {
                // 失败通道（网络/401/5xx）不走结果盒——toast 诚实文案
                toast.error(describeApiError(toApiError(cause)));
              } finally {
                setPending(false);
              }
            }}
          >
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">{nameLabel}</span>
              <Input value={name} onChange={(event) => setName(event.target.value)} autoFocus />
            </Label>
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">{valueLabel}</span>
              {multiline ? (
                <Textarea rows={8} value={value} onChange={(event) => setValue(event.target.value)} />
              ) : (
                <Input type="password" value={value} onChange={(event) => setValue(event.target.value)} />
              )}
              {valueHint ? <span className="text-2xs font-normal text-muted">{valueHint}</span> : null}
            </Label>
            {result !== null ? <CmdResultError result={result} /> : null}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={pending || name === ""}>
                {pending ? "Saving…" : "Save"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

export function SecretsPanel({ projectId, routeId }: { projectId: string; routeId: string }) {
  return <SecretsTab projectId={projectId} routeId={routeId} />;
}

function SecretsTab({ projectId, routeId }: { projectId: string; routeId: string }) {
  const list = useCollection(secretsResource, {
    from: routeId,
    search: secretsSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const put = useCommand(putSecretCommand, { scope: { projectId } });

  const columns: LegacyColumnDef<Secret, any>[] = [
    {
      accessorKey: "name",
      header: "Name",
      meta: { sortField: "name" },
      cell: ({ row }) => <span className="font-mono">{row.original.name}</span>,
    },
    {
      id: "updated",
      header: "Updated",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          {/* v1Secret 只有 updated_at（fingerprint 行无 created_at） */}
          <RelativeTime value={row.original.updated_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <SecretActionsCell name={row.original.name} projectId={projectId} />,
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <ListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={secretsSearch} textPlaceholder="Filter secrets..." />
          <div className="py-2 pr-3">
            <PutForm
              title="Add secret…"
              nameLabel="Name"
              valueLabel="Value"
              valueHint="write-only — the list never shows values"
              onSubmit={(name, value) => put.mutateAsync({ name, value })}
            />
          </div>
        </div>
      }
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No secrets match the filter" : "No secrets yet"}
      emptyDescription={
        filtered ? "Adjust or clear the filter to see rows again." : "Add one — apps reference it by name at deploy freeze time."
      }
      appendix={<CliEquivalent command={`fleetly secrets list --project ${projectId}`} />}
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? row.name ?? ""} />
    </ListPage>
  );
}

function SecretActionsCell({ name, projectId }: { name?: string; projectId: string }) {
  const [deleting, setDeleting] = useState(false);
  const del = useCommand(removeSecretCommand, {
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result && result.status !== "ok") toast.error(result.message);
      else if (failure) toast.error(describeApiError(failure));
    },
  });
  return (
    <div className="text-right">
      <Button
        variant="outline"
        size="sm"
        className="text-[var(--status-danger)]"
        disabled={del.pending}
        onClick={() => setDeleting(true)}
      >
        delete
      </Button>
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete secret ${name ?? ""}?`}
        description="Apps referencing it at deploy freeze time will fail to resolve."
        confirmLabel="Delete secret"
        onConfirm={() => del.mutateAsync({ name: name ?? "" })}
      />
    </div>
  );
}

export function ConfigsPanel({ projectId, routeId }: { projectId: string; routeId: string }) {
  return <ConfigsTab projectId={projectId} routeId={routeId} />;
}

function ConfigsTab({ projectId, routeId }: { projectId: string; routeId: string }) {
  const list = useCollection(configsResource, {
    from: routeId,
    search: configsSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const [selected, setSelected] = useState<Config | null>(null);
  const put = useCommand(putConfigCommand, { scope: { projectId } });

  const columns: LegacyColumnDef<Config, any>[] = [
    {
      accessorKey: "name",
      header: "Name",
      meta: { sortField: "name" },
      cell: ({ row }) => <span className="font-mono">{row.original.name}</span>,
    },
    {
      accessorKey: "version",
      header: "Version",
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.version ?? "—"}</span>,
    },
    {
      id: "updated",
      header: "Updated",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          <RelativeTime value={row.original.created_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <div className="text-right">
          <Button variant="outline" size="sm" onClick={() => setSelected(row.original)}>
            view
          </Button>
        </div>
      ),
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <ListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={configsSearch} textPlaceholder="Filter configs..." />
          <div className="py-2 pr-3">
            <PutForm
              title="Add config…"
              nameLabel="Name"
              valueLabel="Content"
              valueHint="each write freezes a new version"
              multiline
              onSubmit={(name, content) => put.mutateAsync({ name, content })}
            />
          </div>
        </div>
      }
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No configs match the filter" : "No configs yet"}
      emptyDescription={
        filtered ? "Adjust or clear the filter to see rows again." : "Add one — each write freezes a new version."
      }
      appendix={
        <>
          <CliEquivalent command={`fleetly configs list --project ${projectId}`} />
          <ConfigDetailDialog projectId={projectId} config={selected} onClose={() => setSelected(null)} />
        </>
      }
    >
      <DataTable
        list={list}
        columns={columns}
        getRowId={(row) => row.id ?? row.name ?? ""}
        onRowClick={(row) => setSelected(row)}
      />
    </ListPage>
  );
}

// ConfigDetailDialog 是 Config 详情/编辑弹窗（行点击 / view 动词进入）：
// 内容按 GetConfig 回读（Config 是版本化可回读面——与 Secrets 的
// write-only 契约相反）；编辑提交 PUT 同名即冻结新版本，成功后回视图
// 并刷新到新版本号。版本历史浏览平台面未开（ListConfigs 只回每 name
// 最新版），详情先钉当前版。
function ConfigDetailDialog({
  projectId,
  config,
  onClose,
}: {
  projectId: string;
  config: { name?: string } | null;
  onClose: () => void;
}) {
  const name = config?.name ?? "";
  // Config 详情回读伴读（M1 useRecord）：PUT 冻结新版本后经 legacyKeys
  // 双键桥失效（批2 桥值）；空 name 停查（undefined id 门）。
  const detail = useRecord(configDetailResource, { id: name || undefined, scope: { projectId } });
  const [editing, setEditing] = useState(false);
  const [content, setContent] = useState("");
  // Config 编辑壳保留（批 2 只换命令接线）：保存 PUT 同名即冻结新版本，
  // 成功后回视图并刷新到新版本号；rejected/conflict 走结果盒、failure 走
  // toast；详情键失效经 configsResource.legacyKeys 宽前缀桥。
  const save = useCommand(putConfigCommand, {
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setEditing(false);
        toast.success(`${name} saved as version ${result.payload?.config?.version ?? "?"}`);
      } else if (failure) {
        toast.error(describeApiError(failure));
      }
      // rejected/conflict → 结果盒（表单容器，不 toast）
    },
  });
  function close() {
    setEditing(false);
    onClose();
  }
  return (
    <Dialog
      open={config !== null}
      onOpenChange={(open) => {
        if (!open) close();
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-mono">{name}</DialogTitle>
        </DialogHeader>
        {editing ? (
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              save.mutate({ name, content });
            }}
          >
            <Textarea
              rows={16}
              className="font-mono text-xs"
              value={content}
              onChange={(event) => setContent(event.target.value)}
              autoFocus
            />
            {save.result != null && save.result.status !== "ok" ? <CmdResultError result={save.result} /> : null}
            <DialogFooter>
              <Button type="button" variant="outline" disabled={save.pending} onClick={() => setEditing(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={save.pending || content === (detail.data?.content ?? "")}>
                {save.pending ? "Saving…" : "Save as new version"}
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <>
            <p className="text-xs text-muted">
              version <span className="font-mono">{detail.data?.version ?? "…"}</span>
              {" · updated "}
              <RelativeTime value={detail.data?.created_at} />
              {" · "}
              {new TextEncoder().encode(detail.data?.content ?? "").length} bytes
            </p>
            {detail.status === "loading" ? (
              <Skeleton className="h-48 w-full" />
            ) : detail.status === "error" ? (
              <FormError error={detail.error ?? undefined} />
            ) : (
              <pre className="max-h-[50vh] overflow-auto rounded-lg border bg-panel-2/40 p-3 font-mono text-xs break-all whitespace-pre-wrap">
                {detail.data?.content}
              </pre>
            )}
            {detail.status === "error" ? (
              <DialogFooter>
                <Button type="button" variant="outline" onClick={close}>
                  Close
                </Button>
                <Button type="button" onClick={() => void detail.refetch()}>
                  Retry
                </Button>
              </DialogFooter>
            ) : (
              <DialogFooter>
                <CopyButton value={detail.data?.content ?? ""} label="Copy" />
                <Button
                  type="button"
                  size="sm"
                  disabled={detail.status !== "success"}
                  onClick={() => {
                    setContent(detail.data?.content ?? "");
                    setEditing(true);
                  }}
                >
                  <PencilIcon data-icon-start-inline />
                  Edit
                </Button>
              </DialogFooter>
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function VariablesPanel({
  projectId,
  routeId,
  apps,
}: {
  projectId: string;
  routeId: string;
  apps: Array<{ id: string; name: string }>;
}) {
  return <VariablesTab projectId={projectId} routeId={routeId} apps={apps} />;
}

function VariablesTab({
  projectId,
  routeId,
  apps,
}: {
  projectId: string;
  routeId: string;
  apps: Array<{ id: string; name: string }>;
}) {
  const list = useCollection(sharedVariablesResource, {
    from: routeId,
    search: sharedVarsSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const [banner, setBanner] = useState<string | null>(null);
  const putCommand = useCommand(putSharedVariableCommand, { scope: { projectId } });
  // affected_apps 横幅逻辑保留（批 2 命令化）：ok 载荷带受影响 App 名单
  // ——redeploy 提示链；rejected/conflict 由 PutForm 结果盒披露、失败通
  // 道 toast（PutForm 内单源）。
  async function put(name: string, value: string): Promise<CommandResult<unknown>> {
    const result = await putCommand.mutateAsync({ name, value });
    if (result.status === "ok") {
      const affected = (result.payload.affected_apps ?? []).flatMap((id: string | undefined) =>
        id ? [apps.find((app) => app.id === id)?.name ?? id.slice(0, 10)] : [],
      );
      setBanner(
        affected.length === 0
          ? `${name} saved — no app references it yet`
          : `${name} saved — redeploy to pick it up: ${affected.join(",")}`,
      );
    }
    return result;
  }

  const columns: LegacyColumnDef<SharedVariable, any>[] = [
    {
      accessorKey: "name",
      header: "Name",
      meta: { sortField: "name" },
      cell: ({ row }) => <span className="font-mono">{row.original.name}</span>,
    },
    {
      accessorKey: "value",
      header: "Value",
      cell: ({ row }) => (
        <span className="block max-w-xs truncate font-mono text-xs text-muted" title={row.original.value}>
          {row.original.value}
        </span>
      ),
    },
    {
      accessorKey: "updated_at",
      header: "Updated",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          <RelativeTime value={row.original.updated_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <VariableActionsCell row={row.original} projectId={projectId} apps={apps} />,
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <ListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={sharedVarsSearch} textPlaceholder="Filter variables..." />
          <div className="py-2 pr-3">
            <PutForm
              title="Add variable…"
              nameLabel="Name"
              valueLabel="Value"
              valueHint="merged into app env at deploy freeze time"
              onSubmit={put}
            />
          </div>
        </div>
      }
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No variables match the filter" : "No shared variables yet"}
      emptyDescription={
        filtered ? "Adjust or clear the filter to see rows again." : "Add one — it merges into app env at deploy freeze time."
      }
      appendix={
        <>
          <CliEquivalent command={`fleetly shared-variables list --project ${projectId}`} />
          {putCommand.pending ? <p className="text-xs text-muted">Working…</p> : null}
          {banner ? <p className="text-xs text-[var(--status-success)]">{banner}</p> : null}
        </>
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? row.name ?? ""} />
    </ListPage>
  );
}

function VariableActionsCell({
  row,
  projectId,
  apps,
}: {
  row: SharedVariable;
  projectId: string;
  apps: Array<{ id: string; name: string }>;
}) {
  const [result, setResult] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  // 行删（ConfirmDialog 壳保留）：ok 的 affected_apps 披露保留行内绿条
  // （redeploy 提示链）；rejected/conflict 与失败通道走 toast（无容器动作）。
  const del = useCommand(removeSharedVariableCommand, {
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        const affected = result.payload?.affected_apps ?? [];
        setResult(
          affected.length === 0
            ? `${row.name} deleted`
            : `${row.name} deleted — redeploy to pick it up: ${affected
                .flatMap((id) => (id ? [apps.find((app) => app.id === id)?.name ?? id.slice(0, 10)] : []))
                .join(",")}`,
        );
      } else if (result) {
        toast.error(result.message);
      } else if (failure) {
        toast.error(describeApiError(failure));
      }
    },
  });
  return (
    <div className="text-right align-top">
      <Button
        variant="outline"
        size="sm"
        className="text-[var(--status-danger)]"
        disabled={del.pending}
        onClick={() => setDeleting(true)}
      >
        delete
      </Button>
      {result ? <div className="mt-1 text-2xs text-[var(--status-success)]">{result}</div> : null}
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete shared variable ${row.name ?? ""}?`}
        description="Apps referencing it keep the last merged value until the next deploy freeze."
        confirmLabel="Delete variable"
        onConfirm={() => del.mutateAsync({ name: row.name ?? "" })}
      />
    </div>
  );
}

// ---- uploads（上传流工具栏不动；列表读面本批 kit 化，15s 轮询沿用） ----

export function UploadsPanel({ projectId, routeId }: { projectId: string; routeId: string }) {
  return <UploadsTab projectId={projectId} routeId={routeId} />;
}

function UploadsTab({ projectId, routeId }: { projectId: string; routeId: string }) {
  const list = useCollection(uploadsResource, {
    from: routeId,
    search: volumesSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  // App 目录伴读（M1 useCollectionData——Uploads 引用反查用，不进 URL）。
  const apps = useCollectionData(appsResource, {
    scope: { projectId },
    window: { size: 200 },
    refetchInterval: 60_000,
    enabled: projectId !== "",
  }).rows;
  // 批4（2026-10-27）：冻结 Spec 反查迁 kit appSpecsResource（同 Volumes
  // tab 口径；Referenced by 反查逻辑不动）。
  const specsList = useCollection(appSpecsResource, {
    from: routeId,
    search: appSpecsLookup,
    scope: { projectId },
    query: { refetchInterval: 60_000 },
  });
  const specs = new Map<string, AppSpecRow["spec"]>(specsList.rows.map((row) => [row.appId, row.spec]));
  const specsPending = specsList.status === "loading";
  const inputRef = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<string | null>(null);

  // upload → apps 引用反查（原 UsageIndex Upload→apps 面收编入列）：扫
  // App 冻结 Spec 的 source.upload.id（客户端 fan-out，ADR-0057 惯例）。
  const uploadIndex = specIndex(specs, (spec) => (spec.source?.upload?.id ? [spec.source.upload.id] : []));
  const appName = (id: string) => apps.find((app) => app.id === id)?.name ?? id;

  // webkitdirectory 是非标准属性（目录选择面）：React TS 类型不含它，
  // 经 ref 挂属性（Chromium/Firefox 均支持目录选择）。
  useEffect(() => {
    inputRef.current?.setAttribute("webkitdirectory", "");
    inputRef.current?.setAttribute("directory", "");
  }, []);

  async function onFiles(files: FileList | null) {
    if (files == null || files.length === 0) return;
    setPending(true);
    setError(null);
    setResult(null);
    try {
      const tar = await buildTar(Array.from(files), rootPrefixOf(Array.from(files)));
      const response = await apiSendRaw<{ id?: string; digest?: string; deduplicated?: boolean; size_bytes?: string }>(
        `/v1/uploads?project_id=${encodeURIComponent(projectId)}`,
        new Blob([tar as unknown as BlobPart], { type: "application/x-tar" }),
        "application/x-tar",
      );
      setResult(
        `${response.id ?? "?"} uploaded (${(Number(response.size_bytes ?? 0) / 1024).toFixed(1)} KiB)${response.deduplicated ? " — content-addressed dedup hit" : ""}`,
      );
    } catch (cause) {
      setError(cause);
    } finally {
      setPending(false);
      if (inputRef.current) inputRef.current.value = "";
    }
  }

  const columns: LegacyColumnDef<Upload, any>[] = [
    {
      accessorKey: "id",
      header: "Upload",
      cell: ({ row }) => (
        <span className="font-mono text-xs" title={row.original.id}>
          {row.original.id}
        </span>
      ),
    },
    {
      id: "size",
      header: "Size",
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted">{(Number(row.original.size_bytes ?? 0) / 1024).toFixed(1)} KiB</span>
      ),
    },
    {
      accessorKey: "digest",
      header: "Digest",
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted" title={row.original.digest}>
          {row.original.digest?.slice(0, 16)}…
        </span>
      ),
    },
    {
      accessorKey: "created_at",
      header: "Created",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          <RelativeTime value={row.original.created_at} />
        </span>
      ),
    },
    {
      id: "referenced_by",
      header: "Referenced by",
      cell: ({ row }) => {
        // 异步反查占位是块外槽（页面列 cell 自绘——kit 块无此槽）
        if (specsPending) return <span className="text-xs text-muted">…</span>;
        const users = (uploadIndex.get(row.original.id ?? "") ?? []).map(appName);
        if (users.length === 0) return <span className="text-xs text-muted">—</span>;
        return (
          <span className="flex items-center gap-1 text-xs text-muted">
            <LinkIcon className="size-3" />
            {users.join(", ")}
          </span>
        );
      },
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <ListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-3">
            <FilterBar list={list} schema={uploadsSearch} textPlaceholder="Filter uploads..." />
            <span className="hidden text-xs text-muted lg:inline">deterministic tar · .git skipped · content-addressed</span>
          </div>
          <div className="py-2 pr-3">
            <input ref={inputRef} type="file" multiple className="hidden" onChange={(event) => void onFiles(event.target.files)} />
            <Button size="sm" disabled={pending} onClick={() => inputRef.current?.click()}>
              <PlusIcon data-icon-start-inline />
              {pending ? "Uploading…" : "Upload directory…"}
            </Button>
          </div>
        </div>
      }
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No uploads match the filter" : "No uploads"}
      emptyDescription={
        filtered ? "Adjust or clear the filter to see rows again." : "Pick a directory to build a deployable source."
      }
      appendix={
        <>
          <CliEquivalent command={`fleetly uploads list --project ${projectId}`} />
          {pending ? <p className="text-xs text-muted">Packing & uploading…</p> : null}
          {error != null ? <FormError error={error} /> : null}
          {result != null ? <p className="text-xs text-muted">{result}</p> : null}
          <p className="text-xs text-muted">
            Upload references resolve from frozen app specs — they go live on the app's next deploy.
          </p>
        </>
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
    </ListPage>
  );
}
