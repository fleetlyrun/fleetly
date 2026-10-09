import { useEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDownIcon } from "lucide-react";
import type { LogFrame } from "@/api/streams";
import { decodeLine } from "./use-log-stream";

// LogViewer（Workbench 原型，UI v2 批 3）：@tanstack/react-virtual 虚拟
// 渲染——5000 帧环形缓冲不再全量 DOM（旧页卡顿面消除）；autoscroll 智能
// 停（用户上滚即挂起 + jump-to-live 浮标）；行内 warn/error 着色。

export function LogViewer({ frames, streaming }: { frames: LogFrame[]; streaming: boolean }) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const [stuck, setStuck] = useState(true);

  const virtualizer = useVirtualizer({
    count: frames.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 22,
    overscan: 24,
    getItemKey: (index) => index,
  });

  useEffect(() => {
    if (stuck && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [frames.length, stuck]);

  function onScroll() {
    const el = scrollRef.current;
    if (el == null) return;
    const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 40;
    setStuck(atBottom);
  }

  return (
    <div className="relative min-h-0 flex-1">
      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="size-full overflow-auto rounded-lg border bg-[oklch(0.135_0.015_285)] py-2 font-mono text-xs leading-relaxed dark:bg-[oklch(0.135_0.015_285)]"
        style={{ color: "var(--term-fg, oklch(0.88 0.01 285))" }}
      >
        {frames.length === 0 ? (
          <div className="p-6 text-center text-muted-foreground">
            {streaming ? "Waiting for frames…" : "Pick filters and press Start."}
          </div>
        ) : (
          <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
            {virtualizer.getVirtualItems().map((virtualRow) => {
              const frame = frames[virtualRow.index];
              const message = decodeLine(frame.line);
              const level = /error|fatal|panic/i.test(message) ? "error" : /warn/i.test(message) ? "warn" : "";
              return (
                <div
                  key={virtualRow.key}
                  data-index={virtualRow.index}
                  ref={virtualizer.measureElement}
                  className="flex gap-3.5 px-3.5 hover:bg-white/[0.04]"
                  style={{ position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${virtualRow.start}px)` }}
                >
                  <span className="flex-none whitespace-nowrap text-[oklch(0.6_0.01_285)]">{frame.time ?? ""}</span>
                  <span className="flex-none whitespace-nowrap text-[oklch(0.55_0.02_285)]">
                    {[frame.node, frame.container?.slice(0, 12)].filter(Boolean).join(" ") || frame.workload_id?.slice(0, 12) || ""}
                  </span>
                  <span
                    className={`min-w-0 flex-1 whitespace-pre-wrap break-all ${
                      level === "error" ? "text-[var(--status-danger)]" : level === "warn" ? "text-[var(--status-warning)]" : ""
                    }`}
                  >
                    {message}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </div>
      {!stuck && frames.length > 0 ? (
        <button
          type="button"
          onClick={() => {
            setStuck(true);
            const el = scrollRef.current;
            if (el) el.scrollTop = el.scrollHeight;
          }}
          className="absolute bottom-3.5 left-1/2 flex -translate-x-1/2 items-center gap-1.5 rounded-full bg-primary px-3.5 py-1.5 text-xs font-semibold text-primary-foreground shadow-lg"
        >
          <ArrowDownIcon className="size-3" />
          Jump to live
        </button>
      ) : null}
    </div>
  );
}
