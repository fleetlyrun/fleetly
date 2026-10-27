import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { SchedulesPanel, TasksPanel } from "@/features/tasks/panels";
import { ModulePage } from "@/components/domain/module-page";
import { tasksSearch } from "@/kit/tasks";

// 自动化页（UI v2 批 4；模板化收编 ModulePage）：Tasks + Schedules 双
// tab（URL param）——项目语境来自 URL，手贴 project ID 交互退役。tab
// 形态对齐 App Detail 下划线式（对齐批 5 复裁）。创建入口在各 tab 工具
// 栏行右侧。
// 列表文件原为 p.$projectId.tasks.tsx（无 index）——tasks.$taskId 点嵌套
// 在其下而本页不渲染 Outlet，详情路由 URL 可达但组件永不挂载（2026-10-12
// 走查 W-6 实咬，f6069a8 起既有）。改名 index 对齐 databases 先例：列表
// = index 子路由、详情 = 无布局兄弟路由。
// 2026-10-26 批1 useCollection 化：validateSearch 改合并形态（tab 键走既有
// zod 解析 + collection 查询键 q/filter/sort/page/size 走 tasksSearch——
// toSearchSchema 会丢 tab 键，两段必须合并）；双 tab 双列表共享 tasksSearch
// 同一实例，q/分页跨 tab 持久是接受的语义（tab 切换经函数式 search 展开保
// 留其余键）。from=Route.id 经 props 注入面板（panels 不反查路由，避免
// route ↔ feature 循环引用）。
const tasksTabSearch = z.object({ tab: z.enum(["tasks", "schedules"]).optional() });

export const Route = createFileRoute("/_shell/p/$projectId/tasks/")({
  validateSearch: (input: Record<string, unknown>) => ({
    ...tasksTabSearch.parse(input),
    ...tasksSearch.toSearchSchema()["~standard"].validate(input).value,
  }),
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
      onTabChange={(value) => void navigate({ search: (prev) => ({ ...prev, tab: value }) })}
    >
      {tab === "tasks" ? (
        <TasksPanel projectId={projectId} from={Route.id} />
      ) : (
        <SchedulesPanel projectId={projectId} from={Route.id} />
      )}
    </ModulePage>
  );
}
