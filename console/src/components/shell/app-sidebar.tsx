import { Link, useRouterState } from "@tanstack/react-router";
import { BoxIcon, PlusIcon, SparklesIcon, RocketIcon, SquareTerminalIcon } from "lucide-react";
import { navSections, type NavItem } from "@/lib/nav";
import { useProjectId } from "@/lib/project";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarSeparator,
} from "@/components/ui/sidebar";
import { ProjectSwitcher } from "./project-switcher";

// AppSidebar（IA v3 T1 外壳）：项目域用途分组（Build / Data & Storage /
// Network / Monitor / Configuration）+ PLATFORM 分界 + 平台域（Fleet /
// Admin）；+ New 承接 Quickstart/Templates 入口（导航撤项、路由
// 保留）。项目域以当前项目语境生成；active 判式 = 前缀匹配。
export function AppSidebar() {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const [projectId] = useProjectId();
  const sections = navSections(projectId || undefined);

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <div className="flex items-center gap-2 px-2 py-1.5">
          <span
            aria-hidden
            className="size-7 flex-none rounded-lg shadow-[0_0_12px_color-mix(in_oklch,var(--primary)_35%,transparent)]"
            style={{ backgroundImage: "var(--brand-gradient)" }}
          />
          <span className="font-heading text-[15px] font-bold tracking-tight group-data-[collapsible=icon]:hidden">
            fleetly
          </span>
          <NewMenu />
        </div>
        <ProjectSwitcher />
      </SidebarHeader>
      <SidebarContent>
        {sections.map((section, index) => (
          <div key={section.label} className="contents">
            {section.zone === "platform" && sections[index - 1]?.zone !== "platform" ? (
              <div className="group-data-[collapsible=icon]:hidden">
                <SidebarSeparator className="mx-2" />
                <p className="px-4 pt-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-muted-foreground/70">
                  Platform
                </p>
              </div>
            ) : null}
            <SidebarGroup>
              <SidebarGroupLabel>{section.label}</SidebarGroupLabel>
              <SidebarGroupContent>
                <SidebarMenu>
                  {section.items.map((item) => (
                    <SidebarMenuItem key={`${item.label}-${item.to}`}>
                      <SidebarMenuButton asChild isActive={isActive(item, pathname)} tooltip={item.label} disabled={item.needsProject}>
                        <Link to={item.to}>
                          <item.icon />
                          <span>{item.label}</span>
                        </Link>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  ))}
                </SidebarMenu>
              </SidebarGroupContent>
            </SidebarGroup>
          </div>
        ))}
      </SidebarContent>
      <SidebarFooter>
        <div className="flex items-center gap-1.5 px-2 py-1 text-[11px] text-muted-foreground group-data-[collapsible=icon]:hidden">
          <BoxIcon className="size-3" />
          <span>console v2</span>
        </div>
      </SidebarFooter>
    </Sidebar>
  );
}

function isActive(item: NavItem, pathname: string): boolean {
  if (item.to === "/overview") return pathname === "/overview" || pathname.startsWith("/p/");
  return pathname === item.to || pathname.startsWith(`${item.to}/`);
}

// + New（IA v3 T1）：Project / App（Quickstart 向导）/ From template 三入口
// 承接撤项的 Templates/Quickstart 导航（路由保留，⌘K 同链路）。
function NewMenu() {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label="Create new…"
          className="ml-auto grid size-6 place-items-center rounded-md border text-muted-foreground hover:bg-muted hover:text-foreground group-data-[collapsible=icon]:hidden"
        >
          <PlusIcon className="size-3.5" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        <DropdownMenuItem onClick={() => window.location.assign("/quickstart")}>
          <RocketIcon />
          New project / app…
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => window.location.assign("/templates")}>
          <SparklesIcon />
          New app from template…
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => window.location.assign("/quickstart")}>
          <SquareTerminalIcon />
          Quickstart guide
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
