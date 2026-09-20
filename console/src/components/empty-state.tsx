// 空态：图标 + 标题 + 指引（列表/表格空数据的统一呈现，替代裸文本行）。

import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

export function EmptyState({
  icon: Icon,
  title,
  hint,
  children,
  className,
}: {
  icon: LucideIcon;
  title: string;
  hint?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-1.5 px-6 py-10 text-center",
        className,
      )}
    >
      <Icon aria-hidden className="mb-1 h-8 w-8 text-muted-foreground/40" />
      <p className="text-sm font-medium">{title}</p>
      {hint ? (
        <p className="max-w-md text-xs text-muted-foreground">{hint}</p>
      ) : null}
      {children}
    </div>
  );
}
