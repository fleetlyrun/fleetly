import { createFileRoute } from "@tanstack/react-router";
import { RoutesPanel } from "@/features/resources/panels";
import { PageFrame } from "@/components/domain/page-frame";
import { useApps } from "@/lib/catalog";

// 路由页（UI v2 批 4；模板化收编 PageFrame）：host → app/process/port
//（managed proxy 面）。创建入口在工具栏行右侧（对齐批 5 用户复裁：页头
// 钮位退役）。
export const Route = createFileRoute("/_shell/p/$projectId/routes")({
  component: RoutesPage,
});

function RoutesPage() {
  const { projectId } = Route.useParams();
  const apps = useApps(projectId);
  return (
    <PageFrame title="Routes" description="Traffic enters through routes on the managed proxy">
      <RoutesPanel projectId={projectId} apps={apps.data ?? []} />
    </PageFrame>
  );
}
