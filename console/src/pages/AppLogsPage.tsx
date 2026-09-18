// 日志页：实时跟随（NDJSON 流）+ 历史检索（时间窗/limit/source）。
//
// 断线续读口径：FollowLogs 契约无游标参数（proto/logs.proto），重连策略
// = 记录最后一条日志的时间戳 → 先以 since=<last_ts> 回放历史补缺口 →
// 再重开跟随流。游标语义在事件流（seq）实现，见 EventsPage。

import { useQuery } from "@tanstack/react-query";
import { Pause, Play, RefreshCw, ScrollText } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router-dom";

import {
  getRevisionSpec,
  listHistoryLogs,
  listRevisions,
} from "@/api/endpoints";
import { followLogs, StreamError } from "@/api/streams";
import type { LogEntryView } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { formatTime } from "@/lib/utils";

const LIVE_CAP = 2000;
const ALL_SERVICES = "__all__";
const RECONNECT_DELAY_MS = 1500;

function entryKey(e: LogEntryView): string {
  return `${e.at ?? ""}|${e.service}|${e.source}|${e.line}`;
}

/** 服务名清单：从最近 active revision 的 canonical JSON compose 提取。 */
function useServiceNames(app: string) {
  const revisionsQuery = useQuery({
    queryKey: ["revisions", app],
    queryFn: () => listRevisions(app),
  });
  const active = revisionsQuery.data?.revisions.find(
    (r) => r.status === "active",
  );
  const specQuery = useQuery({
    queryKey: ["revision-spec", app, active?.id],
    queryFn: () => getRevisionSpec(app, active!.id),
    enabled: active !== undefined,
  });
  return useMemo(() => {
    if (!specQuery.data?.compose) return [];
    try {
      const spec = JSON.parse(specQuery.data.compose) as {
        services?: Record<string, unknown>;
      };
      return Object.keys(spec.services ?? {});
    } catch {
      return [];
    }
  }, [specQuery.data]);
}

export function AppLogsPage() {
  const { name = "" } = useParams();
  const [service, setService] = useState<string>(ALL_SERVICES);
  const [source, setSource] = useState<"container" | "build" | "all">("all");
  const [live, setLive] = useState(true);
  const [entries, setEntries] = useState<LogEntryView[]>([]);
  const [streamError, setStreamError] = useState<string>("");
  const [autoScroll, setAutoScroll] = useState(true);

  // 历史检索窗（可选；空 = 最近 limit 条）。
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");

  const serviceNames = useServiceNames(name);

  const appendEntries = useCallback((incoming: LogEntryView[]) => {
    if (incoming.length === 0) return;
    setEntries((prev) => {
      const seen = new Set(prev.slice(-LIVE_CAP).map(entryKey));
      const merged = [...prev];
      for (const e of incoming) {
        if (e.source === "build" && source === "container") continue;
        if (e.source === "container" && source === "build") continue;
        const k = entryKey(e);
        if (seen.has(k)) continue;
        seen.add(k);
        lastSeenRef.current = e;
        merged.push(e);
      }
      return merged.slice(-LIVE_CAP);
    });
  }, [source]);

  // 重连续读的续读点：最新一条已累积日志（appendEntries 内更新，见上）。
  const lastSeenRef = useRef<LogEntryView | null>(null);

  // 实时跟随：跟随流断开后回放历史补缺口，再重开跟随（重连退避 1.5s）。
  useEffect(() => {
    if (!live) return;
    let conn: { close(): void } | null = null;
    let timer: number | undefined;
    let disposed = false;

    const open = () => {
      followLogs(name, service === ALL_SERVICES ? undefined : service, {
        onEntry: (entry) => {
          setStreamError("");
          appendEntries([entry]);
        },
        onEnd: (err) => {
          if (disposed) return;
          if (err instanceof StreamError && err.status === 401) {
            // 鉴权失效：全局监听接手（回登录页），此处不再重连。
            setStreamError("stream rejected (401)");
            return;
          }
          setStreamError(
            err instanceof Error ? `${err.message} — reconnecting…` : "stream ended — reconnecting…",
          );
          timer = window.setTimeout(() => {
            if (disposed) return;
            const last = lastSeenRef.current;
            const backfill = last?.at
              ? listHistoryLogs(name, {
                  service: service === ALL_SERVICES ? undefined : service,
                  since: last.at,
                })
                  .then((r) => appendEntries(r.entries))
                  .catch(() => undefined)
              : Promise.resolve();
            void backfill.then(() => {
              if (!disposed) open();
            });
          }, RECONNECT_DELAY_MS);
        },
      })
        .then((c) => {
          if (disposed) c.close();
          else conn = c;
        })
        .catch(() => {
          if (!disposed) {
            timer = window.setTimeout(open, RECONNECT_DELAY_MS);
          }
        });
    };
    open();

    return () => {
      disposed = true;
      if (timer !== undefined) window.clearTimeout(timer);
      conn?.close();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, name, service]);

  // 初始历史回填（最近 200 条），给跟随流一个上下文头部。
  useEffect(() => {
    let cancelled = false;
    listHistoryLogs(name, {
      service: service === ALL_SERVICES ? undefined : service,
      limit: 200,
    })
      .then((r) => {
        if (!cancelled) appendEntries(r.entries);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name, service]);

  const visible = useMemo(() => {
    return entries.filter((e) => {
      if (source === "container" && e.source === "build") return false;
      if (source === "build" && e.source === "container") return false;
      return true;
    });
  }, [entries, source]);

  // 自动滚动到底部。
  const scrollRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (autoScroll && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [visible, autoScroll]);

  const loadHistory = useCallback(() => {
    listHistoryLogs(name, {
      service: service === ALL_SERVICES ? undefined : service,
      since: since ? new Date(since).toISOString() : undefined,
      until: until ? new Date(until).toISOString() : undefined,
      limit: 500,
      source: source === "all" ? "" : source,
    })
      .then((r) => {
        setEntries(r.entries);
      })
      .catch((err) =>
        setStreamError(err instanceof Error ? err.message : String(err)),
      );
  }, [name, service, since, until, source]);

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Streaming logs
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-end gap-3">
            <div className="w-48 space-y-1.5">
              <Label htmlFor="log-service">Service</Label>
              <Select
                value={service}
                onValueChange={(v) => {
                  setService(v);
                  setEntries([]);
                }}
              >
                <SelectTrigger id="log-service">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL_SERVICES}>All services</SelectItem>
                  {serviceNames.map((s) => (
                    <SelectItem key={s} value={s}>
                      {s}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="w-40 space-y-1.5">
              <Label htmlFor="log-source">Source</Label>
              <Select
                value={source}
                onValueChange={(v) => setSource(v as typeof source)}
              >
                <SelectTrigger id="log-source">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All</SelectItem>
                  <SelectItem value="container">container</SelectItem>
                  <SelectItem value="build">build</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button variant="outline" size="sm" onClick={() => setLive((v) => !v)}>
              {live ? (
                <>
                  <Pause aria-hidden className="h-3.5 w-3.5" /> Pause follow
                </>
              ) : (
                <>
                  <Play aria-hidden className="h-3.5 w-3.5" /> Resume follow
                </>
              )}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setAutoScroll((v) => !v)}
            >
              <ScrollText aria-hidden className="h-3.5 w-3.5" />
              {autoScroll ? "Auto-scroll on" : "Auto-scroll off"}
            </Button>
            {streamError ? (
              <span className="text-xs text-amber-700">{streamError}</span>
            ) : (
              <span className="text-xs text-emerald-700">
                {live ? "following" : "paused"}
              </span>
            )}
          </div>

          <div
            ref={scrollRef}
            data-testid="log-stream"
            className="h-[420px] overflow-auto rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-100"
          >
            {visible.length === 0 ? (
              <p className="text-zinc-500">
                No entries yet. Waiting for logs…
              </p>
            ) : (
              visible.map((e) => (
                <div key={entryKey(e)} className="whitespace-pre-wrap break-all">
                  <span className="text-zinc-500">
                    {e.at ? formatTime(e.at) : "—"}{" "}
                  </span>
                  <span className="text-sky-400">{e.service}</span>
                  {e.source === "build" ? (
                    <span className="text-amber-400"> [build] </span>
                  ) : (
                    " "
                  )}
                  {e.stderr ? (
                    <span className="text-red-400">{e.line}</span>
                  ) : (
                    e.line
                  )}
                </div>
              ))
            )}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-sm font-medium text-muted-foreground">
            History search
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="log-since">Since</Label>
              <Input
                id="log-since"
                type="datetime-local"
                value={since}
                onChange={(e) => setSince(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="log-until">Until</Label>
              <Input
                id="log-until"
                type="datetime-local"
                value={until}
                onChange={(e) => setUntil(e.target.value)}
              />
            </div>
            <Button variant="outline" size="sm" onClick={loadHistory}>
              <RefreshCw aria-hidden className="h-3.5 w-3.5" />
              Search history
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
