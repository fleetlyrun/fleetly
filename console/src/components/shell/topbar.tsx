import { useNavigate, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { PlusIcon, RocketIcon, SearchIcon, SparklesIcon, SquareTerminalIcon } from "lucide-react";
import type { ReactNode } from "react";
import { collectionQueryOptions } from "@lynx-go/uikit-core";
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from "@/components/ui/breadcrumb";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { projectsAllQuery, projectsResource } from "@/kit/projects";
import { ThemeToggle } from "./theme-toggle";

// 顶条（AppShell 主区 flex-none 行）：移动菜单触发 + 面包屑 + New + 搜索⌘K。
// 面包屑按 pathname 分段直译，静态段走词条表；项目 ID 段反查目录显
// 项目名（W-1 走查教训：裸 ID 不该出现在面包屑），查无此段时回退截断。
// 项目目录读（批4 kit 资源化）：固定窗伴读不经 URL（useCollection 的
// location 级共享键会被列表页 q/cursor 污染——kit/projects.ts 注）。
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
  components: "Components",
  events: "Events",
  alerts: "Alerts",
  backups: "Backups",
  identity: "Access",
  nodes: "Nodes",
  audit: "Audit",
  settings: "Settings",
  quickstart: "Quickstart",
  templates: "Templates",
  terminal: "Terminal",
};

export function Topbar({ onOpenPalette, mobileNav }: { onOpenPalette: () => void; mobileNav?: ReactNode }) {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  // 60s 轮询对齐旧 catalog useProjects（CATALOG_MS）。
  const projects = useQuery(
    collectionQueryOptions(projectsResource, projectsAllQuery, { refetchInterval: 60_000 }),
  );
  const projectNames = new Map((projects.data?.rows ?? []).map((project) => [project.id, project.name]));
  const crumbs = pathname.split("/").filter(Boolean);

  return (
    <header className="sticky top-0 z-20 flex h-12 flex-none items-center gap-2 border-b border-line bg-bg/95 px-3 backdrop-blur">
      {mobileNav}
      {/* 项目域（/p/…）页面的面包屑由内容区承载（IA v3 原型顶栏无
          crumb——双份是视觉冗余）；平台域页面保留顶条 crumb。 */}
      {pathname.startsWith("/p/") ? null : (
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
                  <BreadcrumbPage className={last ? "" : "text-muted"} title={named === undefined && crumb.length > 20 ? crumb : undefined}>
                    {shown}
                  </BreadcrumbPage>
                </div>
              </BreadcrumbItem>
            );
          })}
        </BreadcrumbList>
      </Breadcrumb>
      )}
      <div className="ml-auto flex items-center gap-1">
        <NewMenu />
        <ThemeToggle />
        <Button variant="ghost" size="sm" className="gap-2 text-muted" onClick={onOpenPalette}>
          <SearchIcon className="size-3.5" />
          <span className="hidden md:inline">Search…</span>
          <kbd className="pointer-events-none rounded border border-line bg-panel-2 px-1.5 font-mono text-2xs text-muted">
            ⌘K
          </kbd>
        </Button>
      </div>
    </header>
  );
}

// + New（IA v3 T1，自 app-sidebar 迁入）：Project / App（Quickstart 向导）/
// From template 三入口承接撤项的 Templates/Quickstart 导航（路由保留，
// ⌘K 同链路）。SPA 导航（原 window.location.assign 整页重载随壳换装收口）。
function NewMenu() {
  const navigate = useNavigate();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" className="gap-1.5">
          <PlusIcon className="size-3.5" />
          <span className="hidden sm:inline">New</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        <DropdownMenuItem onClick={() => void navigate({ to: "/quickstart" })}>
          <RocketIcon />
          New project / app…
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => void navigate({ to: "/templates" })}>
          <SparklesIcon />
          New app from template…
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => void navigate({ to: "/quickstart" })}>
          <SquareTerminalIcon />
          Quickstart guide
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
