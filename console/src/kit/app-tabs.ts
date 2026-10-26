import type { CommandDescriptor } from "@lynx-go/uikit-core/kernel";
import { apiSend } from "@/api/client";
import { appsResource } from "./apps";
import { deploymentsResource } from "./deployments";
import { runCommandMapped } from "./errors";
import { routesResource } from "./routes";

// App 子面（进程/路由/设置）+ nodes enroll 的写动词族（批2 写路径命令化，
// 2026-10-26）。这些是 app 实体的子面动词——不定义独立资源，统一裸
// descriptor + invalidates/legacyKeys 声明；kit 侧失效面按 ADR-0007 词汇
// 引用资源（appsResource/deploymentsResource/routesResource——import 引用，
// 不在本文件重述键名）。注意两条失效通道的分工：
// - invalidates（kit selector）：与命令执行 scope（调用点传 { projectId }）
//   相交匹配 kit 集合键（["collection",<name>,{projectId},…]）；
// - legacyKeys（前缀桥）：存量 react-query 键（catalog/局部 useQuery）。
// 因此 appId 一律走闭包/入参，不进 scope——scope 混入 appId 会破坏 selector
// 的整 scope 相等匹配。

// —— 载荷与入参形态（组件表单 → wire 契约的映射单源在此） ——

export type GitHook = {
  app_id?: string;
  repo?: string;
  branch?: string;
  dockerfile?: string;
  watch_paths?: string[];
  token_prefix?: string;
  created_at?: string;
  updated_at?: string;
};

/** SetGitHook/RotateGitHook 的回显载荷（secret 仅铸造时回显一次） */
export type GitHookPayload = { hook?: GitHook; secret?: string };

/** RefreshGitHook（Deploy now 拉取通道）的受理回执 */
export type RefreshPayload = { deployment_id?: string; commit?: string; admission?: { outcome?: string } };

/** Deploy 受理回执（跳部署详情用） */
export type DeploymentAccepted = { deployment?: { id?: string } };

export interface GitHookDraft {
  repo: string;
  branch: string;
  dockerfile: string;
  watchPaths: string;
}

export interface RouteDraft {
  host: string;
  path: string;
  process: string;
  port: number;
  protocol: string;
  tlsMode: string;
}

export interface AppTabCommands {
  /** RestartProcess（ADR-0069 行级重启）：零 lineage——不动 Spec、不建部署；失效面只有实例观测 */
  restartProcess: CommandDescriptor<{ process: string }, { process: string }>;
  /** 实例数暂存 → Deploy（声明式路径）：冻结 Spec 全量 + 改动组装 spec_file 走 Deploy */
  redeploy: CommandDescriptor<{ specFile: string }, DeploymentAccepted>;
  createRoute: CommandDescriptor<RouteDraft, void>;
  deleteRoute: CommandDescriptor<{ id: string }, void>;
  setGitHook: CommandDescriptor<GitHookDraft, GitHookPayload>;
  rotateGitHook: CommandDescriptor<void, GitHookPayload>;
  refreshGitHook: CommandDescriptor<void, RefreshPayload>;
  deleteGitHook: CommandDescriptor<void, void>;
  deleteApp: CommandDescriptor<void, void>;
}

// appTabCommands 按 (projectId, appId) 关闭出全套子面动词：URL 与存量键
// 需要的 id 在此钉死，组件不再拼 URL/键（测试锚直接对返回的 descriptor
// 断言映射与失效清单）。每渲染新建实例对 useCommand 无害（descriptor 只
// 进 mutationFn/onSuccess 闭包，无订阅关系）。
export function appTabCommands(projectId: string, appId: string): AppTabCommands {
  return {
    // 失效面刻意最小（零 lineage 锚）：仅 exec-instances 前缀（Running 列/
    // 行展开的观测缓存），apps/deployments/spec 面一概不动——与 redeploy
    // 的语义分立是本命令的存在理由。
    restartProcess: {
      run: (input) =>
        runCommandMapped(async () => {
          await apiSend<Record<string, never>>(
            `/v1/apps/${encodeURIComponent(appId)}/processes/${encodeURIComponent(input.process)}/restart`,
            "POST",
            {},
          );
          // ok 文案需要进程名（输入面，不在载荷里）——回执携带回填
          return { process: input.process };
        }),
      legacyKeys: () => [["exec-instances", appId]],
    },
    // 声明式路径：受理即建部署。失效面 = 冻结 Spec（本 tab 重取落新 replicas）
    // + 部署两形态（kit 项目时间线 + legacy per-app/fan-out 前缀）。
    redeploy: {
      run: (input) =>
        runCommandMapped(() => apiSend<DeploymentAccepted>("/v1/deployments", "POST", { app_id: appId, spec_file: input.specFile })),
      invalidates: [{ resource: deploymentsResource.name }],
      legacyKeys: () => [
        ["resources", "app-spec", projectId, appId],
        ["deployments"],
      ],
    },
    // app 面路由 CRUD（项目路由表按 app 挂靠）：kit 面覆盖 resources/panels
    // 的 routes 集合，legacy 面覆盖 catalog useRoutes（本 tab/app 列表页/概览）。
    createRoute: {
      run: (input) =>
        runCommandMapped(async () => {
          await apiSend("/v1/routes", "POST", {
            project_id: projectId,
            host: input.host,
            path: input.path || undefined,
            app_id: appId,
            process: input.process,
            port: input.port,
            protocol: input.protocol,
            tls_mode: input.tlsMode,
          });
        }),
      invalidates: [{ resource: routesResource.name }],
      legacyKeys: () => [["resources", "routes", projectId]],
    },
    deleteRoute: {
      run: (input) =>
        runCommandMapped(async () => {
          await apiSend(`/v1/routes/${encodeURIComponent(input.id)}`, "DELETE");
        }),
      invalidates: [{ resource: routesResource.name }],
      legacyKeys: () => [["resources", "routes", projectId]],
    },
    // Git deploy hook（W-8 通道）：get/set/rotate/refresh/remove。set 是全量
    // upsert（改配置不换 Token）；remove 非幂等（无 hook 即 404，诚实走失败
    // 通道不覆写）。失效面只有 hook 查询（legacy 键，读面在 app-settings）。
    setGitHook: {
      run: (input) =>
        runCommandMapped(() =>
          apiSend<GitHookPayload>(`/v1/apps/${encodeURIComponent(appId)}/hook`, "POST", {
            repo: input.repo,
            branch: input.branch,
            dockerfile: input.dockerfile || undefined,
            watch_paths: input.watchPaths === "" ? undefined : input.watchPaths.split(/[,\s]+/).filter((entry) => entry !== ""),
          }),
        ),
      legacyKeys: () => [["apps", "hook", appId]],
    },
    rotateGitHook: {
      run: () =>
        runCommandMapped(() => apiSend<GitHookPayload>(`/v1/apps/${encodeURIComponent(appId)}/hook/rotate`, "POST", {})),
      legacyKeys: () => [["apps", "hook", appId]],
    },
    // Deploy now（拉取通道）：受理建部署——失效面同 redeploy 的部署两形态
    //（kit 时间线旧代码只失效 legacy 前缀，命令化后统一补 kit 面）。
    refreshGitHook: {
      run: () =>
        runCommandMapped(() =>
          apiSend<RefreshPayload>(`/v1/apps/${encodeURIComponent(appId)}/hook/refresh`, "POST", {}),
        ),
      invalidates: [{ resource: deploymentsResource.name }],
      legacyKeys: () => [["deployments"]],
    },
    deleteGitHook: {
      run: () =>
        runCommandMapped(async () => {
          await apiSend(`/v1/apps/${encodeURIComponent(appId)}/hook`, "DELETE");
        }),
      legacyKeys: () => [["apps", "hook", appId]],
    },
    // App 删除（级联披露在确认框）：apps 两形态（kit 集合 + catalog 目录）
    // + 部署两形态（历史随 app 消失，项目时间线不留幽灵行）。
    deleteApp: {
      run: () =>
        runCommandMapped(async () => {
          await apiSend(`/v1/apps/${encodeURIComponent(appId)}`, "DELETE");
        }),
      invalidates: [{ resource: appsResource.name }, { resource: deploymentsResource.name }],
      legacyKeys: () => [
        ["catalog", "apps", projectId],
        ["deployments"],
      ],
    },
  };
}

// —— nodes 面（本批唯一非 app 动词；批2 任务钉口统一落本文件）——
// enroll 材料铸取（H10）：初始 join 与 rotate 全量同端点。只铸材料、不落
// 任何列表面（节点入册由 daemon 侧 join 异步发生，列表 15s 轮询自跟进）
// ——无失效声明。relay 定向铸造/吊销仍是行内裸调用（nodes-list）。
export const enrollMaterialCommand: CommandDescriptor<{ rotate: boolean }, { join_command?: string }> = {
  run: (input) => runCommandMapped(() => apiSend<{ join_command?: string }>("/v1/nodes/enroll", "POST", { rotate: input.rotate })),
};

