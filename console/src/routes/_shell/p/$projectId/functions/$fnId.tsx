import { createFileRoute } from "@tanstack/react-router";
import { FunctionDetailPage } from "@/features/functions/function-detail";

// Function 详情路由（薄壳；$taskId 同形）：页体住 features（router 插件
// 改写路由文件，页体在 features 才可单测）。
export const Route = createFileRoute("/_shell/p/$projectId/functions/$fnId")({
  component: FunctionDetailRoute,
});

function FunctionDetailRoute() {
  const { projectId, fnId } = Route.useParams();
  return <FunctionDetailPage projectId={projectId} fnId={fnId} routeId={Route.id} />;
}
