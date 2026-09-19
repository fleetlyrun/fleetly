// 本文件由 openapi-typescript 从 genproto/fleetly/server/v1/*.swagger.json 生成
// （console/scripts/gen-api.mjs，`pnpm gen:api`）——不要手改；proto 变更后
// 重新生成并提交。CI（pr.yml console job）以"再生成无 diff"门禁拦截漂移。
// 字段名/类型语义：UseProtoNames（snake_case 声明名）+ proto3 JSON 映射
// （int64 → 字符串；EmitUnpopulated=false → 零值字段缺省，全部属性可选）。

export interface paths {
    "/v1/apps": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["AppsService_ListApps"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{name}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["AppsService_GetApp"];
        put?: never;
        post?: never;
        delete: operations["AppsService_DeleteApp"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{name}/source": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * SetAppSource 设置 webhook 拉源配置（remote url + 分支 + 认证形态；
         *     admin）。branch 同时是 git push 的触发分支（app 配置分支，默认 main）。
         *     认证材料（https_token/ssh_key）经平台 envelope 加密落库，引用不落明文。
         */
        put: operations["AppsService_SetAppSource"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{name}/webhook": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * ShowAppWebhook 回读 webhook/git 触发配置（无敏感投影：secret 只回
         *     configured 位；admin scope——source URL 与分支拓扑属运维面）。
         */
        get: operations["AppsService_ShowAppWebhook"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{name}/webhook-secret": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * SetAppWebhookSecret 设置 per-app webhook 签名密钥（T2.19；admin）。
         *     值经平台 envelope 加密落库（明文不落），show 面只回 configured 位。
         *     未配置 = webhook 端点未启用（404 语义）。
         */
        put: operations["AppsService_SetAppWebhookSecret"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/deployments": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["DeploymentsService_ListDeployments"];
        put?: never;
        post: operations["DeploymentsService_Deploy"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/deployments/git": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * DeployFromGit 是 git push(SSH) 触发入口的服务端入队（T2.19）：post-
         *     receive 钩子经 loopback REST 携带 hook token 调用；compose 字节由服务
         *     端从 bare 仓库 `git show <sha>:compose.{yaml,yml}` 自取（compose 真源
         *     在 git 对象库，不信任客户端传字节）。幂等口径：git push 是显式用户
         *     动作——每次调用都建部署记录（引擎同 spec 重放安全）；(app, sha) 去重
         *     仅属 webhook 入口。scope = deploy（hook token 最小权限）。
         */
        post: operations["DeploymentsService_DeployFromGit"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/rollbacks": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["DeploymentsService_RollbackDeployment"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/deployments/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["DeploymentsService_GetDeployment"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/deployments/{id}/cancel": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["DeploymentsService_CancelDeployment"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/revisions": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["RevisionsService_ListRevisions"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/revisions/{revision_id}/spec": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["RevisionsService_GetRevisionSpec"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/env": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["EnvService_ListEnv"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/env/{key}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["EnvService_GetEnv"];
        put: operations["EnvService_SetEnv"];
        post?: never;
        delete: operations["EnvService_RemoveEnv"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/domains": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["DomainsService_ListAppDomains"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/domains/verify": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["DomainsService_VerifyAppDomains"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/logs": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["LogsService_ListHistoryLogs"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/logs/stream": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["LogsService_FollowLogs"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/events/stream": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["EventsService_WatchEvents"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/system/backups": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * ListBackups 状态备份台账（T2.22，只读）：热备快照的诚实账（kind/路径/
         *     sha256/verify_status）。verify_status=failed 的行是红色告警面的一部分
         *     ——台账如实保留失败行，消费方据此判断备份可用性。
         */
        get: operations["SystemService_ListBackups"];
        put?: never;
        /**
         * TriggerBackup 手动触发一次状态备份（T2.22；deploy scope——写面语义，
         *     与升级编排 pre_upgrade 快照共用同一同步入口；响应即落账后的台账行，
         *     verify_status=failed 时调用方必须视为备份失败而非请求失败歧义态）。
         */
        post: operations["SystemService_TriggerBackup"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/system/ingress": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["SystemService_GetIngressStatus"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/system/nodes": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["SystemService_ListNodes"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/system/ping": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["SystemService_Ping"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/system/status": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["SystemService_GetSystemStatus"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/placement": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["PlacementService_ShowPlacement"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        AppsServiceSetAppSourceBody: {
            /** 拉源 remote URL（file:// 与 https://、ssh:// 形态）。 */
            url?: string;
            /** 触发/拉取分支（默认 main）。 */
            branch?: string;
            /** 认证形态：none | https_token | ssh_key。 */
            auth_kind?: string;
            /**
             * 认证材料（https_token = token 原文；ssh_key = PEM 私钥）。auth_kind =
             *     none 时必须为空；服务端 envelope 加密落库，明文不落、永不回读。
             *     protovalidate 形状约束在服务端用例层按 auth_kind 交叉校验（跨字段
             *     规则—— CEL 交叉字段此处不引入，保持 proto 面最小）。
             */
            auth_secret?: string;
        };
        AppsServiceSetAppWebhookSecretBody: {
            /**
             * webhook 签名密钥（HMAC-SHA256 原料，GitHub/Gitea 同形态）。≥16 字符
             *     ——弱密钥显式拒绝（验签是该端点的唯一认证）。
             */
            secret?: string;
        };
        v1AppView: {
            id?: string;
            name?: string;
            /** 生命周期状态位：active / deleting / deleted。 */
            lifecycle?: string;
            /** 派生状态：running / degraded / blocked / down（读面即时推导）。 */
            derived_state?: string;
            /** Format: date-time */
            created_at?: string;
            /** Format: date-time */
            updated_at?: string;
        };
        v1DeleteAppResponse: {
            name?: string;
            /** 删除推进后的生命周期位（active → deleting）。 */
            lifecycle?: string;
        };
        /**
         * DeploymentView 是部署状态机行的只读投影（词表与 state 层一致：
         *     queued/preparing/building/releasing/observing/succeeded/failed/cancelled）。
         */
        v1DeploymentView: {
            id?: string;
            app?: string;
            /** deploy | rollback。 */
            kind?: string;
            status?: string;
            /** 子状态（blocked_waiting 或空）。 */
            phase?: string;
            revision_id?: string;
            error_code?: string;
            verdict?: string;
            recovery?: string;
            /** 首发失败 scale=0 保留现场。 */
            substrate_halted?: boolean;
            /** Format: date-time */
            first_healthy_at?: string;
            /** Format: int64 */
            downtime_ms?: string;
            /** Format: date-time */
            created_at?: string;
            /** Format: date-time */
            updated_at?: string;
            /**
             * git 触发来源（T2.19）：仅经 git push(SSH)/webhook 入队（DeployFromGit
             *     路径）的部署非空——sha 为 40 位 commit、ref 为 refs/heads/<branch>；
             *     API/CLI 直传 compose 的部署为空（EmitUnpopulated=false 语义下不输出）。
             *     webhook 入口的 (app, sha) 幂等去重即以此字段为判据，读面回显供
             *     AI-Agent/运营核对「这次部署来自哪个 commit」。
             */
            source_git_sha?: string;
            source_git_ref?: string;
        };
        /**
         * ErrorResponse 是 fleetly 对外错误信封的唯一契约定义（发布专项 §2.7、
         *     架构 D21）：gateway HTTPErrorHandler（阶段 3 落地）将 gRPC 错误统一渲染
         *     为本结构，snake_case JSON 输出；code 与 T0.2 错误码注册表（唯一真源，
         *     只增不复用）对齐。
         */
        v1ErrorResponse: {
            /** 稳定错误码字符串（如 "E_COMPOSE_INVALID"），注册表校验只增。 */
            code?: string;
            /** 人读错误信息（面向运维/集成方，不承诺文案稳定）。 */
            message?: string;
            /** 失败所处发布阶段（如 resolve / build / deploy / serve）。 */
            phase?: string;
            /** 关联的部署 ID（无关联时为空）。 */
            deployment_id?: string;
            /** 可执行的修复建议（面向用户展示）。 */
            suggestion?: string;
            /** 结构化附加上下文（machine-readable 键值对）。 */
            context?: {
                [key: string]: string;
            };
            /** 相关文档 URL（错误码文档锚点）。 */
            docs?: string;
        };
        v1GetAppResponse: {
            id?: string;
            name?: string;
            lifecycle?: string;
            derived_state?: string;
            /** Format: date-time */
            created_at?: string;
            /** Format: date-time */
            updated_at?: string;
            placement?: components["schemas"]["v1PlacementView"];
            /** 最近部署（created_at 倒序，至多 5 条；派生状态的正交细节）。 */
            recent_deployments?: components["schemas"]["v1DeploymentView"][];
        };
        v1ListAppsResponse: {
            apps?: components["schemas"]["v1AppView"][];
        };
        /**
         * PlacementView 是 placements 行投影（state ∈ bound/blocked/unresolved，
         *     派生语义见 state-model §2.10；blocked/unresolved 是 app 派生状态
         *     blocked 的来源）。
         */
        v1PlacementView: {
            platform_node_id?: string;
            /** 用户 label 书写原值（显示名或 n_<ULID>；空 = 未声明）。 */
            label_ref?: string;
            state?: string;
            /** 进入当前状态的派生原因（node_down / node_gone 等；可空）。 */
            reason?: string;
            /** 绑定来源（placement 源词表：显式/自动钉住等）。 */
            source?: string;
            /** Format: date-time */
            pinned_at?: string;
            /** Format: date-time */
            created_at?: string;
            /** Format: date-time */
            updated_at?: string;
        };
        v1SetAppSourceResponse: {
            name?: string;
            source_url?: string;
            source_branch?: string;
            auth_kind?: string;
        };
        v1SetAppWebhookSecretResponse: {
            name?: string;
            /** 恒 true（设置成功即已配置）。 */
            configured?: boolean;
        };
        v1ShowAppWebhookResponse: {
            name?: string;
            /** webhook 签名密钥已配置（secret 值永不回读）。 */
            secret_configured?: boolean;
            /** 拉源配置（未设置时 url/branch 为空串、auth_kind = none）。 */
            source_url?: string;
            source_branch?: string;
            /** none | https_token | ssh_key。 */
            source_auth_kind?: string;
            /** git push 触发分支（app 配置分支，默认 main）。 */
            branch?: string;
            /**
             * push/webhook 端点提示（SSH git URL，如 ssh://git@host:8424/<app>.git；
             *     主机位取 control-plane 可达地址的尽力形态）。
             */
            git_remote_hint?: string;
        };
        DeploymentsServiceCancelDeploymentBody: Record<string, never>;
        DeploymentsServiceDeployBody: {
            /**
             * compose 文件内容字节（JSON/YAML 原文；服务端落临时文件走受控子集
             *     校验——compose 违约不动底座、不入队）。
             * Format: byte
             */
            compose?: string;
            /**
             * 破坏性变更确认门控（架构 §2.4 plan/apply 语义，MG-C3）：本次部署相对
             *     最新 revision 的变更集含破坏性操作（服务删除/卷解绑——判定单源在
             *     compose 包，与 plan artifact 的 requires_confirm_destructive 同口径）时，
             *     必须显式置位才放行入队；未置位返回 E_DEPLOY_CONFIRM_REQUIRED、不入队。
             *     首发（无历史 revision）恒非破坏性，置位与否均放行。
             */
            confirm_destructive?: boolean;
        };
        /**
         * DeployFromGitRequest 携带 push 上下文（app 来自 REST 路径）。ref 形如
         *     refs/heads/main；sha 为 40 位十六进制 commit（服务端严格校验）。
         */
        DeploymentsServiceDeployFromGitBody: {
            sha?: string;
            ref?: string;
        };
        DeploymentsServiceRollbackDeploymentBody: {
            /** 回滚目标版本快照 ID；空 = 最近一次成功部署的版本（回退一版）。 */
            target_revision_id?: string;
        };
        v1CancelDeploymentResponse: {
            id?: string;
            /**
             * 置位 cancel_requested 时的状态（取消是异步语义：引擎先归位再落
             *     cancelled；曾健康 409）。
             */
            status?: string;
        };
        /**
         * ComposeWarning 是 compose 校验期非阻断标注的跨面投影（deploy/build 共
         *     用；code = 注册表 W_ 码，无注册码提示以 kind 承载稳定标识）。
         */
        v1ComposeWarning: {
            kind?: string;
            code?: string;
            service?: string;
            message?: string;
        };
        /**
         * DeployFromGitResponse 与 DeployResponse 同投影面（独立消息以满足 buf
         *     lint 的 RPC 响应类型命名纪律；字段语义一致——入队即返回 queued）。
         */
        v1DeployFromGitResponse: {
            deployment_id?: string;
            app?: string;
            status?: string;
            warnings?: components["schemas"]["v1ComposeWarning"][];
        };
        v1DeployResponse: {
            deployment_id?: string;
            app?: string;
            /** 入队即返回，恒 "queued"。 */
            status?: string;
            /**
             * compose 校验期非阻断标注（服务端受控子集校验的警告随响应带出——
             *     T2.18：CLI 改经 RPC 入队后仍保留校验警告的人读呈现）。
             */
            warnings?: components["schemas"]["v1ComposeWarning"][];
        };
        v1GetDeploymentResponse: {
            deployment?: components["schemas"]["v1DeploymentView"];
        };
        v1ListDeploymentsResponse: {
            deployments?: components["schemas"]["v1DeploymentView"][];
        };
        v1RollbackDeploymentResponse: {
            deployment_id?: string;
            app?: string;
            /** 恒 "queued"（入队即返回）。 */
            status?: string;
        };
        v1GetRevisionSpecResponse: {
            revision_id?: string;
            /** Format: int64 */
            seq?: string;
            /**
             * 归一化 compose 快照（canonical JSON 文本；compose.Spec 同构——env 为
             *     key:sha256，值明文结构性不在快照中）。
             */
            compose?: string;
        };
        v1ListRevisionsResponse: {
            revisions?: components["schemas"]["v1RevisionView"][];
        };
        v1RevisionView: {
            id?: string;
            /** Format: int64 */
            seq?: string;
            desired_hash?: string;
            /** active = 可回滚选项；superseded = 被保留窗淘汰（存档）。 */
            status?: string;
            verified?: boolean;
            /** Format: date-time */
            created_at?: string;
        };
        EnvServiceSetEnvBody: {
            /** 明文入参；服务端 envelope 加密落库（值不进审计/事件/日志）。 */
            value?: string;
        };
        /** EnvVarView 是 env 行的无值投影（值恒脱敏——读值走 GetEnv 显式路径）。 */
        v1EnvVarView: {
            key?: string;
            /** platform | system。 */
            source?: string;
            /** pending | effective。 */
            status?: string;
            /** Format: date-time */
            created_at?: string;
            /** Format: date-time */
            updated_at?: string;
        };
        v1GetEnvResponse: {
            app?: string;
            key?: string;
            /** 明文（admin scope 专用路径）。 */
            value?: string;
            /** pending | effective。 */
            status?: string;
        };
        v1ListEnvResponse: {
            env_vars?: components["schemas"]["v1EnvVarView"][];
        };
        v1RemoveEnvResponse: {
            app?: string;
            key?: string;
            /**
             * 平台层台账立即删行；运行实例的 env 快照随下次部署更新（与 pending
             *     同链路语义，取值恒 "pending"——移除不是即时生效面）。
             */
            status?: string;
        };
        v1SetEnvResponse: {
            app?: string;
            key?: string;
            /** 恒 "pending"（随下次部署生效）。 */
            status?: string;
        };
        DomainsServiceVerifyAppDomainsBody: Record<string, never>;
        v1DomainCheckView: {
            domain?: string;
            ips?: string[];
            resolved?: boolean;
            /** 80 端口探测结果（空 = 不可达；否则记录响应状态行）。 */
            http_80?: string;
            /** 443 端口 TLS 握手结果（空 = 不可达）。 */
            https_443?: string;
            /** 实收证书的诚实记录（不经信任判定）。 */
            cert_subject?: string;
            cert_dns_names?: string[];
            /** Format: date-time */
            cert_not_after?: string;
            /** 单域名探测失败原文（resolve 失败等）。 */
            error?: string;
        };
        v1DomainView: {
            service?: string;
            domain?: string;
            /** 路由目标端口（'' = 未同步）。 */
            port?: string;
            /** 证书 PEM 内容 sha256 hex；'' = 尚无证书。 */
            cert_sha256?: string;
            /**
             * 叶证书 NotAfter；未签发时不输出。
             * Format: date-time
             */
            cert_not_after?: string;
            /** Format: date-time */
            created_at?: string;
        };
        v1ListAppDomainsResponse: {
            domains?: components["schemas"]["v1DomainView"][];
        };
        v1VerifyAppDomainsResponse: {
            checks?: components["schemas"]["v1DomainCheckView"][];
        };
        v1FollowLogsResponse: {
            entry?: components["schemas"]["v1LogEntryView"];
        };
        v1ListHistoryLogsResponse: {
            entries?: components["schemas"]["v1LogEntryView"][];
        };
        /** LogEntryView 是单条日志投影。source ∈ container | build。 */
        v1LogEntryView: {
            app?: string;
            service?: string;
            /** Format: date-time */
            at?: string;
            stderr?: boolean;
            line?: string;
            source?: string;
        };
        /**
         * CursorExpiredView 是游标过期断档帧：seq ≤ (oldest_seq - 1) 的事件已被
         *     保留策略清理，消费方应以 oldest_seq 重新拉全量。
         */
        v1CursorExpiredView: {
            /** Format: int64 */
            oldest_seq?: string;
            message?: string;
        };
        /**
         * EventView 是事件行投影（payload 为脱敏 JSON 文本；secret 值禁止进入
         *     事件——state-model §2.9，采集端已保证）。
         */
        v1EventView: {
            /** Format: int64 */
            seq?: string;
            /** Format: date-time */
            at?: string;
            /** 注册表内事件名（deployment.succeeded 等）。 */
            name?: string;
            /** 主题（deployment:<id> / app:<name> 等）。 */
            subject?: string;
            payload?: string;
        };
        v1WatchEventsResponse: {
            event?: components["schemas"]["v1EventView"];
            cursor_expired?: components["schemas"]["v1CursorExpiredView"];
        };
        /**
         * BackupHealth 是系统状态里备份面的明细视图（组件布尔健康的展开：最近
         *     一次备份的时间与校验结论——「绿色成功但实际没备份」的对立面是让
         *     verify_status 与时间直接可见）。
         */
        v1BackupHealth: {
            /** 最近一次备份的台账 ID（= 备份目录名）。 */
            last_backup_id?: string;
            /** 最近一次备份的触发类别（daily/pre_upgrade/post_deploy/manual）。 */
            last_kind?: string;
            /**
             * 最近一次备份的台账落账时刻。
             * Format: date-time
             */
            last_backup_at?: string;
            /** 回读校验结论（verified/failed）。 */
            last_verify_status?: string;
            /** 失败原因原文（verified 行为空）。 */
            last_error?: string;
        };
        /**
         * BackupView 是状态备份台账行的只读投影（state_backups 表）。path 指向
         *     备份目录内的快照文件；manifest.json 与其同目录（含 sha256/密钥指纹/
         *     schema 版本——恢复核对材料，密钥本体绝不入备份目录）。
         */
        v1BackupView: {
            id?: string;
            /**
             * 触发类别：daily / pre_upgrade / post_deploy / manual（历史行可为
             *     hot/cold）。
             */
            kind?: string;
            /** 快照文件路径。 */
            path?: string;
            /** 快照文件 sha256（hex；回读校验对象）。 */
            sha256?: string;
            /** Format: int64 */
            size_bytes?: string;
            /** 回读校验结论：verified / failed（失败行保留——红色告警面的一部分）。 */
            verify_status?: string;
            /** 校验失败原因原文（verified 行为空）。 */
            error?: string;
            /**
             * 台账落账时刻。
             * Format: date-time
             */
            created_at?: string;
        };
        /**
         * CertLedgerView 是证书台账行投影（domains 表 cert 列对照；app 为显示名，
         *     已删除应用回退显示 app id）。
         */
        v1CertLedgerView: {
            app?: string;
            domain?: string;
            cert_sha256?: string;
            /** Format: date-time */
            cert_not_after?: string;
        };
        /**
         * ComponentHealth 是单组件健康如实上报（name = lynx 服务名，如
         *     state.store / state.observer / ingress.traefik）。ok=false 时 error 为
         *     检查器返回原文。
         */
        v1ComponentHealth: {
            name?: string;
            ok?: boolean;
            error?: string;
        };
        v1GetIngressStatusResponse: {
            traefik?: components["schemas"]["v1TraefikView"];
            /** 控制面配置端点监听地址（ingress.config_addr 配置原值）。 */
            config_addr?: string;
            /** 下发给 Traefik 的控制面可达 IP（空 = 自动探测）。 */
            advertise_ip?: string;
            /** ACME challenge 应答器基址（空 = 未接入集中签发）。 */
            responder?: string;
            /**
             * 配置端点两面探测结果（服务端环回执行）：/healthz 无鉴权状态行；
             *     /configs 带 token 鉴权核验（401 = token 缺失/错误，如实报告；200 =
             *     鉴权通过且返回合法 JSON）。不可达 = "unreachable"。
             */
            healthz?: string;
            auth?: string;
            /** 证书台账（有证书的域名行）。 */
            certificates?: components["schemas"]["v1CertLedgerView"][];
            /**
             * 证书存储目录（控制面侧）与其中的 app 清单（meta 索引；目录缺失 =
             *     空清单非错误，cert_dir_error 承载读取故障原文）。
             */
            cert_dir?: string;
            cert_dir_apps?: string[];
            cert_dir_error?: string;
        };
        v1GetSystemStatusResponse: {
            service?: string;
            version?: string;
            components?: components["schemas"]["v1ComponentHealth"][];
            backup?: components["schemas"]["v1BackupHealth"];
        };
        v1ListBackupsResponse: {
            backups?: components["schemas"]["v1BackupView"][];
        };
        v1ListNodesResponse: {
            nodes?: components["schemas"]["v1NodeView"][];
        };
        /**
         * NodeView 是节点观测缓存行的只读投影（state-model §2.2：缓存禁止用于
         *     决策，展示/诊断专用；节点变更用 docker node 原生命令）。观测数据带
         *     observed_at/stale——状态诚实契约（architecture §4.2 横切硬指标）；
         *     nodes 不提供「最后心跳」字段。
         */
        v1NodeView: {
            swarm_node_id?: string;
            /** 平台节点 ID（fleetly.placement.node-id label；未锚定时为空）。 */
            platform_id?: string;
            hostname?: string;
            state?: string;
            availability?: string;
            is_manager?: boolean;
            /** Format: date-time */
            observed_at?: string;
            stale?: boolean;
            labels?: {
                [key: string]: string;
            };
        };
        v1PingResponse: {
            /** 应答服务名（恒 "fleetlyd"）。 */
            service?: string;
            /** 服务版本（构建 -ldflags 注入，未注入时为 "dev"）。 */
            version?: string;
        };
        /**
         * TraefikView 是入口服务实况投影（Swarm service inspect；不可达时 exists
         *     = false 且 error 为探测原文）。
         */
        v1TraefikView: {
            exists?: boolean;
            image?: string;
            /** Format: int32 */
            static_args?: number;
            error?: string;
        };
        v1TriggerBackupRequest: {
            /**
             * 触发类别（缺省 manual；升级编排传 pre_upgrade）。manual/daily/
             *     pre_upgrade/post_deploy 之外取值被请求校验拒绝。
             */
            kind?: string;
        };
        v1TriggerBackupResponse: {
            backup?: components["schemas"]["v1BackupView"];
        };
        v1ShowPlacementResponse: {
            app?: string;
            placement?: components["schemas"]["v1PlacementView"];
            /** 卷注册表（无卷应用为空集）。 */
            volumes?: components["schemas"]["v1VolumeView"][];
        };
        /**
         * VolumeView 是卷注册表行投影（volumes 表；orphaned 状态位经 status 透出
         *     ——删除应用保留卷）。
         */
        v1VolumeView: {
            key?: string;
            name?: string;
            kind?: string;
            node_id?: string;
            mount_path?: string;
            status?: string;
        };
    };
    responses: never;
    parameters: never;
    requestBodies: never;
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    AppsService_ListApps: {
        parameters: {
            query?: {
                /** @description 列表上限（缺省 100；v0.1 单机规模不做分页游标）。 */
                limit?: number;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ListAppsResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    AppsService_GetApp: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 应用名（compose 应用名，权威态唯一键）。 */
                name: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1GetAppResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    AppsService_DeleteApp: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1DeleteAppResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    AppsService_SetAppSource: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["AppsServiceSetAppSourceBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1SetAppSourceResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    AppsService_ShowAppWebhook: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ShowAppWebhookResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    AppsService_SetAppWebhookSecret: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 应用名。 */
                name: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["AppsServiceSetAppWebhookSecretBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1SetAppWebhookSecretResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    DeploymentsService_ListDeployments: {
        parameters: {
            query?: {
                limit?: number;
            };
            header?: never;
            path: {
                /** @description 应用名（列表必选）。 */
                app: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ListDeploymentsResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    DeploymentsService_Deploy: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 应用名（不存在时自动创建——与 CLI deploy 同语义：应用随首次部署创建）。 */
                app: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["DeploymentsServiceDeployBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1DeployResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    DeploymentsService_DeployFromGit: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["DeploymentsServiceDeployFromGitBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1DeployFromGitResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    DeploymentsService_RollbackDeployment: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["DeploymentsServiceRollbackDeploymentBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1RollbackDeploymentResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    DeploymentsService_GetDeployment: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1GetDeploymentResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    DeploymentsService_CancelDeployment: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["DeploymentsServiceCancelDeploymentBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1CancelDeploymentResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    RevisionsService_ListRevisions: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ListRevisionsResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    RevisionsService_GetRevisionSpec: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
                /** @description 目标快照 ID（active 集内；superseded → 404，与回滚选项面同口径）。 */
                revision_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1GetRevisionSpecResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    EnvService_ListEnv: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ListEnvResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    EnvService_GetEnv: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
                key: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1GetEnvResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    EnvService_SetEnv: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
                key: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["EnvServiceSetEnvBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1SetEnvResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    EnvService_RemoveEnv: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
                key: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1RemoveEnvResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    DomainsService_ListAppDomains: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ListAppDomainsResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    DomainsService_VerifyAppDomains: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["DomainsServiceVerifyAppDomainsBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1VerifyAppDomainsResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    LogsService_ListHistoryLogs: {
        parameters: {
            query?: {
                /** @description compose 服务名；空 = 全部服务。 */
                service?: string;
                /** @description 时间窗下界；缺省 = 保留窗起点。 */
                since?: string;
                /** @description 时间窗上界；缺省 = 现在。 */
                until?: string;
                /** @description 返回上限（缺省 200，天花板 1000；超过取最新 limit 条）。 */
                limit?: number;
                /** @description 来源过滤：container | build；空 = 全部。 */
                source?: string;
            };
            header?: never;
            path: {
                app: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ListHistoryLogsResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    LogsService_FollowLogs: {
        parameters: {
            query?: {
                /** @description compose 服务名；空 = 该 app 全部服务。 */
                service?: string;
            };
            header?: never;
            path: {
                app: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response.(streaming responses) */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        result?: components["schemas"]["v1FollowLogsResponse"];
                    };
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    EventsService_WatchEvents: {
        parameters: {
            query?: {
                /** @description 游标：返回 seq > since_seq 的事件（升序）；0 = 从保留窗起点。 */
                since_seq?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response.(streaming responses) */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        result?: components["schemas"]["v1WatchEventsResponse"];
                    };
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    SystemService_ListBackups: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ListBackupsResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    SystemService_TriggerBackup: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["v1TriggerBackupRequest"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1TriggerBackupResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    SystemService_GetIngressStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1GetIngressStatusResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    SystemService_ListNodes: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ListNodesResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    SystemService_Ping: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1PingResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    SystemService_GetSystemStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1GetSystemStatusResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
    PlacementService_ShowPlacement: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ShowPlacementResponse"];
                };
            };
            /** @description An unexpected error response. */
            default: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ErrorResponse"];
                };
            };
        };
    };
}

