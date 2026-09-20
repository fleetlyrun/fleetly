// 状态彩点：fleetly 颜色语义的原子呈现（表格行 / 活动流 / 统计明细共用）。
// 色板口径与 StateBadge 同源（见 state-badge.tsx 顶部注释）；调用方既可传
// 平台状态词（state），也可直接指定色调（tone，如事件名派生场景）。

import { cn } from "@/lib/utils";
import { stateTone, TONE_CLASSES, type StateTone } from "@/components/state-badge";

export function StatusDot({
  state,
  tone,
  className,
}: {
  state?: string;
  tone?: StateTone;
  className?: string;
}) {
  const resolved = tone ?? stateTone(state ?? "");
  return (
    <span
      aria-hidden
      data-tone={resolved}
      className={cn(
        "h-2 w-2 shrink-0 rounded-full",
        TONE_CLASSES[resolved],
        className,
      )}
    />
  );
}
