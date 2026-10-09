import { useNavigate, createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { SchedulesPanel, TasksPanel } from "@/features/tasks/panels";
import { PageHeader } from "@/components/domain/page-header";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

// 自动化页（UI v2 批 4）：Tasks + Schedules 双 tab（URL param）——项目
// 语境来自 URL，手贴 project ID 交互退役。
const tasksSearch = z.object({ tab: z.enum(["tasks", "schedules"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/tasks")({
  validateSearch: tasksSearch,
  component: TasksPage,
});

function TasksPage() {
  const { projectId } = Route.useParams();
  const navigate = useNavigate();
  const tab = Route.useSearch({ select: (search) => search.tab ?? "tasks" });
  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader title="Tasks" description="Programmatic workloads — one-shot and resident pools, cron schedules" />
      <Tabs value={tab} onValueChange={(value) => void navigate({ search: { tab: value as "tasks" | "schedules" } } as never)} className="mb-4">
        <TabsList className="bg-transparent">
          <TabsTrigger value="tasks">Tasks</TabsTrigger>
          <TabsTrigger value="schedules">Schedules</TabsTrigger>
        </TabsList>
      </Tabs>
      {tab === "tasks" ? <TasksPanel projectId={projectId} /> : <SchedulesPanel projectId={projectId} />}
    </div>
  );
}
