import { createFileRoute } from "@tanstack/react-router";
import { BackupsView } from "@/features/fleet/backups-view";

// Backups（IA v3 T7）：数据安全聚合页（每库一行 + 平台快照）。
export const Route = createFileRoute("/_shell/backups")({
  component: BackupsRoute,
});

function BackupsRoute() {
  return <BackupsView />;
}
