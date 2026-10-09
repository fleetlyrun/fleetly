import { createFileRoute } from "@tanstack/react-router";
import { TaskDetailPage } from "@/features/tasks/task-detail";

// Task 详情（IA v3 T3b，3 tabs）：Overview/Runs/Settings——工作负载三兄弟
// 详情范式对齐；run 日志按 T8 结论留二期。
export const Route = createFileRoute("/_shell/p/$projectId/tasks/$taskId")({
  component: TaskDetailRoute,
});

function TaskDetailRoute() {
  const { projectId, taskId } = Route.useParams();
  return <TaskDetailPage projectId={projectId} taskId={taskId} />;
}
