import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "../api/client";
import type { components } from "../api/structure";

// 项目/App 目录查询（三页共用的选择器数据源）：轮询 60s——目录变化慢，
// 部署页的 5s 轮询只作用于部署列表本身。

type StructureSchemas = components["schemas"];

export interface ProjectEntry {
  id: string;
  name: string;
}

export interface AppEntry {
  id: string;
  project_id: string;
  name: string;
}

interface ProjectsResponse {
  projects?: Array<StructureSchemas["v1Project"] | undefined>;
}

interface AppsResponse {
  apps?: Array<StructureSchemas["v1App"] | undefined>;
}

// useProjects 拉取项目目录（100 条上限的首页足够最小面）。
export function useProjects() {
  return useQuery({
    queryKey: ["catalog", "projects"],
    queryFn: async (): Promise<ProjectEntry[]> => {
      const res = await apiFetch<ProjectsResponse>("/v1/projects?limit=100");
      return (res.projects ?? []).flatMap((project) =>
        project?.id && project.name ? [{ id: project.id, name: project.name }] : [],
      );
    },
    refetchInterval: 60_000,
  });
}

// useApps 拉取 App 目录；projectId 空 = 全部（日志页的跨项目选择面）。
export function useApps(projectId: string) {
  return useQuery({
    queryKey: ["catalog", "apps", projectId],
    queryFn: async (): Promise<AppEntry[]> => {
      const query = projectId === "" ? "limit=200" : `project_id=${encodeURIComponent(projectId)}&limit=200`;
      const res = await apiFetch<AppsResponse>(`/v1/apps?${query}`);
      return (res.apps ?? []).flatMap((app) => (app?.id && app.name ? [{ id: app.id, project_id: app.project_id ?? "", name: app.name }] : []));
    },
    refetchInterval: 60_000,
  });
}

// appNameOf 从目录里解析 App 显示名（缺失回退短 id）。
export function appNameOf(apps: AppEntry[] | undefined, appId: string | undefined): string {
  if (!appId) return "—";
  const hit = apps?.find((app) => app.id === appId);
  return hit ? hit.name : `${appId.slice(0, 10)}…`;
}
