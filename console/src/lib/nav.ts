import {
  Activity,
  Bell,
  Box,
  Boxes,
  Clock,
  Database,
  DatabaseBackup,
  Fingerprint,
  Globe,
  HardDrive,
  KeyRound,
  Layers,
  LayoutGrid,
  Package,
  Route as RouteIcon,
  ScrollText,
  Settings,
  ShieldCheck,
  TriangleAlert,
  Users,
  Zap,
  type LucideIcon,
} from "lucide-react";

// 导航单源（IA v3，docs/design/2026-10-09-console-ia-v3.md §3）：项目域
// 用途分组（Build / Data & Storage / Network / Monitor；Configuration 组经
// ADR-0059 同批重组并入 Data & Storage 的 Variables 项）
// + 平台域（Fleet / Admin），zone 标记承载两域分界渲染。撤下
// Templates/Quickstart/Terminal 导航项（路由保留：前两者入 + New 与 ⌘K，
// Terminal 由 App 详情 tab 承接）；Data 拆为 Databases + Storage。
export interface NavItem {
  label: string;
  to: string;
  icon: LucideIcon;
  /** 无项目语境时置灰（项目域专属入口） */
  needsProject?: boolean;
}

export interface NavSection {
  label: string;
  items: NavItem[];
  /** 平台域分界：Fleet 起为跨项目舰队面（渲染为隔断） */
  zone?: "project" | "platform";
}

// navSections 生成导航单源。platformAdmin 是平台域入口的显隐依据（fail-
// closed：whoami 未到/失败传 false 即隐藏）——Nodes 读面是平台管理员门
//（R08-M1/S16-c7：节点无 Team 轴，成员看节点页只会撞 403）。
export function navSections(projectId: string | undefined, platformAdmin = false): NavSection[] {
  const pid = projectId ?? "";
  const projectBase = pid !== "" ? `/p/${encodeURIComponent(pid)}` : "";
  const projectItem = (label: string, to: string, icon: LucideIcon): NavItem => ({
    label,
    to: projectBase === "" ? "/overview" : to,
    icon,
    needsProject: pid === "",
  });
  return [
    {
      label: "Project",
      zone: "project",
      items: [{ label: "Overview", to: "/overview", icon: LayoutGrid }],
    },
    {
      label: "Build",
      zone: "project",
      items: [
        projectItem("Apps", `${projectBase}/apps`, Box),
        projectItem("Deployments", `${projectBase}/deployments`, Layers),
        projectItem("Tasks", `${projectBase}/tasks`, Clock),
        projectItem("Registry", `${projectBase}/registry`, Package),
      ],
    },
    {
      label: "Data & Storage",
      zone: "project",
      items: [
        projectItem("Databases", `${projectBase}/databases`, Database),
        projectItem("Storage", `${projectBase}/storage`, HardDrive),
        // Variables（原 Configuration 组单项，ADR-0059 同批重组；ADR-0060
        // 路由对齐页名）：项目级秘钥/配置/共享变量聚合页——与 Shared
        // Variables / Add variable 成族。
        projectItem("Variables", `${projectBase}/variables`, KeyRound),
      ],
    },
    {
      label: "Network",
      zone: "project",
      items: [
        projectItem("Routes", `${projectBase}/routes`, RouteIcon),
        projectItem("Networks", `${projectBase}/networks`, Globe),
      ],
    },
    {
      label: "Monitor",
      zone: "project",
      items: [
        // Needs you（B2a W4「需要你」页）：project 域诊断聚合——fault_class
        // 分组 + platform 组「等管理员修」特殊形态。
        projectItem("Needs you", `${projectBase}/findings`, TriangleAlert),
        { label: "Logs", to: "/logs", icon: ScrollText },
        { label: "Metrics", to: "/metrics", icon: Activity },
      ],
    },
    {
      label: "Fleet",
      zone: "platform",
      items: [
        // Nodes 仅平台管理员可见（服务端门 R08-M1）：节点清单与在跑容器
        // 是跨租户舰队面，member/team admin 无可见性。
        ...(platformAdmin ? [{ label: "Nodes", to: "/nodes", icon: Box }] : []),
        { label: "Components", to: "/components", icon: Boxes }, // ADR-0059 定名 + ADR-0060 路由对齐页名
        { label: "GitHub Apps", to: "/github-apps", icon: Fingerprint }, // ADR-0073：平台级接入材料（Global 行）
        { label: "Events", to: "/events", icon: Zap },
        { label: "Alerts", to: "/alerts", icon: Bell },
        { label: "Backups", to: "/backups", icon: DatabaseBackup },
      ],
    },
    {
      // Admin（该词在 CONTEXT.md Avoid 表——Team/Project 混淆禁词；v2 同名组沿用）。
      label: "Admin",
      zone: "platform",
      items: [
        // Access（原 Identity，ADR-0060 复裁）：Users/Teams/Roles/Invitations
        // 是"谁能访问什么"的一面——Access 覆盖全；member/account 是禁词。
        { label: "Access", to: "/identity", icon: Users },
        { label: "Audit", to: "/audit", icon: ShieldCheck },
        { label: "Settings", to: "/settings", icon: Settings },
      ],
    },
  ];
}
