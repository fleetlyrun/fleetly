import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";

// EmptyState（UI v2 四态契约）：空态必须可行动——图标 + 一句话 + CTA。
// 基于 shadcn Empty 件包装，页面不自拼布局。
export function EmptyState({
  icon: Icon,
  title,
  description,
  actionLabel,
  onAction,
  action,
}: {
  icon: LucideIcon;
  title: string;
  description?: string;
  actionLabel?: string;
  onAction?: () => void;
  /** 自定义 action（需要 Router Link/asChild 时优先于 actionLabel）。 */
  action?: ReactNode;
}) {
  return (
    <Empty>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Icon />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        {description ? <EmptyDescription>{description}</EmptyDescription> : null}
        {action ?? (actionLabel && onAction ? (
          <Button size="sm" onClick={onAction} className="mt-2">
            {actionLabel}
          </Button>
        ) : null)}
      </EmptyHeader>
    </Empty>
  );
}
