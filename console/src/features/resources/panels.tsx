import { useEffect, useRef, useState } from"react";
import { useQueryClient } from"@tanstack/react-query";
import { PlusIcon } from"lucide-react";
import { toast } from"sonner";
import {
 useApps,
 useConfigs,
 useDatabaseBackups,
 useDatabases,
 useNetworkPeers,
 useNetworks,
 useProjects,
 useRoutes,
 useSecrets,
 useSharedVariables,
 useUploads,
 useVolumes,
} from"@/lib/catalog";
import { apiSend, apiSendRaw } from"@/api/client";
import { specIndex, useAppSpecs } from"@/features/spec/use-app-specs";
import { RelativeTime } from"@/components/domain/relative-time";
import { Button } from"@/components/ui/button";
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
} from"@/components/ui/alert-dialog";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from"@/components/ui/dialog";
import { Input } from"@/components/ui/input";
import { Label } from"@/components/ui/label";
import { buildTar, rootPrefixOf } from"@/lib/tar";
import {
  DangerRowButton,
  EmptyNote,
  ErrorNote,
  Field,
  LoadingNote,
  Modal,
  MutationBanner,
  PrimaryButton,
  RowButton,
  Select,
  TableHead,
  TableWrap,
  TextArea,
  TextInput,
 formatTime,
 shortId,
 useApiMutation,
} from"@/components/ui";

// 资源面板族（UI v2 批 4）：自旧 Resources.tsx 八 tab 近乎原样搬迁——
// 走查过的 mutation 语义零漂移（创建/变更/删除/peer 审批/备份
// verify+restore/browse/目录 tar 上传），外壳由项目语境路由页承载
//（PageHeader + 布局），内部表格批 6 统一 reskin。

// NetworksPanel：网络表 + peers 面（声明/审批/吊销）+ rebuild。
export function NetworksPanel({ projectId }: { projectId: string }) {
 return <NetworksTab projectId={projectId} />;
}

// RoutesPanel：host → app/process/port 路由表 + 创建。
export function RoutesPanel({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
 return <RoutesTab projectId={projectId} apps={apps} />;
}

// 数据域面板：databases（备份/verify/restore/browse）/ volumes / uploads。
export function DatabasesPanel({ projectId }: { projectId: string }) {
 return <DatabasesTab projectId={projectId} />;
}

export function VolumesPanel({ projectId }: { projectId: string }) {
 return <VolumesTab projectId={projectId} />;
}

export function UploadsPanel({ projectId }: { projectId: string }) {
 return <UploadsTab projectId={projectId} />;
}

// 配置域面板：secrets / configs / shared variables（put 型写面）。
export function SecretsPanel({ projectId }: { projectId: string }) {
 return <SecretsTab projectId={projectId} />;
}

export function ConfigsPanel({ projectId }: { projectId: string }) {
 return <ConfigsTab projectId={projectId} />;
}

export function VariablesPanel({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
 return <VariablesTab projectId={projectId} apps={apps} />;
}

// ---- networks ----

function NetworksTab({ projectId }: { projectId: string }) {
 const networks = useNetworks(projectId);
 const peers = useNetworkPeers(projectId);
 const [createOpen, setCreateOpen] = useState(false);
 const [name, setName] = useState("");
 const [egressNone, setEgressNone] = useState(false);
 const create = useApiMutation({
 path:"/v1/networks",
 method:"POST",
 body: () => ({ project_id: projectId, name, egress_none: egressNone || undefined }),
 invalidate: [["resources","networks", projectId]],
  });

 return (
    <section className="flex flex-col gap-3">
      <div className="flex justify-end">
        <RowButton
 onClick={() => {
 setName("");
 setCreateOpen(true);
          }}
        >
          New network…
        </RowButton>
      </div>
      <Modal title="New network"open={createOpen} onClose={() => setCreateOpen(false)}>
        <form
 className="flex flex-col gap-3"
 onSubmit={(event) => {
 event.preventDefault();
 create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
          }}
        >
          <Field label="Name">
            <TextInput value={name} onChange={(event) => setName(event.target.value)} autoFocus />
          </Field>
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input type="checkbox"checked={egressNone} onChange={(event) => setEgressNone(event.target.checked)} />
            Egress none (isolated network)
          </label>
          <MutationBanner pending={create.isPending} error={create.isError ? create.error : null} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={create.isPending || name ===""}>Create</PrimaryButton>
          </div>
        </form>
      </Modal>
      {networks.isPending ? (
        <LoadingNote label="Loading networks…"/>
      ) : networks.isError ? (
        <ErrorNote error={networks.error} />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name","ID","Egress","Created",""]} />
            <tbody>
              {(networks.data ?? []).map((network) => (
                <NetworkRow key={network.id} network={network} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <h2 className="mt-4 text-sm font-semibold text-foreground">peers</h2>
      <p className="text-xs text-muted-foreground">
        Cross-project network attachment: the attaching project declares, the network owner approves.
      </p>
      {peers.isPending ? (
        <LoadingNote label="Loading peers…"/>
      ) : peers.isError ? (
        <ErrorNote error={peers.error} />
      ) : (peers.data ?? []).length === 0 ? (
        <EmptyNote label="No peers declared."/>
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Network","Peer project","State","ID",""]} />
            <tbody>
              {(peers.data ?? []).map((peer) => (
                <PeerRow key={peer.id} peer={peer} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <DeclarePeerForm projectId={projectId} networks={networks.data ?? []} />
    </section>
  );
}

function NetworkRow({ network }: { network: { id?: string; name?: string; egress_none?: boolean; created_at?: string } }) {
 const rebuild = useApiMutation({
 path:"/v1/networks/rebuild",
 method:"POST",
 body: () => ({ network_id: network.id }),
  });
 return (
    <tr className="border-b border hover:bg-muted/40">
      <td className="px-3 py-2 font-medium text-foreground">{network.name}</td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground"title={network.id}>
        {shortId(network.id)}
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{network.egress_none ?"none (isolated)":"open"}</td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{formatTime(network.created_at)}</td>
      <td className="px-3 py-2 text-right">
        <RowButton disabled={rebuild.isPending} onClick={() => void rebuild.mutate()} title="Rebuild the carrier network (minute-scale)">
 rebuild
        </RowButton>
        {rebuild.isError ? <ErrorNote error={rebuild.error} /> : null}
      </td>
    </tr>
  );
}

function PeerRow({ peer }: { peer: { id?: string; network_id?: string; peer_project_id?: string; state?: string } }) {
 const approve = useApiMutation({
 path: `/v1/networks/peers/${encodeURIComponent(peer.id ??"")}/approve`,
 method:"POST",
  });
 const revoke = useApiMutation({
 path: `/v1/networks/peers/${encodeURIComponent(peer.id ??"")}/revoke`,
 method:"POST",
  });
 return (
    <tr className="border-b border hover:bg-muted/40">
      <td className="px-3 py-2 font-mono text-xs text-foreground"title={peer.network_id}>
        {shortId(peer.network_id)}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground"title={peer.peer_project_id}>
        {shortId(peer.peer_project_id)}
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{peer.state}</td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground"title={peer.id}>
        {shortId(peer.id)}
      </td>
      <td className="px-3 py-2 text-right">
        <div className="flex justify-end gap-1">
          <RowButton disabled={approve.isPending} onClick={() => void approve.mutate()}>
 approve
          </RowButton>
          <DangerRowButton confirm="Revoke this peer?"disabled={revoke.isPending} onClick={() => void revoke.mutate()}>
 revoke
          </DangerRowButton>
        </div>
      </td>
    </tr>
  );
}

function DeclarePeerForm({ projectId, networks }: { projectId: string; networks: Array<{ id?: string; name?: string }> }) {
 const projects = useProjects();
 const [networkId, setNetworkId] = useState("");
 const [peerProject, setPeerProject] = useState("");
 const declare = useApiMutation({
 path:"/v1/networks/peers",
 method:"POST",
 body: () => ({ network_id: networkId, project_id: projectId, peer_project_id: peerProject }),
 invalidate: [["resources","network-peers", projectId]],
  });
 return (
    <form
 className="flex flex-wrap items-end gap-2 rounded-lg border border px-3 py-2"
 onSubmit={(event) => {
 event.preventDefault();
 declare.mutate(undefined, { onSuccess: () => declare.reset() });
      }}
    >
      <Field label="Declare peer — network">
        <Select value={networkId} onChange={(event) => setNetworkId(event.target.value)}>
          <option value="">select…</option>
          {networks.map((network) => (
            <option key={network.id} value={network.id}>
              {network.name}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="Peer project (owner of the network you want to attach)">
        <Select value={peerProject} onChange={(event) => setPeerProject(event.target.value)}>
          <option value="">select…</option>
          {(projects.data ?? [])
 .filter((project) => project.id !== projectId)
 .map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
        </Select>
      </Field>
      <PrimaryButton disabled={declare.isPending || networkId ===""|| peerProject ===""}>declare</PrimaryButton>
      <MutationBanner pending={declare.isPending} error={declare.isError ? declare.error : null} success={declare.isSuccess ?"peer declared": null} />
    </form>
  );
}

// ---- routes ----

function RoutesTab({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
 const routes = useRoutes(projectId);
 const [createOpen, setCreateOpen] = useState(false);
 const [form, setForm] = useState({ host:"", path:"", app: apps[0]?.id ??"", process:"web", port:"8080", protocol:"http", tls:"auto"});
 const create = useApiMutation({
 path:"/v1/routes",
 method:"POST",
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
 invalidate: [["resources","routes", projectId]],
  });
 return (
    <section className="flex flex-col gap-3">
      <div className="flex justify-end">
        <RowButton
 onClick={() => {
 setForm((prev) => ({ ...prev, app: apps[0]?.id ?? prev.app }));
 setCreateOpen(true);
          }}
 disabled={apps.length === 0}
        >
          New route…
        </RowButton>
      </div>
      <Modal title="New route"open={createOpen} onClose={() => setCreateOpen(false)}>
        <form
 className="flex flex-col gap-3"
 onSubmit={(event) => {
 event.preventDefault();
 create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
          }}
        >
          <Field label="Host"hint="e.g. shop.203.0.113.10.sslip.io">
            <TextInput value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} autoFocus />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Path"hint="optional prefix">
              <TextInput value={form.path} onChange={(event) => setForm({ ...form, path: event.target.value })} placeholder="/"/>
            </Field>
            <Field label="TLS">
              <Select value={form.tls} onChange={(event) => setForm({ ...form, tls: event.target.value })}>
                <option value="auto">auto</option>
                <option value="none">none</option>
              </Select>
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="App">
              <Select value={form.app} onChange={(event) => setForm({ ...form, app: event.target.value })}>
                {apps.map((app) => (
                  <option key={app.id} value={app.id}>
                    {app.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Process">
              <TextInput value={form.process} onChange={(event) => setForm({ ...form, process: event.target.value })} />
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Port">
              <TextInput value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value })} inputMode="numeric"/>
            </Field>
            <Field label="Protocol">
              <Select value={form.protocol} onChange={(event) => setForm({ ...form, protocol: event.target.value })}>
                <option value="http">http</option>
                <option value="h2c">h2c</option>
                <option value="tcp">tcp</option>
              </Select>
            </Field>
          </div>
          <MutationBanner pending={create.isPending} error={create.isError ? create.error : null} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={create.isPending || form.host ===""|| form.app ===""}>Create</PrimaryButton>
          </div>
        </form>
      </Modal>
      {routes.isPending ? (
        <LoadingNote label="Loading routes…"/>
      ) : routes.isError ? (
        <ErrorNote error={routes.error} />
      ) : (routes.data ?? []).length === 0 ? (
        <EmptyNote label="No routes — traffic enters through routes (managed proxy)."/>
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Host","Path","Target","Port","TLS",""]} />
            <tbody>
              {(routes.data ?? []).map((route) => (
                <RouteRow key={route.id} route={route} apps={apps} projectId={projectId} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

function RouteRow({
 route,
 apps,
 projectId,
}: {
 route: { id?: string; host?: string; path?: string; app_id?: string; process?: string; port?: number; protocol?: string; tls_mode?: string };
 apps: Array<{ id: string; name: string }>;
 projectId: string;
}) {
 const del = useApiMutation({
 path: `/v1/routes/${encodeURIComponent(route.id ??"")}`,
 method:"DELETE",
 invalidate: [["resources","routes", projectId]],
  });
 const appName = apps.find((app) => app.id === route.app_id)?.name ?? shortId(route.app_id);
 return (
    <tr className="border-b border hover:bg-muted/40">
      <td className="px-3 py-2 font-mono text-foreground">{route.host}</td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{route.path ||"—"}</td>
      <td className="px-3 py-2 text-xs text-foreground">
        {appName} / {route.process}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
        {route.port} {route.protocol ? `(${route.protocol})` :""}
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{route.tls_mode}</td>
      <td className="px-3 py-2 text-right">
        <DangerRowButton confirm={`Delete route ${route.host}?`} disabled={del.isPending} onClick={() => void del.mutate()}>
 delete
        </DangerRowButton>
      </td>
    </tr>
  );
}

// ---- volumes（IA v3 二期⑤b 起 v2 形态：Dialog 创建 + AlertDialog 删除
// 确认 + token 类；mutation 语义自批 4 零漂移） ----

function VolumesTab({ projectId }: { projectId: string }) {
 const volumes = useVolumes(projectId);
 const apps = useApps(projectId);
 const databases = useDatabases(projectId);
 const { specs } = useAppSpecs(projectId, apps.data ?? []);
 const [createOpen, setCreateOpen] = useState(false);
 const [name, setName] = useState("");
 const [pinnedNode, setPinnedNode] = useState("");
 const create = useApiMutation({
 path:"/v1/volumes",
 method:"POST",
 body: () => ({ project_id: projectId, name, pinned_node_id: pinnedNode || undefined }),
 invalidate: [["resources","volumes", projectId]],
  });
  // 挂载判据（IA v3 二期⑤b）：卷锚 = 平台卷名（App 冻结 Spec 的卷附件
  // volume_id 与 Database 挂靠卷名公式都按 Name——引擎同口径）。
 const volumeIndex = specIndex(specs, (spec) =>
    (spec.processes ?? []).flatMap((process) => (process.volumes ?? []).map((volume) => volume.volume_id ??"")),
  );
 const appName = (id: string) => (apps.data ?? []).find((app) => app.id === id)?.name ?? id;
 const mountOf = (volume: { name?: string }): string | null => {
 const users = volumeIndex.get(volume.name ??"") ?? [];
 const db = (databases.data ?? []).some((database) => database.name === volume.name);
 if (db) return"carrier data volume of database"+ volume.name;
 if (users.length > 0) return"mounted by"+ users.map(appName).join(",");
 return null;
  };
 return (
    <section className="flex flex-col gap-3">
      <div className="flex justify-end">
        <Button
 size="sm"
 variant="outline"
 onClick={() => {
 setName("");
 setCreateOpen(true);
          }}
        >
          <PlusIcon data-icon-start-inline />
          New volume…
        </Button>
      </div>
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
            {create.isError ? <p className="text-xs text-destructive">{create.error instanceof Error ? create.error.message : String(create.error)}</p> : null}
            <DialogFooter>
              <Button type="button"variant="outline"onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button type="submit"disabled={create.isPending || name ===""}>
                {create.isPending ?"Creating…":"Create"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      {volumes.isPending ? (
        <p className="py-6 text-center text-xs text-muted-foreground">Loading volumes…</p>
      ) : volumes.isError ? (
        <p className="rounded-xl border bg-card p-4 text-xs text-destructive">{volumes.error instanceof Error ? volumes.error.message : String(volumes.error)}</p>
      ) : (volumes.data ?? []).length === 0 ? (
        <p className="rounded-xl border bg-card p-6 text-center text-xs text-muted-foreground">No volumes — create one to attach it from an app's Variables tab.</p>
      ) : (
        <div className="overflow-hidden rounded-xl border">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/40 text-left text-[10.5px] font-semibold tracking-wide text-muted-foreground uppercase">
              <tr>
                <th className="px-3 py-2">Name</th>
                <th className="px-3 py-2">Pinned node</th>
                <th className="px-3 py-2">Created</th>
                <th className="px-3 py-2"/>
              </tr>
            </thead>
            <tbody>
              {(volumes.data ?? []).map((volume) => (
                <VolumeRow key={volume.id} volume={volume} mount={mountOf(volume)} projectId={projectId} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// VolumeRow 行操作（IA v3 二期⑤b）：未挂载才可删（挂载判据由服务端
// E_CONFLICT 兜底——客户端禁用是前置体验面）；删除仅收口平台行，底层
// runtime 卷留存（数据兜底永不级联），AlertDialog 确认文案明示。
function VolumeRow({
 volume,
 mount,
 projectId,
}: {
 volume: { id?: string; name?: string; pinned_node_id?: string; created_at?: string };
 mount: string | null;
 projectId: string;
}) {
 const del = useApiMutation({
 path: `/v1/volumes/${encodeURIComponent(volume.id ??"")}`,
 method:"DELETE",
 invalidate: [["resources","volumes", projectId]],
  });
 return (
    <tr className="border-b transition-colors last:border-b-0 hover:bg-muted/40">
      <td className="px-3 py-2 font-medium">{volume.name}</td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{volume.pinned_node_id ||"any"}</td>
      <td className="px-3 py-2 text-xs text-muted-foreground">
        <RelativeTime value={volume.created_at} />
      </td>
      <td className="px-3 py-2 text-right">
        {mount ? (
          <span className="text-[11px] text-muted-foreground"title={mount}>
 in use
          </span>
        ) : (
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button size="sm"variant="ghost"className="text-destructive hover:text-destructive"disabled={del.isPending}>
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
                  {del.isPending ?"Deleting…":"Delete"}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        )}
      </td>
    </tr>
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
      <RowButton
 onClick={() => {
 setName("");
 setValue("");
 setError(null);
 setOpen(true);
        }}
      >
        {title}
      </RowButton>
      <Modal title={title} open={open} onClose={() => setOpen(false)}>
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
          <Field label={nameLabel}>
            <TextInput value={name} onChange={(event) => setName(event.target.value)} autoFocus />
          </Field>
          <Field label={valueLabel} hint={valueHint}>
            {multiline ? (
              <TextArea rows={8} value={value} onChange={(event) => setValue(event.target.value)} />
            ) : (
              <TextInput type="password"value={value} onChange={(event) => setValue(event.target.value)} />
            )}
          </Field>
          <MutationBanner pending={pending} error={error} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={pending || name ===""}>Save</PrimaryButton>
          </div>
        </form>
      </Modal>
    </>
  );
}

function SecretsTab({ projectId }: { projectId: string }) {
 const secrets = useSecrets(projectId);
 return (
    <section className="flex flex-col gap-3">
      <div className="flex justify-end">
        <PutForm
 title="Put secret…"
 nameLabel="Name"
 valueLabel="Value"
 valueHint="write-only — the list never shows values"
 onSubmit={(name, value) =>
 putJSON("/v1/secrets","PUT", { project_id: projectId, name, value })
          }
        />
      </div>
      {secrets.isPending ? (
        <LoadingNote label="Loading secrets…"/>
      ) : secrets.isError ? (
        <ErrorNote error={secrets.error} />
      ) : (secrets.data ?? []).length === 0 ? (
        <EmptyNote label="No secrets."/>
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name","Updated",""]} />
            <tbody>
              {(secrets.data ?? []).map((secret) => (
                <SecretRow key={secret.id ?? secret.name} row={secret} projectId={projectId} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

function SecretRow({ row, projectId }: { row: KeyValueRow; projectId: string }) {
 const del = useApiMutation({
 path: `/v1/secrets/${encodeURIComponent(projectId)}/${encodeURIComponent(row.name ??"")}`,
 method:"DELETE",
 invalidate: [["resources","secrets", projectId]],
  });
 return (
    <tr className="border-b border hover:bg-muted/40">
      <td className="px-3 py-2 font-mono text-foreground">{row.name}</td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{formatTime(row.updated_at ?? row.created_at)}</td>
      <td className="px-3 py-2 text-right">
        <DangerRowButton confirm={`Delete secret ${row.name}?`} disabled={del.isPending} onClick={() => void del.mutate()}>
 delete
        </DangerRowButton>
      </td>
    </tr>
  );
}

function ConfigsTab({ projectId }: { projectId: string }) {
 const configs = useConfigs(projectId);
 return (
    <section className="flex flex-col gap-3">
      <div className="flex justify-end">
        <PutForm
 title="Put config…"
 nameLabel="Name"
 valueLabel="Content"
 valueHint="each write freezes a new version"
 multiline
 onSubmit={(name, content) => putJSON("/v1/configs","PUT", { project_id: projectId, name, content })}
        />
      </div>
      {configs.isPending ? (
        <LoadingNote label="Loading configs…"/>
      ) : configs.isError ? (
        <ErrorNote error={configs.error} />
      ) : (configs.data ?? []).length === 0 ? (
        <EmptyNote label="No configs."/>
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name","Version","Updated"]} />
            <tbody>
              {(configs.data ?? []).map((config) => (
                <tr key={config.id ?? config.name} className="border-b border hover:bg-muted/40">
                  <td className="px-3 py-2 font-mono text-foreground">{config.name}</td>
                  <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{config.version ??"—"}</td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">{formatTime(config.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
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
 const res = await putJSON<{ affected_apps?: Array<string | undefined> }>("/v1/shared-variables","PUT", {
 project_id: projectId,
 name,
 value,
      });
 const affected = (res.affected_apps ?? []).flatMap((id: string | undefined) => (id ? [apps.find((app) => app.id === id)?.name ?? id.slice(0, 10)] : []));
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
 return (
    <section className="flex flex-col gap-3">
      <div className="flex justify-end">
        <PutForm title="Put variable…"nameLabel="Name"valueLabel="Value"valueHint="merged into app env at deploy freeze time"onSubmit={put} />
      </div>
      <MutationBanner pending={pending} error={bannerError} success={banner} />
      {variables.isPending ? (
        <LoadingNote label="Loading variables…"/>
      ) : variables.isError ? (
        <ErrorNote error={variables.error} />
      ) : (variables.data ?? []).length === 0 ? (
        <EmptyNote label="No shared variables."/>
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name","Value","Updated",""]} />
            <tbody>
              {(variables.data ?? []).map((variable) => (
                <VariableRow key={variable.id ?? variable.name} row={variable} projectId={projectId} apps={apps} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

function VariableRow({
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
 const del = useApiMutation({
 path: `/v1/shared-variables/${encodeURIComponent(projectId)}/${encodeURIComponent(row.name ??"")}`,
 method:"DELETE",
 invalidate: [["resources","shared-variables", projectId]],
  });
 return (
    <tr className="border-b border align-top hover:bg-muted/40">
      <td className="px-3 py-2 font-mono text-foreground">{row.name}</td>
      <td className="max-w-xs truncate px-3 py-2 font-mono text-xs text-muted-foreground"title={row.value}>
        {row.value}
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{formatTime(row.updated_at)}</td>
      <td className="px-3 py-2 text-right">
        <DangerRowButton
 confirm={`Delete shared variable ${row.name}?`}
 disabled={del.isPending}
 onClick={() =>
 del.mutate(undefined, {
 onSuccess: (response) => {
 const affected = (response as { affected_apps?: Array<string | undefined> })?.affected_apps ?? [];
 setResult(
 affected.length === 0
                    ? `${row.name} deleted`
 : `${row.name} deleted — redeploy to pick it up: ${affected
 .flatMap((id) => (id ? [apps.find((app) => app.id === id)?.name ?? id.slice(0, 10)] : []))
 .join(",")}`,
                );
 setError(null);
              },
 onError: (cause) => {
 setError(cause);
 setResult(null);
              },
            })
          }
        >
 delete
        </DangerRowButton>
        {result ? <div className="mt-1 text-[11px] text-emerald-400">{result}</div> : null}
        {error != null ? <div className="mt-1"><ErrorNote error={error} /></div> : null}
      </td>
    </tr>
  );
}

// apiSend 直发助手（PutForm 的提交通道——与 useApiMutation 的差别仅在
// 表单受控生命周期）。
function putJSON<T>(path: string, method: string, body: unknown): Promise<T> {
 return apiSend<T>(path, method, body);
}

// ---- databases ----

const ENGINES = ["postgres","pgvector","redis","mysql","mongo"] as const;

function DatabasesTab({ projectId }: { projectId: string }) {
 const databases = useDatabases(projectId);
 const [createOpen, setCreateOpen] = useState(false);
 const [form, setForm] = useState({ name:"", engine:"postgres", restoreFrom:""});
 const create = useApiMutation({
 path:"/v1/databases",
 method:"POST",
 body: () => ({ project_id: projectId, name: form.name, engine: form.engine, restore_from_backup: form.restoreFrom || undefined }),
 invalidate: [["resources","databases", projectId]],
  });
 return (
    <section className="flex flex-col gap-3">
      <div className="flex justify-end">
        <RowButton
 onClick={() => {
 setForm({ name:"", engine:"postgres", restoreFrom:""});
 setCreateOpen(true);
          }}
        >
          New database…
        </RowButton>
      </div>
      <Modal title="New database"open={createOpen} onClose={() => setCreateOpen(false)}>
        <form
 className="flex flex-col gap-3"
 onSubmit={(event) => {
 event.preventDefault();
 create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
          }}
        >
          <Field label="Name">
            <TextInput value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} autoFocus />
          </Field>
          <Field label="Engine"hint="template-rendered managed service (stop-first, no double generations)">
            <Select value={form.engine} onChange={(event) => setForm({ ...form, engine: event.target.value })}>
              {ENGINES.map((engine) => (
                <option key={engine} value={engine}>
                  {engine}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Restore from backup"hint="optional backup id — restore-on-create">
            <TextInput value={form.restoreFrom} onChange={(event) => setForm({ ...form, restoreFrom: event.target.value })} />
          </Field>
          <MutationBanner pending={create.isPending} error={create.isError ? create.error : null} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={create.isPending || form.name ===""}>Create</PrimaryButton>
          </div>
        </form>
      </Modal>
      {databases.isPending ? (
        <LoadingNote label="Loading databases…"/>
      ) : databases.isError ? (
        <ErrorNote error={databases.error} />
      ) : (databases.data ?? []).length === 0 ? (
        <EmptyNote label="No databases."/>
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name","Engine","Status","Created",""]} />
            <tbody>
              {(databases.data ?? []).map((database) => (
                <DatabaseRow key={database.id} database={database} projectId={projectId} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

function DatabaseRow({ database, projectId }: { database: { id?: string; name?: string; engine?: string; state?: string; status?: string; created_at?: string }; projectId: string }) {
 const [expanded, setExpanded] = useState(false);
 const backups = useDatabaseBackups(expanded ? database.id ??"":"");
 const trigger = useApiMutation({
 path: `/v1/databases/${encodeURIComponent(database.id ??"")}/backups`,
 method:"POST",
 body: () => ({}),
  });
  // browse（F3.6，ADR-0051）：铸造只读会话并在新窗口打开入口 URL（一次性
  // Launcher Ticket；enforcement 层级等回显字段留给后续批的展示面）。
 const browse = useApiMutation<{ url?: string }>({
 path: `/v1/databases/${encodeURIComponent(database.id ??"")}/browse`,
 method:"POST",
 body: () => ({}),
  });
 const del = useApiMutation({
 path: `/v1/databases/${encodeURIComponent(database.id ??"")}`,
 method:"DELETE",
 invalidate: [["resources","databases"]],
  });
 const openBrowse = () => {
 browse.mutate(undefined, {
 onSuccess: (resp) => {
 if (resp?.url) window.open(resp.url,"_blank","noopener");
      },
    });
  };
 return (
    <>
      <tr className="border-b border hover:bg-muted/40">
        <td className="px-3 py-2 font-medium text-foreground">{database.name}</td>
        <td className="px-3 py-2 text-xs text-foreground">{database.engine}</td>
        <td className="px-3 py-2 text-xs text-muted-foreground">{database.state ?? database.status ??"—"}</td>
        <td className="px-3 py-2 text-xs text-muted-foreground">{formatTime(database.created_at)}</td>
        <td className="px-3 py-2 text-right">
          <div className="flex items-center justify-end gap-1">
            <RowButton onClick={() => setExpanded((prev) => !prev)}>{expanded ?"hide backups":"backups"}</RowButton>
            <RowButton
 disabled={browse.isPending}
 onClick={openBrowse}
 title={`Open a read-only ${database.engine ??"database"} browser session in a new window (one-time ticket, 120s)`}
            >
 browse
            </RowButton>
            <RowButton disabled={trigger.isPending} onClick={() => void trigger.mutate()} title="Trigger an on-demand backup">
 backup
            </RowButton>
            <DangerRowButton confirm={`Delete database ${database.name}? Data is destroyed.`} disabled={del.isPending} onClick={() => void del.mutate()}>
 delete
            </DangerRowButton>
          </div>
          {browse.isError ? <div className="mt-1"><ErrorNote error={browse.error} /></div> : null}
          {trigger.isError ? <div className="mt-1"><ErrorNote error={trigger.error} /></div> : null}
          {trigger.isSuccess ? <div className="mt-1 text-[11px] text-emerald-400">backup ledger row created</div> : null}
        </td>
      </tr>
      {expanded ? (
        <tr className="border-b border bg-muted/40">
          <td colSpan={5} className="px-3 py-2">
            {backups.isPending ? (
              <LoadingNote label="Loading backups…"/>
            ) : backups.isError ? (
              <ErrorNote error={backups.error} />
            ) : (backups.data ?? []).length === 0 ? (
              <EmptyNote label="No backups yet."/>
            ) : (
              <table className="w-full text-xs">
                <TableHead columns={["Backup","State","Size","Created",""]} />
                <tbody>
                  {(backups.data ?? []).map((backup) => (
                    <BackupActionRow key={backup.id} backup={backup} projectId={projectId} engine={database.engine ??""} />
                  ))}
                </tbody>
              </table>
            )}
          </td>
        </tr>
      ) : null}
    </>
  );
}

// ---- uploads ----

function UploadsTab({ projectId }: { projectId: string }) {
 const uploads = useUploads(projectId);
 const inputRef = useRef<HTMLInputElement>(null);
 const [pending, setPending] = useState(false);
 const [error, setError] = useState<unknown>(null);
 const [result, setResult] = useState<string | null>(null);

  // webkitdirectory 是非标准属性（目录选择面）：React TS 类型不含它，
  // 经 ref 挂属性（Chromium/Firefox 均支持目录选择）。
 useEffect(() => {
 inputRef.current?.setAttribute("webkitdirectory","");
 inputRef.current?.setAttribute("directory","");
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
 new Blob([tar as unknown as BlobPart], { type:"application/x-tar"}),
"application/x-tar",
      );
 setResult(
        `${response.id ??"?"} uploaded (${(Number(response.size_bytes ?? 0) / 1024).toFixed(1)} KiB)${response.deduplicated ?"— content-addressed dedup hit":""}`,
      );
    } catch (cause) {
 setError(cause);
    } finally {
 setPending(false);
 if (inputRef.current) inputRef.current.value ="";
    }
  }

 return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center justify-end gap-3">
        <span className="text-xs text-muted-foreground">deterministic tar · .git skipped · content-addressed</span>
        <input ref={inputRef} type="file"multiple className="hidden"onChange={(event) => void onFiles(event.target.files)} />
        <Button size="sm"variant="outline"disabled={pending} onClick={() => inputRef.current?.click()}>
          <PlusIcon data-icon-start-inline />
          {pending ?"Uploading…":"Upload directory…"}
        </Button>
      </div>
      {pending ? <p className="text-xs text-muted-foreground">Packing & uploading…</p> : null}
      {error != null ? <p className="text-xs text-destructive">{error instanceof Error ? error.message : String(error)}</p> : null}
      {result != null ? <p className="text-xs text-muted-foreground">{result}</p> : null}
      {uploads.isPending ? (
        <p className="py-6 text-center text-xs text-muted-foreground">Loading uploads…</p>
      ) : uploads.isError ? (
        <p className="rounded-xl border bg-card p-4 text-xs text-destructive">{uploads.error instanceof Error ? uploads.error.message : String(uploads.error)}</p>
      ) : (uploads.data ?? []).length === 0 ? (
        <p className="rounded-xl border bg-card p-6 text-center text-xs text-muted-foreground">No uploads — pick a directory to build a deployable source.</p>
      ) : (
        <div className="overflow-hidden rounded-xl border">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/40 text-left text-[10.5px] font-semibold tracking-wide text-muted-foreground uppercase">
              <tr>
                <th className="px-3 py-2">Upload</th>
                <th className="px-3 py-2">Size</th>
                <th className="px-3 py-2">Digest</th>
                <th className="px-3 py-2">Created</th>
              </tr>
            </thead>
            <tbody>
              {(uploads.data ?? []).map((upload) => (
                <tr key={upload.id} className="border-b transition-colors last:border-b-0 hover:bg-muted/40">
                  <td className="px-3 py-2 font-mono text-xs"title={upload.id}>
                    {upload.id}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{(Number(upload.size_bytes ?? 0) / 1024).toFixed(1)} KiB</td>
                  <td className="px-3 py-2 font-mono text-xs text-muted-foreground"title={upload.digest}>
                    {upload.digest?.slice(0, 16)}…
                  </td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">
                    <RelativeTime value={upload.created_at} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// BackupActionRow 是备份行的动作面（C5 数据面深化）：verify（重算 digest
// 的只读校验——ok/digest/error 诚实呈现）+ restore（按名 create-or-收敛
// 的新库恢复流——restore_from_backup 在场 = 恢复挂起，ADR-0039；恢复是
// 异步任务，目标库行先建后到数据）。
function BackupActionRow({
 backup,
 projectId,
 engine,
}: {
 backup: { id?: string; status?: string; size_bytes?: string | number; finished_at?: string; created_at?: string };
 projectId: string;
 engine: string;
}) {
 const queryClient = useQueryClient();
 const [verifyResult, setVerifyResult] = useState<{ ok: boolean; digest: string; error: string } | null>(null);
 const [verifying, setVerifying] = useState(false);
 const [restoreOpen, setRestoreOpen] = useState(false);
 const [restoreName, setRestoreName] = useState("");
 const [restoring, setRestoring] = useState(false);
 const [restoreError, setRestoreError] = useState<unknown>(null);

 async function runVerify() {
 setVerifying(true);
 setVerifyResult(null);
 try {
 const res = await apiSend<{ ok?: boolean; digest?: string; error?: string }>(
        `/v1/backups/${encodeURIComponent(backup.id ??"")}/verify`,
"POST",
        {},
      );
 setVerifyResult({ ok: res.ok === true, digest: res.digest ??"", error: res.error ??""});
    } catch (cause) {
 setVerifyResult({ ok: false, digest:"", error: (cause as Error).message });
    } finally {
 setVerifying(false);
    }
  }

 async function runRestore() {
 setRestoring(true);
 setRestoreError(null);
 try {
 await apiSend("/v1/databases","POST", {
 project_id: projectId,
 name: restoreName,
 engine,
 restore_from_backup: backup.id,
      });
 void queryClient.invalidateQueries({ queryKey: ["resources","databases"] });
 setRestoreOpen(false);
 setRestoreName("");
    } catch (cause) {
 setRestoreError(cause);
    } finally {
 setRestoring(false);
    }
  }

 return (
    <>
      <tr className="border-b border/40">
        <td className="px-3 py-1.5 font-mono text-muted-foreground"title={backup.id}>
          {shortId(backup.id)}
        </td>
        <td className="px-3 py-1.5 text-muted-foreground">{backup.status ??"—"}</td>
        <td className="px-3 py-1.5 font-mono text-muted-foreground">{backup.size_bytes ? `${(Number(backup.size_bytes) / 1048576).toFixed(2)} MiB` :"—"}</td>
        <td className="px-3 py-1.5 text-muted-foreground">{formatTime(backup.finished_at ?? backup.created_at)}</td>
        <td className="px-3 py-1.5 text-right">
          <div className="flex items-center justify-end gap-1">
            <RowButton disabled={verifying} onClick={() => void runVerify()} title="Recompute the backup digest against the stored object (read-only)">
              {verifying ?"verifying…":"verify"}
            </RowButton>
            <RowButton onClick={() => setRestoreOpen((prev) => !prev)} title="Create a new database restored from this backup">
 restore…
            </RowButton>
          </div>
          {verifyResult != null ? (
            <div className="mt-1 text-[11px]">
              {verifyResult.ok ? (
                <span className="text-emerald-400">ok — digest {verifyResult.digest.slice(0, 16)}…</span>
              ) : (
                <span className="text-destructive">failed — {verifyResult.error ||"verification error"}</span>
              )}
            </div>
          ) : null}
        </td>
      </tr>
      {restoreOpen ? (
        <tr className="border-b bg-muted/30">
          <td colSpan={5} className="px-3 py-2">
            <form
 className="flex flex-wrap items-end gap-2"
 onSubmit={(event) => {
 event.preventDefault();
 void runRestore();
              }}
            >
              <Field label={`New ${engine} database name`} hint="restored from this backup; the restore task runs async after the row is created">
                <TextInput value={restoreName} onChange={(event) => setRestoreName(event.target.value)} placeholder="restored-copy"autoFocus />
              </Field>
              <PrimaryButton disabled={restoring || restoreName ===""}>{restoring ?"Creating…":"Restore"}</PrimaryButton>
            </form>
            {restoreError != null ? <ErrorNote error={restoreError} /> : null}
          </td>
        </tr>
      ) : null}
    </>
  );
}
