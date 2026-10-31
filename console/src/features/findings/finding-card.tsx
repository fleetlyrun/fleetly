import { useState } from "react";
import { ChevronRightIcon, ExternalLinkIcon, WrenchIcon } from "lucide-react";
import type { FindingView } from "@/kit/diagnosis";
import { Button } from "@/components/ui/button";

// FindingCard 是诊断条目的四层形态卡（B2a W4，设计 §2.5）：severity 徽章 /
// fault_class 标签 / 发生了什么（message——注册表 Summary 直通，不转写）/
// 为什么（evidence 折叠区：state/exit/restarts/最近事件）/ 怎么办（fix）。
// 纯读展示件：fix 是深链或建议文案（v1 裁决——动词钮只深链到既有动作
// 面，不在诊断面直发写请求，useCommand 零触碰）；platform 条目无 fix 面
// （「等管理员修」形态——platform 的错永远不读成用户的错）。

// faultClassLabel 是归属四档的显示词（CONTEXT.md Fault Class 词条单义）。
export const FAULT_CLASSES = ["app", "declaration", "platform", "external"] as const;
export type FaultClass = (typeof FAULT_CLASSES)[number];

export function faultClassOf(finding: FindingView): FaultClass {
  return (FAULT_CLASSES as readonly string[]).includes(finding.fault_class ?? "")
    ? ((finding.fault_class ?? "app") as FaultClass)
    : "app";
}

// adviceForField 是无 verb 修复的建议文案（注册表 Fix.Params.field 键集：
// command/healthcheck/image/memory）——声明改动统一走路由到部署面（B1
// 预览承载「确认的正是展示的」，不在诊断面绕过预览直发）。
export function adviceForField(field: string | undefined): string {
  switch (field) {
    case "command":
      return "Fix the command in the workload declaration — submit a new deployment to preview the change before it runs.";
    case "healthcheck":
      return "Fix the health check declaration (path, port, or exec command) — submit a new deployment to preview the change.";
    case "image":
      return "Check the image reference and registry reachability — submit a new deployment to preview any declaration change.";
    case "memory":
      return "Adjust the memory limit in the declaration — submit a new deployment to preview the change.";
    default:
      return "The fix is a declaration change — submit a new deployment to preview it.";
  }
}

// SeverityBadge：两级（warning | advice，ADR-0085 已裁形状）。
function SeverityBadge({ severity }: { severity: string }) {
  if (severity === "advice") {
    return <span className="rounded-full border bg-panel-2/60 px-2 py-0.5 text-2xs font-semibold text-info">advice</span>;
  }
  return <span className="rounded-full border border-warn/40 bg-warn/15 px-2 py-0.5 text-2xs font-semibold text-warn">warning</span>;
}

// FaultClassChip 是归属标签（mono 小片——「往哪看」轴，非问责措辞）。
function FaultClassChip({ faultClass }: { faultClass: FaultClass }) {
  return <span className="rounded-full border bg-panel-2/60 px-2 py-0.5 font-mono text-2xs text-muted">{faultClass}</span>;
}

// FindingFixArea：怎么办层。verb 在册（restart/scale）→ 深链钮（动作宿主
// 在 Processes 页——Restart 行钮与副本暂存面）；无 verb → 建议文案（+ 消费
// 方注入的部署面入口或 app 深链）；platform → 无 fix 面（描述即全部）。
function FindingFixArea({
  finding,
  appBase,
  onOpenDeploy,
}: {
  finding: FindingView;
  appBase?: string;
  onOpenDeploy?: () => void;
}) {
  const fix = finding.fix;
  const verb = fix?.verb ?? "";
  const process = finding.process || finding.workload_id || "process";
  if (verb === "restart" || verb === "scale") {
    // restart/scale 的动作宿主都在 Processes 页（Restart 行钮 / 副本暂存
    // → Deploy）；scale 带 replicas 预填提示。
    const label = verb === "restart" ? `Restart ${process}` : `Scale ${process}${fix?.params?.replicas ? ` to ${fix.params.replicas}` : ""}`;
    const hint =
      verb === "restart"
        ? "Opens the Processes tab — use the row-level Restart there (light and reversible, no confirmation ticket needed)"
        : "Opens the Processes tab — stage the replica count there and deploy";
    return (
      <div className="flex flex-wrap items-center gap-2">
        {appBase ? (
          <Button asChild size="sm" variant="outline" title={hint}>
            <a href={`${appBase}/processes`}>
              <WrenchIcon data-icon-start-inline />
              {label}
            </a>
          </Button>
        ) : (
          <span className="text-2xs text-muted">{hint}</span>
        )}
      </div>
    );
  }
  if (onOpenDeploy != null) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" onClick={onOpenDeploy} title="Declaration changes go through a deployment preview">
          <WrenchIcon data-icon-start-inline />
          Open deploy…
        </Button>
        <span className="text-2xs text-muted">{adviceForField(fix?.params?.field)}</span>
      </div>
    );
  }
  const path = fix?.console_path || appBase;
  return (
    <div className="flex flex-wrap items-center gap-2">
      {path ? (
        <Button asChild size="sm" variant="outline">
          <a href={path}>
            Open the app
            <ExternalLinkIcon className="ml-1 size-3" />
          </a>
        </Button>
      ) : null}
      <span className="text-2xs text-muted">{adviceForField(fix?.params?.field)}</span>
    </div>
  );
}

// FindingCard 本体。evidence 折叠区默认收起（发生了什么/怎么办先上桌，
// 为什么是第二层）。
export function FindingCard({
  finding,
  appBase,
  onOpenDeploy,
}: {
  finding: FindingView;
  /** App 详情基路径（verb 深链/跨页深链的锚；本页条目可省） */
  appBase?: string;
  /** 无 verb 条目的部署面入口（诊断 tab 注入 DeploySheet 开窗） */
  onOpenDeploy?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const faultClass = faultClassOf(finding);
  const evidence = finding.evidence;
  const hasEvidence =
    evidence != null && Boolean(evidence.state || evidence.exit_code != null || evidence.restart_count || evidence.recent_event);
  const locator = finding.process || finding.workload_id;
  return (
    <article className="rounded-xl border bg-panel p-4">
      <div className="flex flex-wrap items-center gap-2">
        <SeverityBadge severity={finding.severity ?? "warning"} />
        <FaultClassChip faultClass={faultClass} />
        {locator ? <span className="font-mono text-2xs text-muted">{locator}</span> : null}
        <span className="ml-auto font-mono text-2xs text-muted" title="Registry entry ID — the semantic source of this finding">
          {finding.finding_id}
        </span>
      </div>
      {/* 发生了什么：注册表 Summary 直通（语义冻结在注册表，实例层不转写）。 */}
      <p className="mt-2 text-sm">{finding.message}</p>
      {faultClass === "platform" ? (
        // platform 特殊形态：无 fix 面——「不是我的问题，等管理员修」。
        <p className="mt-2 text-xs text-muted">
          Not caused by your app — an administrator fixes the platform side. No action is available here.
        </p>
      ) : (
        <>
          {/* 为什么：evidence 折叠区（读不了的面不装懂——缺席即不渲染）。 */}
          {hasEvidence ? (
            <div className="mt-2">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                aria-label={`Toggle evidence of ${finding.finding_id}`}
                onClick={() => setOpen((prev) => !prev)}
              >
                <ChevronRightIcon className={`size-3.5 transition-transform ${open ? "rotate-90" : ""}`} />
                Why
              </Button>
              {open ? (
                <dl className="mt-1 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded-md bg-panel-2/30 p-2.5 text-2xs">
                  <dt className="text-muted">state</dt>
                  <dd className="font-mono">{evidence?.state || "—"}</dd>
                  <dt className="text-muted">exit code</dt>
                  <dd className="font-mono">{evidence?.exit_code != null ? evidence.exit_code : "—"}</dd>
                  <dt className="text-muted">restarts (near window)</dt>
                  <dd className="font-mono">{evidence?.restart_count ? evidence.restart_count : "—"}</dd>
                  <dt className="text-muted">recent event</dt>
                  <dd className="font-mono">{evidence?.recent_event || "—"}</dd>
                </dl>
              ) : null}
            </div>
          ) : null}
          {/* 怎么办 */}
          <div className="mt-2">
            <FindingFixArea finding={finding} appBase={appBase} onOpenDeploy={onOpenDeploy} />
          </div>
        </>
      )}
    </article>
  );
}

// UnreadableFacesChip 是 unreadable 诚实面的小片形态（「需要你」页逐 App
// 内联；诊断 tab 用横幅形态 UnreadableBanner）。
export function UnreadableFacesChip({ faces }: { faces: string[] }) {
  if (faces.length === 0) return null;
  return (
    <span
      className="inline-flex items-center rounded-full border border-warn/40 bg-warn/15 px-2 py-0.5 text-2xs font-semibold text-warn"
      title={`Diagnosis could not read these fact faces: ${faces.join(", ")} — silence there does not mean healthy.`}
    >
      {faces.length} face{faces.length === 1 ? "" : "s"} unreadable
    </span>
  );
}

// UnreadableBanner 是诊断 tab 的 unreadable 横幅（防「安静 = 健康」误读：
// 读不了 ≠ 不存在）。
export function UnreadableBanner({ faces }: { faces: string[] }) {
  if (faces.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-xl border border-warn/40 bg-warn/10 px-4 py-3 text-xs text-warn">
      <span className="font-semibold">
        {faces.length} face{faces.length === 1 ? "" : "s"} unreadable
      </span>
      <span className="text-fg">
        Diagnosis could not read: {faces.join(", ")}. Silence here does not mean healthy — these faces are outside what the rules
        can see.
      </span>
    </div>
  );
}
