import { createFileRoute } from "@tanstack/react-router";
import { NetworksPanel } from "@/features/resources/panels";
import { PageHeader } from "@/components/domain/page-header";

// 网络页（UI v2 批 4）：项目语境 Networks 面板（含 peers 审批面）。
export const Route = createFileRoute("/_shell/p/$projectId/networks")({
  component: NetworksPage,
});

function NetworksPage() {
  const { projectId } = Route.useParams();
  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader title="Networks" description="Project networks and cross-project peers" />
      <NetworksPanel projectId={projectId} />
    </div>
  );
}
