import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { UploadsPanel, VolumesPanel } from "@/features/resources/panels";
import { UsageIndex } from "@/features/spec/usage-index";
import { PageHeader } from "@/components/domain/page-header";
import { PageTabs } from "@/components/domain/page-tabs";

// Storage 页（IA v3 T4，§5.4）：Volumes + Uploads 双模块 tab 化（App
// Detail 下划线式 + URL param——修复此前 defaultValue 非受控深链无效）。
// 挂载反查与 upload→build 反查依赖 app spec 通路（§8 二期 proto），一期
// 沿用资源面板族（批 6 统一 reskin 时换 DataTable）。
const storageSearch = z.object({ tab: z.enum(["volumes", "uploads"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/storage")({
  validateSearch: storageSearch,
  component: StoragePage,
});

function StoragePage() {
  const { projectId } = Route.useParams();
  const navigate = Route.useNavigate();
  const tab = Route.useSearch({ select: (search) => search.tab ?? "volumes" });
  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader title="Storage" description="Volumes and deployable source uploads" />
      <UsageIndex projectId={projectId} />
      <PageTabs
        className="mt-5"
        tabs={[
          { value: "volumes", label: "Volumes" },
          { value: "uploads", label: "Uploads" },
        ]}
        current={tab}
        onChange={(value) => void navigate({ search: { tab: value } })}
      />
      {tab === "volumes" ? <VolumesPanel projectId={projectId} /> : <UploadsPanel projectId={projectId} />}
    </div>
  );
}
