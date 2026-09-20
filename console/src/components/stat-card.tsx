// 统计卡：仪表盘原子（dokploy 同构）——大写小标签 + 大数字 + 辅助行。
// children 用于非数值主体（如状态彩点明细列表）。

import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

export function StatCard({
  label,
  value,
  sub,
  children,
  className,
}: {
  label: string;
  value?: ReactNode;
  sub?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("rounded-xl border bg-card p-5", className)}>
      <div className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </div>
      {value !== undefined ? (
        <div className="mt-2 text-3xl font-semibold tabular-nums tracking-tight">
          {value}
        </div>
      ) : null}
      {children ? <div className="mt-3 flex-1">{children}</div> : null}
      {sub ? <div className="mt-2 truncate text-xs text-muted-foreground">{sub}</div> : null}
    </div>
  );
}
