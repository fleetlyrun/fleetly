import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { NetworksPanel, PeersPanel } from "@/features/resources/panels";
import { ModulePage } from "@/components/domain/module-page";
import { networksSearch } from "@/kit/networks";

// 网络页（UI v2 批 4；模板化收编 ModulePage）：Networks + Peers 双模块
// tab 化（App Detail 下划线式 + URL param——peers 跨项目挂靠面自分拆，
// 对齐批 5 复裁）。
// useCollection 化批 3：validateSearch 合并形态收编 tab 键 + kit 集合参
// 数（toSearchSchema 不认 tab，必须显式并入）；tab 切换走函数式 search
// 保 q/分页跨 tab 持久（接受语义）。
const networksTabSearch = z.object({ tab: z.enum(["networks", "peers"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/networks")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...networksTabSearch.parse(input),
    ...networksSearch.toSearchSchema()["~standard"].validate(input).value,
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
