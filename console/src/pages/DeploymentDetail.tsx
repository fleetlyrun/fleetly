import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { components } from "../api/delivery";
import { apiFetch } from "../api/client";
import { streamDeploymentWait } from "../api/streams";
import { useRevisions } from "../lib/catalog";
import {
  DangerRowButton,
  ErrorNote,
  LoadingNote,
  PageShell,
  RowButton,
  formatTime,
  shortId,
  useApiMutation,
} from "../components/ui";
import { isActiveState, stateBadgeClass } from "./Deployments";

// 部署详情页（F3.1 双代窗叙事——ADR-0048 价值最大化面）：等待流跟踪
//（ADR-0044 决策 1 为本页预留的 wait 消费面）+ 双代窗状态（当前代/上一
// 代、观察窗倒计时）+ per-process strategy（Revision 策略目录）+ 三种别
// 名形态 + 资源代价/跨进程引用诚实边界（架构 §5 口径）。

type Deployment = components["schemas"]["v1Deployment"];
type Revision = components["schemas"]["v1Revision"];

const TERMINAL_STATES = new Set(["succeeded", "failed", "canceled", "superseded"]);

export function DeploymentDetailPage({ id, navigate }: { id: string; navigate: (path: string) => void }) {
  const [deployment, setDeployment] = useState<Deployment | null>(null);
  const [streamError, setStreamError] = useState<unknown>(null);
  const [streamEnded, setStreamEnded] = useState(false);
  const restartToken = useState(0)[1];
  const terminalRef = useRef(false);

  // wait 流生命周期：终态帧后服务端收流（不再重开）；传输层中断 =
  // ended=false，可手动重开。卸载即 abort。
  useEffect(() => {
    const controller = new AbortController();
    setStreamError(null);
    setStreamEnded(false);
    void streamDeploymentWait(id, controller.signal, (frame) => setDeployment(frame as Deployment))
      .then(({ ended }) => {
        setStreamEnded(!ended);
        if (ended) terminalRef.current = true;
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setStreamError(cause);
      });
    return () => controller.abort();
  }, [id, restartToken]);

  const effective = deployment;
  const appId = effective?.app_id ?? "";
  // App 名单跳取（GetApp 注解面）：别名形态 {proc}.{app} 的展示锚。
  const appRow = useQuery({
    queryKey: ["detail", "app", appId],
    enabled: appId !== "",
    queryFn: async (): Promise<string> => {
      const res = await apiFetch<{ app?: { name?: string } }>(`/v1/apps/${encodeURIComponent(appId)}`);
      return res.app?.name ?? "";
    },
  });
  const appName = appRow.data;

  const revisions = useRevisions(appId);
  const revisionIndex = new Map((revisions.data ?? []).map((rev) => [rev.id ?? "", rev]));
  const toRevision = effective?.to_revision ? revisionIndex.get(effective.to_revision) : undefined;

  const cancel = useApiMutation<{ deployment?: Deployment }>({
    path: `/v1/deployments/${encodeURIComponent(id)}/cancel`,
    method: "POST",
  });
  const rollback = useApiMutation<{ deployment?: Deployment }>({
    path: () => `/v1/apps/${encodeURIComponent(appId)}/rollback`,
    method: "POST",
    body: () => ({}),
  });

  if (effective == null && streamError != null) {
    return (
      <PageShell title={`Deployment ${shortId(id)}`}>
        <ErrorNote error={streamError} hint={`GET /v1/deployments/${id}/wait failed.`} />
        <RowButton onClick={() => restartToken((token) => token + 1)}>retry</RowButton>
      </PageShell>
    );
  }
  if (effective == null) return <LoadingNote label="Loading deployment…" />;

  const state = effective.state ?? "";
  const window = blueGreenWindow(effective, toRevision);
  const active = isActiveState(state);

  return (
    <PageShell
      title={`Deployment ${shortId(effective.id)}`}
      hint={`app ${appName ?? shortId(appId)} · ${state}`}
      toolbar={
        <>
          <RowButton onClick={() => navigate("deployments")}>← all deployments</RowButton>
          {active ? (
            <DangerRowButton confirm={`Cancel deployment ${effective.id}?`} disabled={cancel.isPending} onClick={() => void cancel.mutate()}>
              cancel
            </DangerRowButton>
          ) : null}
          {appId !== "" ? (
            <RowButton disabled={rollback.isPending} onClick={() => void rollback.mutate()}>
              rollback app
            </RowButton>
          ) : null}
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-3">
          <span className={`inline-block rounded px-2 py-1 text-sm font-medium ${stateBadgeClass(state)}`}>
            {state}
            {active ? <span className="ml-1 animate-pulse">●</span> : null}
          </span>
          <span className="font-mono text-xs text-slate-500" title={effective.id ?? ""}>
            {effective.id}
          </span>
          {effective.error ? (
            <span className="rounded border border-red-900 bg-red-950/40 px-2 py-1 font-mono text-xs text-red-300">{effective.error}</span>
          ) : null}
        </div>
        {cancel.isError ? <ErrorNote error={cancel.error} /> : null}
        {rollback.isError ? <ErrorNote error={rollback.error} /> : null}
        {rollback.isSuccess && rollback.data?.deployment?.id ? (
          <div className="rounded-md border border-emerald-900/60 bg-emerald-950/40 px-3 py-2 text-xs text-emerald-300">
            rollback accepted — replay deployment {rollback.data.deployment.id}{" "}
            <button type="button" className="underline" onClick={() => navigate(`deployments/${rollback.data?.deployment?.id}`)}>
              open
            </button>
          </div>
        ) : null}

        {window ? <GenerationWindowCard window={window} /> : null}

        <ProcessStrategiesCard deployment={effective} toRevision={toRevision} appName={appName ?? ""} />

        <details className="rounded-lg border border-slate-800 px-3 py-2 text-sm text-slate-300" open>
          <summary className="cursor-pointer text-slate-400">row fields</summary>
          <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 font-mono text-xs text-slate-400">
            <dt>from_revision</dt>
            <dd title={effective.from_revision ?? ""}>{effective.from_revision || "—"}</dd>
            <dt>to_revision</dt>
            <dd title={effective.to_revision ?? ""}>{effective.to_revision || "—"}</dd>
            <dt>generation</dt>
            <dd>{effective.generation ?? "—"}</dd>
            <dt>from_generation</dt>
            <dd>{effective.from_generation ?? "—"}</dd>
            <dt>observe_deadline</dt>
            <dd>{effective.observe_deadline || "—"}</dd>
            <dt>commit_sha</dt>
            <dd>{effective.commit_sha || "—"}</dd>
            <dt>idempotency_key</dt>
            <dd>{effective.idempotency_key || "—"}</dd>
            <dt>superseded_by</dt>
            <dd>{effective.superseded_by || "—"}</dd>
            <dt>first_boot_task_id</dt>
            <dd>{effective.first_boot_task_id || "—"}</dd>
            <dt>created_at</dt>
            <dd>{formatTime(effective.created_at)}</dd>
            <dt>updated_at</dt>
            <dd>{formatTime(effective.updated_at)}</dd>
            <dt>finished_at</dt>
            <dd>{formatTime(effective.finished_at)}</dd>
          </dl>
        </details>

        {streamEnded && !TERMINAL_STATES.has(state) ? (
          <div className="flex items-center gap-3 text-xs text-slate-500">
            <span>stream ended before a terminal state</span>
            <RowButton onClick={() => restartToken((token) => token + 1)}>reopen stream</RowButton>
          </div>
        ) : null}
      </div>
    </PageShell>
  );
}

// ---- 双代窗叙事（ADR-0048 决策 1/2 + 架构 §5 诚实边界） ----

interface GenerationWindow {
  fromGen: string;
  toGen: string;
  observing: boolean;
  deadline: string | undefined;
  processes: string[];
}

// blueGreenWindow 判"双代窗进行中/刚收口"：活跃部署 + from_generation <
// generation + to_revision 存在 blue-green 进程（Console 判式 = proto
// from_generation 注释口径）。收口后的 succeeded 行仍展示窗叙事（终局
// 形态），但不再标注进行中。
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
    deadline: deployment.observe_deadline,
    processes: strategies.filter((entry) => entry?.strategy === "DEPLOY_STRATEGY_BLUE_GREEN").map((entry) => entry?.process ?? "?"),
  };
}

function GenerationWindowCard({ window }: { window: GenerationWindow }) {
  return (
    <div className="rounded-lg border border-sky-900/60 bg-sky-950/20 px-4 py-3">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h3 className="text-sm font-semibold text-sky-200">
          {window.observing ? "blue-green window · observing" : "blue-green window"}
        </h3>
        <span className="font-mono text-xs text-slate-400">
          current generation g{window.toGen} ← serving g{window.fromGen}
        </span>
        {window.deadline ? <ObserveCountdown deadline={window.deadline} /> : null}
      </div>
      <p className="mt-1 text-xs leading-relaxed text-slate-400">
        Both generations of <span className="font-mono text-slate-300">{window.processes.join(", ")}</span> run side by side until the new
        one is fully healthy; traffic switches only after that. During the window <span className="text-slate-300">replicas double</span>{" "}
        for those processes, and cross-process DNS names (<span className="font-mono">{"{process}"}</span> /{" "}
        <span className="font-mono">{"{process}.{app}"}</span>) may round-robin to either generation — use the proxy route or the
        generation name for precise targeting.
      </p>
    </div>
  );
}

// ObserveCountdown 渲染观察窗倒计时（客户端钟对 observe_deadline 求差；
// 到点后由 wait 流的下一帧翻态——倒计时只是呈现，不是判定源）。
function ObserveCountdown({ deadline }: { deadline: string }) {
  const [remaining, setRemaining] = useState(() => deadlineRemaining(deadline));
  useEffect(() => {
    const timer = window.setInterval(() => setRemaining(deadlineRemaining(deadline)), 1000);
    return () => window.clearInterval(timer);
  }, [deadline]);
  if (remaining == null) return null;
  return <span className="font-mono text-xs text-sky-300">observe window: {remaining}</span>;
}

function deadlineRemaining(deadline: string): string | null {
  const date = new Date(deadline);
  if (Number.isNaN(date.getTime())) return null;
  const diff = Math.floor((date.getTime() - Date.now()) / 1000);
  if (diff <= 0) return "closing";
  const minutes = Math.floor(diff / 60);
  const seconds = diff % 60;
  return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
}

// ProcessStrategiesCard：per-process 策略目录（to_revision 的策略投影）+
// 三种别名形态（ADR-0048 决策 3/4：裸名/全名是主引用面，代次名只作调试
// ——gen 随部署漂移）。
function ProcessStrategiesCard({
  deployment,
  toRevision,
  appName,
}: {
  deployment: Deployment;
  toRevision: Revision | undefined;
  appName: string;
}) {
  const strategies = toRevision?.process_strategies ?? [];
  if (strategies.length === 0 && toRevision == null) return null;
  return (
    <div className="rounded-lg border border-slate-800 px-4 py-3">
      <h3 className="text-sm font-semibold text-slate-200">processes</h3>
      {strategies.length === 0 ? (
        <p className="mt-1 text-xs text-slate-500">No strategy catalog on the target revision (older revision or data unavailable).</p>
      ) : (
        <table className="mt-2 w-full text-sm">
          <thead>
            <tr className="border-b border-slate-800 text-left text-xs uppercase tracking-wide text-slate-500">
              <th className="py-1 pr-4 font-medium">process</th>
              <th className="py-1 pr-4 font-medium">strategy</th>
              <th className="py-1 font-medium">DNS names (bare · fully-qualified · generation)</th>
            </tr>
          </thead>
          <tbody>
            {strategies.map((entry) => {
              const process = entry?.process ?? "?";
              const blueGreen = entry?.strategy === "DEPLOY_STRATEGY_BLUE_GREEN";
              return (
                <tr key={process} className="border-b border-slate-800/60">
                  <td className="py-1.5 pr-4 font-mono text-slate-300">{process}</td>
                  <td className="py-1.5 pr-4">
                    <span className={blueGreen ? "rounded bg-sky-950 px-1.5 py-0.5 text-xs text-sky-300" : "text-xs text-slate-500"}>
                      {blueGreen ? "blue-green" : "rolling"}
                    </span>
                  </td>
                  <td className="py-1.5 font-mono text-xs text-slate-400">
                    {process} · {process}.{appName || "app"} · {process}.g{deployment.generation ?? "?"}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      <p className="mt-2 text-[11px] leading-relaxed text-slate-500">
        Bare and fully-qualified names are the stable references; the generation name ({'{process}.g{gen}'}) targets one generation
        for debugging and moves with each deploy.
      </p>
    </div>
  );
}
