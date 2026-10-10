import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { NetworksPanel, PeersPanel } from "@/features/resources/panels";
import { PageHeader } from "@/components/domain/page-header";
import { PageTabs } from "@/components/domain/page-tabs";

// 网络页（UI v2 批 4）：Networks + Peers 双模块 tab 化（App Detail 下划
// 线式 + URL param——peers 跨项目挂靠面自分拆，对齐批 5 复裁）。
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
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader title="Networks" description="Project networks and cross-project peers" />
      <PageTabs
        tabs={[
          { value: "networks", label: "Networks" },
          { value: "peers", label: "Peers" },
        ]}
        current={tab}
        onChange={(value) => void navigate({ search: { tab: value } })}
      />
      {tab === "networks" ? <NetworksPanel projectId={projectId} /> : <PeersPanel projectId={projectId} />}
    </div>
  );
}
