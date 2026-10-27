import { createFileRoute } from "@tanstack/react-router";
import { ProjectOverviewPage } from "@/features/projects/project-overview";

// 项目总览路由（薄壳）：页体住 features/projects/project-overview.tsx
// （router 插件改写路由文件，页体在 features 才可单测）。from=Route.id
// 经 props 注入页体（feature 不反查路由——useCollection 的 search 解码锚）。
export const Route = createFileRoute("/_shell/p/$projectId/")({
  component: ProjectOverviewRoute,
});

function ProjectOverviewRoute() {
  const { projectId } = Route.useParams();
  return <ProjectOverviewPage from={Route.id} projectId={projectId} />;
}
