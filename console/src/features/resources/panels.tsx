import { useEffect, useRef, useState } from "react";
import { PlusIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { toast } from "sonner";
import {
  useApps,
  useConfigs,
  useDatabases,
  useNetworkPeers,
  useNetworks,
  useProjects,
  useRoutes,
  useSecrets,
  useSharedVariables,
  useUploads,
  useVolumes,
} from "@/lib/catalog";
import { apiSend, apiSendRaw } from "@/api/client";
import { useApiMutation } from "@/lib/use-api-mutation";
import { describeError } from "@/lib/api-errors";
import { shortId } from "@/lib/format";
import { specIndex, useAppSpecs } from "@/features/spec/use-app-specs";
import { buildTar, rootPrefixOf } from "@/lib/tar";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { ListCard } from "@/components/domain/list-page";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";

// 资源面板族（UI v2 批 4 → 模板化收编 ListCard）：自旧 Resources.tsx 八
// tab 搬迁——走查过的 mutation 语义零漂移（创建/变更/删除/peer 审批/
// 备份 verify+restore/browse/目录 tar 上传）。表格四态/工具栏/分页/CLI
// 行收口 ListCard；跨行展开特例（数据库备份、restore 表单行走 children
// 渲染槽）。pre-v2 共享层（components/ui.tsx）脱钩退役。
// DatabasesPanel 已随 Databases 一级页（features/databases）消亡删除。

// RowError 是行内动作失败的披露行（v2 口径：分状态文案单源）。
function RowError({ error }: { error: unknown }) {
  const described = describeError(error);
  return (
    <div className="mt-1 text-[11px] text-[var(--status-danger)]">
      {described.title} — <span className="font-mono">{described.detail}</span>
    </div>
  );
}

// FormError 是弹窗表单的提交错误面（danger 信封盒，describeError 单源）。
function FormError({ error }: { error: unknown }) {
  return (
    <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 font-mono text-[11px] break-all text-muted-foreground">
      {describeError(error).detail}
    </div>
  );
}

// ---- networks ----

// NetworksPanel：网络表 + rebuild 行内动词。
export function NetworksPanel({ projectId }: { projectId: string }) {
  return <NetworksTab projectId={projectId} />;
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

function NetworksTab({ projectId }: { projectId: string }) {
  const networks = useNetworks(projectId);

  const columns: LegacyColumnDef<{ id?: string; name?: string; egress_none?: boolean; created_at?: string }, any>[] = [
    { accessorKey: "name", header: "Name", cell: ({ row }) => <span className="font-medium">{row.original.name}</span> },
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

  return (
    <section className="flex flex-col gap-3">
      <ListCard
        label="networks"
        placeholder="Filter networks..."
        rows={networks.data ?? []}
        fieldsOf={(network) => [network.name ?? "", network.id ?? ""]}
        loading={networks.isPending}
        error={networks.isError ? networks.error : null}
        onRetry={() => void networks.refetch()}
        columns={columns}
        cli={`fleetly networks list --project ${projectId}`}
        actions={<NewNetworkButton projectId={projectId} />}
      />
    </section>
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
export function PeersPanel({ projectId }: { projectId: string }) {
  const peers = useNetworkPeers(projectId);
  const networks = useNetworks(projectId);
  return (
    <section className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">
        Cross-project network attachment: the attaching project declares, the network owner approves.
      </p>
      {peers.isPending ? (
        <Skeleton className="h-16 w-full" />
      ) : peers.isError ? (
        <ErrorState error={peers.error} onRetry={() => void peers.refetch()} />
      ) : (peers.data ?? []).length === 0 ? (
        <EmptyState icon={PlusIcon} title="No peers declared" description="Declare a cross-project attachment below." />
      ) : (
        <div className="overflow-x-auto rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Network</TableHead>
                <TableHead>Peer project</TableHead>
                <TableHead>State</TableHead>
                <TableHead>ID</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {(peers.data ?? []).map((peer) => (
                <PeerRow key={peer.id} peer={peer} />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      <DeclarePeerForm projectId={projectId} networks={networks.data ?? []} />
    </section>
  );
}

function PeerRow({ peer }: { peer: { id?: string; network_id?: string; peer_project_id?: string; state?: string } }) {
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
    <>
      <TableRow>
        <TableCell className="font-mono text-xs" title={peer.network_id}>
          {shortId(peer.network_id)}
        </TableCell>
        <TableCell className="font-mono text-xs text-muted-foreground" title={peer.peer_project_id}>
          {shortId(peer.peer_project_id)}
        </TableCell>
        <TableCell className="text-xs text-muted-foreground">{peer.state}</TableCell>
        <TableCell className="font-mono text-xs text-muted-foreground" title={peer.id}>
          {shortId(peer.id)}
        </TableCell>
        <TableCell className="text-right">
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
        </TableCell>
      </TableRow>
      <ConfirmDialog
        open={revoking}
        onOpenChange={setRevoking}
        title="Revoke this peer?"
        description="The cross-project attachment is detached immediately."
        confirmLabel="Revoke"
        onConfirm={() => revoke.mutateAsync()}
      />
    </>
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

// RoutesPanel：host → app/process/port 路由表 + 创建。
export function RoutesPanel({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
  return <RoutesTab projectId={projectId} apps={apps} />;
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
              <span className="text-[11px] font-normal text-muted-foreground">e.g. shop.203.0.113.10.sslip.io</span>
            </Label>
            <div className="grid grid-cols-2 gap-3">
              <Label className="flex flex-col gap-1.5">
                <span className="text-xs font-semibold">Path</span>
                <Input value={form.path} onChange={(event) => setForm({ ...form, path: event.target.value })} placeholder="/" />
                <span className="text-[11px] font-normal text-muted-foreground">optional prefix</span>
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

function RoutesTab({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
  const routes = useRoutes(projectId);

  const columns: LegacyColumnDef<{ id?: string; host?: string; path?: string; app_id?: string; process?: string; port?: number; protocol?: string; tls_mode?: string }, any>[] = [
    { accessorKey: "host", header: "Host", cell: ({ row }) => <span className="font-mono">{row.original.host}</span> },
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

  return (
    <section className="flex flex-col gap-3">
      <ListCard
        label="routes"
        placeholder="Filter routes..."
        rows={routes.data ?? []}
        fieldsOf={(route) => [route.host ?? "", route.app_id ?? "", route.process ?? ""]}
        loading={routes.isPending}
        error={routes.isError ? routes.error : null}
        onRetry={() => void routes.refetch()}
        columns={columns}
        cli={`fleetly routes list --project ${projectId}`}
        actions={<NewRouteButton projectId={projectId} apps={apps} />}
      />
    </section>
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
// 确认 + token 类；mutation 语义自批 4 零漂移） ----

export function VolumesPanel({ projectId }: { projectId: string }) {
  return <VolumesTab projectId={projectId} />;
}

function VolumesTab({ projectId }: { projectId: string }) {
  const volumes = useVolumes(projectId);
  const apps = useApps(projectId);
  const databases = useDatabases(projectId);
  const { specs } = useAppSpecs(projectId, apps.data ?? []);
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
  // volume_id 与 Database 挂靠卷名公式都按 Name——引擎同口径）。
  const volumeIndex = specIndex(specs, (spec) =>
    (spec.processes ?? []).flatMap((process) => (process.volumes ?? []).map((volume) => volume.volume_id ?? "")),
  );
  const appName = (id: string) => (apps.data ?? []).find((app) => app.id === id)?.name ?? id;
  const mountOf = (volume: { name?: string }): string | null => {
    const users = volumeIndex.get(volume.name ?? "") ?? [];
    const db = (databases.data ?? []).some((database) => database.name === volume.name);
    if (db) return "carrier data volume of database " + volume.name;
    if (users.length > 0) return "mounted by " + users.map(appName).join(",");
    return null;
  };

  const columns: LegacyColumnDef<{ id?: string; name?: string; pinned_node_id?: string; created_at?: string }, any>[] = [
    { accessorKey: "name", header: "Name", cell: ({ row }) => <span className="font-medium">{row.original.name}</span> },
    {
      accessorKey: "pinned_node_id",
      header: "Pinned node",
      cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.pinned_node_id || "any"}</span>,
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
      cell: ({ row }) => <VolumeActionsCell volume={row.original} mount={mountOf(row.original)} projectId={projectId} />,
    },
  ];

  return (
    <section className="flex flex-col gap-3">
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
              <span className="text-[11px] font-normal text-muted-foreground">optional platform node id — volume stays on that node</span>
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
      <ListCard
        label="volumes"
        placeholder="Filter volumes..."
        rows={volumes.data ?? []}
        fieldsOf={(volume) => [volume.name ?? "", volume.id ?? ""]}
        loading={volumes.isPending}
        error={volumes.isError ? volumes.error : null}
        onRetry={() => void volumes.refetch()}
        columns={columns}
        actions={
          <Button
            size="sm"
            onClick={() => {
              setName("");
              setCreateOpen(true);
            }}
          >
            <PlusIcon data-icon-start-inline />
            New volume…
          </Button>
        }
        empty={
          <EmptyState
            icon={PlusIcon}
            title="No volumes"
            description="Create one to attach it from an app's Variables tab."
            actionLabel="New volume…"
            onAction={() => {
              setName("");
              setCreateOpen(true);
            }}
          />
        }
      />
    </section>
  );
}

// VolumeActionsCell：未挂载才可删（挂载判据由服务端 E_CONFLICT 兜底——
// 客户端禁用是前置体验面）；删除仅收口平台行，底层 runtime 卷留存（数据
// 兜底永不级联），AlertDialog 确认文案明示。
function VolumeActionsCell({
  volume,
  mount,
  projectId,
}: {
  volume: { id?: string; name?: string };
  mount: string | null;
  projectId: string;
}) {
  const del = useApiMutation({
    path: `/v1/volumes/${encodeURIComponent(volume.id ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "volumes", projectId]],
  });
  if (mount) {
    return (
      <div className="text-right">
        <span className="text-[11px] text-muted-foreground" title={mount}>
          in use
        </span>
      </div>
    );
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

// ---- secrets / configs / shared variables（put 型写面） ----

interface KeyValueRow {
  id?: string;
  name?: string;
  created_at?: string;
  updated_at?: string;
  version?: string;
}

// PutForm 是 put 型写面的统一弹窗（secret/config/shared variable 共用——
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
              {valueHint ? <span className="text-[11px] font-normal text-muted-foreground">{valueHint}</span> : null}
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

export function SecretsPanel({ projectId }: { projectId: string }) {
  return <SecretsTab projectId={projectId} />;
}

function SecretsTab({ projectId }: { projectId: string }) {
  const secrets = useSecrets(projectId);

  const columns: LegacyColumnDef<KeyValueRow, any>[] = [
    { accessorKey: "name", header: "Name", cell: ({ row }) => <span className="font-mono">{row.original.name}</span> },
    {
      id: "updated",
      header: "Updated",
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          <RelativeTime value={row.original.updated_at ?? row.original.created_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <SecretActionsCell name={row.original.name} projectId={projectId} />,
    },
  ];

  return (
    <section className="flex flex-col gap-3">
      <ListCard
        label="secrets"
        placeholder="Filter secrets..."
        rows={secrets.data ?? []}
        fieldsOf={(row) => [row.name ?? ""]}
        loading={secrets.isPending}
        error={secrets.isError ? secrets.error : null}
        onRetry={() => void secrets.refetch()}
        columns={columns}
        cli={`fleetly secrets list --project ${projectId}`}
        actions={
          <PutForm
            title="Put secret…"
            nameLabel="Name"
            valueLabel="Value"
            valueHint="write-only — the list never shows values"
            onSubmit={(name, value) => apiSend("/v1/secrets", "PUT", { project_id: projectId, name, value })}
          />
        }
      />
    </section>
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

export function ConfigsPanel({ projectId }: { projectId: string }) {
  return <ConfigsTab projectId={projectId} />;
}

function ConfigsTab({ projectId }: { projectId: string }) {
  const configs = useConfigs(projectId);

  const columns: LegacyColumnDef<KeyValueRow, any>[] = [
    { accessorKey: "name", header: "Name", cell: ({ row }) => <span className="font-mono">{row.original.name}</span> },
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
  ];

  return (
    <section className="flex flex-col gap-3">
      <ListCard
        label="configs"
        placeholder="Filter configs..."
        rows={configs.data ?? []}
        fieldsOf={(config) => [config.name ?? ""]}
        loading={configs.isPending}
        error={configs.isError ? configs.error : null}
        onRetry={() => void configs.refetch()}
        columns={columns}
        cli={`fleetly configs list --project ${projectId}`}
        actions={
          <PutForm
            title="Put config…"
            nameLabel="Name"
            valueLabel="Content"
            valueHint="each write freezes a new version"
            multiline
            onSubmit={(name, content) => apiSend("/v1/configs", "PUT", { project_id: projectId, name, content })}
          />
        }
      />
    </section>
  );
}

export function VariablesPanel({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
  return <VariablesTab projectId={projectId} apps={apps} />;
}

function VariablesTab({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
  const variables = useSharedVariables(projectId);
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

  const columns: LegacyColumnDef<KeyValueRow & { value?: string }, any>[] = [
    { accessorKey: "name", header: "Name", cell: ({ row }) => <span className="font-mono">{row.original.name}</span> },
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

  return (
    <section className="flex flex-col gap-3">
      {pending ? <p className="text-xs text-muted-foreground">Working…</p> : null}
      {bannerError != null ? <FormError error={bannerError} /> : null}
      {banner ? <p className="text-xs text-[var(--status-success)]">{banner}</p> : null}
      <ListCard
        label="shared variables"
        placeholder="Filter variables..."
        rows={variables.data ?? []}
        fieldsOf={(v) => [v.name ?? ""]}
        loading={variables.isPending}
        error={variables.isError ? variables.error : null}
        onRetry={() => void variables.refetch()}
        columns={columns}
        cli={`fleetly shared-variables list --project ${projectId}`}
        actions={
          <PutForm
            title="Put variable…"
            nameLabel="Name"
            valueLabel="Value"
            valueHint="merged into app env at deploy freeze time"
            onSubmit={put}
          />
        }
      />
    </section>
  );
}

function VariableActionsCell({
  row,
  projectId,
  apps,
}: {
  row: KeyValueRow & { value?: string };
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
      {result ? <div className="mt-1 text-[11px] text-[var(--status-success)]">{result}</div> : null}
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

// ---- uploads ----

export function UploadsPanel({ projectId }: { projectId: string }) {
  return <UploadsTab projectId={projectId} />;
}

function UploadsTab({ projectId }: { projectId: string }) {
  const uploads = useUploads(projectId);
  const inputRef = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<string | null>(null);

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

  const columns: LegacyColumnDef<{ id?: string; digest?: string; size_bytes?: string; created_at?: string }, any>[] = [
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
  ];

  return (
    <section className="flex flex-col gap-3">
      <ListCard
        label="uploads"
        placeholder="Filter uploads..."
        rows={uploads.data ?? []}
        fieldsOf={(upload) => [upload.id ?? "", upload.digest ?? ""]}
        loading={uploads.isPending}
        error={uploads.isError ? uploads.error : null}
        onRetry={() => void uploads.refetch()}
        columns={columns}
        cli={`fleetly uploads list --project ${projectId}`}
        actions={
          <>
            <input ref={inputRef} type="file" multiple className="hidden" onChange={(event) => void onFiles(event.target.files)} />
            <Button size="sm" disabled={pending} onClick={() => inputRef.current?.click()}>
              <PlusIcon data-icon-start-inline />
              {pending ? "Uploading…" : "Upload directory…"}
            </Button>
          </>
        }
        filters={<span className="hidden text-xs text-muted-foreground lg:inline">deterministic tar · .git skipped · content-addressed</span>}
        empty={
          <EmptyState icon={PlusIcon} title="No uploads" description="Pick a directory to build a deployable source." />
        }
      />
      {pending ? <p className="px-1 text-xs text-muted-foreground">Packing & uploading…</p> : null}
      {error != null ? <FormError error={error} /> : null}
      {result != null ? <p className="px-1 text-xs text-muted-foreground">{result}</p> : null}
    </section>
  );
}
