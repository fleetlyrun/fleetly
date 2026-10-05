import { useCallback, useEffect, useRef, useState } from "react";
import { apiFetch } from "../api/client";
import type { EventRow } from "../api/streams";
import { followEvents, mintEventTicket } from "../api/streams";
import { EmptyNote, ErrorNote, LoadingNote, PageShell, formatTime } from "../components/ui";

// 事件流页（F2.6/ADR-0044 决策 1）：补档 GET /v1/events → 票据订阅
// /v1/events/follow（ADR-0026 钉面）。断档（410 / error 帧 E_EVENTS_GONE）
// 页内自动重同步：重读 status 游标后从 last_seq 跟随（保留窗滑过即丢档，
// 诚实呈现 resync 提示而非静默跳号）。

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

export function EventsPage() {
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
              setError(new Error(`event stream rejected (${end.status}) — check the API token in the header`));
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
    <PageShell
      title="Events"
      hint="backlog via GET /v1/events, then following /v1/events/follow (one-time ticket, ADR-0026)"
      toolbar={
        <button
          type="button"
          onClick={toggleFollowing}
          className={
            following
              ? "rounded-md border border-red-800 bg-red-950/50 px-3 py-1.5 text-sm font-medium text-red-300"
              : "rounded-md border border-sky-700 bg-sky-900/40 px-3 py-1.5 text-sm font-medium text-sky-300"
          }
        >
          {following ? "Pause" : "Resume"}
        </button>
      }
    >
      {error ? <ErrorNote error={error} hint="the events surface failed — check the API token in the header." /> : null}
      {resynced ? (
        <div className="rounded-md border border-amber-900/60 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
          Retention window moved past the previous cursor — resynchronized from seq {cursor}; older events are outside the
          retention window.
        </div>
      ) : null}
      <div className="flex items-center gap-2 text-xs text-slate-500">
        <span className={following ? "text-emerald-400" : "text-slate-500"}>{following ? "● following" : "○ paused"}</span>
        <span>cursor seq {cursor ?? "—"}</span>
        <span>
          {rows.length} event{rows.length === 1 ? "" : "s"}
          {rows.length >= MAX_ROWS ? " · buffer capped (oldest dropped)" : ""}
        </span>
      </div>
      {rows.length === 0 ? (
        error ? null : (
          <LoadingNote label="Loading events…" />
        )
      ) : displayed.length === 0 ? (
        <EmptyNote label="No events in the retention window." />
      ) : (
        <div className="max-h-[70vh] overflow-auto rounded-lg border border-slate-800">
          <table className="w-full text-sm">
            <thead className="sticky top-0 bg-slate-900/95">
              <tr className="border-b border-slate-800 text-left text-xs uppercase tracking-wide text-slate-500">
                <th className="px-3 py-2 font-medium">Seq</th>
                <th className="px-3 py-2 font-medium">Event</th>
                <th className="px-3 py-2 font-medium">Aggregate</th>
                <th className="px-3 py-2 font-medium">Time</th>
              </tr>
            </thead>
            <tbody>
              {displayed.map((event) => (
                <tr key={event.seq ?? event.created_at} className="border-b border-slate-800/60 align-top hover:bg-slate-900/40">
                  <td className="px-3 py-1.5 font-mono text-xs text-slate-500">{event.seq ?? "—"}</td>
                  <td className="px-3 py-1.5 font-mono text-xs text-sky-300">{event.name ?? "—"}</td>
                  <td className="px-3 py-1.5 font-mono text-xs text-slate-400">
                    {event.aggregate ?? "—"}
                    {event.aggregate_id ? <span className="text-slate-600"> / {event.aggregate_id}</span> : null}
                  </td>
                  <td className="px-3 py-1.5 text-xs text-slate-400">
                    {formatTime(event.created_at)}
                    {event.payload && event.payload !== "{}" ? (
                      <details className="mt-0.5">
                        <summary className="cursor-pointer text-slate-500 hover:text-slate-300">payload</summary>
                        <pre className="mt-1 max-w-lg overflow-auto rounded bg-slate-950 p-2 font-mono text-xs text-slate-300">
                          {prettyPayload(event.payload)}
                        </pre>
                      </details>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </PageShell>
  );
}
