import { useState } from "react";
import { Modal, PageShell, ErrorNote, LoadingNote, EmptyNote, PrimaryButton, RowButton, DangerRowButton, MutationBanner, TableWrap, TableHead, useApiMutation, formatTime } from "../components/ui";
import { useNodes } from "../lib/catalog";
import type { components as runtimeSchemas } from "../api/runtime";

type Node = runtimeSchemas["schemas"]["v1Node"];

// 节点页（C4）：RuntimeAdmin 的 UI 面——集群成员表（可用态/中继活体）+
// 运维三动词（drain/cordon/uncordon，drain 真迁移任务）+ enroll 材料
// （join/relay 两条命令，等价集群成员权——平台 admin 档才能取）。
// 动词面对齐 CLI nodes 组（F0.19/F3.2/ADR-0049）。
export function NodesPage() {
  const nodes = useNodes();
  const [material, setMaterial] = useState<{ join: string; relay: string } | null>(null);
  const enroll = useApiMutation<{ join_command?: string; relay_command?: string }>({
    path: "/v1/nodes/enroll",
    method: "POST",
    body: () => ({ rotate: false }),
  });
  const rotate = useApiMutation<{ join_command?: string; relay_command?: string }>({
    path: "/v1/nodes/enroll",
    method: "POST",
    body: () => ({ rotate: true }),
  });

  const showMaterial = (data: { join_command?: string; relay_command?: string }) => {
    setMaterial({ join: data.join_command ?? "", relay: data.relay_command ?? "" });
  };

  return (
    <PageShell title="Nodes" hint="cluster membership, relay liveness and the runtime admin verbs">
      <div className="flex flex-col gap-4">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold text-slate-200">Cluster nodes</h2>
          <div className="flex gap-2">
            <RowButton disabled={enroll.isPending} onClick={() => enroll.mutate(undefined, { onSuccess: showMaterial })}>
              Enrollment material…
            </RowButton>
            <DangerRowButton
              confirm="Rotate all join tokens? Previously issued enrollment material stops working immediately (leak handling)."
              disabled={rotate.isPending}
              onClick={() => rotate.mutate(undefined, { onSuccess: showMaterial })}
            >
              rotate join tokens…
            </DangerRowButton>
          </div>
        </div>
        <MutationBanner pending={enroll.isPending || rotate.isPending} error={enroll.error ?? rotate.error} success={null} />
        {nodes.isPending ? <LoadingNote label="loading nodes…" /> : null}
        {nodes.isError ? <ErrorNote error={nodes.error} /> : null}
        {nodes.data != null ? (
          nodes.data.length === 0 ? (
            <EmptyNote label="No nodes registered — the control plane node appears once its daemon has observed the runtime." />
          ) : (
            <TableWrap>
              <table className="w-full text-left text-sm">
                <TableHead columns={["hostname", "role", "availability", "relay", "last seen", "actions"]} />
                <tbody>
                  {[...nodes.data]
                    .sort((a, b) => Number(b.available ?? false) - Number(a.available ?? false))
                    .map((node) => (
                      <NodeRow key={node.platform_id} node={node} />
                    ))}
                </tbody>
              </table>
            </TableWrap>
          )
        ) : null}
        <p className="text-xs text-slate-500">
          drain migrates running tasks off the node (real task movement); cordon/uncordon toggle schedulability. The control-plane node
          hosts the managed carriers (proxy, registry, metrics) and cannot be drained away from them.
        </p>
      </div>
      {material != null ? <MaterialModal material={material} onClose={() => setMaterial(null)} /> : null}
    </PageShell>
  );
}

function NodeRow({ node }: { node: Node }) {
  const drain = useApiMutation({
    path: () => `/v1/nodes/${encodeURIComponent(node.platform_id ?? "")}/drain`,
    method: "POST",
    body: () => ({}),
  });
  const cordon = useApiMutation({
    path: () => `/v1/nodes/${encodeURIComponent(node.platform_id ?? "")}/cordon`,
    method: "POST",
    body: () => ({}),
  });
  const uncordon = useApiMutation({
    path: () => `/v1/nodes/${encodeURIComponent(node.platform_id ?? "")}/uncordon`,
    method: "POST",
    body: () => ({}),
  });
  const busy = drain.isPending || cordon.isPending || uncordon.isPending;
  const lastError = drain.error ?? cordon.error ?? uncordon.error;
  return (
    <>
      <tr className={node.available ? "border-t border-slate-800" : "border-t border-slate-800 opacity-60"}>
        <td className="px-3 py-2 font-medium text-slate-200" title={node.platform_id}>{node.hostname}</td>
        <td className="px-3 py-2 text-xs text-slate-400">{node.role}</td>
        <td className="px-3 py-2">
          <span className={node.available ? "rounded bg-emerald-950/60 px-1.5 py-0.5 text-xs text-emerald-300" : "rounded bg-slate-800 px-1.5 py-0.5 text-xs text-slate-400"} title={node.available ? "schedulable" : "not schedulable — historical registration, cordoned or drained"}>
            {node.available ? "active" : "unavailable"}
          </span>
        </td>
        <td className="px-3 py-2 text-xs">
          {node.relay_online ? (
            <span className="font-mono text-slate-400" title="node relay online — exec sessions can route to this node">
              online{node.relay_version ? ` · ${node.relay_version}` : ""}
            </span>
          ) : (
            <span className="text-red-300" title="node relay offline — exec cannot route; re-run the enrollment relay script to recover">
              offline
            </span>
          )}
        </td>
        <td className="px-3 py-2 text-xs text-slate-500" title={node.last_seen_at}>{formatTime(node.last_seen_at)}</td>
        <td className="px-3 py-2 text-right">
          <div className="flex items-center justify-end gap-1">
            {node.available ? (
              <RowButton disabled={busy} onClick={() => void cordon.mutate()} title="Mark the node unschedulable (reversible)">
                cordon
              </RowButton>
            ) : (
              <RowButton disabled={busy} onClick={() => void uncordon.mutate()} title="Return the node to schedulable">
                uncordon
              </RowButton>
            )}
            <DangerRowButton
              confirm={`Drain ${node.hostname}? Running tasks migrate to other nodes in real time.`}
              disabled={busy}
              onClick={() => void drain.mutate()}
            >
              drain
            </DangerRowButton>
          </div>
        </td>
      </tr>
      {lastError != null ? (
        <tr className="border-t border-slate-800/40">
          <td colSpan={6} className="px-3 py-1">
            <ErrorNote error={lastError} />
          </td>
        </tr>
      ) : null}
    </>
  );
}

// MaterialModal 是 enroll 材料揭示面：join（集群加入）与 relay（节点中继
// 装载）两条命令——等价集群成员权，仅铸造/轮换响应可见。
function MaterialModal({ material, onClose }: { material: { join: string; relay: string }; onClose: () => void }) {
  return (
    <Modal title="Enrollment material (keep private)" open onClose={onClose}>
      <p className="text-xs text-slate-400">
        Run these on the target worker node. The join material is equivalent to cluster membership — treat it like a credential and do not
        paste it into shared channels. Rotating join tokens invalidates previously issued material.
      </p>
      <div className="flex flex-col gap-2">
        <span className="text-xs font-medium text-slate-300">join command</span>
        <pre className="overflow-x-auto rounded-md border border-slate-800 bg-slate-950 px-3 py-2 font-mono text-xs text-emerald-300">{material.join}</pre>
        <span className="text-xs font-medium text-slate-300">node relay install command</span>
        <pre className="overflow-x-auto rounded-md border border-slate-800 bg-slate-950 px-3 py-2 font-mono text-xs text-sky-300">{material.relay}</pre>
      </div>
      <div className="flex justify-end">
        <PrimaryButton onClick={onClose}>Done</PrimaryButton>
      </div>
    </Modal>
  );
}
