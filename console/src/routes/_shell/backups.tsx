import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { BackupsView } from "@/features/fleet/backups-view";
import { backupsSearch } from "@/kit/backups";

// Backups（IA v3 T7）：数据安全聚合页——Databases / Snapshots 双模块
// tab 化（App Detail 下划线式 + URL param，对齐批 5 复裁）。
// 2026-10-26 批1 Wave2-A useCollection 化：validateSearch 改合并形态
//（tab 键走既有 zod 解析 + collection 查询键 q/filter/sort/page/size 走
// backupsSearch——toSearchSchema 会丢 tab 键，两段必须合并）；双 tab 双列
// 表共享 backupsSearch 同一实例（q/分页跨 tab 持久是接受语义）；tab 切换
// 走函数式 search 保留集合键；from=Route.id 注入视图。
const backupsTabSearch = z.object({ tab: z.enum(["databases", "snapshots"]).optional() });

export const Route = createFileRoute("/_shell/backups")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...backupsTabSearch.parse(input),
    ...backupsSearch.toSearchSchema()["~standard"].validate(input).value,
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
