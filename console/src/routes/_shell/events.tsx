import { useCallback, useEffect, useRef, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { PauseIcon, PlayIcon } from "lucide-react";
import { apiFetch } from "@/api/client";
import type { EventRow } from "@/api/streams";
import { followEvents, mintEventTicket } from "@/api/streams";
import { formatAbsolute } from "@/lib/format";
import { ErrorState } from "@/components/domain/error-state";
import { PageHeader } from "@/components/domain/page-header";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

// 事件流页（F2.6/ADR-0044 决策 1 → UI v2 批 5 reskin）：补档
// GET /v1/events → 票据订阅 /v1/events/follow（ADR-0026 钉面）。断档
//（410 / error 帧 E_EVENTS_GONE）页内自动重同步——重读 status 游标后从
// last_seq 跟随（保留窗滑过即丢档，诚实呈现 resync 提示而非静默跳号）。
// 数据面（订阅/退避重连/resync/Pause-Resume）零改动，只换呈现。

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
  const [rows, setRows] = useState<EventRow[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [resynced, setResynced] = useState(false);
  const [following, setFollowing] = useState(false);
  const stopRef = useRef<{ stop: () => void } | null>(null);
  const aliveRef = useRef(true);
  const retryTimer = useRef<number | undefined>(undefined);

  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
      stopRef.current?.stop();
      window.clearTimeout(retryTimer.current);
    };
  }, []);

  const appendRows = useCallback((incoming: EventRow[]) => {
    if (incoming.length === 0) return;
    setRows((prev) => {
      const next = [...prev, ...incoming];
      return next.length > MAX_ROWS ? next.slice(next.length - MAX_ROWS) : next;
    });
  }, []);

  // resync：断档后的快照重同步（ADR-0026 三件套）——重读 status，从
  // last_seq 直接跟随（旧档已滑出保留窗，不假装补齐）。
  const resync = useCallback(async () => {
    stopRef.current?.stop();
    const status = await apiFetch<StatusResponse>("/v1/events/status");
    setCursor(status.last_seq ?? "0");
    setRows([]);
    setResynced(true);
    await subscribe(status.last_seq ?? "0");
  }, []);

  // subscribe：铸票据 → 开跟随流；结束面分诊（410/错误帧 = resync，
  // 传输收口 = 退避重连，停面 = 静默）。
  const subscribe = useCallback(
    async (afterSeq: string) => {
      try {
        const ticket = await mintEventTicket();
        if (!aliveRef.current) return;
        setFollowing(true);
        stopRef.current = followEvents(
          ticket,
          afterSeq,
          (event) => {
            setCursor(event.seq ?? null);
            appendRows([event]);
          },
          (end) => {
            setFollowing(false);
            if (!aliveRef.current) return;
            if (end.status === 410) {
              void resync().catch((err: unknown) => setError(err));
              return;
            }
            if (end.status === 401 || end.status === 403) {
              setError(new Error(`event stream rejected (${end.status}) — the session may have expired; sign in again`));
              return;
            }
            retryTimer.current = window.setTimeout(() => {
              void subscribe(afterSeq).catch((err: unknown) => setError(err));
            }, RECONNECT_MS);
          },
        );
      } catch (err) {
        if (aliveRef.current) setError(err);
      }
    },
    [appendRows, resync],
  );

  // 初始装载：status 定锚 → 补档最近 BACKLOG_LIMIT 条 → 跟随。
  useEffect(() => {
    void (async () => {
      try {
        const status = await apiFetch<StatusResponse>("/v1/events/status");
        const lastSeq = Number(status.last_seq ?? "0");
        const afterSeq = Math.max(0, lastSeq - BACKLOG_LIMIT);
        const list = await apiFetch<ListEventsResponse>(`/v1/events?after_seq=${afterSeq}&limit=${BACKLOG_LIMIT}`);
        if (!aliveRef.current) return;
        setRows((list.events ?? []).flatMap((event) => (event?.seq ? [event] : [])));
        setCursor(status.last_seq ?? "0");
        await subscribe(status.last_seq ?? "0");
      } catch (err) {
        if (aliveRef.current) setError(err);
      }
    })();
  }, [subscribe]);

  // Pause/Resume：手动停跟随 / 从当前游标续订。
  function toggleFollowing() {
    if (following) {
      stopRef.current?.stop();
      window.clearTimeout(retryTimer.current);
      setFollowing(false);
      return;
    }
    setError(null);
    void subscribe(cursor ?? "0").catch((err: unknown) => setError(err));
  }

  const displayed = [...rows].reverse();

  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader
        title="Events"
        description="Backlog via GET /v1/events, then following /v1/events/follow (one-time ticket, ADR-0026)"
        actions={
          <Button size="sm" variant={following ? "outline" : "default"} onClick={toggleFollowing}>
            {following ? <PauseIcon data-icon-start-inline /> : <PlayIcon data-icon-start-inline />}
            {following ? "Pause" : "Resume"}
          </Button>
        }
      />

      {error ? <ErrorState error={error} onRetry={toggleFollowing} /> : null}
      {resynced ? (
        <div className="mb-3 rounded-lg border border-[color-mix(in_oklch,var(--status-warning)_35%,transparent)] bg-[var(--status-warning-bg)] px-3 py-2 text-xs text-[var(--status-warning)]">
          Retention window moved past the previous cursor — resynchronized from seq {cursor}; older events are outside the retention
          window.
        </div>
      ) : null}
      <div className="mb-3 flex items-center gap-3 font-mono text-[11px] text-muted-foreground">
        <span className={following ? "text-[var(--status-success)]" : ""}>{following ? "● following" : "○ paused"}</span>
        <span>cursor seq {cursor ?? "—"}</span>
        <span>
          {rows.length} event{rows.length === 1 ? "" : "s"}
          {rows.length >= MAX_ROWS ? " · buffer capped (oldest dropped)" : ""}
        </span>
      </div>

      <div className="rounded-xl border bg-card">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="sticky top-0 bg-card">Seq</TableHead>
              <TableHead className="sticky top-0 bg-card">Event</TableHead>
              <TableHead className="sticky top-0 bg-card">Aggregate</TableHead>
              <TableHead className="sticky top-0 bg-card">Time</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} className="py-8 text-center text-sm text-muted-foreground">
                  {error != null ? "" : "Loading events…"}
                </TableCell>
              </TableRow>
            ) : displayed.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} className="py-8 text-center text-sm text-muted-foreground">
                  No events in the retention window.
                </TableCell>
              </TableRow>
            ) : (
              displayed.map((event) => (
                <TableRow key={event.seq ?? event.created_at} className="align-top">
                  <TableCell className="font-mono text-xs text-muted-foreground">{event.seq ?? "—"}</TableCell>
                  <TableCell className="font-mono text-xs text-[var(--status-info)]">{event.name ?? "—"}</TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">
                    {event.aggregate ?? "—"}
                    {event.aggregate_id ? <span className="opacity-50"> / {event.aggregate_id}</span> : null}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground" title={formatAbsolute(event.created_at)}>
                    {formatAbsolute(event.created_at)}
                    {event.payload && event.payload !== "{}" ? (
                      <details className="mt-0.5">
                        <summary className="cursor-pointer hover:text-foreground">payload</summary>
                        <pre className="mt-1 max-w-lg overflow-auto rounded border bg-muted/40 p-2 font-mono text-[11px]">
                          {prettyPayload(event.payload)}
                        </pre>
                      </details>
                    ) : null}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
