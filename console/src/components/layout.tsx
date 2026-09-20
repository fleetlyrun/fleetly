// Console 壳：侧边导航 + 内容区。导航 = v0.1 功能面（应用 / 系统 / 事件）。

import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { Boxes, LogOut, Radio, Server } from "lucide-react";

import { useAuth } from "@/auth";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { queryClient } from "@/query";

const NAV_ITEMS = [
  { to: "/apps", label: "Applications", icon: Boxes },
  { to: "/system", label: "System", icon: Server },
  { to: "/events", label: "Events", icon: Radio },
];

export function Layout() {
  const { logout } = useAuth();
  const navigate = useNavigate();

  return (
    <div className="flex min-h-screen">
      <aside className="flex w-56 shrink-0 flex-col border-r bg-muted/40">
        <div className="flex items-center gap-2 px-4 py-4">
          <span className="flex h-7 w-7 items-center justify-center rounded-md bg-primary font-bold text-primary-foreground">
            f
          </span>
          <span className="text-sm font-semibold tracking-wide">
            fleetly console
          </span>
        </div>
        <nav className="flex-1 space-y-1 px-2" aria-label="Main">
          {NAV_ITEMS.map(({ to, label, icon: Icon }) => (
            <NavLink
              key={to}
              to={to}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium text-muted-foreground hover:bg-accent hover:text-foreground",
                  isActive && "bg-accent text-foreground",
                )
              }
            >
              <Icon aria-hidden className="h-4 w-4" />
              {label}
            </NavLink>
          ))}
        </nav>
        <div className="border-t p-2">
          <Button
            variant="ghost"
            size="sm"
            className="w-full justify-start text-muted-foreground"
            onClick={() => {
              // 登出同时清 react-query 缓存（M9-9）：上一操作员的 apps/
              // deployments 等服务端状态不得泄给下一个会话（token 换人后
              // 直接复用旧缓存会闪现他人数据）。
              queryClient.clear();
              logout();
              navigate("/login");
            }}
          >
            <LogOut aria-hidden className="h-4 w-4" />
            Sign out
          </Button>
        </div>
      </aside>
      <main className="min-w-0 flex-1 p-6">
        <Outlet />
      </main>
    </div>
  );
}
