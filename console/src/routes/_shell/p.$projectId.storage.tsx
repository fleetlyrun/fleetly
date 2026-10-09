import { createFileRoute } from "@tanstack/react-router";
import { UploadsPanel, VolumesPanel } from "@/features/resources/panels";
import { PageHeader } from "@/components/domain/page-header";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

// Storage 页（IA v3 T4，§5.4）：Volumes + Uploads 自 /data 拆出独立成页。
// 挂载反查与 upload→build 反查依赖 app spec 通路（§8 二期 proto），一期
// 沿用资源面板族（批 6 统一 reskin 时换 DataTable）。
export const Route = createFileRoute("/_shell/p/$projectId/storage")({
  component: StoragePage,
});

function StoragePage() {
  const { projectId } = Route.useParams();
  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader title="Storage" description="Volumes and deployable source uploads" />
      <Tabs defaultValue="volumes">
        <TabsList className="mb-4">
          <TabsTrigger value="volumes">Volumes</TabsTrigger>
          <TabsTrigger value="uploads">Uploads</TabsTrigger>
        </TabsList>
        <TabsContent value="volumes">
          <VolumesPanel projectId={projectId} />
        </TabsContent>
        <TabsContent value="uploads">
          <UploadsPanel projectId={projectId} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
