import { Link, useRouterState } from "@tanstack/react-router";
import { BoxIcon } from "lucide-react";
import { NAV_SECTIONS, type NavItem } from "@/lib/nav";
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
// active 判式 = 前缀匹配（详情页保持高亮）；过渡期多个 item 指向同一占位
// 路由时同亮，随批 2-5 消亡。
export function AppSidebar() {
  const pathname = useRouterState({ select: (state) => state.location.pathname });

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
        {NAV_SECTIONS.map((section) => (
          <SidebarGroup key={section.label}>
            <SidebarGroupLabel>{section.label}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {section.items.map((item) => (
                  <SidebarMenuItem key={`${item.label}-${item.to}`}>
                    <SidebarMenuButton asChild isActive={isActive(item, pathname)} tooltip={item.label}>
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
          <span>v2 shell · transitional</span>
        </div>
      </SidebarFooter>
    </Sidebar>
  );
}

function isActive(item: NavItem, pathname: string): boolean {
  return item.transitional ? pathname === item.to : pathname === item.to || pathname.startsWith(`${item.to}/`);
}
