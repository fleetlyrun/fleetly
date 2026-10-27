import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { RegistryView } from "@/features/registry/registry-view";
import { registrySearch } from "@/kit/registry";

// Registry v1（IA v3 T5）：按 app 的当前运行内容——Apps / Image catalog
// 双模块 tab 化（App Detail 下划线式 + URL param，对齐批 5 复裁）。
// useCollection 化批 3：validateSearch 合并形态收编 tab 键 + kit 集合参
// 数（toSearchSchema 不认 tab，必须显式并入——catalog/github tab 不消费
// 集合参数，但 q/分页跨 tab 持久是接受语义）；tab 切换走函数式 search
// 保留集合键。
const registryTabSearch = z.object({ tab: z.enum(["apps", "catalog", "github"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/registry")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...registryTabSearch.parse(input),
    ...registrySearch.toSearchSchema()["~standard"].validate(input).value,
  }),
  component: RegistryRoute,
});

function RegistryRoute() {
  const { projectId } = Route.useParams();
  const tab = Route.useSearch({ select: (search) => search.tab ?? "apps" });
  const navigate = Route.useNavigate();
  return (
    <RegistryView
      projectId={projectId}
      routeId={Route.id}
      tab={tab}
      onTabChange={(value) => void navigate({ search: (prev) => ({ ...prev, tab: value }) })}
    />
  );
}
