import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { BackupsView } from "@/features/fleet/backups-view";

// Backups（IA v3 T7）：数据安全聚合页——Databases / Snapshots 双模块
// tab 化（App Detail 下划线式 + URL param，对齐批 5 复裁）。
const backupsSearch = z.object({ tab: z.enum(["databases", "snapshots"]).optional() });

export const Route = createFileRoute("/_shell/backups")({
  validateSearch: backupsSearch,
  component: BackupsRoute,
});

function BackupsRoute() {
  const tab = Route.useSearch({ select: (search) => search.tab ?? "databases" });
  const navigate = Route.useNavigate();
  return <BackupsView tab={tab} onTabChange={(value) => void navigate({ search: { tab: value } })} />;
}
