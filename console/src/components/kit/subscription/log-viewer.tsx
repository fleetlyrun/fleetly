// vendored from @lynx-go/uikit registry/subscription/log-viewer.tsx @0.3.1（2026-10-26 批3 订阅族）
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 本块无方言 token；cn 统一 @/lib/utils。
// 手工步骤（判断题，工具不自动改）：
// - 未映射方言 token text-muted ×3——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// - 未映射方言 token bg-panel ×2——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// - 未映射方言 token border-line ×2——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// - Badge tone= ×1——已适配（torchwood event-feed 先例 + fleetly status vars）：
//   ok=default、err=destructive、warn=secondary+text-warn、neutral=secondary，见 statusBadge
// 本地扩展（2026-10-27 收尾批，上游候选落地——升版重拷时对照重落）：
// - 虚拟滚动 @tanstack/react-virtual（上游注释「虚拟滚动后置」的落地）：
//   缓冲上限 5000 帧不再全量 DOM——measureElement 动态测高（wrap 折行行高
//   不定），overscan 24；跟随/jump-to-live 改 scrollToIndex(end)。
// 其余与上游一致。
import type { SubscriptionEngine } from "@lynx-go/uikit-core/react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDown, Download, Trash2 } from "lucide-react";
import { useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/**
 * LogViewer（subscription 块）—— useSubscription 输出的参照实现（LogViewer↔Subscription）。
 * 固定高度滚动 + 智能跟随（贴底即跟随，上滚即停）+ jump-to-live + 下载/清空；
 * 容量上限由订阅策略的环形缓冲承担（policy.buffer 丢最旧），本块只呈现。
 * 虚拟滚动后置：帧量级在缓冲上限内时固定高度滚动即够（双 console 实证）。
 */

const STATUS_TONE: Record<string, "ok" | "warn" | "err" | "neutral"> = {
  live: "ok",
  reconnecting: "warn",
  paused: "neutral",
  gapped: "warn",
  stalled: "warn",
  ended: "neutral",
  error: "err",
};

// statusBadge 手工适配层（shadcn Badge 无 tone prop——demo tone → variant）。
function statusBadge(tone: "ok" | "warn" | "err" | "neutral"): {
  variant: "default" | "secondary" | "destructive";
  className?: string;
} {
  if (tone === "ok") return { variant: "default" };
  if (tone === "err") return { variant: "destructive" };
  return { variant: "secondary", ...(tone === "warn" ? { className: "text-warn" } : {}) };
}

export interface LogViewerProps<F> {
  sub: SubscriptionEngine<F>;
  formatLine: (frame: F, index: number) => string;
  /** 容器高度（px） */
  height?: number;
  /** 行不断行（默认折行）；长日志行建议保持折行 */
  wrap?: boolean;
  fileName?: string;
}

export function LogViewer<F>({
  sub,
  formatLine,
  height = 420,
  wrap = true,
  fileName = "logs.txt",
}: LogViewerProps<F>) {
  const frames = useSyncExternalStore(sub.subscribeFrames, sub.getFrames, sub.getFrames);
  const state = useSyncExternalStore(sub.subscribeStatus, sub.getState, sub.getState);

  const containerRef = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const badge = statusBadge(STATUS_TONE[state.status] ?? "neutral");

  // 虚拟滚动（本地扩展）：折行行高不定 → measureElement 动态测高；
  // 容器 p-3 内衬随滚动容器，虚拟层以 translateY 定位。
  const virtualizer = useVirtualizer({
    count: frames.length,
    getScrollElement: () => containerRef.current,
    estimateSize: () => 20,
    overscan: 24,
    getItemKey: (index) => index,
  });

  const onScroll = () => {
    const el = containerRef.current;
    if (!el) return;
    setFollowing(el.scrollHeight - el.scrollTop - el.clientHeight < 48);
  };

  // 智能跟随：贴底时新帧到达即贴底（useLayoutEffect 防闪跳）；虚拟面
  // scrollTop 编排归 virtualizer（scrollToIndex end = 旧 scrollHeight 赋值）。
  useLayoutEffect(() => {
    if (following && frames.length > 0) {
      virtualizer.scrollToIndex(frames.length - 1, { align: "end" });
    }
  }, [frames, following, virtualizer]);

  const jumpToLive = () => setFollowing(true);

  const download = () => {
    const text = frames.map((frame, index) => formatLine(frame, index)).join("\n");
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = fileName;
    anchor.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className="overflow-hidden rounded-md border border-line">
      <div className="flex items-center justify-between gap-2 border-b border-line bg-panel px-3 py-1.5">
        <div className="flex items-center gap-2">
          <span className="text-xs font-medium text-muted">Log stream</span>
          <Badge
            variant={badge.variant}
            className={badge.className}
            data-testid="log-status"
          >
            {state.status}
          </Badge>
          <span className="text-[11px] text-muted">{frames.length} lines</span>
        </div>
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" onClick={download} aria-label="Download logs">
            <Download className="size-3.5" />
          </Button>
          <Button variant="ghost" size="sm" onClick={() => sub.clear()} aria-label="Clear logs">
            <Trash2 className="size-3.5" />
          </Button>
        </div>
      </div>
      <div className="relative">
        <div
          ref={containerRef}
          onScroll={onScroll}
          role="log"
          aria-label="log lines"
          className="overflow-y-auto bg-panel/40 p-3 font-mono text-xs leading-5"
          style={{ height }}
        >
          {frames.length === 0 ? (
            <p className="text-muted">Waiting for frames…</p>
          ) : (
            <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
              {virtualizer.getVirtualItems().map((virtualRow) => (
                <div
                  key={virtualRow.key}
                  data-index={virtualRow.index}
                  ref={virtualizer.measureElement}
                  data-line={virtualRow.index}
                  className={cn("absolute left-0 top-0 w-full whitespace-pre", wrap ? "break-all" : "whitespace-nowrap")}
                  style={{ transform: `translateY(${virtualRow.start}px)` }}
                >
                  {formatLine(frames[virtualRow.index], virtualRow.index)}
                </div>
              ))}
            </div>
          )}
        </div>
        {!following ? (
          <Button
            variant="outline"
            size="sm"
            className="absolute bottom-3 left-1/2 -translate-x-1/2 shadow"
            onClick={jumpToLive}
            data-testid="jump-to-live"
          >
            <ArrowDown className="size-3.5" />
            Jump to live
          </Button>
        ) : null}
      </div>
    </div>
  );
}
