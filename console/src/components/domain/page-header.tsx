import type { ReactNode } from "react";

// PageHeader（List/Detail 原型解剖）：面包屑 + 标题 + 描述 + 右侧主操作。
// 全站页头唯一形态——标题层级与操作位不随页面自由发挥。Dokploy 页卡：
// 页头是内层卡顶部的 border-b 区（px-6 py-5），不再是浮在内容上的 mb-6。
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
    <div className="flex flex-wrap items-start gap-4 border-b border-line px-6 py-5">
      <div className="min-w-0 flex-1">
        {breadcrumb ? <div className="mb-1.5">{breadcrumb}</div> : null}
        <h1 className="font-heading text-2xl font-semibold tracking-tight">{title}</h1>
        {description ? <p className="mt-1 text-sm text-muted">{description}</p> : null}
      </div>
      {actions ? <div className="flex flex-none items-center gap-2 pt-0.5">{actions}</div> : null}
    </div>
  );
}
