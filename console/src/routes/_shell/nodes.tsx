import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { CheckCircle2Icon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";
import type { components as runtimeSchemas } from "@/api/runtime";
import { apiSend } from "@/api/client";
import { useNodes } from "@/lib/catalog";
import { describeError } from "@/lib/api-errors";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { ErrorState } from "@/components/domain/error-state";
import { ListToolbar, useListFilter } from "@/components/domain/list-toolbar";
import { PageHeader } from "@/components/domain/page-header";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge, nodeTone } from "@/components/domain/status-badge";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
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

// 节点页（C4 → UI v2 批 5 reskin）：RuntimeAdmin 的 UI 面——集群成员表
//（可用态/中继活体）+ 运维三动词（drain/cordon/uncordon，drain 真迁移
// 任务）+ enroll 材料（join/relay 两条命令，等价集群成员权——platform
// admin 档才能取）。动词面与 W2 措辞（unavailable = 历史行语义）原样。
export const Route = createFileRoute("/_shell/nodes")({
  component: NodesPage,
});

function NodesPage() {
  const nodes = useNodes();
  const [material, setMaterial] = useState<{ join: string; relay: string } | null>(null);
  const [rotating, setRotating] = useState(false);
  const enroll = useEnrollMaterial(false, setMaterial);
  const rotate = useEnrollMaterial(true, setMaterial);

  const [query, setQuery] = useState("");
  const sorted = [...(nodes.data ?? [])].sort((a, b) => Number(b.available ?? false) - Number(a.available ?? false));
  const rows = useListFilter(sorted, query, (node: { platform_id?: string; hostname?: string; role?: string }) => [node.hostname ?? "", node.platform_id ?? "", node.role ?? ""]);

  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader
        title="Nodes"
        description="Cluster membership, relay liveness and the runtime admin verbs"
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
      />

      <div className="rounded-xl border bg-card">
        <div className="px-3 pt-3">
          <ListToolbar label="nodes" value={query} onChange={setQuery} placeholder="Filter nodes..." total={(nodes.data ?? []).length} shown={rows.length} />
        </div>
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
            ) : rows.length === 0 ? (
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
              rows.map((node) => <NodeRow key={node.platform_id} node={node} />)
            )}
          </TableBody>
        </Table>
      </div>
      <p className="mt-3 text-xs text-muted-foreground">
        Drain migrates running tasks off the node (real task movement); cordon/uncordon toggle schedulability. The control-plane node
        hosts the managed carriers (proxy, registry, metrics) and cannot be drained away from them.
      </p>

      {material != null ? <MaterialDialog material={material} onClose={() => setMaterial(null)} /> : null}
      <AlertDialog open={rotating} onOpenChange={setRotating}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Rotate all join tokens?</AlertDialogTitle>
            <AlertDialogDescription>
              Previously issued enrollment material stops working immediately (leak handling). Nodes already joined are unaffected.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction asChild>
              <Button
                variant="destructive"
                disabled={rotate.isPending}
                onClick={() =>
                  rotate.mutate(undefined, {
                    onSuccess: () => setRotating(false),
                  })
                }
              >
                {rotate.isPending ? "Rotating…" : "Rotate tokens"}
              </Button>
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function useEnrollMaterial(rotate: boolean, onMaterial: (data: { join: string; relay: string }) => void) {
  return useMutation({
    mutationFn: () => apiSend<{ join_command?: string; relay_command?: string }>("/v1/nodes/enroll", "POST", { rotate }),
    onSuccess: (data) => onMaterial({ join: data.join_command ?? "", relay: data.relay_command ?? "" }),
    onError: (cause) => toast.error(`${describeError(cause).title} — ${describeError(cause).detail}`),
  });
}

function NodeRow({ node }: { node: Node }) {
  const queryClient = useQueryClient();
  const [actionError, setActionError] = useState<unknown>(null);

  function verb(path: string) {
    setActionError(null);
    return apiSend(path, "POST", {})
      .then(() => queryClient.invalidateQueries({ queryKey: ["nodes"] }))
      .catch((cause: unknown) => setActionError(cause));
  }

  return (
    <>
      <TableRow className={node.available ? "" : "opacity-60"}>
        <TableCell className="font-medium">
          {node.hostname}
          <span className="ml-1.5 font-mono text-[11px] text-muted-foreground" title={node.platform_id}>
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
            <span className="text-[var(--status-danger)]" title="node relay offline — exec cannot route; re-run the enrollment relay script to recover">
              offline
            </span>
          )}
        </TableCell>
        <TableCell className="text-xs text-muted-foreground">
          <RelativeTime value={node.last_seen_at} />
        </TableCell>
        <TableCell className="text-right">
          <div className="flex items-center justify-end gap-1.5">
            {node.available ? (
              <Button
                variant="outline"
                size="sm"
                title="Mark the node unschedulable (reversible)"
                onClick={() => void verb(`/v1/nodes/${encodeURIComponent(node.platform_id ?? "")}/cordon`)}
              >
                cordon
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                title="Return the node to schedulable"
                onClick={() => void verb(`/v1/nodes/${encodeURIComponent(node.platform_id ?? "")}/uncordon`)}
              >
                uncordon
              </Button>
            )}
            <DrainButton hostname={node.hostname ?? ""} onRun={() => verb(`/v1/nodes/${encodeURIComponent(node.platform_id ?? "")}/drain`)} />
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
    </>
  );
}

function DrainButton({ hostname, onRun }: { hostname: string; onRun: () => Promise<unknown> }) {
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  return (
    <>
      <Button variant="outline" size="sm" className="text-[var(--status-danger)]" onClick={() => setOpen(true)}>
        drain
      </Button>
      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Drain {hostname}?</AlertDialogTitle>
            <AlertDialogDescription>Running tasks migrate to other nodes in real time.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction asChild>
              <Button
                variant="destructive"
                disabled={pending}
                onClick={() => {
                  setPending(true);
                  void onRun().finally(() => {
                    setPending(false);
                    setOpen(false);
                  });
                }}
              >
                {pending ? "Draining…" : "Drain node"}
              </Button>
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

// MaterialDialog 是 enroll 材料揭示面：join（集群加入）与 relay（节点中继
// 装载）两条命令——等价集群成员权，仅铸造/轮换响应可见（一次性诚实边界）。
function MaterialDialog({ material, onClose }: { material: { join: string; relay: string }; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Enrollment material (keep private)</DialogTitle>
          <DialogDescription>
            Run these on the target worker node. The join material is equivalent to cluster membership — treat it like a credential.
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
          <div className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">node relay install command</span>
            <div className="relative">
              <pre className="overflow-x-auto rounded-lg border bg-muted/40 px-3 py-2.5 pr-9 font-mono text-xs text-[var(--status-info)]">
                {material.relay}
              </pre>
              <CopyButton value={material.relay} className="absolute top-2 right-2" />
            </div>
          </div>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Done</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
