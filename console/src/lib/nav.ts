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

// 导航单源（UI v2）：侧边栏与 ⌘K 面板共用的分组配置。批 1 为过渡期
// 平铺 URL（旧页面挂新壳），批 2-5 逐项切换到 /p/$projectId 语境树——
// 只改此表不改壳。
export interface NavItem {
  label: string;
  to: string;
  icon: LucideIcon;
  /** 批 1 过渡期指向旧聚合页的占位项（批 2-5 消亡） */
  transitional?: boolean;
}

export interface NavSection {
  label: string;
  items: NavItem[];
}

export const NAV_SECTIONS: NavSection[] = [
  {
    label: "acme-platform", // 批 2 起由项目上下文动态替换
    items: [
      { label: "Overview", to: "/overview", icon: LayoutGrid },
      { label: "Apps", to: "/apps", icon: Box },
      { label: "Deployments", to: "/deployments", icon: Layers },
      { label: "Tasks", to: "/tasks", icon: Clock },
      { label: "Logs", to: "/logs", icon: ScrollText },
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
