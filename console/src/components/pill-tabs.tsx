// 分段式页签（dokploy Overview 同款视觉）：muted 容器 + 激活项浮起。
// 受控组件——key 切换由调用方驱动（详情页走路由，System 页走 searchParams）。

import { cn } from "@/lib/utils";

export function PillTabs({
  value,
  onValueChange,
  items,
  ariaLabel,
  className,
}: {
  value: string;
  onValueChange: (key: string) => void;
  items: { key: string; label: string }[];
  ariaLabel: string;
  className?: string;
}) {
  return (
    <div
      role="tablist"
      aria-label={ariaLabel}
      className={cn(
        "inline-flex max-w-full items-center gap-1 overflow-x-auto rounded-lg bg-muted p-1",
        className,
      )}
    >
      {items.map((item) => {
        const active = item.key === value;
        return (
          <button
            key={item.key}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onValueChange(item.key)}
            className={cn(
              "whitespace-nowrap rounded-md px-3 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground",
              active && "bg-background text-foreground shadow-sm",
            )}
          >
            {item.label}
          </button>
        );
      })}
    </div>
  );
}
