import { useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDownIcon } from "lucide-react";
import { collectionQueryOptions } from "@lynx-go/uikit-core";
import { projectsAllQuery, projectsResource } from "@/kit/projects";
import { useProjectId } from "@/lib/project";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

// 项目切换器（UI v2 信息架构锚，侧栏顶部）：全局项目语境的唯一切换位。
// 批 1 只承载选中记忆；批 2 选中即跳 /p/$projectId（此后项目语境进 URL）。
export function ProjectSwitcher() {
  const [projectId, setProjectId] = useProjectId();
  const navigate = useNavigate();
  // 项目目录读（批4 kit 资源化）：固定窗伴读不经 URL（useCollection 的
  // location 级共享键会被列表页 q/cursor 污染——kit/projects.ts 注）；
  // 60s 轮询对齐旧 catalog useProjects（CATALOG_MS）。写回逻辑/陈旧记忆
  // 校正原样保留，只换数据源。
  const projects = useQuery(
    collectionQueryOptions(projectsResource, projectsAllQuery, { refetchInterval: 60_000 }),
  );
  const list = projects.data?.rows ?? [];
  // store 空时落首个项目——导航语境（navSections）以 store 为准，切换器
  // 展示与导航置灰必须一致（走查发现：显示 n0reg 但 Apps 全灰）。
  // 陈旧记忆校正（W-1，真机咬点）：localStorage 缓存的 id 会随重建/更名
  // 消亡（ADR-0070 可读 id 重生成后旧 ULID 全域 E_NOT_FOUND）——缓存只是
  // 提示，合法性唯一来源是服务端项目面；不在册即回落首个项目。
  useEffect(() => {
    if (list.length === 0) return;
    if (projectId === "" || !list.some((project) => project.id === projectId)) {
      setProjectId(list[0].id);
    }
  }, [projectId, list, setProjectId]);
  const current = list.find((project) => project.id === projectId) ?? list[0];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        {/* AppShell 侧栏头部槽（demo 形态）：flat 边线按钮，非 shadcn sidebar 基元 */}
        <button
          type="button"
          className="flex w-full items-center gap-2 rounded-md border border-line bg-panel-2/50 px-2 py-1.5 text-sm text-fg hover:bg-panel-2 data-[state=open]:bg-panel-2"
        >
          <ProjectAvatar seed={current?.id} label={current?.name ?? "?"} className="size-5 rounded-md text-2xs" />
          <span className="truncate font-semibold">{current?.name ?? (projects.isPending ? "Loading…" : "No project")}</span>
          <ChevronDownIcon className="ml-auto size-3.5 text-muted" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        <DropdownMenuLabel>Projects</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {list.length === 0 ? (
          <DropdownMenuItem disabled>{projects.isPending ? "Loading…" : "No projects yet"}</DropdownMenuItem>
        ) : (
          list.map((project) => (
            <DropdownMenuItem
              key={project.id}
              onClick={() => {
                setProjectId(project.id);
                // 项目语境进 URL（UI v2 信息架构锚）：切换即跳项目总览。
                void navigate({ to: "/p/$projectId", params: { projectId: project.id } });
              }}
            >
              <ProjectAvatar seed={project.id} label={project.name} className="size-5 rounded-md text-2xs" />
              <span className="truncate">{project.name}</span>
            </DropdownMenuItem>
          ))
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
