import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { RegistryView } from "@/features/registry/registry-view";

// Registry v1（IA v3 T5）：按 app 的当前运行内容——Apps / Image catalog
// 双模块 tab 化（App Detail 下划线式 + URL param，对齐批 5 复裁）。
const registrySearch = z.object({ tab: z.enum(["apps", "catalog"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/registry")({
  validateSearch: registrySearch,
  component: RegistryRoute,
});

function RegistryRoute() {
  const { projectId } = Route.useParams();
  const tab = Route.useSearch({ select: (search) => search.tab ?? "apps" });
  const navigate = Route.useNavigate();
  return <RegistryView projectId={projectId} tab={tab} onTabChange={(value) => void navigate({ search: { tab: value } })} />;
}
