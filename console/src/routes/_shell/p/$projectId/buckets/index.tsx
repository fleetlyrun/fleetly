import { createFileRoute } from "@tanstack/react-router";
import { BucketsListPage } from "@/features/buckets/buckets";
import { bucketStoresSearch } from "@/kit/buckets";

// Buckets 一级列表（B5 姐妹页，design §6.2）：目录驱动引擎下拉 + 创建对话框
// + 行动作。快筛/引擎下拉/分页进 URL（bucketStoresSearch）。
export const Route = createFileRoute("/_shell/p/$projectId/buckets/")({
  validateSearch: bucketStoresSearch.toSearchSchema(),
  component: BucketsPageRoute,
});

function BucketsPageRoute() {
  const { projectId } = Route.useParams();
  return <BucketsListPage projectId={projectId} from={Route.id} />;
}
