import { InfoIcon } from "lucide-react";
import { createFileRoute } from "@tanstack/react-router";
import { GitHubAppsView } from "@/features/githubapps/github-apps-view";
import { PageFrame } from "@/components/domain/page-frame";
import { githubAppsSearch } from "@/kit/githubapps";

// GitHub Apps 平台管理页（ADR-0073 阶段 B）：Global 行的 Fleet 段面——
// 平台材料（无 Team 轴），写/读/列仓要求平台管理 token；Project 级行在
// 各项目 Registry 第三 tab，解析时遮蔽 Global。文件名必须是 .index——
// 无后缀的 github-apps.tsx 会被 TanStack 当作 connect 的父布局路由且本页
// 不渲染 Outlet，/github-apps/connect 只会渲染本页（真机咬出：回调卡片
// 整页消失）。
// 2026-10-26 批1 Wave2-A useCollection 化：validateSearch 落
// githubAppsSearch（q/分页进 URL；集合键与 Registry 路由的 registrySearch
// 同键名不同 text 域，tab 语境互不干扰），from=Route.id 注入视图。
export const Route = createFileRoute("/_shell/github-apps/")({
  validateSearch: githubAppsSearch.toSearchSchema(),
  component: GitHubAppsPage,
});

function GitHubAppsPage() {
  return (
    <PageFrame
      title="GitHub Apps"
      description="Platform-wide installations — private-repo access material for every project (ADR-0073)"
    >
      <GitHubAppsView from={Route.id} />
      <p className="mt-3 flex items-start gap-1.5 rounded-lg border bg-panel-2/40 px-3 py-2 text-2xs text-muted">
        <InfoIcon className="mt-px size-3.5 flex-none" />
        Global rows serve every project at clone time; a project-scoped row for the same owner shadows the global one for that
        project only. These rows are platform-admin material — visible and writable only with a platform-admin token.
      </p>
    </PageFrame>
  );
}
