import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { UploadsPanel, VolumesPanel } from "@/features/resources/panels";
import { ModulePage } from "@/components/domain/module-page";
import { uploadsSearch, volumesSearch } from "@/kit/storage";

// Storage 页（IA v3 T4，§5.4；模板化收编 ModulePage）：Volumes + Uploads
// 双模块 tab 化（App Detail 下划线式 + URL param——修复此前 defaultValue
// 非受控深链无效）。挂载/引用反查原为独立 UsageIndex 块（lead 槽）——
// 用户裁决收编进两个列表的 Mounted by / Referenced by 列（信息与行内
// 动词同源，独立块无增量），诚实边界注记随列表卡脚注保留。
// F1 回流批（M1+M3）：双列表各持 namespace schema（volumes.q 与
// uploads.q 并存互不携带——URL 键形从共享 q 变前缀键，存量深链失效随批
// 记录）；validateSearch = tab zod + 两个 toSearchSchema().validate 输出
// 组合直挂（M3 passthrough 保 tab 键，零手写合并器）。
const storageTabSearch = z.object({ tab: z.enum(["volumes", "uploads"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/storage")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...storageTabSearch.parse(input),
    ...volumesSearch.toSearchSchema()["~standard"].validate(input).value,
    ...uploadsSearch.toSearchSchema()["~standard"].validate(input).value,
  }),
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
      tabs={[
        { value: "volumes", label: "Volumes" },
        { value: "uploads", label: "Uploads" },
      ]}
      current={tab}
      onTabChange={(value) => void navigate({ search: (prev: Record<string, unknown>) => ({ ...prev, tab: value }) })}
    >
      {tab === "volumes" ? (
        <VolumesPanel projectId={projectId} routeId={Route.id} />
      ) : (
        <UploadsPanel projectId={projectId} routeId={Route.id} />
      )}
    </ModulePage>
  );
}
