import { Suspense, useState, type ReactNode } from "react";
import { Outlet } from "@tanstack/react-router";
import { BoxIcon } from "lucide-react";
import { AppShell, MobileNav, type NavSection } from "@/components/kit/shell/app-shell";
import { useRecord } from "@lynx-go/uikit-core";
import { sessionResource } from "@/kit/settings";
import { navSections } from "@/lib/nav";
import { useProjectId } from "@/lib/project";
import { CommandPalette } from "./command-palette";
import { ProjectSwitcher } from "./project-switcher";
import { RouteFallback } from "./route-fallback";
import { Topbar } from "./topbar";
import { UserMenu } from "./user-menu";

// 壳布局（uikit AppShell 采纳，demo 视觉）：flat 侧栏（brand/ProjectSwitcher/
// navSections/身份底部）+ 主区（面包屑顶条 + 滚动内容）。⌘K 面板开合态在壳层
// 持有，顶栏触发。navSections 的 needsProject 在此适配为块的 disabled。
export function ShellLayout() {
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [projectId] = useProjectId();
  // 平台域入口显隐依据（fail-closed：whoami 未到/失败即隐藏）——Nodes 读面
  // 自 S16-c7 起是平台管理员门；user-menu/command-palette 持有同一 kit
  // record 键（["record","session","self"]），零额外请求。
  const whoami = useRecord(sessionResource, { id: "self", query: { staleTime: 30_000 } });
  const sections: NavSection[] = navSections(projectId || undefined, whoami.data?.platformAdmin === true).map(
    (section) => ({
      ...section,
      items: section.items.map(({ label, to, icon, needsProject }) => ({
        label,
        to,
        icon,
        disabled: needsProject,
      })),
    }),
  );

  const brand: ReactNode = (
    <>
      {/* 品牌 logo（2026-10-12 换官方 F 叶形标；原图白底已转透明，public/logo.png 128px） */}
      <img src="/logo.png" alt="" aria-hidden className="size-6 flex-none rounded-md" />
      <span>fleetly</span>
    </>
  );
  const header: ReactNode = <ProjectSwitcher />;
  const footer: ReactNode = (
    <div className="flex flex-col gap-1 py-1">
      <UserMenu />
      <div className="flex items-center gap-1.5 px-2 py-1 text-2xs text-muted">
        <BoxIcon className="size-3" />
        <span>console v2</span>
      </div>
    </div>
  );

  return (
    <AppShell
      brand={brand}
      nav={sections}
      header={header}
      footer={footer}
      topbar={
        <Topbar onOpenPalette={() => setPaletteOpen(true)} mobileNav={<MobileNav nav={sections} brand={brand} header={header} footer={footer} />} />
      }
    >
      {/* 懒加载路由切片的壳级兜底（W-3）：无 fallback 时慢链窗口整块
          全黑——spinner 兜住首访/刷新后的 chunk 拉取窗。 */}
      <Suspense fallback={<RouteFallback />}>
        <Outlet />
      </Suspense>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </AppShell>
  );
}
