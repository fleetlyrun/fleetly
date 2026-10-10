import { createFileRoute } from "@tanstack/react-router";
import { NetworksPanel, NewNetworkButton } from "@/features/resources/panels";
import { PageHeader } from "@/components/domain/page-header";

// 网络页（UI v2 批 4）：项目语境 Networks 面板（含 peers 审批面）。
// 创建入口收口 PageHeader 右上主钮（对齐批 4，Apps 基准同款）。
export const Route = createFileRoute("/_shell/p/$projectId/networks")({
  component: NetworksPage,
});

function NetworksPage() {
  const { projectId } = Route.useParams();
  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        title="Networks"
        description="Project networks and cross-project peers"
        actions={<NewNetworkButton projectId={projectId} />}
      />
      <NetworksPanel projectId={projectId} />
    </div>
  );
}
