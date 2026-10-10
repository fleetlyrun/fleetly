import { useRouterState } from "@tanstack/react-router";
import { SearchIcon } from "lucide-react";
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from "@/components/ui/breadcrumb";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { ThemeToggle } from "./theme-toggle";
import { UserMenu } from "./user-menu";
import { useProjects } from "@/lib/catalog";

// 顶栏（UI v2 外壳）：折叠触发 + 面包屑 + 搜索/⌘K + 主题 + 身份菜单。
// 面包屑按 pathname 分段直译，静态段走词条表；项目 ID 段反查目录显
// 项目名（W-1 走查教训：裸 ID 不该出现在面包屑），查无此段时回退截断。
const CRUMB_LABELS: Record<string, string> = {
  p: "Projects",
  overview: "Overview",
  apps: "Apps",
  databases: "Databases",
  storage: "Storage",
  registry: "Registry",
  deployments: "Deployments",
  tasks: "Tasks",
  logs: "Logs",
  metrics: "Metrics",
  observability: "Observability",
  resources: "Resources",
  routes: "Routes",
  networks: "Networks",
  variables: "Variables",
  configuration: "Configuration",
  providers: "Components",
  events: "Events",
  alerts: "Alerts",
  backups: "Backups",
  identity: "Identity",
  nodes: "Nodes",
  audit: "Audit",
  settings: "Settings",
  quickstart: "Quickstart",
  templates: "Templates",
  terminal: "Terminal",
};

export function Topbar({ onOpenPalette }: { onOpenPalette: () => void }) {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const projects = useProjects();
  const projectNames = new Map((projects.data ?? []).map((project) => [project.id, project.name]));
  const crumbs = pathname.split("/").filter(Boolean);

  return (
    <header className="sticky top-0 z-20 flex h-13 flex-none items-center gap-2 border-b bg-background/95 px-3 backdrop-blur">
      <SidebarTrigger />
      <Separator orientation="vertical" className="mr-1 !h-4" />
      <Breadcrumb>
        <BreadcrumbList>
          {crumbs.map((crumb, index) => {
            const last = index === crumbs.length - 1;
            const named = CRUMB_LABELS[crumb] ?? projectNames.get(crumb);
            const shown = named ?? (crumb.length > 20 ? `${crumb.slice(0, 10)}…` : crumb);
            return (
              <BreadcrumbItem key={`${crumb}-${index}`}>
                <div className="flex items-center gap-2">
                  {index > 0 ? <BreadcrumbSeparator /> : null}
                  <BreadcrumbPage className={last ? "" : "text-muted-foreground"} title={named === undefined && crumb.length > 20 ? crumb : undefined}>
                    {shown}
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
