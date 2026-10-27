import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { RefreshCwIcon, ShieldAlertIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection, useCommand, useRecord } from "@lynx-go/uikit-core";
import type { ApiError, CommandResult } from "@lynx-go/uikit-core/kernel";
import { sessionResource } from "@/kit/settings";
import { enrollMaterialCommand } from "@/kit/app-tabs";
import { toastOnSettled } from "@/kit/toast";
import { describeApiError } from "@/kit/errors";
import { nodesResource, nodesSearch, type NodeRow } from "@/kit/nodes";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { PageFrame } from "@/components/domain/page-frame";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, nodeTone } from "@/components/domain/status-badge";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage as FleetListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
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

// 节点页（C4 → UI v2 批 5 reskin → 2026-10-26 批1 Wave2-A useCollection
// 化）：集群成员表 + 运维动词（drain/cordon/uncordon）+ enroll 材料
//（H10：初始 enrollment 只发 join；relay 材料按已锚定节点定向铸造，行内
// `Relay command` 取）。行点击进节点详情（nodes.$nodeId）。
// 平台管理员门（R08-M1/S16-c7）保持门在 useCollection 之外：whoami 未到
// /失败/非平台管理员都不挂集合（fail-closed，非管理员不发 /v1/nodes 请
// 求）——门内表体拆子组件 NodesList，避免条件 return 跨渲染增删 hook。
// 列表骨架换 fleet 块（ListPage × FilterBar × DataTable × Pagination），
// 快筛/分页/available 降序默认排序进 URL；行内动作失败披露行走
// DataTable 本地扩展槽 renderExtraRow（错误态归页面 {nodeId, cause} 单开）。
// 工具栏 Enrollment material/rotate tokens 批2 命令化（useCommand + toast
// 单源），与各 Confirm 弹窗原样保留。

export function NodesPage({ from }: { from: string }) {
  const whoami = useRecord(sessionResource, { id: "self" });
  // 平台管理员门（fail-closed：whoami 未到/失败/非平台管理员都不请求
  // /v1/nodes——成员不再撞 403 页面）。
  const platformAdmin = whoami.data?.platformAdmin === true;

  if (whoami.status === "loading") {
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

  return <NodesList from={from} />;
}

// NodesList：门内的成员表本体（useCollection 15s 轮询，与旧 useNodes 同拍；
// 工具栏两动词 + enroll/relay 揭示弹窗）。
function NodesList({ from }: { from: string }) {
  const navigate = useNavigate();
  const list = useCollection(nodesResource, {
    from,
    search: nodesSearch,
    query: { refetchInterval: 15_000 },
  });
  const [material, setMaterial] = useState<{ join: string } | null>(null);
  const [relayMaterial, setRelayMaterial] = useState<{ nodeId: string; script: string } | null>(null);
  const [rotating, setRotating] = useState(false);
  // 行内动作失败披露（单开：verb 前清、catch 记）——renderExtraRow 消费
  const [actionError, setActionError] = useState<{ nodeId: string; message: string } | null>(null);
  const enroll = useEnrollMaterial(setMaterial);
  const rotate = useEnrollMaterial(setMaterial);

  const columns: LegacyColumnDef<NodeRow, any>[] = [
    {
      accessorKey: "hostname",
      header: "Hostname",
      meta: { sortField: "hostname" },
      cell: ({ row }) => (
        <span className="font-medium" title="View the containers running on this node">
          {row.original.hostname}
          <span className="ml-1.5 font-mono text-2xs text-muted" title={row.original.platform_id}>
            {row.original.platform_id?.slice(0, 8)}…
          </span>
        </span>
      ),
    },
    {
      accessorKey: "role",
      header: "Role",
      meta: { sortField: "role" },
      cell: ({ row }) => <span className="text-xs text-muted">{row.original.role}</span>,
    },
    {
      accessorKey: "available",
      header: "Availability",
      meta: { sortField: "available" },
      cell: ({ row }) => (
        <StatusBadge tone={nodeTone(row.original.available)}>
          {row.original.available ? "active" : "unavailable"}
        </StatusBadge>
      ),
    },
    {
      id: "relay",
      header: "Relay",
      cell: ({ row }) =>
        row.original.relay_online ? (
          <span className="font-mono text-xs text-muted" title="node relay online — exec sessions can route to this node">
            online{row.original.relay_version ? ` · ${row.original.relay_version}` : ""}
          </span>
        ) : (
          <span className="text-xs text-[var(--status-danger)]" title="node relay offline — exec cannot route; issue and run a new relay command to recover">
            offline
          </span>
        ),
    },
    {
      accessorKey: "last_seen_at",
      header: "Last seen",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          <RelativeTime value={row.original.last_seen_at} />
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <NodeActionsCell
          node={row.original}
          onRelayMaterial={(script) => setRelayMaterial({ nodeId: row.original.platform_id ?? "", script })}
          onActionError={(message) => setActionError({ nodeId: row.original.platform_id ?? "", message })}
          clearActionError={() => setActionError(null)}
        />
      ),
    },
  ];

  const filtered = list.query.predicate != null;
  return (
    <PageFrame title="Nodes" description="Cluster membership, relay liveness and the runtime admin verbs">
      <FleetListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FilterBar list={list} schema={nodesSearch} textPlaceholder="Filter nodes..." />
            <div className="flex items-center gap-2 py-2 pr-3">
              <Button variant="outline" size="sm" disabled={enroll.pending} onClick={() => enroll.mutate({ rotate: false })}>
                <RefreshCwIcon data-icon-start-inline />
                Enrollment material…
              </Button>
              <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setRotating(true)}>
                rotate join tokens…
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No nodes match the filter" : "No nodes registered"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : "The control plane node appears once its daemon has observed the runtime."
        }
        appendix={
          <p className="mt-3 text-xs text-muted">
            Drain migrates running tasks off the node (real task movement); cordon/uncordon toggle schedulability. The
            control-plane node hosts the managed carriers (proxy, registry, metrics) and cannot be drained away from them.
          </p>
        }
      >
        <DataTable
          list={list}
          columns={columns}
          getRowId={(row) => row.platform_id ?? ""}
          onRowClick={(node) => void navigate({ to: "/nodes/$nodeId", params: { nodeId: node.platform_id ?? "" } })}
          renderExtraRow={(node) =>
            actionError != null && node.platform_id === actionError.nodeId ? (
              // 命令化后披露消息单源（describeApiError / rejected message
              // 在动作位已折算成字符串——行级不再持错误对象）。
              <div className="px-3 py-2 text-xs text-[var(--status-danger)]">{actionError.message}</div>
            ) : null
          }
        />
      </FleetListPage>

      {material != null ? <MaterialDialog material={material} onClose={() => setMaterial(null)} /> : null}
      {relayMaterial != null ? <RelayDialog material={relayMaterial} onClose={() => setRelayMaterial(null)} /> : null}
      <ConfirmDialog
        open={rotating}
        onOpenChange={setRotating}
        title="Rotate all join tokens?"
        description="Rotate join tokens and revoke all node relay credentials; connected relays drop. Reissue relay commands per node."
        confirmLabel="Rotate tokens"
        pendingLabel="Rotating…"
        onConfirm={() => rotate.mutateAsync({ rotate: true })}
      />
    </PageFrame>
  );
}

// enroll 材料铸取（批2 命令化，kit/app-tabs 的 enrollMaterialCommand）：
// 初始 join 与 rotate 全量同端点同命令——ok 面是材料揭示弹窗（无 toast），
// 失败面 toast 单源。列表 15s 轮询自跟进，无失效声明（材料铸造不落列表）。
function useEnrollMaterial(onMaterial: (data: { join: string }) => void) {
  return useCommand(enrollMaterialCommand, {
    onSettled: toastOnSettled((payload) => onMaterial({ join: payload.join_command ?? "" })),
  });
}

// NodeActionsCell：worker 行的 Relay command/Revoke（平台管理员 + 非
// manager 行才显）与全员 cordon/uncordon/drain——行点击导航由 DataTable
// 的行内交互短路承接（button 点击不触发行导航）。批4 收尾清偿：五个动词
// 全套命令化（nodesResource.commands，默认同资源失效接 kit 集合键——双
// 前缀失效桥退役）；错误经 onActionError 折算成披露消息（failure 通道
// describeApiError、结果通道 rejected/conflict message）。
function NodeActionsCell({
  node,
  onRelayMaterial,
  onActionError,
  clearActionError,
}: {
  node: NodeRow;
  onRelayMaterial: (script: string) => void;
  onActionError: (message: string) => void;
  clearActionError: () => void;
}) {
  const [revoking, setRevoking] = useState(false);
  const nodeId = node.platform_id ?? "";

  // 动作统一 settle 面：ok 即清披露行（列表随同资源失效即时刷新）；
  // rejected/conflict 走披露行（结果通道 message）；failure 走
  // describeApiError（失败通道）。无 ok toast——可用性变化由列表行
  // Availability 徽章即时呈现（与旧 verb() 行为等价）。
  const settle = (onOk?: (payload: unknown) => void) => (result: CommandResult<unknown> | null, failure: ApiError | null) => {
    if (result != null) {
      if (result.status === "ok") {
        clearActionError();
        onOk?.(result.payload);
      } else {
        onActionError(result.message);
      }
    } else if (failure != null) {
      onActionError(describeApiError(failure));
    }
  };

  const cordon = useCommand(nodesResource.commands.cordon, { onSettled: settle() });
  const uncordon = useCommand(nodesResource.commands.uncordon, { onSettled: settle() });
  const drain = useCommand(nodesResource.commands.drain, { onSettled: settle() });
  const issueRelay = useCommand(nodesResource.commands.issueRelayCommand, {
    onSettled: settle((payload) => onRelayMaterial((payload as { relay_command?: string }).relay_command ?? "")),
  });
  const revoke = useCommand(nodesResource.commands.revokeRelay, { onSettled: settle(() => setRevoking(false)) });

  return (
    <div className="flex flex-wrap items-center justify-end gap-1.5">
      {node.role !== "manager" ? (
        <>
          <Button
            variant="outline"
            size="sm"
            disabled={issueRelay.pending}
            title="Issue this node's individual relay credential and show its install command (sensitive; drops the current relay)"
            onClick={() => issueRelay.mutate({ nodeId })}
          >
            {issueRelay.pending ? "Issuing…" : "Relay command"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="text-[var(--status-danger)]"
            title="Revoke this node's relay credential (the connected relay drops)"
            onClick={() => setRevoking(true)}
          >
            Revoke relay
          </Button>
        </>
      ) : null}
      {node.available ? (
        <Button
          variant="outline"
          size="sm"
          disabled={cordon.pending}
          title="Mark the node unschedulable (reversible)"
          onClick={() => cordon.mutate({ nodeId })}
        >
          cordon
        </Button>
      ) : (
        <Button
          variant="outline"
          size="sm"
          disabled={uncordon.pending}
          title="Return the node to schedulable"
          onClick={() => uncordon.mutate({ nodeId })}
        >
          uncordon
        </Button>
      )}
      <DrainButton hostname={node.hostname ?? ""} onRun={() => drain.mutateAsync({ nodeId })} />
      <ConfirmDialog
        open={revoking}
        onOpenChange={setRevoking}
        title={`Revoke relay credential for ${node.hostname ?? nodeId}?`}
        description="The node's relay drops immediately; exec cannot route until you issue a new relay command and run it on the node."
        confirmLabel="Revoke relay"
        pendingLabel="Revoking…"
        onConfirm={() => revoke.mutateAsync({ nodeId })}
      />
    </div>
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
        onClick={() => {
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
              <pre className="overflow-x-auto rounded-lg border bg-panel-2/40 px-3 py-2.5 pr-9 font-mono text-xs text-[var(--status-success)]">
                {material.join}
              </pre>
              <CopyButton value={material.join} className="absolute top-2 right-2" />
            </div>
          </div>
          <p className="text-xs text-muted">
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
          <pre className="overflow-x-auto rounded-lg border bg-panel-2/40 px-3 py-2.5 pr-9 font-mono text-xs text-[var(--status-info)]">
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
