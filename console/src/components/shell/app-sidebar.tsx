import { Link, useRouterState } from "@tanstack/react-router";
import { BoxIcon } from "lucide-react";
import { navSections, type NavItem } from "@/lib/nav";
import { useProjectId } from "@/lib/project";
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
} from "@/components/ui/sidebar";
import { ProjectSwitcher } from "./project-switcher";

// AppSidebar（UI v2 外壳）：三域分组（项目域/Platform/Admin）+ 图标折叠。
// 项目域以当前项目语境生成（切换器 → /p/$id 树）；active 判式 = 前缀匹配。
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
        </div>
        <ProjectSwitcher />
      </SidebarHeader>
      <SidebarContent>
        {sections.map((section) => (
          <SidebarGroup key={section.label}>
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
  return item.transitional ? pathname === item.to : pathname === item.to || pathname.startsWith(`${item.to}/`);
}
