import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useCollection } from "@lynx-go/uikit-core";
import { ActivityIcon, FileJsonIcon, LayersIcon, PencilIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { useSpecDeploy } from "@/lib/spec-deploy";
import { ApiError } from "@/api/client";
import { useAppRevisionSpec, useAppWorkloads, useAppWorkloadsLive, type WorkloadObservation, type WorkloadView } from "@/kit/app-observe";
import { CopyButton } from "@/components/domain/copy-button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { revisionsResource, revisionsLookup } from "@/kit/revisions";


// App 详情 · Spec tab（ADR-0071 配置可观测面）：三层真源并置——
// ① 冻结 Spec（声明了什么，按 Revision 全文）；② Workload 投影（平台会
// 下发什么，渲染与部署路径单源）；③ 在服观测（集群真跑着什么，含真实
// wire-gen）。缺省寻址 = 当前生效（最新成功部署的 to_revision）；"最新
// 冻结 ≠ 当前生效"显形为提示条（部署失败/回滚中的分野锚）。
// 批4（2026-10-27）：revisions 读面迁 kit useCollection（routeId 由宿主
// 路由注入；大页 lookup 不截断历史下拉；15s 轮询同旧拍）。
export function AppSpecTab({ projectId, appId, seq, routeId }: { projectId: string; appId: string; seq?: number; routeId: string }) {
  const navigate = useNavigate();
  const explicitSeq = seq ?? 0;

  // 观测面伴读（收尾批 kit 化）：workloads 投影 / 在服观测 10s / 冻结全文
  // 按生效 seq 寻址——轮询拍子与 404/空态语义在 kit/app-observe 单源。
  const view = useAppWorkloads(projectId, appId, explicitSeq);
  const live = useAppWorkloadsLive(projectId, appId);
  const revisions = useCollection(revisionsResource, {
    from: routeId,
    search: revisionsLookup,
    scope: { projectId, appId },
    query: { refetchInterval: 15_000 },
  });
  const viewSeq = view.data?.revision?.seq ? Number(view.data.revision.seq) : undefined;
  const latestFrozen = revisions.rows[revisions.rows.length - 1];
  const latestFrozenSeq = latestFrozen?.seq ? Number(latestFrozen.seq) : undefined;
  const showingEffective = explicitSeq === 0;

  // 冻结全文按渲染寻址到的 seq 取（缺省寻址先解析出生效 Revision）。
  const frozen = useAppRevisionSpec(projectId, appId, viewSeq);

  // 编辑-部署动线（ADR-0072 写面）：冻结体预填可编辑，部署走 spec_file
  //（hook 应用服务端按运行时 overlay 受理）。
  const [editOpen, setEditOpen] = useState(false);
  const [editBody, setEditBody] = useState("");
  const deploy = useSpecDeploy(appId, (id) => {
    void navigate({
      to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
      params: { projectId, appId, deploymentId: id },
    });
  });
  const editValid = (() => {
    try {
      JSON.parse(editBody);
      return true;
    } catch {
      return false;
    }
  })();

  if (view.isError) {
    const message = view.error instanceof ApiError ? view.error.message : String(view.error);
    return (
      <Card>
        <CardContent className="py-10 text-center text-xs text-muted">
          {message.includes("not found")
            ? "No effective revision yet — deploy once, or pick a revision after the first freeze."
            : message}
        </CardContent>
      </Card>
    );
  }

  const sourceKind = (view.data?.revision?.source_kind ?? "").replace("SOURCE_KIND_", "").toLowerCase();

  return (
    <div className="flex flex-col gap-5">
      {/* 生效锚 + 历史 seq 切换（变更走 search 参数——部署详情 View spec 链接可直达） */}
      <section className="flex flex-wrap items-center gap-3 rounded-xl border bg-panel p-4">
        <Badge variant="outline" className="font-mono">
          R{viewSeq ?? "…"}
        </Badge>
        {sourceKind ? <Badge variant="secondary">{sourceKind}</Badge> : null}
        <span className="min-w-0 flex-1 truncate font-mono text-2xs text-muted" title={view.data?.revision?.digest ?? ""}>
          {view.data?.revision?.digest ?? "…"}
        </span>
        <CopyButton value={view.data?.revision?.digest ?? ""} />
        <Button
          size="sm"
          variant="outline"
          disabled={frozen.data?.spec == null}
          onClick={() => {
            setEditBody(JSON.stringify(frozen.data?.spec, null, 2));
            setEditOpen(true);
          }}
        >
          <PencilIcon data-icon-start-inline />
          Edit &amp; deploy
        </Button>
        <Select
          value={explicitSeq > 0 ? String(explicitSeq) : "effective"}
          onValueChange={(value) => {
            void navigate({
              to: "/p/$projectId/apps/$appId/spec",
              params: { projectId, appId },
              search: value === "effective" ? {} : { seq: Number(value) },
            });
          }}
        >
          <SelectTrigger className="h-7 w-40 text-xs">
            <SelectValue placeholder="Revision" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="effective" className="text-xs">
              Effective (latest succeeded)
            </SelectItem>
            {revisions.rows
              .slice()
              .reverse()
              .map((rev) => (
                <SelectItem key={rev.id ?? rev.seq} value={String(Number(rev.seq ?? 0))} className="text-xs">
                  R{rev.seq} · {(rev.source_kind ?? "").replace("SOURCE_KIND_", "").toLowerCase() || "spec"}
                </SelectItem>
              ))}
          </SelectContent>
        </Select>
      </section>

      {showingEffective && latestFrozenSeq != null && viewSeq != null && latestFrozenSeq > viewSeq ? (
        <p className="rounded-lg border border-[color-mix(in_oklch,var(--status-warning)_35%,transparent)] bg-[var(--status-warning-bg)] px-3 py-2 text-xs text-[var(--status-warning)]">
          Latest frozen R{latestFrozenSeq} is not effective yet — the cluster is running R{viewSeq} (the newest deployment failed or was rolled back).
        </p>
      ) : null}

      {/* ⓪ 受理原文（ADR-0074 决策 7，收 W-9）：compose 源的用户亲笔文件
          原样冻结——IR 之上的可读层；非 compose 源无此卡。 */}
      {frozen.data?.revision?.source_text ? (
        <Card>
          <CardHeader className="flex-row items-center">
            <CardTitle className="flex items-center gap-1.5 text-sm">
              <FileJsonIcon className="size-3.5 text-muted" />
              Compose Source
            </CardTitle>
            <span className="ml-auto flex items-center gap-2">
              <span className="text-2xs text-muted">verbatim as accepted · frozen with the revision</span>
              <CopyButton value={frozen.data.revision.source_text} />
            </span>
          </CardHeader>
          <CardContent>
            <pre className="max-h-96 overflow-auto rounded-lg bg-panel-2/50 p-3 font-mono text-2xs leading-relaxed">
              {frozen.data.revision.source_text}
            </pre>
          </CardContent>
        </Card>
      ) : null}

      {/* ① 冻结 Spec：全文 JSON */}
      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <FileJsonIcon className="size-3.5 text-muted" />
            Frozen Spec
          </CardTitle>
          <span className="ml-auto flex items-center gap-2">
            <span className="text-2xs text-muted">
              {frozen.isFetching ? "loading…" : "protojson · schema-validated at freeze time"}
            </span>
            <CopyButton value={prettySpec(frozen.data)} />
          </span>
        </CardHeader>
        <CardContent>
          <pre className="max-h-96 overflow-auto rounded-lg bg-panel-2/50 p-3 font-mono text-2xs leading-relaxed">
            {prettySpec(frozen.data) || "…"}
          </pre>
        </CardContent>
      </Card>

      {/* ② Workload 投影：平台会下发什么（渲染与部署路径单源） */}
      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <LayersIcon className="size-3.5 text-muted" />
            Projected Workloads
          </CardTitle>
          <span className="ml-auto text-2xs text-muted">
            read-only replay of the deployment projection — secrets never enter this view
          </span>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {(view.data?.workloads ?? []).map((w) => (
            <WorkloadCard key={w.id ?? w.process} workload={w} />
          ))}
          {view.data != null && (view.data.workloads ?? []).length === 0 ? (
            <p className="py-4 text-center text-xs text-muted">No processes in this revision.</p>
          ) : null}
        </CardContent>
      </Card>

      {/* ③ 在服观测：集群真跑着什么（与投影并置 drift 可见） */}
      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <ActivityIcon className="size-3.5 text-muted" />
            Live Carriers
          </CardTitle>
          <span className="ml-auto text-2xs text-muted">in-service observation · 10s snapshot</span>
        </CardHeader>
        <CardContent>
          {live.isError ? (
            <p className="py-4 text-center text-xs text-muted">
              {live.error instanceof ApiError ? live.error.message : String(live.error)}
            </p>
          ) : (live.data ?? []).length === 0 ? (
            <p className="py-4 text-center text-xs text-muted">
              No running carriers — replicas may be stopped or between deploys.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Workload</TableHead>
                  <TableHead>Gen</TableHead>
                  <TableHead>Image</TableHead>
                  <TableHead>Replicas</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Rollout</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(live.data ?? []).map((o: WorkloadObservation) => (
                  <TableRow key={o.workload_id}>
                    <TableCell className="font-mono text-xs">{o.workload_id}</TableCell>
                    <TableCell className="text-xs">{o.generation ?? "—"}</TableCell>
                    <TableCell className="max-w-64 truncate font-mono text-xs" title={o.image ?? ""}>
                      {o.image ?? "—"}
                    </TableCell>
                    <TableCell className="text-xs">{o.replicas ?? "—"}</TableCell>
                    <TableCell className="text-xs">{o.state ?? "—"}</TableCell>
                    <TableCell className="text-xs">
                      {o.rollout_stalled ? (
                        <span className="text-err" title={o.rollout_detail ?? ""}>
                          stalled
                        </span>
                      ) : (
                        <span className="text-muted">ok</span>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Dialog open={editOpen} onOpenChange={setEditOpen}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Edit spec &amp; deploy</DialogTitle>
          </DialogHeader>
          <p className="text-2xs text-muted">
            The frozen spec, prefilled — deploys as spec_file. Hook-owned apps deploy a runtime overlay (the material
            half stays with the frozen revision); env values from app variables re-synthesize on top at freeze.
          </p>
          <Textarea
            rows={16}
            className="font-mono text-2xs"
            value={editBody}
            spellCheck={false}
            onChange={(event) => setEditBody(event.target.value)}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditOpen(false)}>
              Cancel
            </Button>
            <Button
              disabled={!editValid || deploy.isPending}
              onClick={() => {
                deploy.mutate(JSON.parse(editBody), {
                  onSuccess: () => {
                    setEditOpen(false);
                    toast("Deployment created — the edited spec was submitted");
                  },
                  onError: (cause) => toast.error(cause instanceof Error ? cause.message : String(cause)),
                });
              }}
            >
              {deploy.isPending ? "Submitting…" : "Deploy"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// WorkloadCard 是单载体投影卡：image/command/env/ports/探针/资源/调度/
// 卷/网络/DNS——投影视图的用户面全字段（Generation/Materials 不进视图）。
function WorkloadCard({ workload }: { workload: WorkloadView }) {
  const env = Object.entries((workload.env ?? {}) as Record<string, string>);
  const health = workload.healthcheck;
  const healthSummary = !health
    ? "none"
    : health.http_path
      ? `http :${health.http_port ?? "?"}${health.http_path}`
      : health.tcp_port
        ? `tcp :${health.tcp_port}`
        : health.exec?.length
          ? `exec ${health.exec.join(" ")}`
          : "none";
  return (
    <div className="rounded-lg border p-3">
      <div className="flex flex-wrap items-baseline gap-2">
        <span className="text-sm font-medium">{workload.process}</span>
        <Badge variant="secondary">×{Number(workload.replicas ?? 0)} replicas</Badge>
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted" title={workload.image ?? ""}>
          {workload.image ?? ""}
        </span>
      </div>
      <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-2xs">
        <dt className="text-muted">command</dt>
        <dd className="break-all font-mono">{workload.command?.length ? workload.command.join(" ") : "image default"}</dd>
        <dt className="text-muted">ports</dt>
        <dd className="font-mono">
          {(workload.ports ?? []).map((p) => `${p.port}/${(p.protocol ?? "").replace("PROTOCOL_", "").toLowerCase()}`).join("  ") || "—"}
        </dd>
        <dt className="text-muted">healthcheck</dt>
        <dd className="font-mono">{healthSummary}</dd>
        <dt className="text-muted">resources</dt>
        <dd className="font-mono">
          {workload.resources?.cpu_millis || workload.resources?.memory_mb
            ? `${workload.resources?.cpu_millis ? `${workload.resources.cpu_millis}m cpu` : ""}${workload.resources?.cpu_millis && workload.resources?.memory_mb ? " · " : ""}${workload.resources?.memory_mb ? `${workload.resources.memory_mb}MB mem` : ""}`
            : "unlimited"}
        </dd>
        <dt className="text-muted">placement</dt>
        <dd className="font-mono">{workload.placement?.node_ids?.join(", ") || "any node"}</dd>
        <dt className="text-muted">volumes</dt>
        <dd className="font-mono">
          {(Object.values(workload.volumes ?? {}) as Array<{ volume_id?: string; target?: string; read_only?: boolean }>)
            .map((v) => `${v.volume_id}→${v.target}${v.read_only ? " (ro)" : ""}`)
            .join("  ") || "—"}
        </dd>
        <dt className="text-muted">networks</dt>
        <dd className="font-mono">
          {(workload.networks ?? []).join(", ") || "—"}
          {(workload.egress_networks ?? []).length > 0 ? (
            <span className="text-[var(--status-warning)]"> (egress restricted: {(workload.egress_networks ?? []).join(", ")})</span>
          ) : null}
        </dd>
        <dt className="text-muted">dns</dt>
        <dd className="font-mono">{(workload.addressing ?? []).join("  ") || "—"}</dd>
      </dl>
      {env.length > 0 ? (
        <div className="mt-2">
          <p className="text-2xs text-muted">env (synthesized, non-sensitive)</p>
          <div className="mt-1 grid gap-x-4 gap-y-0.5 sm:grid-cols-2">
            {env.map(([key, value]) => (
              <div key={key} className="flex min-w-0 items-baseline gap-2 font-mono text-2xs">
                <span className="shrink-0 text-muted">{key}</span>
                <span className="min-w-0 flex-1 truncate" title={value}>
                  {value}
                </span>
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function prettySpec(data: unknown): string {
  if (data == null || typeof data !== "object" || !("spec" in data)) return "";
  try {
    return JSON.stringify((data as { spec: unknown }).spec, null, 2);
  } catch {
    return "";
  }
}
