import { createFileRoute } from "@tanstack/react-router";
import { ProjectFindingsPage } from "@/features/findings/project-findings";
import { projectFindingsSearch } from "@/kit/diagnosis";

// 「需要你」页（B2a W4，project 域聚合——挂位对照 deployments/tasks 的
// index 子页惯例）：ListProjectFindings 读面按 fault_class 分组渲染；
// platform 组特殊形态（等管理员修，无 fix 钮）；keyset 游标进 URL（kit
// KeysetPagination 翻页）。
export const Route = createFileRoute("/_shell/p/$projectId/findings/")({
  validateSearch: projectFindingsSearch.toSearchSchema(),
  component: FindingsRoute,
});

function FindingsRoute() {
  const { projectId } = Route.useParams();
  return <ProjectFindingsPage projectId={projectId} routeId={Route.id} />;
}
