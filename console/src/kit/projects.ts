import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import type { components as structureSchemas } from "@/api/structure";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// 项目目录资源（批4 读面收尾 R1）：壳层面包屑反查 / ProjectSwitcher 的项目
// 名录。GET /v1/projects?limit=100 与收敛形（id/name 双全才收）照抄
// lib/catalog.ts 的 useProjects；平台域——无 scope。
//
// 消费形态注：伴读面走 useCollectionData（M1 data 层，零 URL）——壳层组件
// 挂在 _shell 布局、随任意子路由渲染，URL 层绑定会误收列表页写进的共享键
//（q/cursor/page 污染项目目录窗口，R1 实咬）。列表化消费（R2 项目页）再落
// projectsSearch。

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

// 目录伴读消费面（2026-10-29 F1 回流批）：useProjectsDirectory/useProjectName
// 伴读钩子退役——消费点（切换器/顶栏/面包屑/⌘K/表单下拉）直接
// useCollectionData(projectsResource, { window: { size: 100 }, refetchInterval: 60_000 })
//（size 100 与 fetch 上限同值＝整表窗；同窗即同缓存键）。
