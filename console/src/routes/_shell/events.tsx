import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useSubscription } from "@lynx-go/uikit-core/react";
import { PauseIcon, PlayIcon, RotateCwIcon } from "lucide-react";
import { apiFetch } from "@/api/client";
import type { EventRow } from "@/api/streams";
import { formatAbsolute } from "@/lib/format";
import { describeApiError } from "@/kit/errors";
import { eventsTransport } from "@/kit/event-stream";
import { EventFeed } from "@/components/kit/subscription/event-feed";
import { ErrorState } from "@/components/domain/error-state";
import { PageFrame } from "@/components/domain/page-frame";
import { Button } from "@/components/ui/button";

// 事件流页（F2.6/ADR-0044 决策 1 → UI v2 批3 订阅族）：补档
// GET /v1/events 作 backlog 前缀，票据订阅 /v1/events/follow（ADR-0026
// 钉面）迁 uikit useSubscription + EventFeed 块。锚点 = status.last_seq
// （backlog→live 交接；cursor 变化 = 重建订阅，gapped 后的原地重锚走
// sub.resync 不换 prop）；断档 410 → transport 报 gapped → 页内自动重同步
// （重读 status 从最新 seq 重订，旧档滑出保留窗即丢，resyncs 计数供黄条）；
// 传输收口 → 引擎退避重开新票；票据/凭证类失败 → auth 终态横幅。
// 倒序（最新在最上）由 EventFeed 承担；Pause/Resume = sub.pause()/resume()。

const BACKLOG_LIMIT = 200;
const MAX_ROWS = 1_000;
const RECONNECT_MS = 3_000;

interface StatusResponse {
  earliest_seq?: string;
  last_seq?: string;
}

interface ListEventsResponse {
  events?: Array<EventRow | undefined>;
}

// prettyPayload 把事件载荷渲染为缩进 JSON（解析失败原样返回）。
function prettyPayload(payload: string | undefined): string {
  if (!payload) return "";
  try {
    return JSON.stringify(JSON.parse(payload), null, 2);
  } catch {
    return payload;
  }
}

export const Route = createFileRoute("/_shell/events")({
  component: EventsPageV2,
});

function EventsPageV2() {
  // backlog = 补档静态前缀（与 live 帧按 seq 去重）；anchor undefined =
  // 待命（装载中），定锚即开。
  const [backlog, setBacklog] = useState<EventRow[]>([]);
  const [anchor, setAnchor] = useState<string | undefined>(undefined);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [loadNonce, setLoadNonce] = useState(0);
  const transport = useMemo(() => eventsTransport(), []);
  const sub = useSubscription(transport, {
    cursor: anchor,
    getCursor: (event) => event.seq,
    policy: { buffer: MAX_ROWS, pauseable: true, retry: { backoffMs: [RECONNECT_MS] } },
  });

  // 初始装载：status 定锚 → 补档最近 BACKLOG_LIMIT 条 → 从 last_seq 跟随
  //（失败可 Retry 重跑装载）。
  useEffect(() => {
    setLoadError(null);
    void (async () => {
      try {
        const status = await apiFetch<StatusResponse>("/v1/events/status");
        const lastSeq = Number(status.last_seq ?? "0");
        const afterSeq = Math.max(0, lastSeq - BACKLOG_LIMIT);
        const list = await apiFetch<ListEventsResponse>(`/v1/events?after_seq=${afterSeq}&limit=${BACKLOG_LIMIT}`);
        setBacklog((list.events ?? []).flatMap((event) => (event?.seq ? [event] : [])));
        setAnchor(status.last_seq ?? "0");
      } catch (err) {
        setLoadError(err);
      }
    })();
  }, [loadNonce]);

  // resync：断档后的快照重同步（ADR-0026 三件套）——重读 status，从最新
  // seq 直接跟随（旧档已滑出保留窗，不假装补齐）；backlog 一并换新。
  const resync = useCallback(() => {
    void (async () => {
      try {
        const status = await apiFetch<StatusResponse>("/v1/events/status");
        setBacklog([]);
        sub.resync(status.last_seq ?? "0");
      } catch {
        // status 读失败：停在 gapped，横幅上的 Resync 按钮可重试。
      }
    })();
  }, [sub]);

  // 断档自动重同步（旧页同口径：410 页内自动裁决，不留给用户）。
  useEffect(() => {
    if (sub.status === "gapped") resync();
  }, [sub, sub.status, resync]);

  const frames = sub.frames;
  const cursorSeq = frames.length > 0 ? frames[frames.length - 1]?.seq : undefined;

  const renderEvent = (event: EventRow): ReactNode => (
    <div className="min-w-0 flex-1">
      <div className="flex min-w-0 items-baseline gap-3">
        <span className="w-14 flex-none font-mono text-muted-foreground">{event.seq ?? "—"}</span>
        <span className="w-52 flex-none truncate font-mono text-[var(--status-info)]">{event.name ?? "—"}</span>
        <span className="min-w-0 flex-1 truncate font-mono text-muted-foreground">
          {event.aggregate ?? "—"}
          {event.aggregate_id ? <span className="opacity-50"> / {event.aggregate_id}</span> : null}
        </span>
        <span className="flex-none text-muted-foreground" title={formatAbsolute(event.created_at)}>
          {formatAbsolute(event.created_at)}
        </span>
      </div>
      {event.payload && event.payload !== "{}" ? (
        <details className="mt-0.5">
          <summary className="cursor-pointer hover:text-foreground">payload</summary>
          <pre className="mt-1 max-w-lg overflow-auto rounded border bg-panel-2/40 p-2 font-mono text-2xs">
            {prettyPayload(event.payload)}
          </pre>
        </details>
      ) : null}
    </div>
  );

  return (
    <PageFrame
      title="Events"
      description="Backlog via GET /v1/events, then following /v1/events/follow (one-time ticket, ADR-0026)"
      actions={
        <Button
          size="sm"
          variant={sub.status === "paused" ? "default" : "outline"}
          onClick={() => (sub.status === "paused" ? sub.resume() : sub.pause())}
        >
          {sub.status === "paused" ? <PlayIcon data-icon-start-inline /> : <PauseIcon data-icon-start-inline />}
          {sub.status === "paused" ? "Resume" : "Pause"}
        </Button>
      }
    >
      {loadError ? (
        <div className="mb-3">
          <ErrorState error={loadError} onRetry={() => setLoadNonce((nonce) => nonce + 1)} />
        </div>
      ) : null}
      {sub.status === "error" && sub.error ? (
        <div className="mb-3 flex items-center gap-3 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          <span>{describeApiError(sub.error)}</span>
          <Button type="button" size="sm" variant="outline" className="ml-auto" onClick={() => sub.restart()}>
            <RotateCwIcon data-icon-start-inline />
            Retry
          </Button>
        </div>
      ) : null}

      <div className="mb-3 flex items-center gap-3 font-mono text-2xs text-muted-foreground">
        <span className={sub.status === "live" ? "text-[var(--status-success)]" : ""}>
          {sub.status === "paused" ? "○ paused" : sub.status === "live" ? "● following" : `● ${sub.status}`}
        </span>
        <span>cursor seq {cursorSeq ?? anchor ?? "—"}</span>
        {frames.length >= MAX_ROWS ? <span>buffer capped (oldest dropped)</span> : null}
      </div>

      <EventFeed
        sub={sub}
        backlog={backlog}
        getKey={(event) => event.seq ?? event.created_at ?? ""}
        render={renderEvent}
        resyncAction={
          <Button type="button" size="sm" variant="outline" onClick={sub.status === "stalled" ? () => sub.restart() : resync}>
            {sub.status === "stalled" ? "Reconnect" : "Resync"}
          </Button>
        }
        emptyText={loadError != null ? "" : anchor === undefined ? "Loading events…" : "No events in the retention window."}
        statusLabels={{ live: "following", paused: "paused" }}
        labels={{
          gapGapped:
            "Retention window moved past the previous cursor — resynchronizing from the latest seq; older events are outside the retention window.",
          gapStalled: "Backoff budget exhausted — press Reconnect to retry the ticket stream.",
          paused: "Paused — frames buffered from cursor on resume",
          count: (n) => `${n} event${n === 1 ? "" : "s"}`,
        }}
      />
    </PageFrame>
  );
}
