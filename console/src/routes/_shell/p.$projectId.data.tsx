import { createFileRoute } from "@tanstack/react-router";
import { DatabasesPanel, UploadsPanel, VolumesPanel } from "@/features/resources/panels";
import { PageHeader } from "@/components/domain/page-header";

// 数据页（UI v2 批 4）：databases（备份/verify/restore/browse）+ volumes
// + uploads 三区——旧巨石 Resources 的 Data 域拆出。
export const Route = createFileRoute("/_shell/p/$projectId/data")({
  component: DataPage,
});

function DataPage() {
  const { projectId } = Route.useParams();
  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader title="Data" description="Databases, volumes and deployable source uploads" />
      <section className="flex flex-col gap-8">
        <DatabasesPanel projectId={projectId} />
        <VolumesPanel projectId={projectId} />
        <UploadsPanel projectId={projectId} />
      </section>
    </div>
  );
}
