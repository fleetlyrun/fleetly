import type { ReactNode } from "react";

// PageHeader（List/Detail 原型解剖）：面包屑 + 标题 + 描述 + 右侧主操作。
// 全站页头唯一形态——标题层级与操作位不随页面自由发挥。
export function PageHeader({
  breadcrumb,
  title,
  description,
  actions,
}: {
  breadcrumb?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-5 flex flex-wrap items-start gap-4">
      <div className="min-w-0 flex-1">
        {breadcrumb ? <div className="mb-1">{breadcrumb}</div> : null}
        <h1 className="font-heading text-xl font-bold tracking-tight">{title}</h1>
        {description ? <p className="mt-0.5 text-[13px] text-muted-foreground">{description}</p> : null}
      </div>
      {actions ? <div className="flex flex-none items-center gap-2 pt-1">{actions}</div> : null}
    </div>
  );
}
