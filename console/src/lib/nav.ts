import {
  Activity,
  Bell,
  Box,
  Clock,
  Database,
  Globe,
  KeyRound,
  Layers,
  LayoutGrid,
  Rocket,
  ScrollText,
  Settings,
  ShieldCheck,
  Sparkles,
  SquareTerminal,
  Users,
  Zap,
  type LucideIcon,
} from "lucide-react";

// 导航单源（UI v2）：侧边栏与 ⌘K 面板共用的分组配置。项目域以当前项目
// 语境生成（URL 即上下文）；批 1 遗留的过渡路由（tasks/logs/metrics/
// resources）随批 3-4 落地实页后逐项切换。
export interface NavItem {
  label: string;
  to: string;
  icon: LucideIcon;
  /** 过渡期指向旧聚合页的占位项（批 3-4 消亡） */
  transitional?: boolean;
  /** 无项目语境时置灰（项目域专属入口） */
  needsProject?: boolean;
}

export interface NavSection {
  label: string;
  items: NavItem[];
}

export function navSections(projectId: string | undefined): NavSection[] {
  const pid = projectId ?? "";
  const projectBase = pid !== "" ? `/p/${encodeURIComponent(pid)}` : "";
  return [
    {
      label: "Project",
      items: [
        { label: "Overview", to: "/overview", icon: LayoutGrid },
        { label: "Apps", to: projectBase === "" ? "/overview" : `${projectBase}/apps`, icon: Box, needsProject: pid === "" },
        { label: "Deployments", to: projectBase === "" ? "/overview" : `${projectBase}/deployments`, icon: Layers, needsProject: pid === "" },
        { label: "Tasks", to: "/tasks", icon: Clock, transitional: true },
        { label: "Logs", to: "/logs", icon: ScrollText, transitional: true },
        { label: "Metrics", to: "/observability", icon: Activity, transitional: true },
        { label: "Networks", to: "/resources", icon: Globe, transitional: true },
        { label: "Data", to: "/resources", icon: Database, transitional: true },
        { label: "Configuration", to: "/resources", icon: KeyRound, transitional: true },
      ],
    },
    {
      label: "Platform",
      items: [
        { label: "Nodes", to: "/nodes", icon: Box },
        { label: "Events", to: "/events", icon: Zap },
        { label: "Alerts", to: "/observability", icon: Bell, transitional: true },
        { label: "Audit", to: "/audit", icon: ShieldCheck },
      ],
    },
    {
      label: "Admin",
      items: [
        { label: "Identity", to: "/identity", icon: Users },
        { label: "Settings", to: "/settings", icon: Settings },
        { label: "Templates", to: "/templates", icon: Sparkles },
        { label: "Quickstart", to: "/quickstart", icon: Rocket },
        { label: "Terminal", to: "/terminal", icon: SquareTerminal },
      ],
    },
  ];
}

// 批 1 兼容出口（app-sidebar/command-palette 之外不再引用；批 6 清理）。
export const NAV_SECTIONS = navSections(undefined);
