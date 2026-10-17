import { Link } from "@tanstack/react-router";
import { ArrowUpRightIcon, BoxesIcon, CheckCircle2Icon } from "lucide-react";
import type { components as runtimeSchemas } from "@/api/runtime";
import { useNodeContainers, useNodes } from "@/lib/catalog";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { PageFrame } from "@/components/domain/page-frame";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, nodeTone } from "@/components/domain/status-badge";
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
type NodeContainer = runtimeSchemas["schemas"]["v1NodeContainer"];

// Node 详情（在跑容器观测，RuntimeNodeInspect 面的 UI 消费）：英雄卡 +
// 节点观测卡（对齐 Task/DB 详情英雄卡形态）+ Containers 表（10s 轮询
// 快照——直读编排器任务/pod 列表，空集 = 观测行非错误）。container 按
// docker 本义使用（2026-10-16 用户裁定）。只读页——cordon/drain 动词面
// 留在列表页行内。
export function NodeDetailPage({ nodeId }: { nodeId: string }) {
  const nodes = useNodes();
  const node = (nodes.data ?? []).find((entry) => entry.platform_id === nodeId);

  return (
    <PageFrame
      breadcrumb={
        <span className="text-xs text-muted-foreground">
          <Link to="/nodes" className="hover:text-foreground">
            Nodes
          </Link>
          <span className="mx-1.5">/</span>
          <span className="text-foreground">{node?.hostname ?? nodeId}</span>
        </span>
      }
    >
      {nodes.isPending ? (
        <Skeleton className="mb-5 h-24 w-full rounded-xl" />
      ) : node == null ? (
        <EmptyState
          icon={CheckCircle2Icon}
          title="Node not found"
          description="It may have left the cluster; the nodes page lists current members."
        />
      ) : (
        <>
          <section className="mb-5 flex flex-wrap items-center gap-4 rounded-xl border bg-card p-5">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <h1 className="font-heading text-xl font-semibold tracking-tight">{node.hostname}</h1>
                <StatusBadge tone={nodeTone(node.available)}>{node.available ? "active" : "unavailable"}</StatusBadge>
                {node.role ? (
                  <span className="rounded-full border px-2 py-0.5 text-2xs font-semibold text-muted-foreground">{node.role}</span>
                ) : null}
              </div>
              <div className="flex items-center gap-1 font-mono text-2xs text-muted-foreground">
                {node.platform_id}
                <CopyButton value={node.platform_id ?? ""} />
              </div>
            </div>
          </section>

          <NodeFacts node={node} />
          <ContainersCard nodeId={nodeId} />
        </>
      )}
    </PageFrame>
  );
}

// NodeFacts 是节点观测卡（定义列表形态对齐 Task 详情 Overview 卡）：
// 中继活体与首末见时间——排障第一眼（relay offline = exec 不可达）。
function NodeFacts({ node }: { node: Node }) {
  return (
    <section className="mb-5 rounded-xl border bg-card p-4">
      <h3 className="mb-3 text-sm font-semibold">Node</h3>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
        <dt className="text-muted-foreground">relay</dt>
        <dd>
          {node.relay_online ? (
            <span className="font-mono" title="node relay online — exec sessions can route to this node">
              online{node.relay_version ? ` · ${node.relay_version}` : ""}
            </span>
          ) : (
            <span className="text-[var(--status-danger)]" title="node relay offline — exec cannot route; re-run the enrollment relay script to recover">
              offline
            </span>
          )}
        </dd>
        <dt className="text-muted-foreground">first seen</dt>
        <dd>
          <RelativeTime value={node.first_seen_at} />
        </dd>
        <dt className="text-muted-foreground">last seen</dt>
        <dd>
          <RelativeTime value={node.last_seen_at} />
        </dd>
      </dl>
    </section>
  );
}

// workloadOf 是容器行的归属文案：App 域显 `app · process`，其余域
//（task/database/browse）workload_id 即域主体，直接呈现。
function workloadOf(row: NodeContainer): string {
  if (row.domain === "app" && row.app_id) {
    return `${row.app_id} · ${row.process ?? ""}`;
  }
  return row.workload_id ?? "—";
}

// workloadCell 是容器行的归属单元：App 域行带 project/app 归属 → 深链
// App 详情（行内投影零解析成本，e922304 挂账收尾）；其余域无 App 语境，
// 纯文本。
function workloadCell(row: NodeContainer) {
  if (row.domain === "app" && row.app_id && row.project_id) {
    return (
      <Link
        to="/p/$projectId/apps/$appId"
        params={{ projectId: row.project_id, appId: row.app_id }}
        className="group/link inline-flex items-center gap-1 text-info hover:underline"
      >
        {workloadOf(row)}
        <ArrowUpRightIcon className="size-3.5 opacity-0 transition-opacity group-hover/link:opacity-100" />
      </Link>
    );
  }
  return workloadOf(row);
}

function ContainersCard({ nodeId }: { nodeId: string }) {
  const containers = useNodeContainers(nodeId);
  const rows = containers.data ?? [];
  return (
    <section className="rounded-xl border bg-card p-4">
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-sm font-semibold">Containers</h3>
        {!containers.isPending && !containers.isError ? (
          <span className="text-2xs text-muted-foreground">{rows.length} running</span>
        ) : null}
      </div>
      {containers.isPending ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 3 }).map((_, index) => (
            <Skeleton key={index} className="h-8 w-full" />
          ))}
        </div>
      ) : containers.isError ? (
        <ErrorState error={containers.error} onRetry={() => void containers.refetch()} />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={BoxesIcon}
          title="No running containers on this node"
          description="Carriers appear here while their tasks are running — drain or scale-down empties the list."
        />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Container</TableHead>
              <TableHead>Workload</TableHead>
              <TableHead>Domain</TableHead>
              <TableHead>Image</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.instance}>
                <TableCell className="font-mono text-xs">
                  <span className="flex items-center gap-1.5">
                    {(row.instance ?? "").slice(0, 14)}…
                    <CopyButton value={row.instance ?? ""} />
                  </span>
                </TableCell>
                <TableCell className="text-xs">{workloadCell(row)}</TableCell>
                <TableCell>
                  {row.domain ? (
                    <span className="rounded-full border px-2 py-0.5 text-2xs font-semibold text-muted-foreground">{row.domain}</span>
                  ) : (
                    <span className="text-xs text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="break-all font-mono text-xs text-muted-foreground">{row.image || "—"}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </section>
  );
}
