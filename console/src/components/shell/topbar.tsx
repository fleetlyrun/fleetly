import { useRouterState } from "@tanstack/react-router";
import { SearchIcon } from "lucide-react";
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from "@/components/ui/breadcrumb";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { ThemeToggle } from "./theme-toggle";
import { UserMenu } from "./user-menu";

// 顶栏（UI v2 外壳）：折叠触发 + 面包屑 + 搜索/⌘K + 主题 + 身份菜单。
// 批 1 面包屑按过渡路由表直译；批 2 起由路由 matches 生成项目语境链。
const CRUMB_LABELS: Record<string, string> = {
  overview: "Overview",
  apps: "Apps",
  deployments: "Deployments",
  tasks: "Tasks",
  logs: "Logs",
  observability: "Observability",
  resources: "Resources",
  identity: "Identity",
  nodes: "Nodes",
  events: "Events",
  audit: "Audit",
  settings: "Settings",
  quickstart: "Quickstart",
  templates: "Templates",
  terminal: "Terminal",
};

export function Topbar({ onOpenPalette }: { onOpenPalette: () => void }) {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const crumbs = pathname.split("/").filter(Boolean);

  return (
    <header className="sticky top-0 z-20 flex h-13 flex-none items-center gap-2 border-b bg-background/95 px-3 backdrop-blur">
      <SidebarTrigger />
      <Separator orientation="vertical" className="mr-1 !h-4" />
      <Breadcrumb>
        <BreadcrumbList>
          {crumbs.map((crumb, index) => {
            const last = index === crumbs.length - 1;
            return (
              <BreadcrumbItem key={`${crumb}-${index}`}>
                <div className="flex items-center gap-2">
                  {index > 0 ? <BreadcrumbSeparator /> : null}
                  <BreadcrumbPage className={last ? "" : "text-muted-foreground"} title={crumb.length > 20 ? crumb : undefined}>
                    {CRUMB_LABELS[crumb] ?? (crumb.length > 20 ? `${crumb.slice(0, 10)}…` : crumb)}
                  </BreadcrumbPage>
                </div>
              </BreadcrumbItem>
            );
          })}
        </BreadcrumbList>
      </Breadcrumb>
      <div className="ml-auto flex items-center gap-1">
        <Button variant="ghost" size="sm" className="gap-2 text-muted-foreground" onClick={onOpenPalette}>
          <SearchIcon className="size-3.5" />
          <span className="hidden md:inline">Search…</span>
          <kbd className="pointer-events-none rounded border bg-muted px-1.5 font-mono text-[10px] text-muted-foreground">
            ⌘K
          </kbd>
        </Button>
        <ThemeToggle />
        <UserMenu />
      </div>
    </header>
  );
}
