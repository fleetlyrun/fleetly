// vendored from @lynx-go/uikit registry/shell/app-shell.tsx @0.3.1（2026-10-26 批0 底座重拷（demo 视觉原生落地））
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 无（demo 视觉原生落地，text-muted/bg-panel/border-line/… 为本仓 @theme 原生 token）；cn 统一 @/lib/utils。
// 本地扩展（拷贝制副本自持，上游候选）：
// - NavSection.items 增 icon?/disabled?（fleetly IA 图标导航 + 无项目语境置灰）
// - NavSection 增 zone?："platform" 分界渲染 hairline 隔断（fleetly 双域 IA）
// - 增 header?/footer? 槽（ProjectSwitcher/UserMenu 的挂点）+ topbar 槽
// - 移动端：<md 侧栏收进 Sheet 抽屉（MobileNav），桌面恒展开 w-56
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
    // 侧栏/顶条 flex-none 固定，主区独立滚动。
    <div className="flex h-screen overflow-hidden">
      <aside className="hidden w-56 shrink-0 flex-col border-r border-line bg-panel/40 md:flex">
        <SidebarBody matchRoute={matchRoute} nav={nav} brand={brand} header={header} footer={footer} />
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        {topbar}
        <main className="min-w-0 flex-1 overflow-y-auto">{children}</main>
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
      <div className="flex items-center gap-2 px-4 py-3 text-sm font-semibold tracking-wide text-accent">{brand}</div>
      {header ? <div className="px-3 pb-2">{header}</div> : null}
      <nav className="flex-1 space-y-4 overflow-y-auto px-2 pb-4">
        {nav.map((section, index) => (
          <div key={section.label}>
            {section.zone === "platform" && nav[index - 1]?.zone !== "platform" ? (
              <div className="mx-2 my-3 border-t border-line/60 pt-3" />
            ) : null}
            <div className="px-2 py-1 text-[11px] font-medium tracking-wider text-muted uppercase">{section.label}</div>
            <ul>
              {section.items.map((item) => {
                const active = matchRoute({ to: item.to, fuzzy: true });
                const Icon = item.icon;
                return (
                  <li key={item.to}>
                    <Link
                      to={item.to}
                      disabled={item.disabled}
                      className={cn(
                        "flex items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-panel-2",
                        active ? "bg-panel-2 text-fg" : "text-muted",
                        item.disabled && "pointer-events-none opacity-50",
                      )}
                    >
                      {Icon ? <Icon className="size-3.5 shrink-0" /> : null}
                      <span>{item.label}</span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>
      {footer ? <div className="border-t border-line/60 px-3 py-2">{footer}</div> : null}
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
      <SheetContent side="left" className="w-64 gap-0 p-0">
        <SheetHeader className="border-b border-line px-4 py-3">
          <SheetTitle className="text-sm font-semibold tracking-wide text-accent">{brand}</SheetTitle>
        </SheetHeader>
        <div className="flex h-[calc(100%-3.25rem)] flex-col bg-panel/40">
          <SidebarBody matchRoute={matchRoute} nav={nav} brand={null} header={header} footer={footer} />
        </div>
      </SheetContent>
    </Sheet>
  );
}
