// vendored from @lynxland/uikit registry/shell/app-shell.tsx @0.3.1（2026-10-26 批0 底座重拷（demo 视觉原生落地））
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 无（demo 视觉原生落地，text-muted/bg-panel/border-line/… 为本仓 @theme 原生 token）；cn 统一 @/lib/utils。
// 本地扩展（拷贝制副本自持，上游候选）：
// - NavSection.items 增 icon?/disabled?（fleetly IA 图标导航 + 无项目语境置灰）
// - NavSection 增 zone?："platform" 分界渲染 hairline 隔断（fleetly 双域 IA）
// - 增 header?/footer? 槽（ProjectSwitcher/UserMenu 的挂点）+ topbar 槽
// - 移动端：<md 侧栏收进 Sheet 抽屉（MobileNav），桌面恒展开浮动 16rem 轨
// - 2026-11-15 Dokploy 视觉：浮动圆角侧栏面板（8px 外距 / 240px 面板）+
//   64px 无边框顶条 + 主内容 px-4 pb-4；面板色走 --sidebar* token。
import { Link, useMatchRoute } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { MenuIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";

/**
 * AppShell（shell 块，本地扩展版）—— 应用骨架：Sidebar（navSections 单源）
 * + 主区。navSections 是数据 prop（加页面 = 加一条路径引用），不是框架注册。
 */
export interface NavItem {
  label: string;
  to: string;
  icon?: React.ComponentType<{ className?: string }>;
  /** 无项目语境时置灰（项目域专属入口） */
  disabled?: boolean;
}

export interface NavSection {
  label: string;
  items: ReadonlyArray<NavItem>;
  /** 平台域分界：Fleet 起为跨项目舰队面（渲染为隔断） */
  zone?: "project" | "platform";
}

export interface AppShellProps {
  brand?: ReactNode;
  nav: ReadonlyArray<NavSection>;
  /** 侧栏头部槽（品牌行之下、导航之上：ProjectSwitcher） */
  header?: ReactNode;
  /** 侧栏底部槽（身份/版本行） */
  footer?: ReactNode;
  /** 主区顶条槽（flex-none：面包屑/搜索/动作；内部自带 min-h） */
  topbar?: ReactNode;
  children: ReactNode;
}

export function AppShell({ brand, nav, header, footer, topbar, children }: AppShellProps) {
  const matchRoute = useMatchRoute();
  return (
    // h-screen：壳自带高度链（本仓无 demo 的 html/body/#root height:100%
    // 规则——h-full 会塌成内容高，侧栏背景/边线只画到内容尾，走查实咬）。
    // 侧栏/顶条 flex-none 固定，主区独立滚动。w-64 轨 + p-2 = 240px 浮动面板。
    <div className="flex h-screen overflow-hidden bg-bg">
      <aside className="hidden w-64 shrink-0 p-2 md:flex">
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border border-sidebar-border bg-sidebar text-fg">
          <SidebarBody matchRoute={matchRoute} nav={nav} brand={brand} header={header} footer={footer} />
        </div>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        {topbar}
        <main className="min-w-0 flex-1 overflow-y-auto px-4 pb-4">{children}</main>
      </div>
    </div>
  );
}

/** 侧栏内容体（桌面 aside 与移动 Sheet 抽屉共用）：品牌/头部/导航/底部。 */
function SidebarBody({
  matchRoute,
  nav,
  brand,
  header,
  footer,
}: {
  matchRoute: ReturnType<typeof useMatchRoute>;
  nav: ReadonlyArray<NavSection>;
  brand?: ReactNode;
  header?: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <>
      <div className="flex shrink-0 items-center gap-2 px-3 pt-3 pb-2 text-sm font-semibold text-fg">{brand}</div>
      {header ? <div className="shrink-0 px-2 pb-3">{header}</div> : null}
      <nav className="flex-1 space-y-5 overflow-y-auto px-2 pb-3">
        {nav.map((section, index) => (
          <div key={`${index}:${section.label}`}>
            {section.zone === "platform" && nav[index - 1]?.zone !== "platform" ? (
              <div className="mx-2 my-2 border-t border-sidebar-border/70 pt-2" />
            ) : null}
            <div className="px-2 py-1 text-2xs font-medium text-muted">{section.label}</div>
            <ul>
              {section.items.map((item, itemIndex) => {
                const active = matchRoute({ to: item.to, fuzzy: true });
                const Icon = item.icon;
                return (
                  <li key={`${itemIndex}:${item.to}:${item.label}`}>
                    <Link
                      to={item.to}
                      disabled={item.disabled}
                      className={cn(
                        "flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm transition-colors",
                        active
                          ? "bg-sidebar-accent font-medium text-fg"
                          : "text-muted hover:bg-panel-2/70 hover:text-fg",
                        item.disabled && "pointer-events-none opacity-50",
                      )}
                    >
                      {Icon ? <Icon className="size-4 shrink-0" /> : null}
                      <span>{item.label}</span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>
      {footer ? <div className="shrink-0 px-2 py-2">{footer}</div> : null}
    </>
  );
}

/** 移动抽屉壳（<md）：Sheet 承载同一 SidebarBody，导航态属外壳层。 */
export function MobileNav({
  nav,
  brand,
  header,
  footer,
}: Pick<AppShellProps, "nav" | "brand" | "header" | "footer">) {
  const [open, setOpen] = useState(false);
  const matchRoute = useMatchRoute();
  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Open navigation" className="md:hidden">
          <MenuIcon className="size-4" />
        </Button>
      </SheetTrigger>
      <SheetContent side="left" className="w-[17rem] gap-0 border-sidebar-border bg-bg p-2">
        <SheetHeader className="sr-only">
          <SheetTitle>{brand}</SheetTitle>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border border-sidebar-border bg-sidebar text-fg">
          <SidebarBody matchRoute={matchRoute} nav={nav} brand={brand} header={header} footer={footer} />
        </div>
      </SheetContent>
    </Sheet>
  );
}
