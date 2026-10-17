import { createFileRoute } from "@tanstack/react-router";
import { ProjectOverviewPage } from "@/features/projects/project-overview";

// 项目总览路由（薄壳）：页体住 features/projects/project-overview.tsx
// （router 插件改写路由文件，页体在 features 才可单测）。
export const Route = createFileRoute("/_shell/p/$projectId/")({
  component: ProjectOverviewRoute,
});

function ProjectOverviewRoute() {
  const { projectId } = Route.useParams();
  return <ProjectOverviewPage projectId={projectId} />;
}
