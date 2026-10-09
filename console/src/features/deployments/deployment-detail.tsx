import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeftIcon, CopyIcon, RocketIcon } from "lucide-react";
import { toast } from "sonner";
import type { components } from "@/api/delivery";
import { apiFetch, apiSend } from "@/api/client";
import { streamDeploymentWait } from "@/api/streams";
import { useRevisions } from "@/lib/catalog";
import { CopyButton } from "@/components/domain/copy-button";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge, isActiveDeploymentState } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatAbsolute, formatDuration } from "@/lib/format";
import { describeError } from "@/lib/api-errors";

// 部署详情（Detail 原型旗舰页，UI v2 批 2）：wait 流跟踪（ADR-0044 决策 1
// 预留面）+ 断流自动重连（指数退避 ×3）+ 终态前 10s 轮询兜底（旧页需手动
// reopen 的顽疾）；蓝绿双代窗叙事（ADR-0048）+ 阶段时间线 + per-process
// 策略表 + Revision diff + Raw 折叠。raw 字段默认收起（旧页默认展开的反面）。

type Deployment = components["schemas"]["v1Deployment"];
type Revision = components["schemas"]["v1Revision"];

const TERMINAL_STATES = new Set(["succeeded", "failed", "canceled", "superseded"]);
const RECONNECT_DELAYS_MS = [1_000, 2_000, 4_000];
const POLL_FALLBACK_MS = 10_000;

const STAGES = ["queued", "preparing", "running", "observing", "succeeded"] as const;

// stageIndex 把任意 state 映射到时间线位（failed/canceled/superseded 落在
// 最近活跃步之后，徽章色已表达终局语义）。
function stageIndex(state: string): number {
  const found = STAGES.indexOf(state as (typeof STAGES)[number]);
  if (found >= 0) return found;
  if (state === "releasing") return 3;
  return 4;
}

export function DeploymentDetailView({
  projectId,
  appId,
  deploymentId,
}: {
  projectId: string;
  appId: string;
  deploymentId: string;
}) {
  const [live, setLive] = useState<Deployment | null>(null);
  const [streamError, setStreamError] = useState<unknown>(null);
  const [streamStalled, setStreamStalled] = useState(false);
  const [restartToken, setRestartToken] = useState(0);
  const mounted = useRef(true);

  // wait 流生命周期：终态收流；传输中断按退避序列自动重开三次，仍未达
  // 终态则转 10s 轮询兜底（详情数据面单查 GET /v1/deployments?app_id）。
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setStreamError(null);
    setStreamStalled(false);
    const run = (attempt: number) => {
      void streamDeploymentWait(deploymentId, controller.signal, (frame) => {
        if (!mounted.current) return;
        setLive(frame as Deployment);
        setRestartToken(0);
      })
        .then(({ ended }) => {
          if (!mounted.current) return;
          if (!ended && attempt < RECONNECT_DELAYS_MS.length) {
            window.setTimeout(() => {
              if (mounted.current && !controller.signal.aborted) run(attempt + 1);
            }, RECONNECT_DELAYS_MS[attempt]);
          } else if (!ended) {
            setStreamStalled(true);
          }
        })
        .catch((cause: unknown) => {
          if (mounted.current && !controller.signal.aborted) setStreamError(cause);
        });
    };
    run(0);
    return () => controller.abort();
  }, [deploymentId, restartToken]);

  // 轮询兜底：流 stalled 或尚未收到首帧时兜住数据面（5s 频率借用 app 列表口径的
  // 低频形态——wait 流健康时本查询 staleTime 内不发）。
  const poll = useQuery({
    queryKey: ["deployment-poll", deploymentId],
    enabled: streamStalled || (live == null && streamError == null),
    refetchInterval: POLL_FALLBACK_MS,
    queryFn: async (): Promise<Deployment | null> => {
      const res = await apiFetch<{ deployments?: Array<Deployment | undefined> }>(
        `/v1/deployments?app_id=${encodeURIComponent(appId)}&limit=50`,
      );
      const hit = (res.deployments ?? []).find((item) => item?.id === deploymentId);
      return hit ?? null;
    },
  });

  const effective = live ?? poll.data ?? null;
  const state = effective?.state ?? "";
  const active = isActiveDeploymentState(state);

  const revisions = useRevisions(appId);
  const revisionIndex = new Map((revisions.data ?? []).map((rev) => [rev.id ?? "", rev]));
  const toRevision = effective?.to_revision ? revisionIndex.get(effective.to_revision) : undefined;

  const invalidate = useCallback(() => {
    void poll.refetch();
  }, [poll]);

  const cancel = useDeployAction(
    `/v1/deployments/${encodeURIComponent(deploymentId)}/cancel`,
    "POST",
    invalidate,
  );
  const rollback = useDeployAction(`/v1/apps/${encodeURIComponent(appId)}/rollback`, "POST", invalidate);

  if (effective == null && streamError != null) {
    return (
      <div className="p-8">
        <div className="text-sm font-semibold text-[var(--status-danger)]">{describeError(streamError).title}</div>
        <div className="mt-1 font-mono text-xs text-muted-foreground">{describeError(streamError).detail}</div>
        <Button variant="outline" size="sm" className="mt-3" onClick={() => setRestartToken((token) => token + 1)}>
          Reconnect stream
        </Button>
      </div>
    );
  }
  if (effective == null) {
    return (
      <div className="flex flex-col gap-3 p-6">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }

  const startedAt = effective.created_at;
  const finishedAt = effective.finished_at;
  const durationSeconds =
    startedAt && finishedAt
      ? Math.max(0, (Date.parse(finishedAt) - Date.parse(startedAt)) / 1000)
      : undefined;
  const windowInfo = blueGreenWindow(effective, toRevision);
  const currentStage = stageIndex(state);

  return (
    <div className="flex flex-col gap-4 p-6">
      {/* hero：状态 + 标识 + 主操作 */}
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="outline" size="icon-sm" asChild>
          <a href={`/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}/deployments`} title="All deployments">
            <ArrowLeftIcon className="size-4" />
          </a>
        </Button>
        <h1 className="font-heading text-lg font-bold tracking-tight">
          Deployment <span className="font-mono">R{toRevision?.seq ?? "?"}</span>
        </h1>
        <DeploymentStatusBadge state={state} />
        <span className="font-mono text-xs text-muted-foreground">{effective.id}</span>
        <CopyButton value={effective.id ?? ""} />
        <div className="ml-auto flex items-center gap-2">
          {active ? (
            <Button
              variant="outline"
              size="sm"
              disabled={cancel.pending}
              onClick={() =>
                cancel.mutate(undefined, {
                  onSuccess: () => toast("Cancellation requested"),
                })
              }
            >
              Cancel
            </Button>
          ) : null}
          <Button
            variant="outline"
            size="sm"
            disabled={rollback.pending}
            onClick={() =>
              rollback.mutate(undefined, {
                onSuccess: (data) => {
                  const replayId = (data as { deployment?: Deployment }).deployment?.id;
                  toast("Rollback accepted", {
                    description: replayId ? `replay deployment ${replayId.slice(0, 12)}…` : undefined,
                  });
                },
              })
            }
          >
            <RocketIcon className="size-3.5" />
            Rollback app
          </Button>
        </div>
      </div>

      {/* 阶段时间线 */}
      <div className="rounded-xl border bg-card px-5 pt-5 pb-4">
        <Timeline currentStage={currentStage} state={state} deployment={effective} />
        <div className="mt-3 flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
          <span>started <RelativeTime value={startedAt} className="font-mono" /></span>
          <span>
            duration{" "}
            <span className="font-mono">{durationSeconds !== undefined ? formatDuration(durationSeconds) : "—"}</span>
          </span>
          {effective.error ? (
            <span className="font-mono text-[var(--status-danger)]" title={effective.error}>
              {effective.error.length > 80 ? `${effective.error.slice(0, 80)}…` : effective.error}
            </span>
          ) : null}
          {streamStalled && !TERMINAL_STATES.has(state) ? (
            <span className="text-[var(--status-warning)]">stream stalled — polling fallback active</span>
          ) : null}
        </div>
      </div>

      {windowInfo ? <GenerationWindowCard window={windowInfo} /> : null}
      {cancel.error ? <ErrorLine error={cancel.error} /> : null}
      {rollback.error ? <ErrorLine error={rollback.error} /> : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <ProcessStrategiesCard deployment={effective} toRevision={toRevision} />
        <RevisionsDiffCard appId={appId} revisions={revisions.data ?? []} />
      </div>

      <details className="rounded-xl border bg-card px-4 py-3">
        <summary className="cursor-pointer text-sm font-semibold text-muted-foreground">Raw fields (protojson)</summary>
        <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 font-mono text-xs text-muted-foreground">
          <dt>app_id</dt><dd>{effective.app_id}</dd>
          <dt>from_revision</dt><dd className="break-all">{effective.from_revision || "—"}</dd>
          <dt>to_revision</dt><dd className="break-all">{effective.to_revision || "—"}</dd>
          <dt>generation</dt><dd>{effective.generation ?? "—"}</dd>
          <dt>from_generation</dt><dd>{effective.from_generation ?? "—"}</dd>
          <dt>observe_deadline</dt><dd>{effective.observe_deadline || "—"}</dd>
          <dt>commit_sha</dt><dd>{effective.commit_sha || "—"}</dd>
          <dt>idempotency_key</dt><dd>{effective.idempotency_key || "—"}</dd>
          <dt>superseded_by</dt><dd>{effective.superseded_by || "—"}</dd>
          <dt>first_boot_task_id</dt><dd>{effective.first_boot_task_id || "—"}</dd>
          <dt>created_at</dt><dd>{formatAbsolute(effective.created_at)}</dd>
          <dt>updated_at</dt><dd>{formatAbsolute(effective.updated_at)}</dd>
          <dt>finished_at</dt><dd>{formatAbsolute(effective.finished_at)}</dd>
        </dl>
      </details>
    </div>
  );
}

// ---- 时间线 ----

function Timeline({
  currentStage,
  state,
  deployment,
}: {
  currentStage: number;
  state: string;
  deployment: Deployment;
}) {
  const done = state === "succeeded";
  const aborted = state === "failed" || state === "canceled" || state === "superseded";
  return (
    <div className="flex items-start">
      {STAGES.map((stage, index) => {
        const isDone = index < currentStage || (done && index === STAGES.length - 1);
        const isCurrent = index === currentStage && !done;
        return (
          <div key={stage} className="flex flex-1 flex-col items-center">
            <div className="flex w-full items-center">
              <span className={`h-0.5 flex-1 ${index === 0 ? "invisible" : isDone ? "bg-[var(--status-success)]" : "bg-border"}`} />
              <span
                className={`grid size-5 flex-none place-items-center rounded-full border-2 text-[10px] ${
                  isDone
                    ? "border-[var(--status-success)] bg-[var(--status-success)] text-white"
                    : isCurrent
                      ? "border-[var(--status-info)] text-[var(--status-info)]"
                      : "border-border-strong text-transparent"
                }`}
              >
                {isDone ? "✓" : isCurrent ? <span className="size-1.5 animate-pulse rounded-full bg-current" /> : "·"}
              </span>
              <span
                className={`h-0.5 flex-1 ${
                  index === STAGES.length - 1
                    ? "invisible"
                    : index < currentStage || (isDone && index === currentStage)
                      ? "bg-[var(--status-success)]"
                      : "bg-border"
                }`}
              />
            </div>
            <span className={`mt-1.5 text-[11.5px] font-semibold ${isDone || isCurrent ? "" : "text-muted-foreground"}`}>
              {stage === "succeeded" && aborted ? "outcome" : stage}
            </span>
            {aborted && index === STAGES.length - 1 ? (
              <span className="font-mono text-[10.5px] text-[var(--status-danger)]">{state}</span>
            ) : null}
          </div>
        );
      })}
      {/* 观察窗倒计时（呈现面，判定源是 wait 流下一帧） */}
      {state === "observing" && deployment.observe_deadline ? (
        <span className="ml-3 font-mono text-xs text-[var(--status-info)]">
          <ObserveCountdown deadline={deployment.observe_deadline} />
        </span>
      ) : null}
    </div>
  );
}

function ObserveCountdown({ deadline }: { deadline: string }) {
  const [remaining, setRemaining] = useState(() => deadlineRemaining(deadline));
  useEffect(() => {
    const timer = window.setInterval(() => setRemaining(deadlineRemaining(deadline)), 1000);
    return () => window.clearInterval(timer);
  }, [deadline]);
  if (remaining == null) return null;
  return <span>observe window: {remaining}</span>;
}

function deadlineRemaining(deadline: string): string | null {
  const date = new Date(deadline);
  if (Number.isNaN(date.getTime())) return null;
  const diff = Math.floor((date.getTime() - Date.now()) / 1000);
  if (diff <= 0) return "closing";
  return `${Math.floor(diff / 60)}m ${String(diff % 60).padStart(2, "0")}s`;
}

// ---- 蓝绿双代窗（ADR-0048 叙事保真） ----

interface GenerationWindow {
  fromGen: string;
  toGen: string;
  observing: boolean;
  processes: string[];
}

function blueGreenWindow(deployment: Deployment, toRevision: Revision | undefined): GenerationWindow | null {
  const from = Number(deployment.from_generation ?? "0");
  const to = Number(deployment.generation ?? "0");
  if (from === 0 || to <= from) return null;
  const strategies = toRevision?.process_strategies ?? [];
  if (!strategies.some((entry) => entry?.strategy === "DEPLOY_STRATEGY_BLUE_GREEN")) return null;
  return {
    fromGen: String(from),
    toGen: String(to),
    observing: deployment.state === "observing",
    processes: strategies
      .filter((entry) => entry?.strategy === "DEPLOY_STRATEGY_BLUE_GREEN")
      .map((entry) => entry?.process ?? "?"),
  };
}

function GenerationWindowCard({ window: info }: { window: GenerationWindow }) {
  return (
    <div className="rounded-xl border border-[color-mix(in_oklch,var(--status-info)_35%,transparent)] bg-[color-mix(in_oklch,var(--status-info)_5%,var(--card))] px-4 py-3">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h3 className="text-sm font-semibold text-[var(--status-info)]">
          {info.observing ? "blue-green window · observing" : "blue-green window"}
        </h3>
        <span className="font-mono text-xs text-muted-foreground">
          current g{info.toGen} ← serving g{info.fromGen}
        </span>
      </div>
      <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
        Both generations of <span className="font-mono text-foreground">{info.processes.join(", ")}</span> run side by side until
        the new one is fully healthy; traffic switches only after that. During the window replicas double for those processes,
        and cross-process DNS names (<span className="font-mono">{"{process}"}</span> /{" "}
        <span className="font-mono">{"{process}.{app}"}</span>) may round-robin to either generation — use the proxy route or
        the generation name for precise targeting.
      </p>
    </div>
  );
}

// ---- 进程策略表 ----

function ProcessStrategiesCard({ deployment, toRevision }: { deployment: Deployment; toRevision: Revision | undefined }) {
  const strategies = toRevision?.process_strategies ?? [];
  return (
    <div className="rounded-xl border bg-card px-4 py-3">
      <h3 className="text-sm font-semibold">Processes</h3>
      {strategies.length === 0 ? (
        <p className="mt-1 text-xs text-muted-foreground">
          No strategy catalog on the target revision (older revision or data unavailable).
        </p>
      ) : (
        <Table className="mt-2 text-[12.5px]">
          <TableHeader>
            <TableRow>
              <TableHead>process</TableHead>
              <TableHead>strategy</TableHead>
              <TableHead>DNS names</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {strategies.map((entry) => {
              const process = entry?.process ?? "?";
              const blueGreen = entry?.strategy === "DEPLOY_STRATEGY_BLUE_GREEN";
              return (
                <TableRow key={process}>
                  <TableCell className="font-mono">{process}</TableCell>
                  <TableCell>
                    <span className={blueGreen ? "text-[var(--status-info)]" : "text-muted-foreground"}>
                      {blueGreen ? "blue-green" : "rolling"}
                    </span>
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">
                    {process} · {process}.{deployment.app_id?.slice(0, 10)}…{blueGreen ? ` · ${process}.g${deployment.generation ?? "?"}` : ""}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}
      <p className="mt-2 text-[11px] leading-relaxed text-muted-foreground">
        Bare and fully-qualified names are stable references; the generation name targets one generation for debugging and
        moves with each deploy.
      </p>
    </div>
  );
}

// ---- Revision diff ----

type DiffEntry = { path?: string; old_value?: string; new_value?: string };

function RevisionsDiffCard({ appId, revisions }: { appId: string; revisions: Revision[] }) {
  const seqs = revisions
    .map((rev) => Number(rev.seq ?? "0"))
    .filter((seq) => seq > 0)
    .sort((a, b) => b - a);
  const [fromSeq, setFromSeq] = useState("");
  const [toSeq, setToSeq] = useState("");
  const diff = useQuery({
    queryKey: ["revisions", "diff", appId, fromSeq, toSeq],
    enabled: fromSeq !== "" && toSeq !== "" && fromSeq !== toSeq,
    queryFn: async (): Promise<DiffEntry[]> => {
      const res = await apiFetch<{ entries?: Array<DiffEntry | undefined> }>(
        `/v1/revisions/diff?app_id=${encodeURIComponent(appId)}&from_seq=${fromSeq}&to_seq=${toSeq}`,
      );
      return (res.entries ?? []).flatMap((entry) => (entry != null ? [entry] : []));
    },
  });
  return (
    <div className="rounded-xl border bg-card px-4 py-3">
      <h3 className="text-sm font-semibold">Revision diff</h3>
      <div className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
        from
        <DiffSeqSelect value={fromSeq} onChange={setFromSeq} seqs={seqs} />
        to
        <DiffSeqSelect value={toSeq} onChange={setToSeq} seqs={seqs} />
      </div>
      {diff.data != null ? (
        diff.data.length === 0 ? (
          <p className="mt-2 text-xs text-muted-foreground">no field-level differences between the selected revisions.</p>
        ) : (
          <Table className="mt-2 text-xs">
            <TableHeader>
              <TableRow>
                <TableHead>path</TableHead>
                <TableHead>from</TableHead>
                <TableHead>to</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {diff.data.map((entry) => (
                <TableRow key={entry.path}>
                  <TableCell className="font-mono">{entry.path}</TableCell>
                  <TableCell className="max-w-40 truncate font-mono text-[var(--status-danger)]" title={entry.old_value}>
                    {entry.old_value || "—"}
                  </TableCell>
                  <TableCell className="max-w-40 truncate font-mono text-[var(--status-success)]" title={entry.new_value}>
                    {entry.new_value || "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )
      ) : (
        <p className="mt-2 text-xs text-muted-foreground">pick two revisions to diff.</p>
      )}
      {diff.isFetching ? <Separator className="my-2" /> : null}
    </div>
  );
}

function DiffSeqSelect({ value, onChange, seqs }: { value: string; onChange: (value: string) => void; seqs: number[] }) {
  return (
    <select
      value={value}
      onChange={(event) => onChange(event.target.value)}
      className="rounded-md border bg-background px-2 py-1 font-mono text-xs"
    >
      <option value="">—</option>
      {seqs.map((seq) => (
        <option key={seq} value={String(seq)}>
          R{seq}
        </option>
      ))}
    </select>
  );
}

// ---- 行动 mutation 与错误行 ----

function useDeployAction(path: string, method: string, invalidate: () => void) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [data, setData] = useState<unknown>(null);
  const mutate = useCallback(
    (body: undefined, handlers?: { onSuccess?: (data: unknown) => void }) => {
      setPending(true);
      setError(null);
      void apiSend(path, method, body)
        .then((response) => {
          setData(response);
          invalidate();
          handlers?.onSuccess?.(response);
        })
        .catch((cause: unknown) => setError(cause))
        .finally(() => setPending(false));
    },
    [path, method, invalidate],
  );
  return { mutate, pending, error, data };
}

function ErrorLine({ error }: { error: unknown }) {
  const described = describeError(error);
  return (
    <div className="flex items-center gap-2 rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 text-xs">
      <span className="font-semibold text-[var(--status-danger)]">{described.title}</span>
      <span className="font-mono text-muted-foreground">{described.detail}</span>
      <CopyIcon className="size-3 text-muted-foreground" />
    </div>
  );
}
