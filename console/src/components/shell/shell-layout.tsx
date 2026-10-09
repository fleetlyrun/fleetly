import { useState } from "react";
import { Outlet } from "@tanstack/react-router";
import { CommandPalette } from "./command-palette";
import { AppSidebar } from "./app-sidebar";
import { Topbar } from "./topbar";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { TooltipProvider } from "@/components/ui/tooltip";

// 壳布局（UI v2 外壳解剖）：Sidebar（分组导航+切换器）+ Topbar（面包屑/
// 搜索/主题/身份）+ 内容区。⌘K 面板的开合态在壳层持有，顶栏触发。
// TooltipProvider 在壳层挂（radix-nova 的 SidebarProvider 不内含，折叠
// tooltip 由应用根供给——ui/** 上游原样不自改）。
export function ShellLayout() {
  const [paletteOpen, setPaletteOpen] = useState(false);
  return (
    <TooltipProvider delayDuration={0}>
      <SidebarProvider>
        <AppSidebar />
        <SidebarInset>
          <Topbar onOpenPalette={() => setPaletteOpen(true)} />
          <main className="min-w-0 flex-1">
            <Outlet />
          </main>
        </SidebarInset>
        <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      </SidebarProvider>
    </TooltipProvider>
  );
}
