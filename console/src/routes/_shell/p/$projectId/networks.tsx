import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { NetworksPanel, PeersPanel } from "@/features/resources/panels";
import { ModulePage } from "@/components/domain/module-page";
import { networksSearch, peersSearch } from "@/kit/networks";

// 网络页（UI v2 批 4；模板化收编 ModulePage）：Networks + Peers 双模块
// tab 化（App Detail 下划线式 + URL param——peers 跨项目挂靠面自分拆，
// 对齐批 5 复裁）。
// F1 回流批（M1+M3）：双列表各持 namespace schema（networks.q 与 peers.q
// 并存互不携带——URL 键形从共享 q 变前缀键，存量深链失效随批记录）；
// validateSearch = tab zod + 两个 toSearchSchema().validate 输出组合直挂
//（M3 passthrough 保 tab 键，零手写合并器）。
const networksTabSearch = z.object({ tab: z.enum(["networks", "peers"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/networks")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...networksTabSearch.parse(input),
    ...networksSearch.toSearchSchema()["~standard"].validate(input).value,
    ...peersSearch.toSearchSchema()["~standard"].validate(input).value,
  }),
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
      onTabChange={(value) => void navigate({ search: (prev) => ({ ...prev, tab: value }) })}
    >
      {tab === "networks" ? (
        <NetworksPanel projectId={projectId} routeId={Route.id} />
      ) : (
        <PeersPanel projectId={projectId} routeId={Route.id} />
      )}
    </ModulePage>
  );
}
