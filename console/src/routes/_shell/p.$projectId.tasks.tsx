import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { SchedulesPanel, TasksPanel } from "@/features/tasks/panels";
import { ModulePage } from "@/components/domain/module-page";

// 自动化页（UI v2 批 4；模板化收编 ModulePage）：Tasks + Schedules 双
// tab（URL param）——项目语境来自 URL，手贴 project ID 交互退役。tab
// 形态对齐 App Detail 下划线式（对齐批 5 复裁）。创建入口在各 tab 工具
// 栏行右侧。
const tasksSearch = z.object({ tab: z.enum(["tasks", "schedules"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/tasks")({
  validateSearch: tasksSearch,
  component: TasksPage,
});

function TasksPage() {
  const { projectId } = Route.useParams();
  const navigate = Route.useNavigate();
  const tab = Route.useSearch({ select: (search) => search.tab ?? "tasks" });
  return (
    <ModulePage
      title="Tasks"
      description="Programmatic workloads — one-shot and resident pools, cron schedules"
      tabs={[
        { value: "tasks", label: "Tasks" },
        { value: "schedules", label: "Schedules" },
      ]}
      current={tab}
      onTabChange={(value) => void navigate({ search: { tab: value } })}
    >
      {tab === "tasks" ? <TasksPanel projectId={projectId} /> : <SchedulesPanel projectId={projectId} />}
    </ModulePage>
  );
}
