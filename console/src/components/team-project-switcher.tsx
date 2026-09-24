// 顶栏团队/项目切换器（v0.3 W2-S5，rbac-teams 设计 §7）：团队切换 + 项目
// 切换（原生 select——jsdom 可测、键盘友好，PatPage 项目绑定同款取舍）。
// 选择持久化与角色能力在 lib/context.tsx；切换器只做选择面。选项文案用
// 限定形 team/prj（D-W0-9 跨团队展示口径——项目 slug 仅队内唯一）。

import { UsersRound } from "lucide-react";

import { useProjectContext } from "@/lib/context";

export function TeamProjectSwitcher() {
  const {
    teams,
    projects,
    selectedTeamSlug,
    selectedProjectSlug,
    setTeamSlug,
    setProjectSlug,
  } = useProjectContext();

  // 项目选项收敛到选中团队（未选团队 = 全部可见项目，选项即限定形全列）。
  const projectOptions = selectedTeamSlug
    ? projects.filter((p) => p.team_slug === selectedTeamSlug)
    : projects;

  if (teams.length === 0) {
    // 无团队投影（Me 未回/异常）——不渲染空选择面。
    return null;
  }

  return (
    <div className="flex items-center gap-1.5">
      <UsersRound aria-hidden className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      {/* 原生 select（非 Radix）——单层简单下拉，可测性取舍同 PatPage。 */}
      <select
        aria-label="Team context"
        data-testid="team-switcher"
        className="h-8 max-w-40 rounded-md border border-input bg-background px-2 text-xs"
        value={selectedTeamSlug ?? ""}
        onChange={(e) => setTeamSlug(e.target.value === "" ? null : e.target.value)}
      >
        <option value="">All teams</option>
        {teams.map((t) => (
          <option key={t.team_id} value={t.team_slug ?? ""}>
            {t.team_name || t.team_slug} ({t.role})
          </option>
        ))}      </select>
      <select
        aria-label="Project context"
        data-testid="project-switcher"
        className="h-8 max-w-44 rounded-md border border-input bg-background px-2 text-xs"
        value={selectedProjectSlug ?? ""}
        onChange={(e) => setProjectSlug(e.target.value === "" ? null : e.target.value)}
        disabled={projectOptions.length === 0}
      >
        <option value="">All projects</option>
        {projectOptions.map((p) => (
          <option key={p.id} value={p.slug ?? ""}>
            {p.team_slug}/{p.slug}
          </option>
        ))}
      </select>
    </div>
  );
}
