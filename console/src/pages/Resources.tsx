import { useEffect, useRef, useState } from "react";
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
} from "../lib/catalog";
import { apiSend, apiSendRaw } from "../api/client";
import { buildTar, rootPrefixOf } from "../lib/tar";
import {
  DangerRowButton,
  EmptyNote,
  ErrorNote,
  Field,
  LoadingNote,
  Modal,
  MutationBanner,
  PageShell,
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
} from "../components/ui";

// 资源页（F3.1 写面）：项目轴 + 八资源 tab——networks（含 peer 面）/
// routes / volumes / secrets / configs / shared variables / databases（含
// 备份触发）/ uploads（含目录 tar 上传）。动词面对齐 CLI（创建/变更/
// 删除；服务端守卫即 UX——受影响提示与拒绝文案原样呈现）。

const TABS = ["networks", "routes", "volumes", "secrets", "configs", "variables", "databases", "uploads"] as const;
type Tab = (typeof TABS)[number];

export function ResourcesPage() {
  const [projectId, setProjectId] = useState("");
  const [tab, setTab] = useState<Tab>("networks");
  const projects = useProjects();
  const apps = useApps(projectId || projects.data?.[0]?.id || "");
  const effectiveProjectId = projectId || projects.data?.[0]?.id || "";

  return (
    <PageShell
      title="Resources"
      hint="project-scoped infrastructure and materials"
      toolbar={
        <select
          value={effectiveProjectId}
          onChange={(event) => setProjectId(event.target.value)}
          className="rounded-md border border-slate-700 bg-slate-900 px-2 py-1 text-sm text-slate-200"
        >
          {(projects.data ?? []).length === 0 ? <option value="">no projects</option> : null}
          {(projects.data ?? []).map((project) => (
            <option key={project.id} value={project.id}>
              {project.name}
            </option>
          ))}
        </select>
      }
    >
      <div className="flex flex-wrap gap-1">
        {TABS.map((candidate) => (
          <button
            key={candidate}
            type="button"
            onClick={() => setTab(candidate)}
            className={
              candidate === tab
                ? "rounded-md bg-slate-800 px-3 py-1.5 text-sm font-medium text-slate-100"
                : "rounded-md px-3 py-1.5 text-sm text-slate-400 hover:bg-slate-900 hover:text-slate-200"
            }
          >
            {candidate}
          </button>
        ))}
      </div>
      {projects.isPending ? (
        <LoadingNote label="Loading projects…" />
      ) : projects.isError ? (
        <ErrorNote error={projects.error} />
      ) : effectiveProjectId === "" ? (
        <EmptyNote label="No projects yet — create one on the Apps tab." />
      ) : tab === "networks" ? (
        <NetworksTab projectId={effectiveProjectId} />
      ) : tab === "routes" ? (
        <RoutesTab projectId={effectiveProjectId} apps={apps.data ?? []} />
      ) : tab === "volumes" ? (
        <VolumesTab projectId={effectiveProjectId} />
      ) : tab === "secrets" ? (
        <SecretsTab projectId={effectiveProjectId} />
      ) : tab === "configs" ? (
        <ConfigsTab projectId={effectiveProjectId} />
      ) : tab === "variables" ? (
        <VariablesTab projectId={effectiveProjectId} apps={apps.data ?? []} />
      ) : tab === "databases" ? (
        <DatabasesTab projectId={effectiveProjectId} />
      ) : (
        <UploadsTab projectId={effectiveProjectId} />
      )}
    </PageShell>
  );
}

// ---- networks ----

function NetworksTab({ projectId }: { projectId: string }) {
  const networks = useNetworks(projectId);
  const peers = useNetworkPeers(projectId);
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
      <Modal title="New network" open={createOpen} onClose={() => setCreateOpen(false)}>
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
          <label className="flex items-center gap-2 text-xs text-slate-400">
            <input type="checkbox" checked={egressNone} onChange={(event) => setEgressNone(event.target.checked)} />
            Egress none (isolated network)
          </label>
          <MutationBanner pending={create.isPending} error={create.isError ? create.error : null} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={create.isPending || name === ""}>Create</PrimaryButton>
          </div>
        </form>
      </Modal>
      {networks.isPending ? (
        <LoadingNote label="Loading networks…" />
      ) : networks.isError ? (
        <ErrorNote error={networks.error} />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name", "ID", "Egress", "Created", ""]} />
            <tbody>
              {(networks.data ?? []).map((network) => (
                <NetworkRow key={network.id} network={network} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <h2 className="mt-4 text-sm font-semibold text-slate-300">peers</h2>
      <p className="text-xs text-slate-500">
        Cross-project network attachment: the attaching project declares, the network owner approves.
      </p>
      {peers.isPending ? (
        <LoadingNote label="Loading peers…" />
      ) : peers.isError ? (
        <ErrorNote error={peers.error} />
      ) : (peers.data ?? []).length === 0 ? (
        <EmptyNote label="No peers declared." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Network", "Peer project", "State", "ID", ""]} />
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
    path: "/v1/networks/rebuild",
    method: "POST",
    body: () => ({ network_id: network.id }),
  });
  return (
    <tr className="border-b border-slate-800/60 hover:bg-slate-900/40">
      <td className="px-3 py-2 font-medium text-slate-200">{network.name}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-500" title={network.id}>
        {shortId(network.id)}
      </td>
      <td className="px-3 py-2 text-xs text-slate-400">{network.egress_none ? "none (isolated)" : "open"}</td>
      <td className="px-3 py-2 text-xs text-slate-500">{formatTime(network.created_at)}</td>
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
    path: `/v1/networks/peers/${encodeURIComponent(peer.id ?? "")}/approve`,
    method: "POST",
  });
  const revoke = useApiMutation({
    path: `/v1/networks/peers/${encodeURIComponent(peer.id ?? "")}/revoke`,
    method: "POST",
  });
  return (
    <tr className="border-b border-slate-800/60 hover:bg-slate-900/40">
      <td className="px-3 py-2 font-mono text-xs text-slate-300" title={peer.network_id}>
        {shortId(peer.network_id)}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-slate-400" title={peer.peer_project_id}>
        {shortId(peer.peer_project_id)}
      </td>
      <td className="px-3 py-2 text-xs text-slate-400">{peer.state}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-500" title={peer.id}>
        {shortId(peer.id)}
      </td>
      <td className="px-3 py-2 text-right">
        <div className="flex justify-end gap-1">
          <RowButton disabled={approve.isPending} onClick={() => void approve.mutate()}>
            approve
          </RowButton>
          <DangerRowButton confirm="Revoke this peer?" disabled={revoke.isPending} onClick={() => void revoke.mutate()}>
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
    path: "/v1/networks/peers",
    method: "POST",
    body: () => ({ network_id: networkId, project_id: projectId, peer_project_id: peerProject }),
    invalidate: [["resources", "network-peers", projectId]],
  });
  return (
    <form
      className="flex flex-wrap items-end gap-2 rounded-lg border border-slate-800 px-3 py-2"
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
      <PrimaryButton disabled={declare.isPending || networkId === "" || peerProject === ""}>declare</PrimaryButton>
      <MutationBanner pending={declare.isPending} error={declare.isError ? declare.error : null} success={declare.isSuccess ? "peer declared" : null} />
    </form>
  );
}

// ---- routes ----

function RoutesTab({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
  const routes = useRoutes(projectId);
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
      <Modal title="New route" open={createOpen} onClose={() => setCreateOpen(false)}>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            create.mutate(undefined, { onSuccess: () => setCreateOpen(false) });
          }}
        >
          <Field label="Host" hint="e.g. shop.203.0.113.10.sslip.io">
            <TextInput value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} autoFocus />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Path" hint="optional prefix">
              <TextInput value={form.path} onChange={(event) => setForm({ ...form, path: event.target.value })} placeholder="/" />
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
              <TextInput value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value })} inputMode="numeric" />
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
            <PrimaryButton disabled={create.isPending || form.host === "" || form.app === ""}>Create</PrimaryButton>
          </div>
        </form>
      </Modal>
      {routes.isPending ? (
        <LoadingNote label="Loading routes…" />
      ) : routes.isError ? (
        <ErrorNote error={routes.error} />
      ) : (routes.data ?? []).length === 0 ? (
        <EmptyNote label="No routes — traffic enters through routes (managed proxy)." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Host", "Path", "Target", "Port", "TLS", ""]} />
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
    path: `/v1/routes/${encodeURIComponent(route.id ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "routes", projectId]],
  });
  const appName = apps.find((app) => app.id === route.app_id)?.name ?? shortId(route.app_id);
  return (
    <tr className="border-b border-slate-800/60 hover:bg-slate-900/40">
      <td className="px-3 py-2 font-mono text-slate-200">{route.host}</td>
      <td className="px-3 py-2 font-mono text-xs text-slate-400">{route.path || "—"}</td>
      <td className="px-3 py-2 text-xs text-slate-300">
        {appName} / {route.process}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-slate-400">
        {route.port} {route.protocol ? `(${route.protocol})` : ""}
      </td>
      <td className="px-3 py-2 text-xs text-slate-400">{route.tls_mode}</td>
      <td className="px-3 py-2 text-right">
        <DangerRowButton confirm={`Delete route ${route.host}?`} disabled={del.isPending} onClick={() => void del.mutate()}>
          delete
        </DangerRowButton>
      </td>
    </tr>
  );
}

// ---- volumes ----

function VolumesTab({ projectId }: { projectId: string }) {
  const volumes = useVolumes(projectId);
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [pinnedNode, setPinnedNode] = useState("");
  const create = useApiMutation({
    path: "/v1/volumes",
    method: "POST",
    body: () => ({ project_id: projectId, name, pinned_node_id: pinnedNode || undefined }),
    invalidate: [["resources", "volumes", projectId]],
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
          New volume…
        </RowButton>
      </div>
      <Modal title="New volume" open={createOpen} onClose={() => setCreateOpen(false)}>
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
          <Field label="Pinned node" hint="optional platform node id — volume stays on that node">
            <TextInput value={pinnedNode} onChange={(event) => setPinnedNode(event.target.value)} />
          </Field>
          <MutationBanner pending={create.isPending} error={create.isError ? create.error : null} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={create.isPending || name === ""}>Create</PrimaryButton>
          </div>
        </form>
      </Modal>
      {volumes.isPending ? (
        <LoadingNote label="Loading volumes…" />
      ) : volumes.isError ? (
        <ErrorNote error={volumes.error} />
      ) : (volumes.data ?? []).length === 0 ? (
        <EmptyNote label="No volumes." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name", "Pinned node", "Created"]} />
            <tbody>
              {(volumes.data ?? []).map((volume) => (
                <tr key={volume.id} className="border-b border-slate-800/60 hover:bg-slate-900/40">
                  <td className="px-3 py-2 font-medium text-slate-200">{volume.name}</td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-400">{volume.pinned_node_id || "any"}</td>
                  <td className="px-3 py-2 text-xs text-slate-500">{formatTime(volume.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
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
              <TextInput type="password" value={value} onChange={(event) => setValue(event.target.value)} />
            )}
          </Field>
          <MutationBanner pending={pending} error={error} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={pending || name === ""}>Save</PrimaryButton>
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
            putJSON("/v1/secrets", "PUT", { project_id: projectId, name, value })
          }
        />
      </div>
      {secrets.isPending ? (
        <LoadingNote label="Loading secrets…" />
      ) : secrets.isError ? (
        <ErrorNote error={secrets.error} />
      ) : (secrets.data ?? []).length === 0 ? (
        <EmptyNote label="No secrets." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name", "Updated", ""]} />
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
    path: `/v1/secrets/${encodeURIComponent(projectId)}/${encodeURIComponent(row.name ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "secrets", projectId]],
  });
  return (
    <tr className="border-b border-slate-800/60 hover:bg-slate-900/40">
      <td className="px-3 py-2 font-mono text-slate-200">{row.name}</td>
      <td className="px-3 py-2 text-xs text-slate-500">{formatTime(row.updated_at ?? row.created_at)}</td>
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
          onSubmit={(name, content) => putJSON("/v1/configs", "PUT", { project_id: projectId, name, content })}
        />
      </div>
      {configs.isPending ? (
        <LoadingNote label="Loading configs…" />
      ) : configs.isError ? (
        <ErrorNote error={configs.error} />
      ) : (configs.data ?? []).length === 0 ? (
        <EmptyNote label="No configs." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name", "Version", "Updated"]} />
            <tbody>
              {(configs.data ?? []).map((config) => (
                <tr key={config.id ?? config.name} className="border-b border-slate-800/60 hover:bg-slate-900/40">
                  <td className="px-3 py-2 font-mono text-slate-200">{config.name}</td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-400">{config.version ?? "—"}</td>
                  <td className="px-3 py-2 text-xs text-slate-500">{formatTime(config.created_at)}</td>
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
      const res = await putJSON<{ affected_apps?: Array<string | undefined> }>("/v1/shared-variables", "PUT", {
        project_id: projectId,
        name,
        value,
      });
      const affected = (res.affected_apps ?? []).flatMap((id: string | undefined) => (id ? [apps.find((app) => app.id === id)?.name ?? id.slice(0, 10)] : []));
      setBanner(
        affected.length === 0
          ? `${name} saved — no app references it yet`
          : `${name} saved — redeploy to pick it up: ${affected.join(", ")}`,
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
        <PutForm title="Put variable…" nameLabel="Name" valueLabel="Value" valueHint="merged into app env at deploy freeze time" onSubmit={put} />
      </div>
      <MutationBanner pending={pending} error={bannerError} success={banner} />
      {variables.isPending ? (
        <LoadingNote label="Loading variables…" />
      ) : variables.isError ? (
        <ErrorNote error={variables.error} />
      ) : (variables.data ?? []).length === 0 ? (
        <EmptyNote label="No shared variables." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name", "Value", "Updated", ""]} />
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
    path: `/v1/shared-variables/${encodeURIComponent(projectId)}/${encodeURIComponent(row.name ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "shared-variables", projectId]],
  });
  return (
    <tr className="border-b border-slate-800/60 align-top hover:bg-slate-900/40">
      <td className="px-3 py-2 font-mono text-slate-200">{row.name}</td>
      <td className="max-w-xs truncate px-3 py-2 font-mono text-xs text-slate-400" title={row.value}>
        {row.value}
      </td>
      <td className="px-3 py-2 text-xs text-slate-500">{formatTime(row.updated_at)}</td>
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
                        .join(", ")}`,
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

const ENGINES = ["postgres", "pgvector", "redis", "mysql", "mongo"] as const;

function DatabasesTab({ projectId }: { projectId: string }) {
  const databases = useDatabases(projectId);
  const [createOpen, setCreateOpen] = useState(false);
  const [form, setForm] = useState({ name: "", engine: "postgres", restoreFrom: "" });
  const create = useApiMutation({
    path: "/v1/databases",
    method: "POST",
    body: () => ({ project_id: projectId, name: form.name, engine: form.engine, restore_from_backup: form.restoreFrom || undefined }),
    invalidate: [["resources", "databases", projectId]],
  });
  return (
    <section className="flex flex-col gap-3">
      <div className="flex justify-end">
        <RowButton
          onClick={() => {
            setForm({ name: "", engine: "postgres", restoreFrom: "" });
            setCreateOpen(true);
          }}
        >
          New database…
        </RowButton>
      </div>
      <Modal title="New database" open={createOpen} onClose={() => setCreateOpen(false)}>
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
          <Field label="Engine" hint="template-rendered managed service (stop-first, no double generations)">
            <Select value={form.engine} onChange={(event) => setForm({ ...form, engine: event.target.value })}>
              {ENGINES.map((engine) => (
                <option key={engine} value={engine}>
                  {engine}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Restore from backup" hint="optional backup id — restore-on-create">
            <TextInput value={form.restoreFrom} onChange={(event) => setForm({ ...form, restoreFrom: event.target.value })} />
          </Field>
          <MutationBanner pending={create.isPending} error={create.isError ? create.error : null} success={null} />
          <div className="flex justify-end">
            <PrimaryButton disabled={create.isPending || form.name === ""}>Create</PrimaryButton>
          </div>
        </form>
      </Modal>
      {databases.isPending ? (
        <LoadingNote label="Loading databases…" />
      ) : databases.isError ? (
        <ErrorNote error={databases.error} />
      ) : (databases.data ?? []).length === 0 ? (
        <EmptyNote label="No databases." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name", "Engine", "Status", "Created", ""]} />
            <tbody>
              {(databases.data ?? []).map((database) => (
                <DatabaseRow key={database.id} database={database} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

function DatabaseRow({ database }: { database: { id?: string; name?: string; engine?: string; state?: string; status?: string; created_at?: string } }) {
  const [expanded, setExpanded] = useState(false);
  const backups = useDatabaseBackups(expanded ? database.id ?? "" : "");
  const trigger = useApiMutation({
    path: `/v1/databases/${encodeURIComponent(database.id ?? "")}/backups`,
    method: "POST",
    body: () => ({}),
  });
  // browse（F3.6，ADR-0051）：铸造只读会话并在新窗口打开入口 URL（一次性
  // Launcher Ticket；enforcement 层级等回显字段留给后续批的展示面）。
  const browse = useApiMutation<{ url?: string }>({
    path: `/v1/databases/${encodeURIComponent(database.id ?? "")}/browse`,
    method: "POST",
    body: () => ({}),
  });
  const del = useApiMutation({
    path: `/v1/databases/${encodeURIComponent(database.id ?? "")}`,
    method: "DELETE",
    invalidate: [["resources", "databases"]],
  });
  const openBrowse = () => {
    browse.mutate(undefined, {
      onSuccess: (resp) => {
        if (resp?.url) window.open(resp.url, "_blank", "noopener");
      },
    });
  };
  return (
    <>
      <tr className="border-b border-slate-800/60 hover:bg-slate-900/40">
        <td className="px-3 py-2 font-medium text-slate-200">{database.name}</td>
        <td className="px-3 py-2 text-xs text-slate-300">{database.engine}</td>
        <td className="px-3 py-2 text-xs text-slate-400">{database.state ?? database.status ?? "—"}</td>
        <td className="px-3 py-2 text-xs text-slate-500">{formatTime(database.created_at)}</td>
        <td className="px-3 py-2 text-right">
          <div className="flex items-center justify-end gap-1">
            <RowButton onClick={() => setExpanded((prev) => !prev)}>{expanded ? "hide backups" : "backups"}</RowButton>
            <RowButton
              disabled={browse.isPending}
              onClick={openBrowse}
              title={`Open a read-only ${database.engine ?? "database"} browser session in a new window (one-time ticket, 120s)`}
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
        <tr className="border-b border-slate-800/60 bg-slate-950/40">
          <td colSpan={5} className="px-3 py-2">
            {backups.isPending ? (
              <LoadingNote label="Loading backups…" />
            ) : backups.isError ? (
              <ErrorNote error={backups.error} />
            ) : (backups.data ?? []).length === 0 ? (
              <EmptyNote label="No backups yet." />
            ) : (
              <table className="w-full text-xs">
                <TableHead columns={["Backup", "State", "Size", "Created"]} />
                <tbody>
                  {(backups.data ?? []).map((backup) => (
                    <tr key={backup.id} className="border-b border-slate-800/40">
                      <td className="px-3 py-1.5 font-mono text-slate-400" title={backup.id}>
                        {shortId(backup.id)}
                      </td>
                      <td className="px-3 py-1.5 text-slate-400">{backup.status ?? "—"}</td>
                      <td className="px-3 py-1.5 font-mono text-slate-500">{backup.size_bytes ? `${Number(backup.size_bytes) / 1048576} MiB` : "—"}</td>
                      <td className="px-3 py-1.5 text-slate-500">{formatTime(backup.finished_at ?? backup.created_at)}</td>
                    </tr>
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

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center justify-end gap-3">
        <span className="text-xs text-slate-500">deterministic tar · .git skipped · content-addressed</span>
        <input ref={inputRef} type="file" multiple className="hidden" onChange={(event) => void onFiles(event.target.files)} />
        <RowButton disabled={pending} onClick={() => inputRef.current?.click()}>
          {pending ? "Uploading…" : "Upload directory…"}
        </RowButton>
      </div>
      <MutationBanner pending={pending} error={error} success={result} />
      {uploads.isPending ? (
        <LoadingNote label="Loading uploads…" />
      ) : uploads.isError ? (
        <ErrorNote error={uploads.error} />
      ) : (uploads.data ?? []).length === 0 ? (
        <EmptyNote label="No uploads — pick a directory to build a deployable source." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Upload", "Size", "Digest", "Created"]} />
            <tbody>
              {(uploads.data ?? []).map((upload) => (
                <tr key={upload.id} className="border-b border-slate-800/60 hover:bg-slate-900/40">
                  <td className="px-3 py-2 font-mono text-xs text-slate-300" title={upload.id}>
                    {upload.id}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-400">{(Number(upload.size_bytes ?? 0) / 1024).toFixed(1)} KiB</td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-500" title={upload.digest}>
                    {upload.digest?.slice(0, 16)}…
                  </td>
                  <td className="px-3 py-2 text-xs text-slate-500">{formatTime(upload.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}
