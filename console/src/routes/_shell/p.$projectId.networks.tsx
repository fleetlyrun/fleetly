import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { NetworksPanel, PeersPanel } from "@/features/resources/panels";
import { ModulePage } from "@/components/domain/module-page";

// 网络页（UI v2 批 4；模板化收编 ModulePage）：Networks + Peers 双模块
// tab 化（App Detail 下划线式 + URL param——peers 跨项目挂靠面自分拆，
// 对齐批 5 复裁）。
const networksSearch = z.object({ tab: z.enum(["networks", "peers"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/networks")({
  validateSearch: networksSearch,
  component: NetworksPage,
});

function NetworksPage() {
  const { projectId } = Route.useParams();
  const navigate = Route.useNavigate();
  const tab = Route.useSearch({ select: (search) => search.tab ?? "networks" });
  return (
    <ModulePage
      title="Networks"
      description="Project networks and cross-project peers"
      tabs={[
        { value: "networks", label: "Networks" },
        { value: "peers", label: "Peers" },
      ]}
      current={tab}
      onTabChange={(value) => void navigate({ search: { tab: value } })}
    >
      {tab === "networks" ? <NetworksPanel projectId={projectId} /> : <PeersPanel projectId={projectId} />}
    </ModulePage>
  );
}
