import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { UploadsPanel, VolumesPanel } from "@/features/resources/panels";
import { UsageIndex } from "@/features/spec/usage-index";
import { ModulePage } from "@/components/domain/module-page";

// Storage 页（IA v3 T4，§5.4；模板化收编 ModulePage）：Volumes + Uploads
// 双模块 tab 化（App Detail 下划线式 + URL param——修复此前 defaultValue
// 非受控深链无效）。挂载反查与 upload→build 反查依赖 app spec 通路；
// UsageIndex 走 lead 槽（tab 条之上的页级前置物）。
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
    <ModulePage
      title="Storage"
      description="Volumes and deployable source uploads"
      lead={<UsageIndex projectId={projectId} />}
      tabs={[
        { value: "volumes", label: "Volumes" },
        { value: "uploads", label: "Uploads" },
      ]}
      current={tab}
      onTabChange={(value) => void navigate({ search: { tab: value } })}
    >
      {tab === "volumes" ? <VolumesPanel projectId={projectId} /> : <UploadsPanel projectId={projectId} />}
    </ModulePage>
  );
}
