import type { LucideIcon } from "lucide-react";
import { RotateCwIcon, ShieldAlertIcon } from "lucide-react";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { describeError } from "@/lib/api-errors";

// ErrorState（UI v2 四态契约）：错误必须诚实且可行动——分状态文案出自
// lib/api-errors 单源（401/403/404/5xx/网络各有其话），信封原文附底，
// 重试是一等按钮。
export function ErrorState({
  icon,
  error,
  onRetry,
}: {
  icon?: LucideIcon;
  error: unknown;
  onRetry?: () => void;
}) {
  const described = describeError(error);
  const Icon = icon ?? (error instanceof ApiError && error.status === 403 ? ShieldAlertIcon : RotateCwIcon);
  return (
    <Empty>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Icon />
        </EmptyMedia>
        <EmptyTitle>{described.title}</EmptyTitle>
        <EmptyDescription className="max-w-md">{described.hint}</EmptyDescription>
        <span className="max-w-lg truncate font-mono text-[11px] text-muted-foreground" title={described.detail}>
          {described.detail}
        </span>
        {onRetry ? (
          <Button variant="outline" size="sm" onClick={onRetry} className="mt-2">
            <RotateCwIcon data-icon-start-inline />
            Retry
          </Button>
        ) : null}
      </EmptyHeader>
    </Empty>
  );
}
