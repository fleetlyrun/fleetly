import type { LucideIcon } from "lucide-react";
import { RotateCwIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";

// ErrorState（UI v2 四态契约）：错误必须诚实且可行动——分状态文案由
// describeError（lib/api-errors，批 1 接 ApiError 分类）解析，这里保证
// "出了什么事 + 现在该怎么办（重试一等按钮）"的最低形态。
export function ErrorState({
  icon: Icon = RotateCwIcon,
  title,
  description,
  onRetry,
}: {
  icon?: LucideIcon;
  title: string;
  description?: string;
  onRetry?: () => void;
}) {
  return (
    <Empty>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Icon />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        {description ? <EmptyDescription className="max-w-md whitespace-pre-wrap">{description}</EmptyDescription> : null}
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
