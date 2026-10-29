import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { RegistryView } from "@/features/registry/registry-view";
import { registrySearch } from "@/kit/registry";

// Registry v1（IA v3 T5）：按 app 的当前运行内容——Apps / Image catalog
// 双模块 tab 化（App Detail 下划线式 + URL param，对齐批 5 复裁）。
// F1 回流批（M3）：单列表 + tab 键的双 schema 并存直挂——tab zod 与
// registrySearch.toSearchSchema().validate 输出组合，passthrough 保 tab 键
// 存活（0.3.1 时代「toSearchSchema 会丢 tab 键」的有损 normalize 根因已
// 修，此形态从救场补丁转正为常驻形态；catalog/github tab 不消费集合参数，
// q/分页跨 tab 持久保持）；tab 切换走函数式 search 保留集合键。
// 单集合路由不切 namespace（URL 键形零变化）。
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
