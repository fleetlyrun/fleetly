import { createFileRoute } from "@tanstack/react-router";
import { useRouter } from "@tanstack/react-router";
import { DeploymentDetailPage } from "@/pages/DeploymentDetail";

// 过渡路由（UI v2 批 1）：部署详情旧页挂新壳。批 2 起该页在
// /p/$projectId/apps/$appId/deployments/$deploymentId 语境下叙事化重构。
export const Route = createFileRoute("/_shell/deployments/$deploymentId")({
  component: DeploymentDetailBridge,
});

function DeploymentDetailBridge() {
  const { deploymentId } = Route.useParams();
  const router = useRouter();
  return (
    <DeploymentDetailPage
      id={deploymentId}
      navigate={(path) => {
        // 旧页内相对导航（"deployments" | "deployments/<id>"）翻译为干净路径
        router.history.push(path === "deployments" ? "/deployments" : `/deployments/${path.split("/")[1] ?? ""}`);
      }}
    />
  );
}
