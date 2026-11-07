import { createFileRoute } from "@tanstack/react-router";
import { BucketStoreDetailPage } from "@/features/buckets/buckets";

// Bucket store 详情（B5 姐妹页，4 tabs）：Overview/Metrics/Logs/Settings——
// **无 Backups tab**（silo 转储挂账，目录 capabilities.backup=false 如实）、
// 无 Browse（轻量 S3 web 工具挂账独立评估）。
export const Route = createFileRoute("/_shell/p/$projectId/buckets/$bucketId")({
  component: BucketStoreDetailRoute,
});

function BucketStoreDetailRoute() {
  const { projectId, bucketId } = Route.useParams();
  // routeId 线程：读面 useCollection 的 from 锚（Database/PubSub Detail 同款）。
  return <BucketStoreDetailPage projectId={projectId} bucketId={bucketId} routeId={Route.id} />;
}
