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

// useApps 拉取 App 目录。ListApps 契约 project_id 必填（structure.proto
// 校验；曾按"空 = 全部"发送，吃回 E_INVALID_ARGUMENT 噪声——2026-10-05
// 走查 F4），故空 projectId 直接禁查（React Query enabled 门）：各页在
// 项目未选时呈现各自的引导态，而不是错误面板。
export function useApps(projectId: string) {
  return useQuery({
    queryKey: ["catalog", "apps", projectId],
    enabled: projectId !== "",
    queryFn: async (): Promise<AppEntry[]> => {
      const query = `project_id=${encodeURIComponent(projectId)}&limit=200`;
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
