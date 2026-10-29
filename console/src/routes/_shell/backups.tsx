import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { BackupsView } from "@/features/fleet/backups-view";
import { databasesSearch, snapshotsSearch } from "@/kit/backups";

// Backups（IA v3 T7）：数据安全聚合页——Databases / Snapshots 双模块
// tab 化（App Detail 下划线式 + URL param，对齐批 5 复裁）。
// F1 回流批（M1+M3）：双列表各持 namespace schema（databases.q 与
// snapshots.q 并存互不携带——URL 键形从共享 q 变前缀键，存量深链失效随批
// 记录）；validateSearch = tab zod + 两个 toSearchSchema().validate 输出
// 组合直挂（M3 passthrough 保 tab 键，零手写合并器）。from=Route.id 注入
// 视图。
const backupsTabSearch = z.object({ tab: z.enum(["databases", "snapshots"]).optional() });

export const Route = createFileRoute("/_shell/backups")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...backupsTabSearch.parse(input),
    ...databasesSearch.toSearchSchema()["~standard"].validate(input).value,
    ...snapshotsSearch.toSearchSchema()["~standard"].validate(input).value,
  }),
  component: BackupsRoute,
});

function BackupsRoute() {
  const tab = Route.useSearch({ select: (search) => search.tab ?? "databases" });
  const navigate = Route.useNavigate();
  return (
    <BackupsView
      tab={tab}
      from={Route.id}
      onTabChange={(value) => void navigate({ search: (prev) => ({ ...prev, tab: value }) })}
    />
  );
}
