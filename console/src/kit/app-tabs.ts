import type { CommandDescriptor, ResourceScope } from "@lynx-go/uikit-core/kernel";
import { apiFetch, apiSend } from "@/api/client";
import { appsResource } from "./apps";
import { deploymentsResource, type PreviewTeardownPayload } from "./deployments";
import { runCommandMapped } from "./errors";
import { routesResource } from "./routes";

// App 子面（进程/路由/设置）+ nodes enroll 的写动词族（批2 写路径命令化，
// 2026-10-26）。这些是 app 实体的子面动词——不定义独立资源，统一裸
// descriptor + invalidates 声明；kit 侧失效面按 ADR-0007 词汇引用资源
//（appsResource/deploymentsResource/routesResource——import 引用，不在本
// 文件重述键名）。F1 回流批（M2 scope 同源）：工厂命令统一声明
// scopeOf: () => ({ projectId, appId })——同一份 scope 既喂 run(ctx) 又喂
// 失效匹配（投影匹配：命令 scope 在读资源声明的键集上逐键相等即命中，
// 携带声明外的多余键不阻断——appId 参与匹配不破坏 projectId 级读键的命中）。
// 调用点零手传 scope（0.3.1 时代「appId 不进 scope 保整对象全等」的理由
// 随投影匹配退役）。

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
  /** 实例数暂存 → Deploy（声明式路径）：冻结 Spec 全量 + 改动组装 spec_file 走 Deploy。
   *  skip = B1 W4 双击确认的二击形态（ConfirmSkipDialog；--yes 通道，proof=skipped 进审计） */
  redeploy: CommandDescriptor<{ specFile: string; skip?: boolean }, DeploymentAccepted>;
  createRoute: CommandDescriptor<RouteDraft, void>;
  deleteRoute: CommandDescriptor<{ id: string }, void>;
  setGitHook: CommandDescriptor<GitHookDraft, GitHookPayload>;
  rotateGitHook: CommandDescriptor<void, GitHookPayload>;
  refreshGitHook: CommandDescriptor<void, RefreshPayload>;
  deleteGitHook: CommandDescriptor<void, void>;
  /** PreviewTeardown（B1 拆除预演）：级联/保留清单 + 驱动权冲突 + 铸票；无失效面（只读孪生 + 铸票） */
  previewTeardown: CommandDescriptor<void, PreviewTeardownPayload>;
  /** DeleteApp（delete 族强制集）：ticket 来自 previewTeardown 的铸票 */
  deleteApp: CommandDescriptor<{ ticket?: string }, void>;
}

// appTabCommands 按 (projectId, appId) 关闭出全套子面动词：URL 需要的 id
// 在此钉死，组件不再拼 URL（测试锚直接对返回的 descriptor 断言映射与失效
// 清单）。身份经 scopeOf 声明派生（M2：从闭包迁入声明——引擎解析
// effectiveScope 单源喂执行与失效）。每渲染新建实例对 useCommand 无害
//（descriptor 只进 mutationFn/onSuccess 闭包，无订阅关系）。
export function appTabCommands(projectId: string, appId: string): AppTabCommands {
  // M2 同源声明：全套动词的失效 scope 从闭包派生（调用点零手传）。
  const scopeOf = (): ResourceScope => ({ projectId, appId });
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
      scopeOf,
    },
    // 声明式路径：受理即建部署。失效面 = 冻结 Spec（本 tab 重取落新 replicas）
    // + 部署两形态（kit 项目时间线 + legacy per-app/fan-out 前缀）。
    // B1 W4 遗留直发面收口（2026-10-31）：Processes 暂存提交钮（钮词钦定
    // 豁免见 wording 守卫）经 ConfirmSkipDialog 双击形态接确认动线——
    // skip = 二击显式跳过（--yes 通道，proof=skipped 进审计）；预览化
    // （DeploySheet 面板铸票）留 B2a。
    redeploy: {
      run: (input) =>
        runCommandMapped(() =>
          apiSend<DeploymentAccepted>("/v1/deployments", "POST", {
            app_id: appId,
            spec_file: input.specFile,
            ...(input.skip ? { confirmation: { skip: true } } : {}),
          }),
        ),
      scopeOf,
      invalidates: [{ resource: deploymentsResource.name }, { resource: "app-spec-sheet" }],
      // ["deployments"] 前缀桥：project-overview AppTile 的裸键（["deployments",
      // "latest", appId]）仍在读——catalog 其余键族零读者后（F1 回流批审计）
      // 唯一存活的存量键面。
      legacyKeys: () => [["deployments"]],
    },
    // app 面路由 CRUD（项目路由表按 app 挂靠）：kit 面覆盖 resources/panels
    // 的 routes 集合，legacy 面覆盖 catalog useRoutes（本 tab/app 列表页/概览）。
    createRoute: {
      scopeOf,
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
    },
    deleteRoute: {
      scopeOf,
      run: (input) =>
        runCommandMapped(async () => {
          await apiSend(`/v1/routes/${encodeURIComponent(input.id)}`, "DELETE");
        }),
      invalidates: [{ resource: routesResource.name }],
    },
    // Git deploy hook（W-8 通道）：get/set/rotate/refresh/remove。set 是全量
    // upsert（改配置不换 Token）；remove 非幂等（无 hook 即 404，诚实走失败
    // 通道不覆写）。失效面只有 hook 查询（legacy 键，读面在 app-settings）。
    setGitHook: {
      scopeOf,
      run: (input) =>
        runCommandMapped(() =>
          apiSend<GitHookPayload>(`/v1/apps/${encodeURIComponent(appId)}/hook`, "POST", {
            repo: input.repo,
            branch: input.branch,
            dockerfile: input.dockerfile || undefined,
            watch_paths: input.watchPaths === "" ? undefined : input.watchPaths.split(/[,\s]+/).filter((entry) => entry !== ""),
          }),
        ),
      // hook 读面在 kit record（app-hook）——kit selector 命中即翻转（M2
      // 投影匹配下零手传 scope 自动命中）。
      invalidates: [{ resource: "app-hook" }],
    },
    rotateGitHook: {
      scopeOf,
      run: () =>
        runCommandMapped(() => apiSend<GitHookPayload>(`/v1/apps/${encodeURIComponent(appId)}/hook/rotate`, "POST", {})),
      invalidates: [{ resource: "app-hook" }],
    },
    // Deploy now（拉取通道）：受理建部署——失效面同 redeploy 的部署两形态
    //（kit 时间线旧代码只失效 legacy 前缀，命令化后统一补 kit 面）。
    refreshGitHook: {
      scopeOf,
      run: () =>
        runCommandMapped(() =>
          apiSend<RefreshPayload>(`/v1/apps/${encodeURIComponent(appId)}/hook/refresh`, "POST", {}),
        ),
      invalidates: [{ resource: deploymentsResource.name }],
      // AppTile 存量键面（同 redeploy 注）。
      legacyKeys: () => [["deployments"]],
    },
    deleteGitHook: {
      scopeOf,
      run: () =>
        runCommandMapped(async () => {
          await apiSend(`/v1/apps/${encodeURIComponent(appId)}/hook`, "DELETE");
        }),
      invalidates: [{ resource: "app-hook" }],
    },
    // App 删除（B1 W4 拆除确认闭环）：DeleteApp 在 delete 族强制集——
    // ticket 由 previewTeardown 铸（宽档确认框先读级联/保留清单再落到钮
    // 上，设计稿 §1.6）。REST 形态注意：DeleteApp 无 body（proto 注解
    // delete: "/v1/apps/{id}"），confirmation 走 **query 参数**
    // confirmation.ticket=…（gateway 生成面，api/structure.ts 同形）。
    // Header 形态已查明不可行（修复批 F4）：grpc-gateway 生成物只从 query
    // 装配请求字段（structure.pb.gw.go PopulateQueryParameters），自定义
    // header 无字段绑定通道，ConfirmGuard 也只读请求消息面——票串进 URL
    // 是既有契约形态，改以 cache:"no-store" 收口（带票 URL 不入浏览器
    // HTTP 缓存）。级联披露在确认框（PreviewTeardown 载荷填充）；失效面
    // = apps 集合 + 部署两形态（历史随 app 消失，项目时间线不留幽灵行）。
    previewTeardown: {
      scopeOf,
      run: () =>
        runCommandMapped(() =>
          apiSend<PreviewTeardownPayload>(`/v1/apps/${encodeURIComponent(appId)}/preview-teardown`, "POST", {}),
        ),
    },
    deleteApp: {
      scopeOf,
      run: (input) =>
        runCommandMapped(async () => {
          const query = input?.ticket ? `?confirmation.ticket=${encodeURIComponent(input.ticket)}` : "";
          await apiFetch(`/v1/apps/${encodeURIComponent(appId)}${query}`, {
            method: "DELETE",
            cache: "no-store",
          });
        }),
      invalidates: [{ resource: appsResource.name }, { resource: deploymentsResource.name }],
      // AppTile 存量键面（同 redeploy 注）。
      legacyKeys: () => [["deployments"]],
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

