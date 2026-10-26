import { useState, useEffect, useRef } from "react";
import { LinkIcon, PencilIcon, PlusIcon } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { toast } from "sonner";
import { useCollection } from "@lynx-go/uikit-core";
import { useApps, useDatabases, useNetworks, useProjects } from "@/lib/catalog";
import { apiSend, apiSendRaw, apiFetch } from "@/api/client";
import { useApiMutation } from "@/lib/use-api-mutation";
import { describeError } from "@/lib/api-errors";
import { shortId } from "@/lib/format";
import { specIndex, useAppSpecs } from "@/features/spec/use-app-specs";
import { buildTar, rootPrefixOf } from "@/lib/tar";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage as FleetListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import {
  networkPeersResource,
  networksResource,
  networksSearch,
  type Network,
  type NetworkPeer,
} from "@/kit/networks";
import { routesResource, routesSearch, type Route } from "@/kit/routes";
import { storageSearch, uploadsResource, volumesResource, type Upload, type Volume } from "@/kit/storage";
import {
  configsResource,
  secretsResource,
  sharedVariablesResource,
  variablesSearch,
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

// 资源面板族（UI v2 批 4 → 模板化收编 ListCard → useCollection 化批 3
// 收编 kit 块）：自旧 Resources.tsx 八 tab 搬迁——走查过的 mutation 语义
// 零漂移（创建/变更/删除/peer 审批/目录 tar 上传）。列表读面走 kit 资源
// （useCollection + ListPage × FilterBar × DataTable × Pagination，快筛/
// 分页/排序进 URL）；写面（useApiMutation/弹窗/ConfirmDialog/上传流）
// 保持本仓自持待批 2 迁移。 Mounted by / Referenced by 反查列的异步占
// 位是块外槽（页面列 cell 自绘）。DatabasesPanel 已随 Databases 一级页
// （features/databases）消亡删除。

// RowError 是行内动作失败的披露行（v2 口径：分状态文案单源）。
function RowError({ error }: { error: unknown }) {
  const described = describeError(error);
  return (
    <div className="mt-1 text-2xs text-[var(--status-danger)]">
      {described.title} — <span className="font-mono">{described.detail}</span>
    </div>
  );
}

// FormError 是弹窗表单的提交错误面（danger 信封盒，describeError 单源）。
function FormError({ error }: { error: unknown }) {
  return (
    <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 font-mono text-2xs break-all text-muted-foreground">
      {describeError(error).detail}
    </div>
  );
}

// ---- networks ----

// NetworksPanel：网络表 + rebuild 行内动词（kit 化：快筛/分页/排序进
// URL，15s 轮询沿用）。
export function NetworksPanel({ projectId, routeId }: { projectId: string; routeId: string }) {
  return <NetworksTab projectId={projectId} routeId={routeId} />;
}

// NewNetworkButton 是 Networks 工具栏主动作（弹窗与 mutation 自包含）。
export function NewNetworkButton({ projectId }: { projectId: string }) {
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [egressNone, setEgressNone] = useState(false);
  const create = useApiMutation({
    path: "/v1/networks",
    method: "POST",
    body: () => ({ project_id: projectId, name, egress_none: egressNone || undefined }),
    invalidate: [["resources", "networks", projectId]],
  });
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          setName("");
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
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
            }}
          >
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Name</span>
              <Input value={name} onChange={(event) => setName(event.target.value)} autoFocus />
            </Label>
            <label className="flex items-center gap-2 text-xs text-muted-foreground">
              <input
                type="checkbox"
                checked={egressNone}
                onChange={(event) => setEgressNone(event.target.checked)}
              />
              Egress none (isolated network)
            </label>
            {create.isError ? <FormError error={create.error} /> : null}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending || name === ""}>
                {create.isPending ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </form>
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
        <span className="font-mono text-xs text-muted-foreground" title={row.original.id}>
          {shortId(row.original.id)}
        </span>
      ),
    },
    {
      accessorKey: "egress_none",
      header: "Egress",
      cell: ({ row }) => <span className="text-xs text-muted-foreground">{row.original.egress_none ? "none (isolated)" : "open"}</span>,
    },
    {
      accessorKey: "created_at",
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
      cell: ({ row }) => <NetworkActionsCell networkId={row.original.id} />,
    },
  ];

  // 源空与过滤空两套文案（旧页 filterEmpty 契约）：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <FleetListPage
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
    </FleetListPage>
  );
}

// NetworkActionsCell 是行内 rebuild 动词（失败披露在钮下——走查语义原样）。
function NetworkActionsCell({ networkId }: { networkId?: string }) {
  const rebuild = useApiMutation({
    path: "/v1/networks/rebuild",
    method: "POST",
    body: () => ({ network_id: networkId }),
  });
  return (
    <div className="text-right">
      <Button
        variant="outline"
        size="sm"
        disabled={rebuild.isPending}
        onClick={() => void rebuild.mutate()}
        title="Rebuild the carrier network (minute-scale)"
      >
        rebuild
      </Button>
      {rebuild.isError ? <RowError error={rebuild.error} /> : null}
    </div>
  );
}

// PeersPanel 是 Networks 页 Peers tab（跨项目挂靠面：审批表 + 声明表单）。
// 本批收编 kit 列表（resource networkPeers + DataTable，原自绘表退役）；
// approve/revoke 动词与 DeclarePeerForm 写面不动。
export function PeersPanel({ projectId, routeId }: { projectId: string; routeId: string }) {
  const list = useCollection(networkPeersResource, {
    from: routeId,
    search: networksSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  // DeclarePeerForm 的网络下拉数据（写面伴读，不经 kit 集合）。
  const networks = useNetworks(projectId);

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
        <span className="font-mono text-xs text-muted-foreground" title={row.original.peer_project_id}>
          {shortId(row.original.peer_project_id)}
        </span>
      ),
    },
    {
      accessorKey: "state",
      header: "State",
      cell: ({ row }) => <span className="text-xs text-muted-foreground">{row.original.state}</span>,
    },
    {
      id: "id",
      header: "ID",
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground" title={row.original.id}>
          {shortId(row.original.id)}
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <PeerActionsCell peer={row.original} />,
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <section className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">
        Cross-project network attachment: the attaching project declares, the network owner approves.
      </p>
      <FleetListPage
        list={list}
        toolbar={<FilterBar list={list} schema={networksSearch} textPlaceholder="Filter peers..." />}
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No peers match the filter" : "No peers declared"}
        emptyDescription={
          filtered ? "Adjust or clear the filter to see rows again." : "Declare a cross-project attachment below."
        }
      >
        <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
      </FleetListPage>
      <DeclarePeerForm projectId={projectId} networks={networks.data ?? []} />
    </section>
  );
}

// PeerActionsCell：行内 approve/revoke（失败披露在钮下——走查语义原样；
// revoke 经 ConfirmDialog 确认）。
function PeerActionsCell({ peer }: { peer: NetworkPeer }) {
  const approve = useApiMutation({
    path: `/v1/networks/peers/${encodeURIComponent(peer.id ?? "")}/approve`,
    method: "POST",
  });
  const revoke = useApiMutation({
    path: `/v1/networks/peers/${encodeURIComponent(peer.id ?? "")}/revoke`,
    method: "POST",
  });
  const [revoking, setRevoking] = useState(false);
  return (
    <div className="flex flex-col items-end">
      <div className="flex justify-end gap-1">
        <Button variant="outline" size="sm" disabled={approve.isPending} onClick={() => void approve.mutate()}>
          approve
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="text-[var(--status-danger)]"
          disabled={revoke.isPending}
          onClick={() => setRevoking(true)}
        >
          revoke
        </Button>
      </div>
      {approve.isError ? <RowError error={approve.error} /> : null}
      {revoke.isError ? <RowError error={revoke.error} /> : null}
      <ConfirmDialog
        open={revoking}
        onOpenChange={setRevoking}
        title="Revoke this peer?"
        description="The cross-project attachment is detached immediately."
        confirmLabel="Revoke"
        onConfirm={() => revoke.mutateAsync()}
      />
    </div>
  );
}

function DeclarePeerForm({ projectId, networks }: { projectId: string; networks: Array<{ id?: string; name?: string }> }) {
  const projects = useProjects();
  const [networkId, setNetworkId] = useState("");
  const [peerProject, setPeerProject] = useState("");
  const declare = useApiMutation({
    path: "/v1/networks/peers",
    method: "POST",
    body: () => ({ network_id: networkId, project_id: projectId, peer_project_id: peerProject }),
    invalidate: [["resources", "network-peers", projectId]],
  });
  return (
    <form
      className="flex flex-wrap items-end gap-2 rounded-lg border px-3 py-2"
      onSubmit={(event) => {
        event.preventDefault();
        declare.mutate(undefined, { onSuccess: () => declare.reset() });
      }}
    >
      <Label className="flex flex-col gap-1 text-xs">
        <span className="font-medium text-foreground">Declare peer — network</span>
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
        <span className="font-medium text-foreground">Peer project (owner of the network you want to attach)</span>
        <Select value={peerProject} onValueChange={setPeerProject}>
          <SelectTrigger className="h-8 w-44">
            <SelectValue placeholder="select…" />
          </SelectTrigger>
          <SelectContent>
            {(projects.data ?? [])
              .filter((project) => project.id !== projectId)
              .map((project) => (
                <SelectItem key={project.id} value={project.id}>
                  {project.name}
                </SelectItem>
              ))}
          </SelectContent>
        </Select>
      </Label>
      <Button type="submit" size="sm" disabled={declare.isPending || networkId === "" || peerProject === ""}>
        declare
      </Button>
      {declare.isPending ? <span className="text-xs text-muted-foreground">Working…</span> : null}
      {declare.isError ? <FormError error={declare.error} /> : null}
      {declare.isSuccess ? <span className="text-xs text-[var(--status-success)]">peer declared</span> : null}
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

// NewRouteButton 是 Routes 工具栏主动作；项目尚无 App 时诚实禁用。
export function NewRouteButton({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
  const [createOpen, setCreateOpen] = useState(false);
  const [form, setForm] = useState({ host: "", path: "", app: apps[0]?.id ?? "", process: "web", port: "8080", protocol: "http", tls: "auto" });
  const create = useApiMutation({
    path: "/v1/routes",
    method: "POST",
    body: () => ({
      project_id: projectId,
      host: form.host,
      path: form.path || undefined,
      app_id: form.app,
      process: form.process,
      port: Number(form.port),
      protocol: form.protocol,
      tls_mode: form.tls,
    }),
    invalidate: [["resources", "routes", projectId]],
  });
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          setForm((prev) => ({ ...prev, app: apps[0]?.id ?? prev.app }));
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
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
            }}
          >
            <Label className="flex flex-col gap-1.5">
              <span className="text-xs font-semibold">Host</span>
              <Input value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} autoFocus />
              <span className="text-2xs font-normal text-muted-foreground">e.g. shop.203.0.113.10.sslip.io</span>
            </Label>
            <div className="grid grid-cols-2 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Path</span>
                <Input value={form.path} onChange={(event) => setForm({ ...form, path: event.target.value })} placeholder="/" />
                <span className="text-2xs font-normal text-muted-foreground">optional prefix</span>
              </Label>
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">TLS</span>
                <Select value={form.tls} onValueChange={(value) => setForm({ ...form, tls: value })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="auto">auto</SelectItem>
                    <SelectItem value="none">none</SelectItem>
                  </SelectContent>
                </Select>
              </Label>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">App</span>
                <Select value={form.app} onValueChange={(value) => setForm({ ...form, app: value })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {apps.map((app) => (
                      <SelectItem key={app.id} value={app.id}>
                        {app.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Label>
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Process</span>
                <Input value={form.process} onChange={(event) => setForm({ ...form, process: event.target.value })} />
              </Label>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Port</span>
                <Input value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value })} inputMode="numeric" />
              </Label>
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Protocol</span>
                <Select value={form.protocol} onValueChange={(value) => setForm({ ...form, protocol: value })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="http">http</SelectItem>
                    <SelectItem value="h2c">h2c</SelectItem>
                    <SelectItem value="tcp">tcp</SelectItem>
                  </SelectContent>
                </Select>
              </Label>
            </div>
            {create.isError ? <FormError error={create.error} /> : null}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending || form.host === "" || form.app === ""}>
                {create.isPending ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </form>
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
      cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.path || "—"}</span>,
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
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.port} {row.original.protocol ? `(${row.original.protocol})` : ""}
        </span>
      ),
    },
    {
      accessorKey: "tls_mode",
      header: "TLS",
      cell: ({ row }) => <span className="text-xs text-muted-foreground">{row.original.tls_mode}</span>,
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
    <FleetListPage
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
    </FleetListPage>
  );
}

function RouteActionsCell({ routeId, host, projectId }: { routeId?: string; host?: string; projectId: string }) {
  const [deleting, setDeleting] = useState(false);
  const del = useApiMutation({
    path: `/v1/routes/${encodeURIComponent(routeId ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "routes", projectId]],
  });
  return (
    <div className="text-right">
      <Button
        variant="outline"
        size="sm"
        className="text-[var(--status-danger)]"
        disabled={del.isPending}
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
        onConfirm={() => del.mutateAsync()}
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
    search: storageSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const apps = useApps(projectId);
  const databases = useDatabases(projectId);
  const { specs, pending: specsPending } = useAppSpecs(projectId, apps.data ?? []);
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [pinnedNode, setPinnedNode] = useState("");
  const create = useApiMutation({
    path: "/v1/volumes",
    method: "POST",
    body: () => ({ project_id: projectId, name, pinned_node_id: pinnedNode || undefined }),
    invalidate: [["resources", "volumes", projectId]],
  });
  // 挂载判据（IA v3 二期⑤b）：卷锚 = 平台卷名（App 冻结 Spec 的卷附件
  // volume_id 与 Database 挂靠卷名公式都按 Name——引擎同口径；W-7 对齐）。
  const volumeIndex = specIndex(specs, (spec) =>
    (spec.processes ?? []).flatMap((process) => (process.volumes ?? []).map((volume) => volume.volume_id ?? "")),
  );
  const appName = (id: string) => (apps.data ?? []).find((app) => app.id === id)?.name ?? id;
  const mountOf = (volume: { name?: string }): { carrier: boolean; users: string[] } => {
    const users = (volumeIndex.get(volume.name ?? "") ?? []).map(appName);
    const carried = (databases.data ?? []).some((database) => database.name === volume.name);
    return { carrier: carried, users: carried ? [] : users };
  };
  const openCreate = () => {
    setName("");
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
      cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.pinned_node_id || "any"}</span>,
    },
    {
      id: "mounted_by",
      header: "Mounted by",
      cell: ({ row }) => {
        // 异步反查占位是块外槽（页面列 cell 自绘——kit 块无此槽）
        if (specsPending) return <span className="text-xs text-muted-foreground">…</span>;
        const mount = mountOf(row.original);
        if (mount.carrier) {
          return (
            <span className="flex items-center gap-1 text-xs text-muted-foreground">
              <LinkIcon className="size-3" />
              database {row.original.name} (carrier data volume)
            </span>
          );
        }
        if (mount.users.length > 0) {
          return (
            <span className="flex items-center gap-1 text-xs text-muted-foreground">
              <LinkIcon className="size-3" />
              {mount.users.join(", ")}
            </span>
          );
        }
        return <span className="text-xs text-muted-foreground">—</span>;
      },
    },
    {
      accessorKey: "created_at",
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
    <FleetListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={storageSearch} textPlaceholder="Filter volumes..." />
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
              <form
                className="flex flex-col gap-3"
                onSubmit={(event) => {
                  event.preventDefault();
                  create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
                }}
              >
                <Label className="flex flex-col gap-1.5">
                  <span className="text-xs font-semibold">Name</span>
                  <Input value={name} onChange={(event) => setName(event.target.value)} autoFocus />
                </Label>
                <Label className="flex flex-col gap-1.5">
                  <span className="text-xs font-semibold">Pinned node</span>
                  <Input value={pinnedNode} onChange={(event) => setPinnedNode(event.target.value)} />
                  <span className="text-2xs font-normal text-muted-foreground">optional platform node id — volume stays on that node</span>
                </Label>
                {create.isError ? <FormError error={create.error} /> : null}
                <DialogFooter>
                  <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                    Cancel
                  </Button>
                  <Button type="submit" disabled={create.isPending || name === ""}>
                    {create.isPending ? "Creating…" : "Create"}
                  </Button>
                </DialogFooter>
              </form>
            </DialogContent>
          </Dialog>
          <p className="text-xs text-muted-foreground">
            Mounts resolve from frozen app specs — new references go live on the app's next deploy.
          </p>
        </>
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
    </FleetListPage>
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
  const del = useApiMutation({
    path: `/v1/volumes/${encodeURIComponent(volume.id ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "volumes", projectId]],
  });
  if (mounted) {
    return null;
  }
  return (
    <div className="text-right">
      <AlertDialog>
        <AlertDialogTrigger asChild>
          <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" disabled={del.isPending}>
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
              disabled={del.isPending}
              onClick={(event) => {
                event.preventDefault();
                del.mutate(undefined, { onSuccess: () => toast(`Volume ${volume.name} deleted`) });
              }}
            >
              {del.isPending ? "Deleting…" : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ---- secrets / configs / shared variables（put 型写面；列表读面本批
// kit 化，三 tab 共享 variablesSearch，均 15s 轮询沿用） ----

// PutForm 是 Add 型写面的统一弹窗（secret/config/shared variable 共用，
// API 动词是 PUT——组件名承动词面，按钮文案统一 Add *，用户裁决 2026-10-11）：
// 名称 + 值（或 multiline 内容），错误信封盒披露）。
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
  onSubmit: (name: string, value: string) => Promise<unknown>;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          setName("");
          setValue("");
          setError(null);
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
              setError(null);
              try {
                await onSubmit(name, value);
                setOpen(false);
              } catch (cause) {
                setError(cause);
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
              {valueHint ? <span className="text-2xs font-normal text-muted-foreground">{valueHint}</span> : null}
            </Label>
            {error !== null ? <FormError error={error} /> : null}
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
    search: variablesSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });

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
        <span className="text-xs text-muted-foreground">
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
    <FleetListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={variablesSearch} textPlaceholder="Filter secrets..." />
          <div className="py-2 pr-3">
            <PutForm
              title="Add secret…"
              nameLabel="Name"
              valueLabel="Value"
              valueHint="write-only — the list never shows values"
              onSubmit={(name, value) => apiSend("/v1/secrets", "PUT", { project_id: projectId, name, value })}
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
    </FleetListPage>
  );
}

function SecretActionsCell({ name, projectId }: { name?: string; projectId: string }) {
  const [deleting, setDeleting] = useState(false);
  const del = useApiMutation({
    path: `/v1/secrets/${encodeURIComponent(projectId)}/${encodeURIComponent(name ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "secrets", projectId]],
  });
  return (
    <div className="text-right">
      <Button
        variant="outline"
        size="sm"
        className="text-[var(--status-danger)]"
        disabled={del.isPending}
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
        onConfirm={() => del.mutateAsync()}
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
    search: variablesSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const [selected, setSelected] = useState<Config | null>(null);

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
      cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.version ?? "—"}</span>,
    },
    {
      id: "updated",
      header: "Updated",
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
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
    <FleetListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={variablesSearch} textPlaceholder="Filter configs..." />
          <div className="py-2 pr-3">
            <PutForm
              title="Add config…"
              nameLabel="Name"
              valueLabel="Content"
              valueHint="each write freezes a new version"
              multiline
              onSubmit={(name, content) => apiSend("/v1/configs", "PUT", { project_id: projectId, name, content })}
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
    </FleetListPage>
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
  const detail = useQuery({
    queryKey: ["resources", "config", projectId, name],
    enabled: name !== "",
    queryFn: async () => {
      const res = await apiFetch<{ config?: { version?: string; created_at?: string; content?: string } }>(
        `/v1/configs/${encodeURIComponent(projectId)}/${encodeURIComponent(name)}`,
      );
      return res.config;
    },
  });
  const [editing, setEditing] = useState(false);
  const [content, setContent] = useState("");
  const save = useApiMutation<{ config?: { version?: string } }>({
    path: "/v1/configs",
    method: "PUT",
    body: () => ({ project_id: projectId, name, content }),
    invalidate: [
      ["resources", "configs", projectId],
      ["resources", "config", projectId, name],
    ],
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
              save.mutate(undefined, {
                onSuccess: (res) => {
                  setEditing(false);
                  toast(`${name} saved as version ${res.config?.version ?? "?"}`);
                },
              });
            }}
          >
            <Textarea
              rows={16}
              className="font-mono text-xs"
              value={content}
              onChange={(event) => setContent(event.target.value)}
              autoFocus
            />
            {save.isError ? <FormError error={save.error} /> : null}
            <DialogFooter>
              <Button type="button" variant="outline" disabled={save.isPending} onClick={() => setEditing(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={save.isPending || content === (detail.data?.content ?? "")}>
                {save.isPending ? "Saving…" : "Save as new version"}
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <>
            <p className="text-xs text-muted-foreground">
              version <span className="font-mono">{detail.data?.version ?? "…"}</span>
              {" · updated "}
              <RelativeTime value={detail.data?.created_at} />
              {" · "}
              {new TextEncoder().encode(detail.data?.content ?? "").length} bytes
            </p>
            {detail.isPending ? (
              <Skeleton className="h-48 w-full" />
            ) : detail.isError ? (
              <FormError error={detail.error} />
            ) : (
              <pre className="max-h-[50vh] overflow-auto rounded-lg border bg-panel-2/40 p-3 font-mono text-xs break-all whitespace-pre-wrap">
                {detail.data?.content}
              </pre>
            )}
            {detail.isError ? (
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
                  disabled={detail.isPending || detail.isError}
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
    search: variablesSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const [banner, setBanner] = useState<string | null>(null);
  const [bannerError, setBannerError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  async function put(name: string, value: string) {
    setPending(true);
    setBanner(null);
    setBannerError(null);
    try {
      const res = await apiSend<{ affected_apps?: Array<string | undefined> }>("/v1/shared-variables", "PUT", {
        project_id: projectId,
        name,
        value,
      });
      const affected = (res.affected_apps ?? []).flatMap((id: string | undefined) =>
        id ? [apps.find((app) => app.id === id)?.name ?? id.slice(0, 10)] : [],
      );
      setBanner(
        affected.length === 0
          ? `${name} saved — no app references it yet`
          : `${name} saved — redeploy to pick it up: ${affected.join(",")}`,
      );
    } catch (cause) {
      setBannerError(cause);
    } finally {
      setPending(false);
    }
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
        <span className="block max-w-xs truncate font-mono text-xs text-muted-foreground" title={row.original.value}>
          {row.original.value}
        </span>
      ),
    },
    {
      accessorKey: "updated_at",
      header: "Updated",
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
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
    <FleetListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FilterBar list={list} schema={variablesSearch} textPlaceholder="Filter variables..." />
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
          {pending ? <p className="text-xs text-muted-foreground">Working…</p> : null}
          {bannerError != null ? <FormError error={bannerError} /> : null}
          {banner ? <p className="text-xs text-[var(--status-success)]">{banner}</p> : null}
        </>
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? row.name ?? ""} />
    </FleetListPage>
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
  const [error, setError] = useState<unknown>(null);
  const [deleting, setDeleting] = useState(false);
  const del = useApiMutation<{ affected_apps?: Array<string | undefined> }>({
    path: `/v1/shared-variables/${encodeURIComponent(projectId)}/${encodeURIComponent(row.name ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "shared-variables", projectId]],
  });
  return (
    <div className="text-right align-top">
      <Button
        variant="outline"
        size="sm"
        className="text-[var(--status-danger)]"
        disabled={del.isPending}
        onClick={() => setDeleting(true)}
      >
        delete
      </Button>
      {result ? <div className="mt-1 text-2xs text-[var(--status-success)]">{result}</div> : null}
      {error != null ? <RowError error={error} /> : null}
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete shared variable ${row.name ?? ""}?`}
        description="Apps referencing it keep the last merged value until the next deploy freeze."
        confirmLabel="Delete variable"
        onConfirm={() =>
          del.mutateAsync().then((response) => {
            const affected = response?.affected_apps ?? [];
            setResult(
              affected.length === 0
                ? `${row.name} deleted`
                : `${row.name} deleted — redeploy to pick it up: ${affected
                    .flatMap((id) => (id ? [apps.find((app) => app.id === id)?.name ?? id.slice(0, 10)] : []))
                    .join(",")}`,
            );
            setError(null);
          })
        }
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
    search: storageSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const apps = useApps(projectId);
  const { specs, pending: specsPending } = useAppSpecs(projectId, apps.data ?? []);
  const inputRef = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<string | null>(null);

  // upload → apps 引用反查（原 UsageIndex Upload→apps 面收编入列）：扫
  // App 冻结 Spec 的 source.upload.id（客户端 fan-out，ADR-0057 惯例）。
  const uploadIndex = specIndex(specs, (spec) => (spec.source?.upload?.id ? [spec.source.upload.id] : []));
  const appName = (id: string) => (apps.data ?? []).find((app) => app.id === id)?.name ?? id;

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
        <span className="font-mono text-xs text-muted-foreground">{(Number(row.original.size_bytes ?? 0) / 1024).toFixed(1)} KiB</span>
      ),
    },
    {
      accessorKey: "digest",
      header: "Digest",
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground" title={row.original.digest}>
          {row.original.digest?.slice(0, 16)}…
        </span>
      ),
    },
    {
      accessorKey: "created_at",
      header: "Created",
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          <RelativeTime value={row.original.created_at} />
        </span>
      ),
    },
    {
      id: "referenced_by",
      header: "Referenced by",
      cell: ({ row }) => {
        // 异步反查占位是块外槽（页面列 cell 自绘——kit 块无此槽）
        if (specsPending) return <span className="text-xs text-muted-foreground">…</span>;
        const users = (uploadIndex.get(row.original.id ?? "") ?? []).map(appName);
        if (users.length === 0) return <span className="text-xs text-muted-foreground">—</span>;
        return (
          <span className="flex items-center gap-1 text-xs text-muted-foreground">
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
    <FleetListPage
      list={list}
      toolbar={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-3">
            <FilterBar list={list} schema={storageSearch} textPlaceholder="Filter uploads..." />
            <span className="hidden text-xs text-muted-foreground lg:inline">deterministic tar · .git skipped · content-addressed</span>
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
          {pending ? <p className="text-xs text-muted-foreground">Packing & uploading…</p> : null}
          {error != null ? <FormError error={error} /> : null}
          {result != null ? <p className="text-xs text-muted-foreground">{result}</p> : null}
          <p className="text-xs text-muted-foreground">
            Upload references resolve from frozen app specs — they go live on the app's next deploy.
          </p>
        </>
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
    </FleetListPage>
  );
}
