import { createFileRoute } from "@tanstack/react-router";
import { FunctionsPanel } from "@/features/functions/panels";
import { PageFrame } from "@/components/domain/page-frame";
import { functionsSearch } from "@/kit/functions";

// Functions 列表路由（ADR-0102 任务⑥；databases/tasks index 先例）：
// 快筛/分页/过滤进 URL（kit schema）；创建入口在工具栏右侧。
export const Route = createFileRoute("/_shell/p/$projectId/functions/")({
  validateSearch: functionsSearch.toSearchSchema(),
  component: FunctionsPage,
});

function FunctionsPage() {
  const { projectId } = Route.useParams();
  return (
    <PageFrame
      title="Functions"
      description="Request-driven functions — single code entry + URL, scaled by the platform"
    >
      <FunctionsPanel projectId={projectId} from={Route.id} />
    </PageFrame>
  );
}
