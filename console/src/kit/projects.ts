import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type CollectionQuery, type ResourceScope, type Segment } from "@lynx-go/uikit-core/kernel";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core/react";
import type { components as structureSchemas } from "@/api/structure";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// 项目目录资源（批4 读面收尾 R1）：壳层面包屑反查 / ProjectSwitcher 的项目
// 名录。GET /v1/projects?limit=100 与收敛形（id/name 双全才收）照抄
// lib/catalog.ts 的 useProjects；平台域——无 scope。
//
// 消费形态注：伴读面走 collectionQueryOptions + 固定窗（projectsAllQuery），
// 不走 useCollection——壳层组件挂在 _shell 布局、随任意子路由渲染，而
// useCollection 经 useSearch(strict:false) 解码的是整个 location 的共享键
//（q/cursor/page），列表页（identity/variables 等）写进的查询参数会误收
// 进项目目录窗口。伴读不经 URL 是行为等价换装（旧 catalog 钩子亦无 URL
// 绑定），列表化消费（R2 项目页）再落 projectsSearch。

export interface ProjectEntry {
  id: string;
  name: string;
}

type ProjectRow = structureSchemas["schemas"]["v1Project"];

export async function fetchProjects(_ctx: ResourceScope, signal?: AbortSignal): Promise<ProjectEntry[]> {
  try {
    const rows = await fetchRows<ProjectRow>(
      `/v1/projects${qs({ limit: 100 })}`,
      (res) => (res as { projects?: Array<ProjectRow | undefined> }).projects,
      signal,
    );
    // 收敛形与 catalog useProjects 同款：id/name 双全才收（裸行不带名的
    // 宽松生成形不进目录）。
    return rows.flatMap((project) => (project.id && project.name ? [{ id: project.id, name: project.name }] : []));
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（错误态分诊契约）
    throw toApiError(err);
  }
}

export const projectsResource = defineResource({
  name: "projects",
  getId: (row: ProjectEntry) => row.id,
  collection: clientCollection(fetchProjects),
});

// 伴读固定窗：整表渲染（面包屑反查/切换器/⌘K 面板都要全名录），size 100
// 与 fetch 上限同值——共享此常量即共享 queryKey（collectionKey 嵌窗形）。
export const projectsAllQuery: CollectionQuery = { window: { size: 100 } };

// useProjectsDirectory 是目录伴读钩子（表单/名字反查消费；60s 与旧
// catalog useProjects 同拍）——落 kit 层让消费面免于裸 useQuery。
export function useProjectsDirectory(options?: { refetchInterval?: number }) {
  return useQuery<Segment<ProjectEntry>>({
    ...collectionQueryOptions(projectsResource, projectsAllQuery, {
      refetchInterval: options?.refetchInterval ?? 60_000,
    }),
  });
}

// useProjectName 是项目名反查伴读（面包屑唯一文案源——裸 ID 不进 crumb，
// W-1 走查口径）；目录未达返回 undefined，调用方回退通用词。
export function useProjectName(projectId: string): string | undefined {
  const projects = useProjectsDirectory();
  return projects.data?.rows.find((project) => project.id === projectId)?.name;
}
