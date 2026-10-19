import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { CheckCircle2Icon, RefreshCwIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";
import type { components as runtimeSchemas } from "@/api/runtime";
import { apiSend } from "@/api/client";
import { useNodes, useWhoami } from "@/lib/catalog";
import { describeError } from "@/lib/api-errors";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { ListCard } from "@/components/domain/list-page";
import { PageFrame } from "@/components/domain/page-frame";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, nodeTone } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

type Node = runtimeSchemas["schemas"]["v1Node"];

// 节点页（C4 → UI v2 批 5 reskin；模板化收编 PageFrame+ListCard）：页头
// 动词钮随对齐批 5 裁决收进工具栏主动作槽。表格体走 children 逃生舱自
// 绘——行级动作错误行（cordon/drain 的行内失败披露）是 DataTable 列模型
// 表达不了的特例；四态语义原样。RuntimeAdmin 的 UI 面——集群成员表
//（可用态/中继活体）+ 运维三动词（drain/cordon/uncordon，drain 真迁移
// 任务）+ enroll 材料（H10：初始 enrollment 只发 join；relay 材料按已锚定
// 节点定向铸造，行内 `Relay command` 取）。行点击进节点详情（在跑容器
// 观测，nodes.$nodeId）。index 路由与 $nodeId 平级（apps.index/$appId
// 同范式——无父 layout）。
export function NodesPage() {
  const whoami = useWhoami(true);
  // 平台管理员门（R08-M1/S16-c7）：节点清单与在跑容器是平台级读面，服务端
  // 门已收口；Console 只做显隐（fail-closed：whoami 未到/失败/非平台管理员
  // 都不请求 /v1/nodes——成员不再撞 403 页面）。
  const platformAdmin = whoami.data?.platformAdmin === true;
  const nodes = useNodes(platformAdmin);
  const [material, setMaterial] = useState<{ join: string } | null>(null);
  const [relayMaterial, setRelayMaterial] = useState<{ nodeId: string; script: string } | null>(null);
  const [rotating, setRotating] = useState(false);
  const enroll = useEnrollMaterial(false, setMaterial);
  const rotate = useEnrollMaterial(true, setMaterial);

  if (whoami.isPending) {
    return (
      <PageFrame title="Nodes" description="Cluster membership, relay liveness and the runtime admin verbs">
        <Skeleton className="h-48 w-full rounded-xl" />
      </PageFrame>
    );
  }
  if (!platformAdmin) {
    return (
      <PageFrame title="Nodes" description="Cluster membership, relay liveness and the runtime admin verbs">
        <EmptyState
          icon={ShieldAlertIcon}
          title="Platform admin only"
          description="Cluster nodes and their running containers are platform-scoped. Sign in as the platform owner to inspect them."
        />
      </PageFrame>
    );
  }

  const sorted = [...(nodes.data ?? [])].sort((a, b) => Number(b.available ?? false) - Number(a.available ?? false));

  return (
    <PageFrame title="Nodes" description="Cluster membership, relay liveness and the runtime admin verbs">
      <ListCard
        label="nodes"
        placeholder="Filter nodes..."
        rows={sorted}
        fieldsOf={(node) => [node.hostname ?? "", node.platform_id ?? "", node.role ?? ""]}
        actions={
          <>
            <Button variant="outline" size="sm" disabled={enroll.isPending} onClick={() => enroll.mutate()}>
              <RefreshCwIcon data-icon-start-inline />
              Enrollment material…
            </Button>
            <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setRotating(true)}>
              rotate join tokens…
            </Button>
          </>
        }
      >
        {({ pageRows }) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Hostname</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Availability</TableHead>
                <TableHead>Relay</TableHead>
                <TableHead>Last seen</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes.isPending ? (
                Array.from({ length: 2 }).map((_, index) => (
                  <TableRow key={index}>
                    {Array.from({ length: 6 }).map((_, cell) => (
                      <TableCell key={cell}>
                        <Skeleton className="h-5 w-24" />
                      </TableCell>
                    ))}
                  </TableRow>
                ))
              ) : nodes.isError ? (
                <TableRow>
                  <TableCell colSpan={6}>
                    <ErrorState error={nodes.error} onRetry={() => void nodes.refetch()} />
                  </TableCell>
                </TableRow>
              ) : pageRows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6}>
                    <EmptyState
                      icon={CheckCircle2Icon}
                      title="No nodes registered"
                      description="The control plane node appears once its daemon has observed the runtime."
                    />
                  </TableCell>
                </TableRow>
              ) : (
                pageRows.map((node) => (
                  <NodeRow
                    key={node.platform_id}
                    node={node}
                    platformAdmin={platformAdmin}
                    onRelayMaterial={(script) => setRelayMaterial({ nodeId: node.platform_id ?? "", script })}
                  />
                ))
              )}
            </TableBody>
          </Table>
        )}
      </ListCard>
      <p className="mt-3 text-xs text-muted-foreground">
        Drain migrates running tasks off the node (real task movement); cordon/uncordon toggle schedulability. The control-plane node
        hosts the managed carriers (proxy, registry, metrics) and cannot be drained away from them.
      </p>

      {material != null ? <MaterialDialog material={material} onClose={() => setMaterial(null)} /> : null}
      {relayMaterial != null ? <RelayDialog material={relayMaterial} onClose={() => setRelayMaterial(null)} /> : null}
      <ConfirmDialog
        open={rotating}
        onOpenChange={setRotating}
        title="Rotate all join tokens?"
        description="Rotate join tokens and revoke all node relay credentials; connected relays drop. Reissue relay commands per node."
        confirmLabel="Rotate tokens"
        pendingLabel="Rotating…"
        onConfirm={() => rotate.mutateAsync()}
      />
    </PageFrame>
  );
}

function useEnrollMaterial(rotate: boolean, onMaterial: (data: { join: string }) => void) {
  return useMutation({
    mutationFn: () => apiSend<{ join_command?: string }>("/v1/nodes/enroll", "POST", { rotate }),
    onSuccess: (data) => onMaterial({ join: data.join_command ?? "" }),
    onError: (cause) => toast.error(`${describeError(cause).title} — ${describeError(cause).detail}`),
  });
}

function NodeRow({
  node,
  platformAdmin,
  onRelayMaterial,
}: {
  node: Node;
  platformAdmin: boolean;
  onRelayMaterial: (script: string) => void;
}) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [actionError, setActionError] = useState<unknown>(null);
  const [revoking, setRevoking] = useState(false);
  const nodeId = node.platform_id ?? "";

  function verb(path: string, body: Record<string, unknown> = {}) {
    setActionError(null);
    return apiSend(path, "POST", body)
      .then(() => queryClient.invalidateQueries({ queryKey: ["nodes"] }))
      .catch((cause: unknown) => setActionError(cause));
  }

  // Relay command（H10 §4.6）：重铸即断旧连接——先取脚本、就地执行；不是
  // 只读预览。响应携带该节点个体凭证（脚本是 node-root 等价材料）。
  function relayCommand() {
    setActionError(null);
    void apiSend<{ relay_command?: string }>("/v1/nodes/enroll", "POST", { node_id: nodeId })
      .then((data) => onRelayMaterial(data.relay_command ?? ""))
      .catch((cause: unknown) => setActionError(cause));
  }

  return (
    <>
      <TableRow
        className={`cursor-pointer ${node.available ? "" : "opacity-60"}`}
        title="View the containers running on this node"
        onClick={() => navigate({ to: "/nodes/$nodeId", params: { nodeId } })}
      >
        <TableCell className="font-medium">
          {node.hostname}
          <span className="ml-1.5 font-mono text-2xs text-muted-foreground" title={node.platform_id}>
            {node.platform_id?.slice(0, 8)}…
          </span>
        </TableCell>
        <TableCell className="text-xs text-muted-foreground">{node.role}</TableCell>
        <TableCell>
          <StatusBadge tone={nodeTone(node.available)}>{node.available ? "active" : "unavailable"}</StatusBadge>
        </TableCell>
        <TableCell className="text-xs">
          {node.relay_online ? (
            <span className="font-mono text-muted-foreground" title="node relay online — exec sessions can route to this node">
              online{node.relay_version ? ` · ${node.relay_version}` : ""}
            </span>
          ) : (
            <span className="text-[var(--status-danger)]" title="node relay offline — exec cannot route; issue and run a new relay command to recover">
              offline
            </span>
          )}
        </TableCell>
        <TableCell className="text-xs text-muted-foreground">
          <RelativeTime value={node.last_seen_at} />
        </TableCell>
        <TableCell className="text-right">
          <div className="flex items-center justify-end gap-1.5">
            {platformAdmin && node.role !== "manager" ? (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  title="Issue this node's individual relay credential and show its install command (sensitive; drops the current relay)"
                  onClick={(event) => {
                    event.stopPropagation();
                    relayCommand();
                  }}
                >
                  Relay command
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="text-[var(--status-danger)]"
                  title="Revoke this node's relay credential (the connected relay drops)"
                  onClick={(event) => {
                    event.stopPropagation();
                    setRevoking(true);
                  }}
                >
                  Revoke relay
                </Button>
              </>
            ) : null}
            {node.available ? (
              <Button
                variant="outline"
                size="sm"
                title="Mark the node unschedulable (reversible)"
                onClick={(event) => {
                  event.stopPropagation();
                  void verb(`/v1/nodes/${encodeURIComponent(nodeId)}/cordon`);
                }}
              >
                cordon
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                title="Return the node to schedulable"
                onClick={(event) => {
                  event.stopPropagation();
                  void verb(`/v1/nodes/${encodeURIComponent(nodeId)}/uncordon`);
                }}
              >
                uncordon
              </Button>
            )}
            <DrainButton
              hostname={node.hostname ?? ""}
              onRun={() => verb(`/v1/nodes/${encodeURIComponent(nodeId)}/drain`)}
            />
          </div>
        </TableCell>
      </TableRow>
      {actionError != null ? (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={6} className="py-2">
            <span className="text-xs text-[var(--status-danger)]">
              {describeError(actionError).title} — <span className="font-mono">{describeError(actionError).detail}</span>
            </span>
          </TableCell>
        </TableRow>
      ) : null}
      <ConfirmDialog
        open={revoking}
        onOpenChange={setRevoking}
        title={`Revoke relay credential for ${node.hostname ?? nodeId}?`}
        description="The node's relay drops immediately; exec cannot route until you issue a new relay command and run it on the node."
        confirmLabel="Revoke relay"
        pendingLabel="Revoking…"
        onConfirm={() => verb("/v1/nodes/enroll", { node_id: nodeId, revoke: true })}
      />
    </>
  );
}

function DrainButton({ hostname, onRun }: { hostname: string; onRun: () => Promise<unknown> }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="text-[var(--status-danger)]"
        onClick={(event) => {
          event.stopPropagation();
          setOpen(true);
        }}
      >
        drain
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={`Drain ${hostname}?`}
        description="Running tasks migrate to other nodes in real time."
        confirmLabel="Drain node"
        pendingLabel="Draining…"
        onConfirm={onRun}
      />
    </>
  );
}

// MaterialDialog 是初始 enrollment 材料揭示面（H10：只有 join 命令）：
// relay 材料不再随集群级 enrollment 产出——节点锚定后从行内 `Relay
// command` 按节点取（每节点个体凭证，单独揭示）。
function MaterialDialog({ material, onClose }: { material: { join: string }; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Enrollment material (keep private)</DialogTitle>
          <DialogDescription>
            Run this on the target worker node. The join material is equivalent to cluster membership — treat it like a credential.
            Rotating join tokens invalidates previously issued material.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">join command</span>
            <div className="relative">
              <pre className="overflow-x-auto rounded-lg border bg-muted/40 px-3 py-2.5 pr-9 font-mono text-xs text-[var(--status-success)]">
                {material.join}
              </pre>
              <CopyButton value={material.join} className="absolute top-2 right-2" />
            </div>
          </div>
          <p className="text-xs text-muted-foreground">
            After the node anchors, fetch its relay command from the node row.
          </p>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Done</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// RelayDialog 揭示单个节点的 relay 装载脚本（H10 §4.6）：脚本含该节点个体
// 凭证（fltrelay_ 明文），是 node-root 等价材料；重铸即断旧连接。
function RelayDialog({ material, onClose }: { material: { nodeId: string; script: string }; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Relay command for {material.nodeId} (node-root material)</DialogTitle>
          <DialogDescription>
            Treat this script as node-root material: it carries the individual relay credential of this node. Run it on the node
            immediately — issuing it already dropped the previous relay connection, and the old script stops authenticating.
          </DialogDescription>
        </DialogHeader>
        <div className="relative">
          <pre className="overflow-x-auto rounded-lg border bg-muted/40 px-3 py-2.5 pr-9 font-mono text-xs text-[var(--status-info)]">
            {material.script}
          </pre>
          <CopyButton value={material.script} className="absolute top-2 right-2" />
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Done</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
