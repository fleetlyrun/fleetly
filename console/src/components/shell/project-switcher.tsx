import { useNavigate } from "@tanstack/react-router";
import { ChevronDownIcon } from "lucide-react";
import { useProjects } from "@/lib/catalog";
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
import { SidebarMenuButton } from "@/components/ui/sidebar";

// 项目切换器（UI v2 信息架构锚，侧栏顶部）：全局项目语境的唯一切换位。
// 批 1 只承载选中记忆；批 2 选中即跳 /p/$projectId（此后项目语境进 URL）。
export function ProjectSwitcher() {
  const [projectId, setProjectId] = useProjectId();
  const navigate = useNavigate();
  const projects = useProjects();
  const list = projects.data ?? [];
  const current = list.find((project) => project.id === projectId) ?? list[0];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <SidebarMenuButton className="border data-[state=open]:bg-sidebar-accent">
          <ProjectAvatar seed={current?.id} label={current?.name ?? "?"} className="size-5 rounded-md text-[10px]" />
          <span className="truncate font-semibold">{current?.name ?? (projects.isPending ? "Loading…" : "No project")}</span>
          <ChevronDownIcon className="ml-auto size-3.5 text-muted-foreground" />
        </SidebarMenuButton>
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
              <ProjectAvatar seed={project.id} label={project.name} className="size-5 rounded-md text-[10px]" />
              <span className="truncate">{project.name}</span>
            </DropdownMenuItem>
          ))
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
