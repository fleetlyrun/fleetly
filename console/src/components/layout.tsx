// Console 壳（dokploy 式）：可折叠侧边栏（分组导航 + 版本页脚）
// + 顶栏（折叠钮 / 团队·项目切换器 / 面包屑 / 时钟 / 主题 / 用户菜单）+
// 内容区。导航 = v0.1 功能面（应用 / 事件 / 系统）+ v0.3 团队面（Teams）
// + 平台管理员面（Admin，仅 is_platform_admin 可见——W2-S5，rbac-teams
// 设计 §7）。折叠偏好持久化 localStorage；小屏首帧默认折叠。身份与退出
// 收拢在顶栏用户菜单。

import { useQuery } from "@tanstack/react-query";
import {
  Boxes,
  Database,
  House,
  PanelLeft,
  Radio,
  Server,
  UsersRound,
} from "lucide-react";
import { useEffect, useState } from "react";
import { NavLink, Outlet } from "react-router-dom";

import { getSystemStatus } from "@/api/endpoints";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { ThemeToggle } from "@/components/theme-toggle";
import { TimeBadge } from "@/components/time-badge";
import { TeamProjectSwitcher } from "@/components/team-project-switcher";
import { UserMenu } from "@/components/user-menu";
import { Button } from "@/components/ui/button";
import { useIsPlatformAdmin } from "@/lib/context";
import { cn } from "@/lib/utils";

const COLLAPSE_KEY = "fleetly.console.sidebar-collapsed";

const NAV_MAIN = [{ to: "/", label: "Home", icon: House, end: true }];
const NAV_PLATFORM = [
  { to: "/apps", label: "Applications", icon: Boxes, end: false },
  { to: "/databases", label: "Databases", icon: Database, end: false },
  { to: "/events", label: "Events", icon: Radio, end: false },
  { to: "/system", label: "System", icon: Server, end: false },
];
const NAV_TEAMS = [{ to: "/teams", label: "Teams", icon: UsersRound, end: false }];

function useSidebarCollapsed() {
  // 无持久化偏好时按视口宽度定初值（窄屏折叠；宽屏展开）。一次性求值，
  // 不进 effect（setState-in-effect 纪律）。
  const [collapsed, setCollapsed] = useState(() => {
    try {
      const stored = localStorage.getItem(COLLAPSE_KEY);
      if (stored !== null) return stored === "1";
    } catch {
      // 存储不可用——回落视口判定。
    }
    return typeof window !== "undefined" && window.innerWidth < 768;
  });

  useEffect(() => {
    try {
      localStorage.setItem(COLLAPSE_KEY, collapsed ? "1" : "0");
    } catch {
      // 忽略。
    }
  }, [collapsed]);

  return [collapsed, () => setCollapsed((v) => !v)] as const;
}

function NavItem({
  to,
  label,
  icon: Icon,
  end,
  collapsed,
}: {
  to: string;
  label: string;
  icon: typeof Boxes;
  end?: boolean;
  collapsed: boolean;
}) {
  return (
    <NavLink
      to={to}
      end={end}
      title={collapsed ? label : undefined}
      className={({ isActive }) =>
        cn(
          "flex items-center gap-2.5 rounded-md px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground",
          isActive && "bg-accent text-foreground",
          collapsed && "justify-center px-0",
        )
      }
    >
      <Icon aria-hidden className="h-4 w-4 shrink-0" />
      {!collapsed && label}
    </NavLink>
  );
}

export function Layout() {
  const [collapsed, toggleCollapsed] = useSidebarCollapsed();
  const isPlatformAdmin = useIsPlatformAdmin();

  // 版本号：侧边栏页脚（与 System 页共享查询缓存；静默失败即隐藏）。
  const { data: status } = useQuery({
    queryKey: ["system", "status"],
    queryFn: getSystemStatus,
    staleTime: 5 * 60 * 1000,
    refetchOnWindowFocus: false,
  });
  const version = status?.version ? `v${status.version}` : "";

  return (
    <div className="flex min-h-screen">
      <aside
        className={cn(
          "sticky top-0 flex h-screen shrink-0 flex-col border-r bg-muted/30 transition-[width] duration-200",
          collapsed ? "w-14" : "w-60",
        )}
      >
        <div
          className={cn(
            "flex h-14 shrink-0 items-center border-b",
            collapsed ? "justify-center" : "gap-2.5 px-4",
          )}
        >
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary text-sm font-bold text-primary-foreground">
            f
          </span>
          {!collapsed ? (
            <span className="min-w-0 leading-tight">
              <span className="block truncate text-sm font-semibold">fleetly</span>
              <span className="block text-[10px] uppercase tracking-widest text-muted-foreground">
                console
              </span>
            </span>
          ) : null}
        </div>

        <nav className="flex-1 space-y-1 overflow-y-auto px-2 py-3" aria-label="Main">
          {NAV_MAIN.map((item) => (
            <NavItem key={item.to} {...item} collapsed={collapsed} />
          ))}
          {NAV_TEAMS.map((item) => (
            <NavItem key={item.to} {...item} collapsed={collapsed} />
          ))}
          {!collapsed ? (
            <div className="px-3 pb-1 pt-4 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/70">
              Platform
            </div>
          ) : (
            <div className="pb-1 pt-4" />
          )}
          {NAV_PLATFORM.map((item) => (
            <NavItem key={item.to} {...item} collapsed={collapsed} />
          ))}
          {isPlatformAdmin ? (
            <NavItem
              to="/admin/users"
              label="Admin"
              icon={Server}
              end={false}
              collapsed={collapsed}
            />
          ) : null}
        </nav>

        <div className="shrink-0 border-t p-2">
          {!collapsed && version ? (
            <div className="px-2 py-1 text-[10px] text-muted-foreground">
              Version {version}
            </div>
          ) : null}
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-20 flex h-14 shrink-0 items-center gap-2 border-b bg-background/95 px-4 backdrop-blur">
          <Button
            variant="ghost"
            size="icon"
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            onClick={toggleCollapsed}
          >
            <PanelLeft aria-hidden className="h-4 w-4" />
          </Button>
          <TeamProjectSwitcher />
          <Breadcrumbs />
          <div className="ml-auto flex items-center gap-1.5">
            <TimeBadge />
            <ThemeToggle />
            <UserMenu />
          </div>
        </header>
        <main className="mx-auto w-full max-w-[1440px] flex-1 p-4 md:p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
