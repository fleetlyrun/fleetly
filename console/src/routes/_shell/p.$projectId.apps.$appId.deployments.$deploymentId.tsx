import { createFileRoute } from "@tanstack/react-router";
import { DeploymentDetailView } from "@/features/deployments/deployment-detail";

// 部署详情（UI v2 叙事旗舰页）：wait 流 + 断流重连 + 轮询兜底。
export const Route = createFileRoute(
  "/_shell/p/$projectId/apps/$appId/deployments/$deploymentId",
)({
  component: RouteWrapper,
});

function RouteWrapper() {
  const { projectId, appId, deploymentId } = Route.useParams();
  return <DeploymentDetailView projectId={projectId} appId={appId} deploymentId={deploymentId} />;
}
