import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as ghSchemas } from "@/api/github_apps";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// GitHub Apps 域资源适配（2026-10-26 批1 Wave2-A useCollection 化）：双语
// 境单资源——ctx.projectId 在场 = Project 级行（Registry 第三 tab），缺席
// = 平台级 Global 面（Fleet 段管理页）。scope 是可选轴，资源不声明 scope
//（声明即未就绪停用，Global 面会被关死）；调用方按语境显式传 scope
//（projectId 或不传），queryKey 由 scope 段自然分面。REST 端点与收敛口
// 径照抄 features/githubapps 旧 inline 查询（rowsOf 同款 undefined 行过
// 滤）。无轮询——与旧 inline 查询同现状（写后失效即时刷新）。

export type GitHubAppRow = ghSchemas["schemas"]["v1GitHubApp"];

export async function fetchGitHubApps(ctx: ResourceScope, signal?: AbortSignal): Promise<GitHubAppRow[]> {
  const projectId = ctx.projectId;
  const params = projectId != null && projectId !== "" ? { project_id: projectId } : { global: "true" };
  try {
    return await fetchRows<GitHubAppRow>(
      `/v1/github-apps${qs(params)}`,
      (res) => (res as { apps?: Array<GitHubAppRow | undefined> }).apps,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（states 块错误分诊契约）
    throw toApiError(err);
  }
}

export const githubAppsResource = defineResource({
  name: "github-apps",
  getId: (row: GitHubAppRow) => row.id ?? "",
  collection: clientCollection(fetchGitHubApps),
});

// URL 搜索 schema：text 快筛 account_login/id（与旧页 fieldsOf 同字段集）。
export const githubAppsSearch = collectionQuerySchema({
  text: ["account_login", "id"],
  defaultSize: 20,
});
