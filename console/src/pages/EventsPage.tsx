// 事件流（全局 activity）：WatchEvents NDJSON 流 + seq 游标断线续读
// （订阅逻辑在 use-event-stream hook，与 Home 活动流共用）。行可展开
// payload JSON（脱敏由采集端保证——state-model §2.9）。

import { Search } from "lucide-react";
import { Fragment, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";

import { eventTone, useEventStream } from "@/hooks/use-event-stream";
import { useSubjectResolver } from "@/hooks/use-subject-resolver";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { StatusDot } from "@/components/status-dot";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { formatTime } from "@/lib/utils";
import type { EventView } from "@/api/types";

export function EventsPage() {
  const { events, connected, notice, lastSeq, clear } = useEventStream();
  const [expanded, setExpanded] = useState<string | null>(null);
  // subject 可读化（W2-7）：展示层反解业务名，过滤仍按原始 subject 匹配。
  const resolveSubject = useSubjectResolver();
  // 深链预填（W5-S2 degraded 解释卡入口）：?q=app:<name> 初始化过滤——
  // 链接直达「该 app 的事件流」；随后仍可自由改过滤。
  const [searchParams] = useSearchParams();
  const [filter, setFilter] = useState(searchParams.get("q") ?? "");

  // 客户端过滤（事件名/主题包含匹配）；新到事件实时进出视图。
  const visible = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    const ordered = [...events].reverse();
    if (!needle) return ordered;
    return ordered.filter(
      (e) =>
        (e.name ?? "").toLowerCase().includes(needle) ||
        (e.subject ?? "").toLowerCase().includes(needle),
    );
  }, [events, filter]);

  return (
    <div className="space-y-4">
      <PageHeader
        title="Events"
        description="Platform activity stream (live)."
        actions={
          <>
            {notice ? (
              <span className="text-xs text-amber-600 dark:text-amber-400">{notice}</span>
            ) : (
              <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <span
                  aria-hidden
                  className={`h-2 w-2 rounded-full ${
                    connected ? "bg-emerald-500 animate-pulse" : "bg-zinc-400"
                  }`}
                />
                {connected ? `streaming (cursor seq ${lastSeq})` : "connecting…"}
              </span>
            )}
            <Button variant="outline" size="sm" onClick={clear}>
              Clear view
            </Button>
          </>
        }
      />

      <Card>
        <CardContent className="p-0">
          <div className="border-b px-4 py-3">
            <div className="relative">
              <Search
                aria-hidden
                className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
              />
              <Input
                aria-label="Filter events"
                placeholder="Filter by name or subject…"
                className="w-72 pl-8"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
            </div>
          </div>

          {events.length === 0 ? (
            <EmptyState
              icon={Search}
              title="No events received yet."
              hint="Deploy something or wait for platform activity — events stream in live."
            />
          ) : visible.length === 0 ? (
            <EmptyState
              icon={Search}
              title="No events match the filter."
              hint="Broaden the filter to see more of the stream."
            />
          ) : (
            <ul className="divide-y text-sm" data-testid="events-list">
              {visible.map((e) => (
                <EventRow
                  key={e.seq}
                  event={e}
                  subjectLabel={resolveSubject(e.subject)}
                  expanded={expanded === e.seq}
                  onToggle={() => setExpanded((x) => (x === e.seq ? null : e.seq ?? ""))}
                />
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function EventRow({
  event,
  subjectLabel,
  expanded,
  onToggle,
}: {
  event: EventView;
  subjectLabel: string;
  expanded: boolean;
  onToggle: () => void;
}) {
  return (
    <Fragment>
      <li
        className="flex cursor-pointer flex-wrap items-center gap-2.5 px-4 py-2.5 transition-colors hover:bg-muted/40"
        onClick={onToggle}
      >
        <span className="w-14 shrink-0 font-mono text-xs text-muted-foreground">
          #{event.seq}
        </span>
        <StatusDot tone={eventTone(event.name)} />
        <span className="font-medium">{event.name}</span>
        <code className="truncate rounded bg-muted px-1.5 py-0.5 text-xs" title={event.subject}>
          {subjectLabel}
        </code>
        <span className="ml-auto whitespace-nowrap text-xs text-muted-foreground">
          {formatTime(event.at)}
        </span>
      </li>
      {expanded ? (
        <li className="px-4 pb-3">
          <pre className="max-h-56 overflow-auto rounded-md bg-zinc-950 p-3 font-mono text-xs text-zinc-100">
            {prettyPayload(event.payload ?? "")}
          </pre>
        </li>
      ) : null}
    </Fragment>
  );
}

function prettyPayload(payload: string): string {
  try {
    return JSON.stringify(JSON.parse(payload), null, 2);
  } catch {
    return payload;
  }
}
