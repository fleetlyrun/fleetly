import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { cn } from "cn";

// DetailHero（Dokploy 页面卡形态，2026-11-15 Task 2）：详情页 hero 不再
// 是浮在内容上的圆角卡片，而是内层页面卡的顶部区段——border-b + px-6
// py-5，左侧头像/图标，中部标题 + 徽标 + 可读 id（meta），右侧动作组。
// App/DB/Bucket/PubSub/Task/Node/Deployment 只供数据与槽位，不重写几何：
// App 的 source/firing 走 badges，Deploy/Redeploy/Logs/Terminal/Rollback
// 与 Cancel 等动作原样进 actions。
export function DetailHero({
  icon,
  title,
  badges,
  meta,
  actions,
  children,
  className,
}: {
  /** 左侧头像/图标（ProjectAvatar 或 DetailHeroIcon） */
  icon?: ReactNode;
  title: ReactNode;
  /** 标题行徽标（状态/来源/告警等） */
  badges?: ReactNode;
  /** 标题下的可读 id/元信息行（通常 id + CopyButton） */
  meta?: ReactNode;
  /** 右侧动作组 */
  actions?: ReactNode;
  /** 标题块下方的附加行（可选） */
  children?: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("flex flex-wrap items-center gap-4 border-b border-line px-6 py-5", className)}>
      {icon ? <div className="flex-none">{icon}</div> : null}
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="font-heading text-2xl font-semibold tracking-tight">{title}</h1>
          {badges}
        </div>
        {meta ? (
          <div className="mt-1 flex flex-wrap items-center gap-1 font-mono text-2xs text-muted">{meta}</div>
        ) : null}
        {children}
      </div>
      {actions ? <div className="ml-auto flex max-w-full flex-wrap items-center justify-end gap-2">{actions}</div> : null}
    </section>
  );
}

// DetailHeroIcon：无品牌实体的详情（Node/Deployment）用统一图标砖，
// 尺寸与 ProjectAvatar 的 size-11 对齐。
export function DetailHeroIcon({ icon: Icon }: { icon: LucideIcon }) {
  return (
    <span
      aria-hidden
      className="grid size-11 flex-none place-items-center rounded-xl border border-line bg-panel-2/60 text-muted"
    >
      <Icon className="size-5" />
    </span>
  );
}
