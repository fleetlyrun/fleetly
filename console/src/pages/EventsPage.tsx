// 事件流（全局 activity）：WatchEvents NDJSON 流 + seq 游标断线续读。
// 游标过期（cursor_expired 帧）→ 以 oldest_seq 重拉全量。事件行可展开
// payload JSON（脱敏由采集端保证——state-model §2.9）。

import { RefreshCw } from "lucide-react";
import { Fragment, useCallback, useEffect, useRef, useState } from "react";

import { ReconnectBackoff } from "@/api/backoff";
import { watchEvents } from "@/api/streams";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { formatTime } from "@/lib/utils";
import type { EventView } from "@/api/types";

const EVENT_CAP = 500;

/** 已解包（去掉 gateway result 层）的事件帧投影。 */
type InnerEventFrame = { event?: EventView; cursor_expired?: import("@/api/types").CursorExpiredView };

export function EventsPage() {
  const [events, setEvents] = useState<EventView[]>([]);
  const [connected, setConnected] = useState(false);
  const [notice, setNotice] = useState("");
  const cursorRef = useRef(0); // 重连续读点（仅在回调中写；渲染派生值见 lastSeq）
  const [expanded, setExpanded] = useState<string | null>(null);

  // 渲染面派生游标（不读 ref）。生成类型口径：零值/缺省 seq 回落 "0"。
  const lastSeq = events.length > 0 ? (events[events.length - 1].seq ?? "0") : "0";

  const mergeFrames = useCallback((frames: InnerEventFrame[]) => {
    for (const frame of frames) {
      if (frame.cursor_expired) {
        // 断档信号：seq ≤ oldest_seq-1 已被清理——以 oldest_seq 重拉。
        //（oldest_seq 是 proto int64 的 JSON 字符串形态）
        const oldest = Number(frame.cursor_expired.oldest_seq);
        setNotice(
          `event cursor expired (oldest kept seq ${frame.cursor_expired.oldest_seq}) — resyncing`,
        );
        cursorRef.current = Math.max(0, oldest - 1);
        setEvents([]);
        continue;
      }
      if (frame.event) {
        setNotice("");
        const incoming = frame.event;
        setEvents((prev) => {
          if (prev.some((e) => e.seq === incoming.seq)) return prev;
          return [...prev, incoming].slice(-EVENT_CAP);
        });
        cursorRef.current = Number(incoming.seq ?? 0);
      }
    }
  }, []);

  // 重连延迟走共享指数退避（D4-③：1.5s 起 ×2、上限 30s、±20% 抖动；服务
  // 端连续立即正常关流时降频并提示）。
  useEffect(() => {
    let conn: { close(): void } | null = null;
    let timer: number | undefined;
    let disposed = false;
    const backoff = new ReconnectBackoff();

    const open = () => {
      watchEvents(cursorRef.current, {
        onFrame: (frame) => mergeFrames([frame]),
        onEnd: (err) => {
          if (disposed) return;
          setConnected(false);
          if (err && err instanceof Error && "status" in err && (err as { status?: number }).status === 401) {
            // 全局登出已由流式层统一触发（stream.ts 的 401 处置），
            // 此处仅展示并不再重连。
            setNotice("stream rejected (401)");
            return;
          }
          const delay = backoff.nextDelayMs(err === undefined);
          setNotice(
            `stream ended — reconnecting with seq cursor${
              backoff.throttled ? " (server keeps closing the stream; retries slowed)" : ""
            }…`,
          );
          timer = window.setTimeout(() => {
            if (!disposed) open();
          }, delay);
        },
      })
        .then((c) => {
          if (disposed) c.close();
          else {
            conn = c;
            backoff.markOpen();
            setConnected(true);
          }
        })
        .catch(() => {
          if (!disposed) {
            timer = window.setTimeout(open, backoff.nextDelayMs(false));
          }
        });
    };
    open();

    return () => {
      disposed = true;
      if (timer !== undefined) window.clearTimeout(timer);
      conn?.close();
    };
  }, [mergeFrames]);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Events</h1>
        <div className="flex items-center gap-3">
          {notice ? <span className="text-xs text-amber-700">{notice}</span> : (
              <span className={`text-xs ${connected ? "text-emerald-700" : "text-muted-foreground"}`}>
                {connected ? `streaming (cursor seq ${lastSeq})` : "connecting…"}
              </span>
          )}
          <Button variant="outline" size="sm" onClick={() => setEvents([])}>
            <RefreshCw aria-hidden className="h-3.5 w-3.5" />
            Clear view
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Activity ({events.length})
          </CardTitle>
        </CardHeader>
        <CardContent>
          {events.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No events received yet. Deploy something or wait for platform
              activity.
            </p>
          ) : (
            <ul className="divide-y text-sm" data-testid="events-list">
              {[...events].reverse().map((e) => (
                <Fragment key={e.seq}>
                  <li className="flex cursor-pointer flex-wrap items-center gap-2 py-2"
                      onClick={() => setExpanded((x) => (x === e.seq ? null : e.seq ?? ""))}>
                    <span className="font-mono text-xs text-muted-foreground">
                      #{e.seq}
                    </span>
                    <span className="font-medium">{e.name}</span>
                    <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
                      {e.subject}
                    </code>
                    <span className="ml-auto whitespace-nowrap text-xs text-muted-foreground">
                      {formatTime(e.at)}
                    </span>
                  </li>
                  {expanded === e.seq ? (
                    <li className="pb-2">
                      <pre className="max-h-56 overflow-auto rounded-md bg-zinc-950 p-3 font-mono text-xs text-zinc-100">
                        {prettyPayload(e.payload ?? "")}
                      </pre>
                    </li>
                  ) : null}
                </Fragment>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function prettyPayload(payload: string): string {
  try {
    return JSON.stringify(JSON.parse(payload), null, 2);
  } catch {
    return payload;
  }
}
