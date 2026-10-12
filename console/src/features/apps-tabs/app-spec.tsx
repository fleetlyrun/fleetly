import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ActivityIcon, FileJsonIcon, LayersIcon } from "lucide-react";
import { ApiError, apiFetch } from "@/api/client";
import type { components as Delivery } from "@/api/delivery";
import type { components as Workloads } from "@/api/workloads";
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
import { useRevisions } from "@/lib/catalog";

type WorkloadView = Delivery["schemas"]["v1Workload"];
type WorkloadObservation = Workloads["schemas"]["v1WorkloadObservation"];

// App 详情 · Spec tab（ADR-0071 配置可观测面）：三层真源并置——
// ① 冻结 Spec（声明了什么，按 Revision 全文）；② Workload 投影（平台会
// 下发什么，渲染与部署路径单源）；③ 在服观测（集群真跑着什么，含真实
// wire-gen）。缺省寻址 = 当前生效（最新成功部署的 to_revision）；"最新
// 冻结 ≠ 当前生效"显形为提示条（部署失败/回滚中的分野锚）。
export function AppSpecTab({ projectId, appId, seq }: { projectId: string; appId: string; seq?: number }) {
  const navigate = useNavigate();
  const explicitSeq = seq ?? 0;

  const view = useQuery({
    queryKey: ["app-workloads", appId, explicitSeq],
    queryFn: async () => {
      const qs = explicitSeq > 0 ? `?seq=${explicitSeq}` : "";
      return apiFetch<{ revision?: { seq?: string; digest?: string; created_at?: string; source_kind?: string }; workloads?: WorkloadView[] }>(
        `/v1/apps/${encodeURIComponent(appId)}/workloads${qs}`,
      );
    },
  });
  const live = useQuery({
    queryKey: ["app-workloads-live", appId],
    queryFn: async () =>
      apiFetch<{ observations?: WorkloadObservation[] }>(`/v1/apps/${encodeURIComponent(appId)}/workloads/live`),
    refetchInterval: 10_000,
  });
  const revisions = useRevisions(appId);
  const viewSeq = view.data?.revision?.seq ? Number(view.data.revision.seq) : undefined;
  const latestFrozen = revisions.data?.[(revisions.data ?? []).length - 1];
  const latestFrozenSeq = latestFrozen?.seq ? Number(latestFrozen.seq) : undefined;
  const showingEffective = explicitSeq === 0;

  // 冻结全文按渲染寻址到的 seq 取（缺省寻址先解析出生效 Revision）。
  const frozen = useQuery({
    queryKey: ["app-revision-spec", appId, viewSeq ?? -1],
    enabled: viewSeq != null && viewSeq > 0,
    queryFn: async () =>
      apiFetch<{ spec?: unknown }>(`/v1/revisions/${encodeURIComponent(appId)}/${viewSeq}`),
  });

  if (view.isError) {
    const message = view.error instanceof ApiError ? view.error.message : String(view.error);
    return (
      <Card>
        <CardContent className="py-10 text-center text-xs text-muted-foreground">
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
      <section className="flex flex-wrap items-center gap-3 rounded-xl border bg-card p-4">
        <Badge variant="outline" className="font-mono">
          R{viewSeq ?? "…"}
        </Badge>
        {sourceKind ? <Badge variant="secondary">{sourceKind}</Badge> : null}
        <span className="min-w-0 flex-1 truncate font-mono text-2xs text-muted-foreground" title={view.data?.revision?.digest ?? ""}>
          {view.data?.revision?.digest ?? "…"}
        </span>
        <CopyButton value={view.data?.revision?.digest ?? ""} />
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
            {(revisions.data ?? [])
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

      {/* ① 冻结 Spec：全文 JSON */}
      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <FileJsonIcon className="size-3.5 text-muted-foreground" />
            Frozen Spec
          </CardTitle>
          <span className="ml-auto flex items-center gap-2">
            <span className="text-2xs text-muted-foreground">
              {frozen.isFetching ? "loading…" : "protojson · schema-validated at freeze time"}
            </span>
            <CopyButton value={prettySpec(frozen.data)} />
          </span>
        </CardHeader>
        <CardContent>
          <pre className="max-h-96 overflow-auto rounded-lg bg-muted/50 p-3 font-mono text-2xs leading-relaxed">
            {prettySpec(frozen.data) || "…"}
          </pre>
        </CardContent>
      </Card>

      {/* ② Workload 投影：平台会下发什么（渲染与部署路径单源） */}
      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <LayersIcon className="size-3.5 text-muted-foreground" />
            Projected Workloads
          </CardTitle>
          <span className="ml-auto text-2xs text-muted-foreground">
            read-only replay of the deployment projection — secrets never enter this view
          </span>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {(view.data?.workloads ?? []).map((w) => (
            <WorkloadCard key={w.id ?? w.process} workload={w} />
          ))}
          {view.data != null && (view.data.workloads ?? []).length === 0 ? (
            <p className="py-4 text-center text-xs text-muted-foreground">No processes in this revision.</p>
          ) : null}
        </CardContent>
      </Card>

      {/* ③ 在服观测：集群真跑着什么（与投影并置 drift 可见） */}
      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <ActivityIcon className="size-3.5 text-muted-foreground" />
            Live Carriers
          </CardTitle>
          <span className="ml-auto text-2xs text-muted-foreground">in-service observation · 10s snapshot</span>
        </CardHeader>
        <CardContent>
          {live.isError ? (
            <p className="py-4 text-center text-xs text-muted-foreground">
              {live.error instanceof ApiError ? live.error.message : String(live.error)}
            </p>
          ) : (live.data?.observations ?? []).length === 0 ? (
            <p className="py-4 text-center text-xs text-muted-foreground">
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
                {(live.data?.observations ?? []).map((o) => (
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
                        <span className="text-destructive" title={o.rollout_detail ?? ""}>
                          stalled
                        </span>
                      ) : (
                        <span className="text-muted-foreground">ok</span>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// WorkloadCard 是单载体投影卡：image/command/env/ports/探针/资源/调度/
// 卷/网络/DNS——投影视图的用户面全字段（Generation/Materials 不进视图）。
function WorkloadCard({ workload }: { workload: WorkloadView }) {
  const env = Object.entries(workload.env ?? {});
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
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground" title={workload.image ?? ""}>
          {workload.image ?? ""}
        </span>
      </div>
      <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-2xs">
        <dt className="text-muted-foreground">command</dt>
        <dd className="break-all font-mono">{workload.command?.length ? workload.command.join(" ") : "image default"}</dd>
        <dt className="text-muted-foreground">ports</dt>
        <dd className="font-mono">
          {(workload.ports ?? []).map((p) => `${p.port}/${(p.protocol ?? "").replace("PROTOCOL_", "").toLowerCase()}`).join("  ") || "—"}
        </dd>
        <dt className="text-muted-foreground">healthcheck</dt>
        <dd className="font-mono">{healthSummary}</dd>
        <dt className="text-muted-foreground">resources</dt>
        <dd className="font-mono">
          {workload.resources?.cpu_millis || workload.resources?.memory_mb
            ? `${workload.resources?.cpu_millis ? `${workload.resources.cpu_millis}m cpu` : ""}${workload.resources?.cpu_millis && workload.resources?.memory_mb ? " · " : ""}${workload.resources?.memory_mb ? `${workload.resources.memory_mb}MB mem` : ""}`
            : "unlimited"}
        </dd>
        <dt className="text-muted-foreground">placement</dt>
        <dd className="font-mono">{workload.placement?.node_ids?.join(", ") || "any node"}</dd>
        <dt className="text-muted-foreground">volumes</dt>
        <dd className="font-mono">
          {(workload.volumes ?? []).map((v) => `${v.volume_id}→${v.target}${v.read_only ? " (ro)" : ""}`).join("  ") || "—"}
        </dd>
        <dt className="text-muted-foreground">networks</dt>
        <dd className="font-mono">
          {(workload.networks ?? []).join(", ") || "—"}
          {(workload.egress_networks ?? []).length > 0 ? (
            <span className="text-[var(--status-warning)]"> (egress restricted: {(workload.egress_networks ?? []).join(", ")})</span>
          ) : null}
        </dd>
        <dt className="text-muted-foreground">dns</dt>
        <dd className="font-mono">{(workload.addressing ?? []).join("  ") || "—"}</dd>
      </dl>
      {env.length > 0 ? (
        <div className="mt-2">
          <p className="text-2xs text-muted-foreground">env (synthesized, non-sensitive)</p>
          <div className="mt-1 grid gap-x-4 gap-y-0.5 sm:grid-cols-2">
            {env.map(([key, value]) => (
              <div key={key} className="flex min-w-0 items-baseline gap-2 font-mono text-2xs">
                <span className="shrink-0 text-muted-foreground">{key}</span>
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
