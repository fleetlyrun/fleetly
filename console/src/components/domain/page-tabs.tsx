import type { ReactNode } from "react";
import { cn } from "cn";

// TabRail 是页面级模块 tab 的共享分段轨（2026-11-15 Dokploy 视觉）：浅底
// 容器 + 白/亮面板的活动段，活动段的边框/阴影只做轻提示，不再用 accent
// 下边框。轨本身零外边距——间距归调用侧（详情页放 px-6 pt-4 包装，
// ModulePage 保留页头下的 mb-4）。TabRail 同时服务受控按钮（PageTabs）
// 与路由锚点（App Detail 的 TabRailLink），两族视觉必须同源。
export function TabRail({ className, children }: { className?: string; children: ReactNode }) {
  return (
    <div className={cn("flex w-full items-center gap-1 overflow-x-auto rounded-lg bg-panel-2/60 p-1", className)}>
      {children}
    </div>
  );
}

// tabRailItemClass 是段件唯一形态（按钮与锚点共用）：活动段仅靠浅色
// 面板/边框/轻阴影提示，未活动段透明底 hover 提亮。
export function tabRailItemClass(active: boolean, className?: string): string {
  return cn(
    "min-w-fit flex-1 rounded-md border px-3 py-1.5 text-center text-sm font-medium whitespace-nowrap transition-colors",
    active
      ? "border-line bg-panel text-fg shadow-sm"
      : "border-transparent text-muted hover:bg-panel/60 hover:text-fg",
    className,
  );
}

// TabRailLink：路由式锚点段（App Detail 的 tab 是路由而非 search
// param）。锚点保留裸 href 的整页导航语义（现状不动），视觉与 PageTabs
// 的按钮段同源。
export function TabRailLink({
  href,
  active,
  children,
  className,
}: {
  href: string;
  active: boolean;
  children: ReactNode;
  className?: string;
}) {
  return (
    <a href={href} aria-current={active ? "page" : undefined} className={tabRailItemClass(active, className)}>
      {children}
    </a>
  );
}

// PageTabs 是 search-param 驱动的受控段轨（一页多模块用 tab 组织）。泛型
// T 让 onChange 回传收窄后的 tab value 字面量联合，调用侧 navigate({
// search }) 免 cast。URL param 深链由各页自带。
export function PageTabs<T extends string>({
  tabs,
  current,
  onChange,
  className,
}: {
  tabs: Array<{ value: T; label: string }>;
  current: T;
  onChange: (value: T) => void;
  className?: string;
}) {
  return (
    <TabRail className={className}>
      {tabs.map((tab) => {
        const active = tab.value === current;
        return (
          <button
            key={tab.value}
            type="button"
            onClick={() => onChange(tab.value)}
            aria-current={active ? "page" : undefined}
            className={tabRailItemClass(active)}
          >
            {tab.label}
          </button>
        );
      })}
    </TabRail>
  );
}
