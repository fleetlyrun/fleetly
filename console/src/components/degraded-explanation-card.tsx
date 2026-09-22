// degraded 一等 UI（W5-S2 挂账收口，E6 观测专项设计 §3.3）：应用 degraded
// 态的常驻解释卡——不再是只有 badge 的二等态。数据源 = 现有 app state
//（derived_state）+ WatchEvents 既有流（不新增后端 API）；文案（英文）=
// 对账发现 substrate 缺失/发布不稳定的语义 + 指引链接到该 app 的事件流。
//
// 两个变体：
//   - full（AppOverview 常驻卡）：可挂事件流订阅（DegradedExplanationCardLive
//     ——只在 degraded 时挂载，ready 态零额外流）；
//   - compact（AppsPage 应用行内联）：纯展示 + 链接（行内不各开事件流）。

import { TriangleAlert } from "lucide-react";
import { Link } from "react-router-dom";

import { useEventStream } from "@/hooks/use-event-stream";
import type { EventView } from "@/api/types";
import { formatTime } from "@/lib/utils";

/** 会把 app 打入 degraded 的事件词表（state-model §2.10 派生规则来源）。 */
const DEGRADED_REASON_EVENTS = new Set([
  "app.substrate_missing",
  "app.degraded",
  "app.instability_detected",
]);

/** 事件流窗口内该 app 最近一条 degraded 因由事件（升序输入 → 倒序找）。 */
export function lastDegradedEvent(
  app: string,
  events: EventView[],
): EventView | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (!e) continue;
    if (e.subject === `app:${app}` && DEGRADED_REASON_EVENTS.has(e.name ?? "")) {
      return e;
    }
  }
  return null;
}

/**
 * 常驻解释卡：degraded 语义的诚实解释 + 事件流链接。compact = 应用行内联
 * 形态（同锚点、无事件订阅）。
 */
export function DegradedExplanationCard({
  app,
  compact = false,
  event,
}: {
  app: string;
  compact?: boolean;
  event?: EventView | null;
}) {
  const eventsHref = `/events?q=${encodeURIComponent(`app:${app}`)}`;
  if (compact) {
    return (
      <div
        data-testid="degraded-explanation-card"
        className="mt-1.5 flex items-start gap-1.5 rounded-md border border-amber-500/50 bg-amber-500/10 px-2 py-1.5 text-xs"
      >
        <TriangleAlert aria-hidden className="mt-0.5 h-3 w-3 shrink-0 text-amber-600 dark:text-amber-400" />
        <span className="text-amber-700 dark:text-amber-400">
          Runtime reconciliation found managed service(s) missing from the
          substrate (or an unstable release) —{" "}
          <Link to={eventsHref} className="underline hover:no-underline">
            view events
          </Link>
        </span>
      </div>
    );
  }
  return (
    <div
      data-testid="degraded-explanation-card"
      className="rounded-lg border border-amber-500/50 bg-amber-500/10 p-4"
    >
      <div className="flex items-center gap-2">
        <TriangleAlert aria-hidden className="h-4 w-4 text-amber-600 dark:text-amber-400" />
        <h3 className="text-sm font-semibold text-amber-700 dark:text-amber-400">
          Why is this app degraded?
        </h3>
      </div>
      <p className="mt-2 text-sm text-muted-foreground">
        Runtime reconciliation reported this app as degraded: its managed
        service(s) were found missing from the substrate (removed outside the
        platform) or the running release failed its stability checks. The
        derived view has been corrected and stays degraded until the app
        returns to a healthy state — redeploy to converge.
      </p>
      {event ? (
        <p className="mt-2 text-xs text-muted-foreground">
          Latest related event:{" "}
          <code className="rounded bg-muted px-1 py-0.5">{event.name}</code>
          {event.at ? ` at ${formatTime(event.at)}` : ""}
        </p>
      ) : null}
      <p className="mt-2 text-xs">
        <Link
          to={eventsHref}
          className="inline-flex items-center gap-1 font-medium text-amber-700 underline hover:no-underline dark:text-amber-400"
        >
          View this app&apos;s event stream
        </Link>
      </p>
    </div>
  );
}

/**
 * 带事件订阅的全量形态（AppOverview 用）：挂载即订阅 WatchEvents（既有
 * 流，seq 游标续读），在窗口内检索该 app 最近一条 degraded 因由事件并展示
 * ——调用方保证只在 degraded 时挂载（ready 态零额外流）。
 */
export function DegradedExplanationCardLive({ app }: { app: string }) {
  const { events } = useEventStream();
  return (
    <DegradedExplanationCard app={app} event={lastDegradedEvent(app, events)} />
  );
}
