import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type QuerySelector, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as ghSchemas } from "@/api/github_apps";
import { apiSend } from "@/api/client";
import { runCommandMapped, toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// GitHub Apps 域资源适配（2026-10-26 批1 Wave2-A useCollection 化 + 批2
// 写面 useCommand 化）：双语境单资源——ctx.projectId 在场 = Project 级行
//（Registry 第三 tab），缺席 = 平台级 Global 面（Fleet 段管理页）。scope 是可选轴，资源不声明 scope
//（声明即未就绪停用，Global 面会被关死）；调用方按语境显式传 scope
//（projectId 或不传），queryKey 由 scope 段自然分面。REST 端点与收敛口
// 径照抄 features/githubapps 旧 inline 查询（rowsOf 同款 undefined 行过
// 滤）。无轮询——与旧 inline 查询同现状（写后失效即时刷新，批2 起统一
// 由 commands 的 invalidates/legacyKeys 声明承载）。

export type GitHubAppRow = ghSchemas["schemas"]["v1GitHubApp"];

// ---- 写面命令（批2 useCommand 化）----
// put/remove 落行即全量并轨失效：invalidates allScopes（project/global 两
// 面集合键都过——project 行 shadow global 行，旧行为就是整前缀失效）+
// legacyKeys ["github-apps"]（repos 子查询 ["github-apps",appId,"repos"] 前
// 缀——批1 写后双失效特判在此退役，统一由 commands 声明承载）。manifest
// 三动词是流编排（不改行集，旧行无失效）：start/consume 显式空失效；
// complete 落行与 put 同口径。

/** 落行动词的全量失效声明（双语境集合 + 存量键桥） */
const landingInvalidates: ReadonlyArray<QuerySelector> = [{ resource: "github-apps", allScopes: true }];

export interface PutGitHubAppInput {
  /** 数字串表单值（run 内转 number——protojson 口径） */
  appId: string;
  installationId: string;
  privateKeyPem: string;
  /** GHES 面；空 = github.com */
  apiBase?: string;
  /** 在场 = Project 级行；缺席 = Global 行 */
  projectId?: string;
}

// decodeFlowStateProject 读签名态的可读载荷（base64url(JSON)——服务端验
// 签，本地只读 project 作用域供 complete 落行用）。解码失败返回 null。
export function decodeFlowStateProject(state: string): { projectId?: string } | null {
  try {
    const [body] = state.split(".");
    const json = JSON.parse(atob(body.replace(/-/g, "+").replace(/_/g, "/")));
    return { projectId: typeof json.p === "string" && json.p !== "" ? json.p : undefined };
  } catch {
    return null;
  }
}

export const githubAppsResource = defineResource({
  name: "github-apps",
  getId: (row: GitHubAppRow) => row.id ?? "",
  collection: clientCollection(fetchGitHubApps),
  commands: {
    // 手动添加/换钥（upsert：rotating a key is a re-put away）——保存时平台
    // 实调 GitHub 验证 app + installation + key 并解析 owner。
    put: {
      run: (input: PutGitHubAppInput) => {
        const body: Record<string, unknown> = {
          app_id: Number(input.appId),
          installation_id: Number(input.installationId),
          private_key_pem: input.privateKeyPem,
          ...(input.projectId != null ? { project_id: input.projectId } : { global: true }),
        };
        if (input.apiBase != null && input.apiBase.trim() !== "") {
          body.api_base = input.apiBase.trim();
        }
        return runCommandMapped<{ app?: GitHubAppRow }>(() => apiSend<{ app?: GitHubAppRow }>("/v1/github-apps", "POST", body));
      },
      invalidates: landingInvalidates,
      legacyKeys: () => [["github-apps"]],
    },
    remove: {
      run: (input: { id: string }) =>
        runCommandMapped(() => apiSend(`/v1/github-apps/${encodeURIComponent(input.id)}`, "DELETE", {})),
      invalidates: landingInvalidates,
      legacyKeys: () => [["github-apps"]],
    },
    // manifest 自动建流三段（ADR-0073 决策 8）：start 发起（浏览器整页 POST
    // 到 GitHub）、consume 换安装页 URL、complete 落行。
    manifestStart: {
      run: (input: { projectId?: string }) =>
        runCommandMapped<{ action_url?: string; manifest?: string }>(() =>
          apiSend<{ action_url?: string; manifest?: string }>("/v1/github-apps/manifest/start", "POST", {
            ...(input.projectId != null ? { project_id: input.projectId } : { global: true }),
          }),
        ),
      invalidates: [],
    },
    manifestConsume: {
      run: (input: { code: string; state: string }) =>
        runCommandMapped<{ install_url?: string; app_name?: string }>(() =>
          apiSend<{ install_url?: string; app_name?: string }>("/v1/github-apps/manifest/consume", "POST", {
            code: input.code,
            state: input.state,
          }),
        ),
      invalidates: [],
    },
    // complete 的流态恢复失败（setup 回调缺 state 且本地副本丢失）映射为
    // rejected 结果——回跳页把结果通道当错误态呈现，不算传输失败。
    manifestComplete: {
      run: (input: { state: string; installationId: string }) => {
        const scope = decodeFlowStateProject(input.state);
        if (scope == null) {
          return Promise.resolve({
            status: "rejected" as const,
            code: "flow_state_unrecoverable",
            message: "the flow state could not be recovered — restart the flow from the GitHub Apps page",
          });
        }
        return runCommandMapped<{ app?: { id?: string; project_id?: string; account_login?: string } }>(() =>
          apiSend<{ app?: { id?: string; project_id?: string; account_login?: string } }>(
            "/v1/github-apps/manifest/complete",
            "POST",
            {
              state: input.state,
              installation_id: Number(input.installationId),
              project_id: scope.projectId ?? "",
            },
          ),
        );
      },
      invalidates: landingInvalidates,
      legacyKeys: () => [["github-apps"]],
    },
  },
});

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

// URL 搜索 schema：text 快筛 account_login/id（与旧页 fieldsOf 同字段集）。
export const githubAppsSearch = collectionQuerySchema({
  text: ["account_login", "id"],
  defaultSize: 20,
});
