import { createFileRoute } from "@tanstack/react-router";
import { PubSubsListPage } from "@/features/pubsubs/pubsubs";
import { pubsubsSearch } from "@/kit/pubsubs";

// PubSubs 一级列表（B4c 姐妹页，design §6.2）：目录驱动引擎下拉 + 创建对话
// 框 + 行动作。快筛/引擎下拉/分页进 URL（pubsubsSearch）。
export const Route = createFileRoute("/_shell/p/$projectId/pubsubs/")({
  validateSearch: pubsubsSearch.toSearchSchema(),
  component: PubSubsPageRoute,
});

function PubSubsPageRoute() {
  const { projectId } = Route.useParams();
  return <PubSubsListPage projectId={projectId} from={Route.id} />;
}
