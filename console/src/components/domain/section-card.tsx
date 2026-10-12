import type { ReactNode } from "react";
import { cn } from "cn";

// SectionCard 是 Settings 原型的区块卡（自 settings.tsx 本地 Card 收编全
// 站）：图标 + 标题 + 右侧动作位 + 内容体。设置/治理类页面的区块唯一
// 形态——区块内小表/表单各异，区块壳不自由发挥。
export function SectionCard({
  icon: Icon,
  title,
  action,
  className,
  children,
}: {
  icon?: React.ComponentType<{ className?: string }>;
  title: ReactNode;
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <section className={cn("rounded-xl border bg-card", className)}>
      <div className="flex items-center gap-2 border-b px-5 py-3.5">
        {Icon ? <Icon className="size-4 text-muted-foreground" /> : null}
        <h2 className="text-sm font-semibold">{title}</h2>
        {action ? <div className="ml-auto">{action}</div> : null}
      </div>
      <div className="px-5 py-4">{children}</div>
    </section>
  );
}
