import { cn } from "cn";

// 状态徽章单源（UI v2 一致性契约）：全站状态值域 → 语义色映射只在此处，
// 各页面不得私造配色（旧 STATE_STYLES 四处重复的反面收敛）。
// 语义色走 styles.css 的 --status-* token（双主题各自定值）。

export type StatusTone = "success" | "info" | "warning" | "danger" | "neutral";

const TONE_CLASS: Record<StatusTone, string> = {
  success: "text-[var(--status-success)] bg-[var(--status-success-bg)] border-[color-mix(in_oklch,var(--status-success)_30%,transparent)]",
  info: "text-[var(--status-info)] bg-[var(--status-info-bg)] border-[color-mix(in_oklch,var(--status-info)_30%,transparent)]",
  warning: "text-[var(--status-warning)] bg-[var(--status-warning-bg)] border-[color-mix(in_oklch,var(--status-warning)_30%,transparent)]",
  danger: "text-[var(--status-danger)] bg-[var(--status-danger-bg)] border-[color-mix(in_oklch,var(--status-danger)_30%,transparent)]",
  neutral: "text-muted-foreground bg-muted border-border",
};

export function statusToneClass(tone: StatusTone): string {
  return TONE_CLASS[tone];
}

// DEPLOYMENT_TONES 钉 Deployment.state 值域（delivery.proto 口径）；活跃态
// 带呼吸点。未知值回退 neutral（不猜语义）。
const DEPLOYMENT_TONES: Record<string, StatusTone> = {
  succeeded: "success",
  failed: "danger",
  canceled: "warning",
  superseded: "neutral",
  queued: "neutral",
  preparing: "info",
  running: "info",
  observing: "info",
  releasing: "info",
};

export function deploymentTone(state: string | undefined): StatusTone {
  return DEPLOYMENT_TONES[state ?? ""] ?? "neutral";
}

// 活跃部署态（可 cancel 的窗口）——与 CLI/server 判式同口径。
export function isActiveDeploymentState(state: string | undefined): boolean {
  return state === "running" || state === "observing" || state === "releasing" || state === "queued" || state === "preparing";
}

// NODE_TONES 钉 Node availability 值域（runtime 口径；W2 走查措辞：
// unavailable 是历史行语义，不是"故障"）。
const NODE_TONES: Record<string, StatusTone> = {
  available: "success",
  cordon: "warning",
  drain: "warning",
  unavailable: "neutral",
  unknown: "neutral",
};

export function nodeTone(availability: string | undefined): StatusTone {
  return NODE_TONES[availability ?? ""] ?? "neutral";
}

// ALERT_TONES 钉告警评估态值域。
const ALERT_TONES: Record<string, StatusTone> = {
  firing: "danger",
  ok: "success",
  pending: "warning",
  inactive: "neutral",
};

export function alertTone(state: string | undefined): StatusTone {
  return ALERT_TONES[state ?? ""] ?? "neutral";
}

export function StatusBadge({
  tone,
  pulse = false,
  className,
  children,
}: {
  tone: StatusTone;
  pulse?: boolean;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11.5px] font-semibold whitespace-nowrap",
        TONE_CLASS[tone],
        className,
      )}
    >
      <span className={cn("size-1.5 rounded-full bg-current", pulse && "animate-pulse")} />
      {children}
    </span>
  );
}

export function DeploymentStatusBadge({ state, className }: { state: string | undefined; className?: string }) {
  return (
    <StatusBadge tone={deploymentTone(state)} pulse={isActiveDeploymentState(state)} className={className}>
      {state ?? "unknown"}
    </StatusBadge>
  );
}
