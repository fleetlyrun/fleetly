import { Suspense, useState } from "react";
import { Outlet } from "@tanstack/react-router";
import { CommandPalette } from "./command-palette";
import { AppSidebar } from "./app-sidebar";
import { RouteFallback } from "./route-fallback";
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
        {/* min-w-0：SidebarInset（vendored）无此位，flex min-content 下限会把
            main 钉在内容固有宽上——Databases 等宽表页整壳横向溢出（走查 W-2）。 */}
        <SidebarInset className="min-w-0">
          <Topbar onOpenPalette={() => setPaletteOpen(true)} />
          <main className="min-w-0 flex-1">
            {/* 懒加载路由切片的壳级兜底（W-3）：无 fallback 时慢链窗口整块
                全黑——spinner 兜住首访/刷新后的 chunk 拉取窗。 */}
            <Suspense fallback={<RouteFallback />}>
              <Outlet />
            </Suspense>
          </main>
        </SidebarInset>
        <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      </SidebarProvider>
    </TooltipProvider>
  );
}
