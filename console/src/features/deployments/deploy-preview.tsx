import { cn } from "cn";
import { statusToneClass, type StatusTone } from "@/components/domain/status-badge";
import type {
  PreviewDeployPayload,
  PreviewDiffEntry,
  PreviewWarning,
  PreviewWorkloadChange,
} from "@/kit/deployments";

// DeploySheet 预览步的载荷渲染面（B1 Change Preview，设计稿 §1.6 +
// §1.3 预览载荷语义，ADR-0085）。纯函数半边（分组/徽章/增量 diff）与
// 渲染半边同文件分层——纯函数是组件测试的单测锚。
// 诚实边界（§1.3）：预览展示渲染意图非 runtime 现场（现场面 =
// GetAppWorkloadObservations，Spec tab）；warnings 的 detail 文案全部来自
// 服务端结构化载荷，本模块只做分组与固定指引文案，不重写服务端口径。

// ---- 滚动后果判定（rollout 枚举 → 徽章形态） ----
// 三态语义（§1.3）：zero_touch = 零触碰（内容等价跳过）；scale_only =
// 纯副本伸缩（零载体替换）；replace = 载体替换（rolling 逐代/blue-green
// 双代窗）。配色经 status-badge 单源（TONE_CLASS），不私造。

const ROLLOUT_BADGES: Record<string, { label: string; tone: StatusTone }> = {
  WORKLOAD_ROLLOUT_ZERO_TOUCH: { label: "zero-touch", tone: "success" },
  WORKLOAD_ROLLOUT_SCALE_ONLY: { label: "scale-only", tone: "info" },
  WORKLOAD_ROLLOUT_REPLACE: { label: "replace", tone: "warning" },
};

export function rolloutBadgeOf(rollout: string | undefined): { label: string; tone: StatusTone } {
  if (rollout && ROLLOUT_BADGES[rollout]) return ROLLOUT_BADGES[rollout];
  return { label: "unknown", tone: "neutral" };
}

const ACTION_LABELS: Record<string, string> = {
  WORKLOAD_CHANGE_ACTION_ADDED: "add",
  WORKLOAD_CHANGE_ACTION_REMOVED: "remove",
  WORKLOAD_CHANGE_ACTION_CHANGED: "change",
};

export function actionLabelOf(action: string | undefined): string {
  return ACTION_LABELS[action ?? ""] ?? "?";
}

// ---- warnings 分组（九 kind 只增枚举，§1.3） ----
// label 是用户面分组词；guidance 是规格点名的指引文案（LIVE_DRIFT 与
// IN_FLIGHT_DEPLOYMENT 必带——「操作者第一问」之外的两条高频困惑腿）。
// 未知 kind（服务端新增、console 未识）回退原始枚举词 + 通组渲染，不吞。

interface WarningGroupMeta {
  label: string;
  guidance?: string;
}

const WARNING_GROUP_META: Record<string, WarningGroupMeta> = {
  PREVIEW_WARNING_KIND_REPLICAS_CHANGE: { label: "Replicas change" },
  PREVIEW_WARNING_KIND_BLUE_GREEN_WINDOW: { label: "Blue-green window" },
  PREVIEW_WARNING_KIND_VOLUME_PIN_MOVE: { label: "Volume pinning change" },
  PREVIEW_WARNING_KIND_HOOK_OWNERSHIP_MERGE: { label: "Hook ownership merge" },
  PREVIEW_WARNING_KIND_PRE_DEPLOY_JOBS_PRESENT: { label: "Pre-deploy jobs" },
  PREVIEW_WARNING_KIND_ADMISSION_PRECHECK: { label: "Admission precheck" },
  PREVIEW_WARNING_KIND_IN_FLIGHT_DEPLOYMENT: {
    label: "In-flight deployments",
    guidance:
      "A queued or running deployment will stale this ticket when it completes. Confirming now queues this deploy — admission (queued / merged / superseded) decides the real execution order, not this preview.",
  },
  PREVIEW_WARNING_KIND_LIVE_DRIFT: {
    label: "Live drift",
    guidance:
      "Live replicas differ from the declared state — this deploy converges the cluster back to the declared count. To keep the live value, declare that replicas count in this deploy (rollout becomes scale-only, zero container replacement).",
  },
  PREVIEW_WARNING_KIND_DIGEST_PENDING: {
    label: "Digest pending",
    guidance: "The image reference for this workload settles after the build completes.",
  },
};

export interface WarningGroup {
  key: string;
  label: string;
  guidance?: string;
  items: PreviewWarning[];
}

export function groupWarnings(warnings: ReadonlyArray<PreviewWarning | undefined> | undefined): WarningGroup[] {
  const groups: WarningGroup[] = [];
  const byKey = new Map<string, WarningGroup>();
  for (const warning of warnings ?? []) {
    if (warning == null) continue;
    const key = warning.kind ?? "PREVIEW_WARNING_KIND_UNSPECIFIED";
    let group = byKey.get(key);
    if (group == null) {
      group = { key, label: WARNING_GROUP_META[key]?.label ?? key, guidance: WARNING_GROUP_META[key]?.guidance, items: [] };
      byKey.set(key, group);
      groups.push(group);
    }
    group.items.push(warning);
  }
  return groups;
}

// ---- 重预览增量展示（STALE 后：新预览 vs 上一次所见，§1.6） ----
// 「这次会写而上次不写的 / 上次写而这次不写的」：workload_changes 按
// workload_id 对齐，spec_diff 按 path 对齐，warnings 按 kind+detail 对齐。
// 纯函数——组件态保存的 seen 载荷与重预览载荷都进这里，渲染只吃结果。

export interface PreviewDelta {
  workloadsAdded: PreviewWorkloadChange[];
  workloadsDropped: PreviewWorkloadChange[];
  diffAdded: PreviewDiffEntry[];
  diffDropped: PreviewDiffEntry[];
  warningsAdded: PreviewWarning[];
  warningsDropped: PreviewWarning[];
}

const workloadKey = (change: PreviewWorkloadChange | undefined): string =>
  change?.workload_id ?? change?.image ?? "?";
const diffKey = (entry: PreviewDiffEntry | undefined): string => entry?.path ?? "?";
const warningKey = (warning: PreviewWarning | undefined): string => `${warning?.kind ?? "?"}\n${warning?.detail ?? ""}`;

function partition<T>(
  nextInput: ReadonlyArray<T | undefined> | undefined,
  seenInput: ReadonlyArray<T | undefined> | undefined,
  keyOf: (item: T) => string,
): { added: T[]; dropped: T[] } {
  const next = (nextInput ?? []).filter((item) => item != null) as T[];
  const seen = (seenInput ?? []).filter((item) => item != null) as T[];
  const nextKeys = new Set(next.map(keyOf));
  const seenKeys = new Set(seen.map(keyOf));
  return {
    added: next.filter((item) => !seenKeys.has(keyOf(item))),
    dropped: seen.filter((item) => !nextKeys.has(keyOf(item))),
  };
}

export function diffPreviews(next: PreviewDeployPayload, seen: PreviewDeployPayload): PreviewDelta {
  const workloads = partition(next.workload_changes, seen.workload_changes, workloadKey);
  const diff = partition(next.spec_diff, seen.spec_diff, diffKey);
  const warnings = partition(next.warnings, seen.warnings, warningKey);
  return {
    workloadsAdded: workloads.added,
    workloadsDropped: workloads.dropped,
    diffAdded: diff.added,
    diffDropped: diff.dropped,
    warningsAdded: warnings.added,
    warningsDropped: warnings.dropped,
  };
}

export function previewDeltaIsEmpty(delta: PreviewDelta): boolean {
  return (
    delta.workloadsAdded.length === 0 &&
    delta.workloadsDropped.length === 0 &&
    delta.diffAdded.length === 0 &&
    delta.diffDropped.length === 0 &&
    delta.warningsAdded.length === 0 &&
    delta.warningsDropped.length === 0
  );
}

// ---- 渲染面 ----

function Pill({ tone, children, mono = false }: { tone: StatusTone; children: React.ReactNode; mono?: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-1.5 py-0.5 text-2xs font-semibold whitespace-nowrap",
        statusToneClass(tone),
        mono && "font-mono font-normal",
      )}
    >
      {children}
    </span>
  );
}

function WorkloadChangeRow({ change }: { change: PreviewWorkloadChange }) {
  const rollout = rolloutBadgeOf(change.rollout);
  const replicas =
    change.replicas_from != null || change.replicas_to != null
      ? `${change.replicas_from ?? "?"} → ${change.replicas_to ?? "?"}`
      : null;
  const details = [
    change.healthcheck,
    change.volumes && change.volumes.length > 0 ? `volumes: ${change.volumes.join(", ")}` : null,
    change.networks && change.networks.length > 0 ? `networks: ${change.networks.join(", ")}` : null,
  ].filter((part): part is string => part != null && part !== "");
  return (
    <div className="rounded-lg border bg-panel-2/40 px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <Pill tone={rollout.tone}>{rollout.label}</Pill>
        <span className="font-mono text-xs font-semibold">{change.workload_id ?? "?"}</span>
        <Pill tone="neutral" mono>{actionLabelOf(change.action)}</Pill>
        {replicas ? <span className="text-2xs text-muted">replicas {replicas}</span> : null}
      </div>
      {change.image ? <p className="mt-1 break-all font-mono text-2xs text-muted">{change.image}</p> : null}
      {details.length > 0 ? <p className="mt-1 text-2xs leading-snug text-muted">{details.join(" · ")}</p> : null}
    </div>
  );
}

function DiffList({ entries }: { entries: PreviewDiffEntry[] }) {
  return (
    <div className="max-h-44 overflow-y-auto rounded-lg border bg-panel-2/40 px-2.5 py-2">
      <ul className="flex flex-col gap-1.5">
        {entries.map((entry) => (
          <li key={entry.path ?? ""} className="break-all font-mono text-2xs leading-snug">
            <span className="text-fg">{entry.path ?? "?"}</span>
            {entry.old_value != null || entry.new_value != null ? (
              <span className="text-muted">
                {" "}
                {entry.old_value ?? "—"} → {entry.new_value ?? "—"}
              </span>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

// 增量展示的一组（added/dropped 共用行形态：workload id / diff path / 警示词）。
function DeltaGroup({ title, entries }: { title: string; entries: string[] }) {
  if (entries.length === 0) return null;
  return (
    <div>
      <p className="text-2xs font-semibold text-fg">{title}</p>
      <ul className="mt-1 flex flex-col gap-0.5">
        {entries.map((entry) => (
          <li key={entry} className="break-all font-mono text-2xs text-muted">
            {entry}
          </li>
        ))}
      </ul>
    </div>
  );
}

function DeltaBlock({ delta }: { delta: PreviewDelta }) {
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-[color-mix(in_oklch,var(--status-warning)_40%,transparent)] bg-[var(--status-warning-bg)] px-3 py-2.5">
      <p className="text-2xs font-semibold uppercase tracking-wide text-[var(--status-warning)]">
        Since the preview you last saw
      </p>
      <DeltaGroup
        title="New in this preview (would be written now, not last time)"
        entries={[
          ...delta.workloadsAdded.map((change) => `${actionLabelOf(change.action)} ${workloadKey(change)}`),
          ...delta.diffAdded.map((entry) => diffKey(entry)),
          ...delta.warningsAdded.map((warning) => warningKey(warning).replace("\n", ": ")),
        ]}
      />
      <DeltaGroup
        title="No longer in this preview (was written last time, not now)"
        entries={[
          ...delta.workloadsDropped.map((change) => `${actionLabelOf(change.action)} ${workloadKey(change)}`),
          ...delta.diffDropped.map((entry) => diffKey(entry)),
          ...delta.warningsDropped.map((warning) => warningKey(warning).replace("\n", ": ")),
        ]}
      />
      {previewDeltaIsEmpty(delta) ? (
        <p className="text-2xs text-muted">No differences — only the ticket was refreshed.</p>
      ) : null}
    </div>
  );
}

// DeployPreviewPanel 是预览步主体：spec diff 摘要 / workload_changes 逐条
// 带 rollout 徽章 / warnings 分组（LIVE_DRIFT 与 IN_FLIGHT_DEPLOYMENT 带
// 指引文案）/ 增量块（STALE 重预览时）。载荷形状 = v1PreviewDeployResponse
// （§1.3 预览载荷语义；env 值集不在载荷——脱敏红线，服务端执法）。
export function DeployPreviewPanel({
  payload,
  delta = null,
}: {
  payload: PreviewDeployPayload;
  /** STALE 重预览时非空：新载荷 vs 上一次所见的增量（组件态保存 seen） */
  delta?: PreviewDelta | null;
}) {
  const changes = payload.workload_changes ?? [];
  const diff = payload.spec_diff ?? [];
  const warningGroups = groupWarnings(payload.warnings);
  const ticketMinutes = payload.ticket_expires_in != null ? Math.max(1, Math.round(payload.ticket_expires_in / 60)) : null;
  return (
    <div className="flex flex-col gap-4" data-testid="deploy-preview">
      <div>
        <h4 className="text-sm font-semibold">Preview — confirm what this deploy writes</h4>
        <p className="mt-0.5 text-2xs leading-snug text-muted">
          Read-only twin of the deploy intake (same validation rules). Confirming redeems the one-shot ticket for exactly
          this change.
          {ticketMinutes != null ? ` Ticket valid for ~${ticketMinutes} min.` : ""}
        </p>
      </div>

      <div className="flex flex-col gap-2">
        <p className="text-2xs font-semibold uppercase tracking-wide text-muted">
          Workloads ({changes.length})
        </p>
        {changes.length === 0 ? (
          <p className="text-2xs text-muted">No workload changes — the deploy writes no carriers.</p>
        ) : (
          changes.map((change) => <WorkloadChangeRow key={workloadKey(change)} change={change} />)
        )}
      </div>

      <div className="flex flex-col gap-2">
        <p className="text-2xs font-semibold uppercase tracking-wide text-muted">Spec diff ({diff.length})</p>
        {diff.length === 0 ? (
          <p className="text-2xs text-muted">No field-level changes against the baseline (first deploy = full add).</p>
        ) : (
          <DiffList entries={diff} />
        )}
      </div>

      {warningGroups.length > 0 ? (
        <div className="flex flex-col gap-2.5">
          <p className="text-2xs font-semibold uppercase tracking-wide text-muted">Warnings</p>
          {warningGroups.map((group) => (
            <div
              key={group.key}
              className="rounded-lg border border-[color-mix(in_oklch,var(--status-warning)_35%,transparent)] bg-[var(--status-warning-bg)] px-2.5 py-2"
            >
              <p className="text-2xs font-semibold text-[var(--status-warning)]">
                {group.label}
                {group.items.length > 1 ? ` ×${group.items.length}` : ""}
              </p>
              {group.guidance ? <p className="mt-1 text-2xs leading-snug text-fg">{group.guidance}</p> : null}
              <ul className="mt-1 flex flex-col gap-0.5">
                {group.items.map((item, index) =>
                  item.detail ? (
                    <li key={index} className="break-words text-2xs leading-snug text-muted">
                      {item.detail}
                    </li>
                  ) : null,
                )}
              </ul>
            </div>
          ))}
        </div>
      ) : null}

      {delta != null ? <DeltaBlock delta={delta} /> : null}
    </div>
  );
}
