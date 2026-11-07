import { createFileRoute } from "@tanstack/react-router";
import { PubSubDetailPage } from "@/features/pubsubs/pubsubs";

// PubSub 详情（B4c 姐妹页，5 tabs）：Overview/Metrics/Logs/Backups/Settings
//（无 Browse——nats 无轻量 web 工具，ADR-0096 决策 4 挂账）。
export const Route = createFileRoute("/_shell/p/$projectId/pubsubs/$pubsubId")({
  component: PubSubDetailRoute,
});

function PubSubDetailRoute() {
  const { projectId, pubsubId } = Route.useParams();
  // routeId 线程：读面 useCollection 的 from 锚（Database Detail 同款）。
  return <PubSubDetailPage projectId={projectId} pubsubId={pubsubId} routeId={Route.id} />;
}
