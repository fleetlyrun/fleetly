import { createFileRoute } from "@tanstack/react-router";
import { RoutesPanel } from "@/features/resources/panels";
import { PageFrame } from "@/components/domain/page-frame";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core/react";
import { scoped } from "@lynx-go/uikit-core/kernel";
import { appsResource, appsDirectorySearch } from "@/kit/apps";
import { routesSearch } from "@/kit/routes";

// 路由页（UI v2 批 4；模板化收编 PageFrame）：host → app/process/port
//（managed proxy 面）。创建入口在工具栏行右侧（对齐批 5 用户复裁：页头
// 钮位退役）。
// useCollection 化批 3：单列表路由直挂 kit 搜索 schema（无 tab 键，无需
// 合并形态）——快筛/分页/排序首次进 URL（旧页是本地态，纯增量）。
export const Route = createFileRoute("/_shell/p/$projectId/routes")({
  validateSearch: routesSearch.toSearchSchema(),
  component: RoutesPage,
});

function RoutesPage() {
  const { projectId } = Route.useParams();
  // 批4 集成收口：App 目录伴读迁 kit（collectionQueryOptions + scoped——
  // 表单/名字反查不进 URL）。
  const appsQuery = useQuery({
    ...collectionQueryOptions(scoped(appsResource, { projectId }), appsDirectorySearch.defaults, {
      refetchInterval: 60_000,
    }),
    enabled: projectId !== "",
  });
  const apps = appsQuery.data?.rows ?? [];
  return (
    <PageFrame title="Routes" description="Traffic enters through routes on the managed proxy">
      <RoutesPanel projectId={projectId} routeId={Route.id} apps={[...apps]} />
    </PageFrame>
  );
}
