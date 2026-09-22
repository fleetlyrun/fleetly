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
         *     admin）。source_branch 同时是 git push 的触发分支（app 配置分支，
         *     默认 main）。
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
    "/v1/apps/{app}/logs/search": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * SearchLogs 统一检索（E6 观测专项设计 §3.1，W5-S1）：日志库
         *     （VictoriaLogs）LogsQL 检索面。keyword 构造为转义后的字面量短语
         *     （用户输入永不裸拼进查询串）；VL 不可达或当前 backend=jsonl 时以
         *     E_LOGS_BACKEND_UNAVAILABLE 诚实报错（不返回空列表冒充）。检索面只
         *     覆盖入湖窗口内的日志（切换前的 JSONL 历史不在检索面——设计 §2.3
         *     「检索不跨界」的诚实边界）。
         */
        get: operations["LogsService_SearchLogs"];
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
    "/v1/logs-backend": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * GetLogsBackend 日志后端视图（W5-S1：mode / 是否显式设置 / 部署态 /
         *     ingest streak / 丢弃计数——CLI `logs backend show` 与 Console 卡共面）。
         */
        get: operations["LogsService_GetLogsBackend"];
        /**
         * SetLogsBackend 切换日志后端（victorialogs | jsonl）：保存即生效——
         *     duty 收敛部署/移除（卷保留），采集路由下拍切换。
         */
        put: operations["LogsService_SetLogsBackend"];
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
    "/v1/system/nodes/join-guide": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * GetJoinGuide join 向导（E1-8，multi-node §2.3/D-MN-13；admin scope
         *     ——响应含 join token 材料）。base_domain 为空 → E_MULTI_NODE_REQUIRES_
         *     BASE_DOMAIN（409，D-MN-13：多节点未启用显式拒绝）。服务端生成 join
         *     命令、按 worker_ip 的精确防火墙放行规则（只生成不自动应用）、DNS
         *     步骤与完成判据；防火墙规则文本附「--harden-firewall 自动应用维持
         *     reserved」口径（平台不静默改用户防火墙）。
         */
        get: operations["SystemService_GetJoinGuide"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/system/nodes/join-token:rotate": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * RotateJoinToken 轮换 swarm join token（E1-8，D-MN-1：锚定完成后自动
         *     rotate 把泄露窗口收敛到分钟级；批量场景 manual 后手动执行）。role
         *     缺省 worker；rotate 后旧 token 立即失效。经底座 swarm 面执行，轮换
         *     记审计（node.join_token_rotated，§5.3——审计不设事件）。
         */
        post: operations["SystemService_RotateJoinToken"];
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
    "/v1/system/s3": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * GetS3Settings 对象存储设置只读面（E3 对象存储 §5.1/E3-2；admin scope
         *     ——端点/桶/凭证指纹属平台敏感配置）。secret 只回 fingerprint（sha256
         *     前 8），绝不回明文；s3.mode=unset 时其余字段为空。
         */
        get: operations["SystemService_GetS3Settings"];
        /**
         * UpdateS3Settings 全量保存对象存储设置（PUT 语义：请求即新状态，空字段
         *     即清空——避免「改 mode 残留旧凭证」的静默状态；secret_access_key 为
         *     明文字段，只写不读，传输面 TLS 承载机密性，持久层 envelope 加密）。
         *     互斥校验 fail-fast：mode=external 必填四项；mode=rustfs 四项必须为空；
         *     public_exposed=true 仅 rustfs 且需 base_domain（E_S3_PUBLIC_REQUIRES_
         *     BASE_DOMAIN）。保存落审计 + 事件 s3.updated（payload 带模式不带走秘密）。
         */
        put: operations["SystemService_UpdateS3Settings"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/system/s3:test": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * TestS3Connection S3 连接探针（E3 对象存储 §2.1 诚实契约：put→get→
         *     delete 一枚探针对象并逐字节比对——通过 = 能认证/能写/能读回，不是 TCP
         *     探活）。可带候选配置（未保存也能测）；全部候选字段为空 = 测已存配置。
         *     探针失败以 E_S3_TEST_FAILED 报错，失败步与底层错误摘要进信封 context。
         */
        post: operations["SystemService_TestS3Connection"];
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
        /**
         * UpdatePlacement 显式换点（E1-7，multi-node §2.6；admin scope——破坏性
         *     确认路径）。目标节点存在且 ready（直读）；有卷应用 data_ack 必填
         *     （restored|discarded，缺省 → E_VOLUME_NODE_MISMATCH——前哨语义前置）；
         *     discarded 需 confirm（→ E_PLACEMENT_MOVE_REQUIRES_ACK）。落库同事务
         *     （绑定换绑 + 卷行 prev 登记 + placement.changed + 审计）；换点不自动
         *     部署，由用户发起部署收敛。
         */
        put: operations["PlacementService_UpdatePlacement"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/placement/migrate-plan": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * GetPlacementMigrationPlan restic 迁移 runbook（E1-7，multi-node §2.8/
         *     D-MN-10；read scope）：服务端生成步骤文档（真实卷名/节点名填充），
         *     restic 备份/恢复由用户在两节点执行——平台不编排远端数据移动。
         */
        get: operations["PlacementService_GetPlacementMigrationPlan"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/volumes": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * ListVolumes 跨 app 卷清单（E1-7，multi-node §2.8；read scope）：active
         *     在册行、orphaned 孤儿行（删除应用保留）与残留指引（prev_platform_
         *     node_id 非空的 active 行派生 residual 标记）。status 过滤缺省输出全部；
         *     不建生命周期 API、不做远端删除（D18）。
         */
        get: operations["PlacementService_ListVolumes"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/cron-runs": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * ListCronRuns 读运行台账（scheduled_at 倒序；service 空 = 该 app 全部
         *     schedule 的行；保留窗每 schedule 最近 20 条，janitor 清理）。
         */
        get: operations["CronService_ListCronRuns"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/services/{service}/trigger": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * TriggerCronRun 手动触发一次（走与到点触发完全相同的链路：重叠 skip、
         *     绑定节点前哨、一次性 job 创建、cron_runs 行与事件；本路径追加审计
         *     cron.manual_triggered）。重叠/节点不可用不报错——响应携带 skipped 行
         *     与原因（与调度器处置一致）。
         */
        post: operations["CronService_TriggerCronRun"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * ListDatabases 库实例列表（name 字典序；deleted tombstone 不进默认列表
         *     ——与 apps 列表同口径）。
         */
        get: operations["DatabaseService_ListDatabases"];
        put?: never;
        /**
         * CreateDatabase 创建库实例（受理即 provisioning——无 created 态；凭据
         *     生成一次、age 密文落库；收敛器异步建现场过健康门 → ready）。
         */
        post: operations["DatabaseService_CreateDatabase"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * GetDatabase 库实例详情（连接信息脱敏投影——密码明文零离开存储；显式
         *     reveal 面随 S4/S6 轮换与 Console 票据）。
         */
        get: operations["DatabaseService_GetDatabase"];
        put?: never;
        post?: never;
        /**
         * DeleteDatabase 删除受理（引用守卫通过后 → deleting tombstone 第一拍；
         *     reap duty 幂等清理受管对象。confirm = 实例名——数据安全两段式确认；
         *     delete_volumes 默认 false = 卷保留转 orphaned，true = 删数据卷不可逆）。
         */
        delete: operations["DatabaseService_DeleteDatabase"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}/backups": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * ListDatabaseBackups 备份台账列表（created_at 降序——恢复目标选择与
         *     Console 备份列表的数据源；kind/snapshot/size/verify_status/error 全量
         *     事实面）。
         */
        get: operations["DatabaseService_ListDatabaseBackups"];
        put?: never;
        /**
         * TriggerDatabaseBackup 手动备份受理（E4 S5，managed-databases §2.6）：
         *     异步受理（job 分钟级——响应即 accepted，结论经台账与 db.backup_* 事件
         *     披露；在途备份无台账行）。合法前置态 ready/degraded（§2.3 操作表）；
         *     per 实例操作互斥（备份/恢复/升级并发第二笔 → 409）。s3.mode=unset →
         *     E_S3_NOT_CONFIGURED（409——诚实拒绝，与注入前哨同码同语义）。
         */
        post: operations["DatabaseService_TriggerDatabaseBackup"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}/credentials": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * RevealDatabaseCredentials 连接信息显式展开（admin scope；§2.5「Console
         *     库详情页展示连接信息，密码默认脱敏、显式展开」的 API 面——含密码明文
         *     与完整 URL；审计 db.reveal 承载敏感访问留痕，不产生事件）。
         */
        get: operations["DatabaseService_RevealDatabaseCredentials"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}/restore": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * RestoreDatabaseBackup 原地恢复受理（破坏性两段式 confirm + 快照归属
         *     守卫：只重放本实例台账内的快照）。停库重放：实例 scale 0 → job 挂卷
         *     rw 重放 → 重部署（异步——结论经 db.restore_* 事件披露；恢复中断 =
         *     实例保持停止 + E_DB_RESTORE_FAILED 事件的 critical 口径，§2.6）。
         */
        post: operations["DatabaseService_RestoreDatabaseBackup"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}/resume": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** ResumeDatabase 恢复（paused → provisioning 重收敛 → ready/degraded）。 */
        post: operations["DatabaseService_ResumeDatabase"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}/retry": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * RetryDatabase 显式重试（failed → provisioning 重收敛；现场保留语义下
         *     失败的唯一出边）。
         */
        post: operations["DatabaseService_RetryDatabase"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}/rotate": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * RotateDatabaseCredentials 凭据轮换（E4 managed-databases §2.5，S4）：
         *     破坏性两段式（confirm = 实例名）。合法前置态 ready/degraded/paused
         *     （§2.3 操作表；PG 暂停期拒绝——postgres 需运行中实例才能 ALTER USER，
         *     如实报 409 并提示先 resume）。引擎侧成功后：引用方 system 物化行回
         *     pending + 平台自动触发全部引用 app 重部署（各自走正常部署队列）。
         */
        post: operations["DatabaseService_RotateDatabaseCredentials"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}/settings": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * UpdateDatabaseSettings 设置变更（限额 + 备份计划；任意非终态准入、主
         *     状态不变；限额变更 = spec 重建由收敛器在下一拍承载）。镜像/引擎参数
         *     受管（违规模板字段不存在于请求——E_DB_TEMPLATE_UNSUPPORTED 面）。
         */
        put: operations["DatabaseService_UpdateDatabaseSettings"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}/suspend": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** SuspendDatabase 暂停（scale 0 保留服务与卷；引用方连不上是诚实暴露）。 */
        post: operations["DatabaseService_SuspendDatabase"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/databases/{name}/upgrade": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * UpgradeDatabase 受控升级受理（E4 S5，managed-databases §2.2）：①
         *     pre_upgrade 备份门（verify 通过才继续——失败实例不动）②digest 换新
         *     受控重建 ③健康门 ④失败 = digest 归位 + db.upgrade_failed + 状态落
         *     degraded。异步受理（备份门与健康门是分钟级）；paused = 仅换 spec 不
         *     重启（resume 时以新版本重建）。合法前置态 ready/degraded/paused。
         */
        post: operations["DatabaseService_UpgradeDatabase"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/secrets": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * ListSecrets 该 app 全部 secret（按 name 字典序；只投影名称/指纹/时间锚
         *     ——值与密文零出现）。
         */
        get: operations["SecretsService_ListSecrets"];
        put?: never;
        /**
         * SetSecret 写入（覆盖即轮换）：值经 age envelope 加密落 app_secrets；
         *     审计 secret.set（diff 只带名称与 hash8 指纹——值零出现）。
         */
        post: operations["SecretsService_SetSecret"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/apps/{app}/secrets/{name}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * RemoveSecret 删除单条（幂等不做：不存在 404；审计 secret.removed）。
         *     已被运行中服务引用的 removal 不追写部署——引用方下次部署 preflight
         *     E_SECRET_NOT_FOUND 诚实失败（移除声明再部署的既有语义）。
         */
        delete: operations["SecretsService_RemoveSecret"];
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
            source_url?: string;
            /** app 配置分支（默认 main）：push 触发与 webhook 拉取共用此分支。 */
            source_branch?: string;
            /** 认证形态：none | https_token | ssh_key。 */
            source_auth_kind?: string;
            /**
             * 认证材料（https_token = token 原文；ssh_key = PEM 私钥）。source_auth_kind =
             *     none 时必须为空；服务端 envelope 加密落库，明文不落、永不回读。
             *     protovalidate 形状约束在服务端用例层按 source_auth_kind 交叉校验（跨字段
             *     规则—— CEL 交叉字段此处不引入，保持 proto 面最小）。
             */
            source_auth_secret?: string;
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
            /** 同记录恢复记录（replay | blocked；空 = 无）。 */
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
            /**
             * 失败所处管线阶段（如 resolve / build / deploy / serve；勿与部署子状态
             *     phase 混用——该字段 2026-09-20 命名审查由 phase 更名 stage）。
             */
            stage?: string;
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
            source_auth_kind?: string;
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
            /** app 配置分支（默认 main）：push 触发与 webhook 拉取共用此分支。 */
            source_branch?: string;
            /** none | https_token | ssh_key。 */
            source_auth_kind?: string;
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
             * 破坏性变更确认门控（架构 §2.4 变更计划/确认语义，MG-C3）：本次部署相对
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
        v1GetLogsBackendResponse: {
            view?: components["schemas"]["v1LogsBackendView"];
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
         * LogsBackendView 是日志后端视图（E6 设计 §2.2/§2.3 诚实口径：模式、
         *     是否显式设置、部署态、ingest streak、丢弃计数常驻可见）。
         */
        v1LogsBackendView: {
            /**
             * 生效模式：victorialogs | jsonl（缺省 victorialogs——V2-1 默认捆绑；
             *     未显式设置时 mode 已投影为缺省值，set 标志区分「缺省生效」）。
             */
            backend?: string;
            /** 该键是否被显式保存过（false = 缺省态生效）。 */
            backend_set?: boolean;
            /**
             * 部署态（backend=victorialogs 时）：deployed（服务在位）| pending
             *     （duty 收敛中）| removed（backend=jsonl 或服务已移除）；面未装配
             *     （测试形态）= unknown。
             */
            deployment?: string;
            /** 入湖 streak 是否降级中（VL 不可达——检索降级，直播不受影响）。 */
            ingest_degraded?: boolean;
            /**
             * 降级 streak 起点（未降级不输出）。
             * Format: date-time
             */
            ingest_degraded_since?: string;
            /**
             * 进程启动以来溢出丢弃的累计行数。
             * Format: uint64
             */
            dropped_total?: string;
        };
        /**
         * SearchLogRow 是检索命中的单行（字段与入湖行对齐：_time/_msg/app/
         *     service/source/stderr——E6 设计 §3.1 行集契约）。
         */
        v1SearchLogRow: {
            /** Format: date-time */
            at?: string;
            app?: string;
            service?: string;
            source?: string;
            stderr?: boolean;
            msg?: string;
            /**
             * 访问行（source=access）的结构化字段透传（W5-S2 设计 §3.2：method/
             *     status/host/path/route/duration_ms/client_ip/deployment_id——入湖
             *     白名单词表内回读；deployment_id 为滚动窗内**近似**归因，多副本滚动
             *     窗内外流量可能分属新旧两代部署）。container/build 行为空。
             */
            fields?: {
                [key: string]: string;
            };
        };
        v1SearchLogsResponse: {
            rows?: components["schemas"]["v1SearchLogRow"][];
            /** 下一页游标（空 = 没有更多命中）。 */
            next_cursor?: string;
        };
        v1SetLogsBackendRequest: {
            backend?: string;
        };
        v1SetLogsBackendResponse: {
            view?: components["schemas"]["v1LogsBackendView"];
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
            /**
             * 远端上传结论（E3-3 上传轨）：none（未上传——s3.mode=unset 合法态或
             *     上传步未执行）/ ok / failed。本地 verify 语义不变（上传失败不回写
             *     verify_status）。
             */
            upload_status?: string;
            /**
             * 最近一次上传尝试的完成时刻（ok/failed 都记；从未尝试不输出）。
             * Format: date-time
             */
            uploaded_at?: string;
            /**
             * 上传失败原因摘要（截断上界在存储层；不含 secret——restic env 凭证
             *     材料禁止进台账/事件/读面）。
             */
            upload_error?: string;
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
        /**
         * FirewallRule 是一条防火墙放行规则文本（方向 + 端口/协议 + 用途 + 可
         *     复制命令；只生成不自动应用——平台不静默改用户防火墙，multi-node §2.3）。
         */
        v1FirewallRule: {
            /** 方向词表：worker_to_manager / bidirectional / public_to_all。 */
            direction?: string;
            /** 端口/协议（如 2377/tcp、7946/tcp+udp、4789/udp、8423/tcp、80,443/tcp）。 */
            port?: string;
            /** 用途（集群管理 / gossip / overlay VXLAN / Traefik 配置端点 TLS / 应用入口）。 */
            purpose?: string;
            /** 规则文本（manager 侧或 worker 侧可复制的 iptables 命令/说明）。 */
            rule?: string;
            /** 规则应用在哪一侧（manager / worker / both）。 */
            side?: string;
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
        v1GetJoinGuideResponse: {
            guide?: components["schemas"]["v1JoinGuideView"];
        };
        v1GetS3SettingsResponse: {
            settings?: components["schemas"]["v1S3SettingsView"];
        };
        v1GetSystemStatusResponse: {
            service?: string;
            version?: string;
            components?: components["schemas"]["v1ComponentHealth"][];
            backup?: components["schemas"]["v1BackupHealth"];
        };
        /** JoinGuideView 是 join 向导输出（服务端生成，multi-node §2.3）。 */
        v1JoinGuideView: {
            /** 完整 join 命令（docker swarm join --token SWMTKN-… <manager-addr>:2377）。 */
            join_command?: string;
            /** manager 可达地址（swarm advertise addr 或请求覆盖值）。 */
            manager_addr?: string;
            /**
             * worker join token（admin scope 的 token 材料；向导后按 join.token_
             *     rotate=auto 自动轮换的口径见 D-MN-1）。
             */
            worker_token?: string;
            /** 平台域名（DNS 步骤的主体）。 */
            base_domain?: string;
            /** manager 侧放行规则（按 worker_ip 生成）。 */
            manager_firewall_rules?: components["schemas"]["v1FirewallRule"][];
            /** worker 侧放行规则。 */
            worker_firewall_rules?: components["schemas"]["v1FirewallRule"][];
            /**
             * worker 前置门禁命令（docker version ≥29.8.1 + iptables legacy 判定
             *     ——与 install.sh 同判据的命令形态；复制到 worker 执行）。
             */
            worker_preflight_commands?: string[];
            /**
             * DNS 步骤（既有应用/平台子域 A 记录追加 worker IP；ctrl.<base> 保持
             *     仅 manager；fleetly domains verify 复核）。
             */
            dns_steps?: string[];
            /**
             * 完成判据（向导自动推进面：节点观测拍出现 → 锚定 node.joined →
             *     ready + Traefik 任务 running）。
             */
            completion_checks?: string[];
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
            /**
             * 绑定其上的应用 ID 清单（E1-8，multi-node §2.7/D-MN-9：读时 join
             *     placements 权威表，无迁移——UI「已钉应用」交叉引用面）。
             */
            pinned_app_ids?: string[];
        };
        v1PingResponse: {
            /** 应答服务名（恒 "fleetlyd"）。 */
            service?: string;
            /** 服务版本（构建 -ldflags 注入，未注入时为 "dev"）。 */
            version?: string;
        };
        v1RotateJoinTokenRequest: {
            /** 轮换目标 token 的角色：worker（缺省）| manager。 */
            role?: string;
        };
        v1RotateJoinTokenResponse: {
            /** 轮换后的角色与新 token（旧 token 立即失效；admin scope 材料）。 */
            role?: string;
            token?: string;
        };
        /**
         * S3ConnectionTestResult 是探针结构化结果：endpoint 回显脱敏（secret 不
         *     回显）、各步耗时、失败步。ok=false 时 failed_step 指向首个失败步。
         */
        v1S3ConnectionTestResult: {
            ok?: boolean;
            endpoint_url?: string;
            region?: string;
            bucket?: string;
            path_style?: boolean;
            steps?: components["schemas"]["v1S3ProbeStep"][];
            failed_step?: string;
        };
        /** S3ProbeStep 是探针单步结果（put/get/delete；诚实契约：失败步可定位）。 */
        v1S3ProbeStep: {
            /** 步骤名：put | get | delete。 */
            step?: string;
            ok?: boolean;
            /**
             * 该步耗时（毫秒）。
             * Format: int64
             */
            duration_ms?: string;
            /** 失败时的底层错误摘要（不含 secret 材料）。 */
            error?: string;
        };
        /**
         * S3SettingsView 是 s3.* 设置的只读投影。secret 只回 fingerprint（明文
         *     sha256 前 8 hex；空 = 未设置）——读面永无明文（写面 UpdateS3Settings
         *     承载 secret 明文，TLS 传输面 + envelope 持久层）。
         */
        v1S3SettingsView: {
            /**
             * 模式词表：unset（缺省，未配置）| external（外部 S3 端点）| rustfs
             *     （托管 RustFS，opt-in）。
             */
            mode?: string;
            /**
             * S3 端点 URL（含 scheme，如 https://s3.amazonaws.com；rustfs 模式下
             *     服务端派生 http://rustfs:9000）。
             */
            endpoint_url?: string;
            region?: string;
            bucket?: string;
            access_key_id?: string;
            /** secret 指纹（sha256 前 8 hex），非 secret 本体。 */
            secret_fingerprint?: string;
            /** path-style 寻址（RustFS/MinIO 类自建端点 true，AWS 虚拟主机式 false）。 */
            path_style?: boolean;
            /** 公网子域开关（仅 rustfs 模式可开；开启后 s3.<base> 公网可达）。 */
            public_exposed?: boolean;
            /**
             * 最近一次保存时刻（从未保存 → 不输出）。
             * Format: date-time
             */
            updated_at?: string;
        };
        v1TestS3ConnectionRequest: {
            /**
             * 候选配置（未保存也能测）：任一字段非零即视为候选配置；全空 = 测已存
             *     配置（s3.mode=unset 时已存配置不存在，拒绝）。
             */
            endpoint_url?: string;
            region?: string;
            bucket?: string;
            access_key_id?: string;
            secret_access_key?: string;
            path_style?: boolean;
        };
        v1TestS3ConnectionResponse: {
            result?: components["schemas"]["v1S3ConnectionTestResult"];
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
        v1UpdateS3SettingsRequest: {
            /** 模式词表（空 = unset）。external↔rustfs 互斥校验见 rpc 注记。 */
            mode?: string;
            endpoint_url?: string;
            region?: string;
            bucket?: string;
            access_key_id?: string;
            /**
             * secret 明文（只写字段；读面只见 fingerprint）。PUT 语义：留空 = 无
             *     secret（切换到 rustfs/unset 时随全量覆写自然清空外部凭证）。
             */
            secret_access_key?: string;
            path_style?: boolean;
            public_exposed?: boolean;
        };
        v1UpdateS3SettingsResponse: {
            settings?: components["schemas"]["v1S3SettingsView"];
        };
        /** UpdatePlacementRequest 是显式换点请求（admin scope；破坏性确认路径）。 */
        PlacementServiceUpdatePlacementBody: {
            /** 目标节点（唯一显示名或平台 ID）。 */
            node?: string;
            /** 数据处置声明：""（无卷应用）| restored | discarded。 */
            data_ack?: string;
            /** 破坏性确认（data_ack=discarded 时必填——源节点数据成为残留）。 */
            confirm?: boolean;
        };
        v1GetPlacementMigrationPlanResponse: {
            app?: string;
            /** 源/目标节点人读形态（hostname (platform ID)）。 */
            from_node?: string;
            to_node?: string;
            /** 涉及的 active 卷。 */
            volumes?: components["schemas"]["v1VolumeView"][];
            /** 顺序步骤（停写 → restic 备份/恢复 → rebind → deploy 收敛 → 残留清理）。 */
            steps?: components["schemas"]["v1MigrationStep"][];
            /** 计划级警示（如目标节点当前非 ready）。 */
            warnings?: string[];
        };
        v1ListVolumesResponse: {
            /** 应用显示名（已删除应用回退显示 app id——与证书台账同口径）。 */
            volumes?: components["schemas"]["v1VolumeView"][];
        };
        /** MigrationStep 是迁移 runbook 的一步（title 短语 + 可复制 detail）。 */
        v1MigrationStep: {
            title?: string;
            detail?: string;
        };
        v1ShowPlacementResponse: {
            app?: string;
            placement?: components["schemas"]["v1PlacementView"];
            /** 卷注册表（无卷应用为空集）。 */
            volumes?: components["schemas"]["v1VolumeView"][];
        };
        v1UpdatePlacementResponse: {
            app?: string;
            placement?: components["schemas"]["v1PlacementView"];
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
            /** 平台节点 ID（与 PlacementView.platform_node_id 同词族）。 */
            platform_node_id?: string;
            mount_path?: string;
            status?: string;
            /**
             * 数据原在节点（E1-7 迁移 00010：显式换点登记的源节点；空 = 从未跨
             *     节点迁移）。指向源节点的残留副本清理指引。
             */
            prev_platform_node_id?: string;
            /**
             * 残留标记（prev_platform_node_id 非空的 active 行派生 = 源节点有
             *     待清理副本，docker volume rm 后平台对账消失；只指引不代删——D18）。
             */
            residual?: boolean;
        };
        CronServiceTriggerCronRunBody: Record<string, never>;
        /**
         * CronRunView 是一次 cron 触发的台账投影（状态词表 started | succeeded |
         *     failed | timeout | skipped；skipped 行带 skip_reason：
         *     overlap | node_unavailable | missed_downtime | interrupted）。
         */
        v1CronRunView: {
            id?: string;
            service?: string;
            expression?: string;
            /**
             * 命中的 cron 点（手动触发 = 触发时刻）。
             * Format: date-time
             */
            scheduled_at?: string;
            /**
             * job 启动时刻（skipped 行不输出）。
             * Format: date-time
             */
            started_at?: string;
            /**
             * 终态收口时刻（在途/skipped 行不输出）。
             * Format: date-time
             */
            finished_at?: string;
            status?: string;
            skip_reason?: string;
            /** 一次性 job 服务名（skipped 行不输出）。 */
            job_service?: string;
            /** 失败/超时原因摘要。 */
            error?: string;
        };
        v1ListCronRunsResponse: {
            app?: string;
            runs?: components["schemas"]["v1CronRunView"][];
        };
        v1TriggerCronRunResponse: {
            run?: components["schemas"]["v1CronRunView"];
        };
        DatabaseServiceRestoreDatabaseBackupBody: {
            /** 恢复目标（restic snapshot 标识——必须在本实例台账内，跨实例误指 422）。 */
            snapshot?: string;
            /**
             * 破坏性确认 = 实例名原样回传（mismatch → 400——原地重放覆盖数据卷上
             *     的现库，与 DeleteDatabase 同型的数据安全面）。
             */
            confirm?: string;
        };
        DatabaseServiceResumeDatabaseBody: Record<string, never>;
        DatabaseServiceRetryDatabaseBody: Record<string, never>;
        /**
         * RotateDatabaseCredentialsRequest 是凭据轮换受理（破坏性两段式：confirm =
         *     实例名原样回传，mismatch → 400——与 DeleteDatabase 同型的数据安全面）。
         */
        DatabaseServiceRotateDatabaseCredentialsBody: {
            confirm?: string;
        };
        DatabaseServiceSuspendDatabaseBody: Record<string, never>;
        DatabaseServiceTriggerDatabaseBackupBody: {
            /**
             * 备份类别（缺省 manual；API 面只受理 manual——daily/pre_upgrade 是平台
             *     调度与升级门的内部类别）。
             */
            kind?: string;
        };
        DatabaseServiceUpdateDatabaseSettingsBody: {
            limits?: components["schemas"]["v1DatabaseLimits"];
            backup_plan?: components["schemas"]["v1DatabaseBackupPlan"];
        };
        DatabaseServiceUpgradeDatabaseBody: {
            /**
             * 破坏性确认 = 实例名原样回传（mismatch → 400——受控重建有停机窗口，
             *     且失败路径触发 digest 归位重建）。
             */
            confirm?: string;
        };
        v1CreateDatabaseRequest: {
            /**
             * 库实例名（^[a-z0-9][a-z0-9_-]*$——与 app 名同字符集规则；对象前缀族
             *     fleetly-db-* 与 app 名族解耦，app 与库实例可重名）。
             */
            name?: string;
            /** 模板 ID（平台内置注册表：postgres-16 / redis-7；未知 → 400）。 */
            template?: string;
            limits?: components["schemas"]["v1DatabaseLimits"];
            backup_plan?: components["schemas"]["v1DatabaseBackupPlan"];
        };
        v1CreateDatabaseResponse: {
            database?: components["schemas"]["v1DatabaseView"];
        };
        /**
         * DatabaseBackupPlan 是备份计划（§5.4 配置键 databases.backup_* 的 per
         *     实例覆盖；0 值字段 = 平台缺省）。
         */
        v1DatabaseBackupPlan: {
            /** Format: int32 */
            interval_hours?: number;
            /** Format: int32 */
            keep?: number;
            /** Format: int32 */
            hour_utc?: number;
        };
        /** DatabaseBackupView 是一行备份台账投影（§2.6 台账裁决的全量事实面）。 */
        v1DatabaseBackupView: {
            id?: string;
            /** 备份类别（daily|manual|pre_upgrade）。 */
            kind?: string;
            /** restic repo 内 snapshot 标识（db/<instance>/ 命名空间寻址，非文件路径）。 */
            snapshot?: string;
            /**
             * 导出流字节量（0 = 未记录）。
             * Format: int64
             */
            size_bytes?: string;
            /** 回读校验状态（unverified|verified|failed——「备份假成功」零容忍）。 */
            verify_status?: string;
            /** 失败/校验失败原因摘要（单行；凭据材料零出现）。 */
            error?: string;
            /** Format: date-time */
            created_at?: string;
        };
        /**
         * DatabaseConnectionView 是连接信息脱敏投影（§2.5 键集的只读面）。url 中
         *     密码段恒为固定掩码（********）——明文零离开存储；host = 实例名 DNS 别名
         *     （引用方 app 内即以此可达）。
         */
        v1DatabaseConnectionView: {
            host?: string;
            /** Format: int32 */
            port?: number;
            /**
             * PG 有 user/database；Redis 不输出（0 值 + 空串在 EmitUnpopulated=false
             *     下不出现）。
             */
            user?: string;
            database?: string;
            /** 掩码 URL（postgres://fleetly:********@<host>:5432/<db> / redis://:********@<host>:6379/0）。 */
            url?: string;
            /** 密码指纹（sha256 前 8 hex——只判「是不是那个值」，材料零出现）。 */
            password_fingerprint?: string;
        };
        /** DatabaseLimits 是资源限额（仅 limits——镜像/引擎参数受管）。 */
        v1DatabaseLimits: {
            /**
             * CPU 限额（核数；0 = 模板缺省）。
             * Format: double
             */
            cpu_seconds?: number;
            /**
             * 内存限额（字节；0 = 模板缺省）。
             * Format: int64
             */
            memory_bytes?: string;
        };
        /**
         * DatabaseView 是库实例投影（状态 = 生命周期态；连接信息脱敏——密码明文
         *     零离开存储，url 已掩码、password_fingerprint 供「是不是那个 secret」比
         *     对；显式 reveal 面随 S4/S6）。last_error 是最近一次收敛失败原因（failed
         *     诊断面；'' = 无失败现场）。
         */
        v1DatabaseView: {
            id?: string;
            name?: string;
            template?: string;
            image_digest?: string;
            /** 生命周期状态位（provisioning|ready|failed|degraded|paused|deleting|deleted）。 */
            status?: string;
            /** 放置绑定（平台节点 ID；空 = 未绑定——provisioning 首拍前）。 */
            placement?: string;
            volume?: components["schemas"]["v1DatabaseVolumeView"];
            connection?: components["schemas"]["v1DatabaseConnectionView"];
            backup_plan?: components["schemas"]["v1DatabaseBackupPlan"];
            limits?: components["schemas"]["v1DatabaseLimits"];
            /** Format: date-time */
            created_at?: string;
            /** Format: date-time */
            updated_at?: string;
            /** Format: date-time */
            credential_updated_at?: string;
            /** 最近一次收敛失败原因（failed/deleting 前的现场快照；空 = 无失败现场）。 */
            last_error?: string;
            /**
             * 可升级位（E4 S5，§2.2 升级语义）：instance.image_digest ≠ 模板当前钉
             *     定镜像 = true（升级逐实例 opt-in——既有实例不自动变）。
             */
            upgrade_available?: boolean;
        };
        v1DatabaseVolumeView: {
            name?: string;
            status?: string;
            /** 数据节点（卷钉住语义——与 placement 一致）。 */
            platform_node_id?: string;
        };
        v1DeleteDatabaseResponse: {
            name?: string;
            status?: string;
        };
        v1GetDatabaseResponse: {
            database?: components["schemas"]["v1DatabaseView"];
        };
        v1ListDatabaseBackupsResponse: {
            backups?: components["schemas"]["v1DatabaseBackupView"][];
        };
        v1ListDatabasesResponse: {
            databases?: components["schemas"]["v1DatabaseView"][];
        };
        /**
         * 异步受理响应：停库重放分钟级——结论经 db.restore_completed /
         *     db.restore_failed 事件与实例 last_error 披露；恢复中断 = 实例保持停止
         *     （人工 runbook 随事件/错误文本）。
         */
        v1RestoreDatabaseBackupResponse: {
            name?: string;
            snapshot?: string;
            /** 恒为 "accepted"。 */
            status?: string;
        };
        v1ResumeDatabaseResponse: {
            database?: components["schemas"]["v1DatabaseView"];
        };
        v1RetryDatabaseResponse: {
            database?: components["schemas"]["v1DatabaseView"];
        };
        /**
         * RevealDatabaseCredentialsResponse 是连接信息的显式展开投影（§2.5 键集
         *     全量 + 密码明文——admin scope 专用面；值只出现在本响应，不进日志/事件/
         *     审计，审计 db.reveal 只记访问事实）。
         */
        v1RevealDatabaseCredentialsResponse: {
            name?: string;
            template?: string;
            host?: string;
            /** Format: int32 */
            port?: number;
            /** PG 有 user/database；Redis 不输出（空串）。 */
            user?: string;
            database?: string;
            /** 密码明文（显式展开面的设计内例外；凭据字符集 [a-zA-Z0-9]）。 */
            password?: string;
            /** 标准 URI（密码段为明文——与 GetEnv 明文读同级的 admin 面）。 */
            url?: string;
        };
        v1RotateDatabaseCredentialsResponse: {
            database?: components["schemas"]["v1DatabaseView"];
            /**
             * 平台自动重部署的引用 app 名单（各自走正常部署队列；credential_updated_at
             *     展示随 database.credential_updated_at 刷新）。
             */
            redeployed_apps?: string[];
        };
        v1SuspendDatabaseResponse: {
            database?: components["schemas"]["v1DatabaseView"];
        };
        /**
         * 异步受理响应：job 分钟级——结论经台账（ListDatabaseBackups）与
         *     db.backup_succeeded / db.backup_failed 事件披露；在途备份无台账行。
         */
        v1TriggerDatabaseBackupResponse: {
            name?: string;
            kind?: string;
            /** 恒为 "accepted"。 */
            status?: string;
        };
        v1UpdateDatabaseSettingsResponse: {
            database?: components["schemas"]["v1DatabaseView"];
        };
        /**
         * 异步受理响应：备份门与健康门是分钟级——结论经 db.upgrade_started/
         *     finished/failed 事件披露。view = 受理时刻投影（upgrade_available 尚为
         *     true；digest 切换随编排推进）。
         */
        v1UpgradeDatabaseResponse: {
            database?: components["schemas"]["v1DatabaseView"];
            /** 恒为 "accepted"。 */
            status?: string;
        };
        SecretsServiceSetSecretBody: {
            /**
             * secret 声明名（compose 服务级 secrets 引用的短名；合法 /run/secrets/<name>
             *     文件名字符集 ^[A-Za-z0-9][A-Za-z0-9._-]*$——与 internal/compose 的
             *     secret 名校验同规则，注释锚互指）。
             */
            name?: string;
            /**
             * 值（明文；age 加密在服务端。上限 64KiB sanity——Swarm secret 单对象
             *     上界 500KB 的宽松内档；长度进形状层即拒，不落日志）。
             */
            value?: string;
        };
        v1ListSecretsResponse: {
            secrets?: components["schemas"]["v1SecretView"][];
        };
        v1RemoveSecretResponse: {
            app?: string;
            name?: string;
        };
        /**
         * SecretView 是平台密钥库条目的只读投影（值/密文/明文零出现——D-DB-7
         *     无值读回；hash8 是唯一的值比对面）。
         */
        v1SecretView: {
            name?: string;
            hash8?: string;
            /** Format: date-time */
            created_at?: string;
            /** Format: date-time */
            updated_at?: string;
        };
        v1SetSecretResponse: {
            app?: string;
            name?: string;
            /** 值指纹（sha256 前 8 hex——「是不是那个值」比对面；值材料零出现）。 */
            hash8?: string;
            /** Format: date-time */
            created_at?: string;
            /** Format: date-time */
            updated_at?: string;
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
                /**
                 * @description 应用名（不存在时自动创建——与 CLI deploy 同语义：应用随首次部署创建）。
                 *     compose 应用名与请求 app 必须一致（A1：不一致 → E_COMPOSE_UNSUPPORTED，
                 *     不误建 app、不入队）。
                 */
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
    LogsService_SearchLogs: {
        parameters: {
            query?: {
                /**
                 * @description 可选的 app 过滤集（预留跨应用语义；当前检索面为单 app，额外值不
                 *     放行——诚实边界）。
                 */
                apps?: string[];
                /** @description 全文关键词（构造为转义后的 LogsQL 字面量短语——注入安全硬性条款）。 */
                keyword?: string;
                /** @description 时间窗下界；缺省 = 不设下界（保留窗即 VL -retentionPeriod）。 */
                time_start?: string;
                /** @description 时间窗上界；缺省 = 现在。 */
                time_end?: string;
                /** @description compose 服务名过滤集。 */
                services?: string[];
                /**
                 * @description 来源过滤集：container | build | access（access 随 W5-S2 访问日志
                 *     采集进入词表）。
                 */
                sources?: string[];
                /** @description 返回上限（缺省 200，天花板 1000）。 */
                limit?: number;
                /**
                 * @description 分页游标（服务端签发的下一页凭证；空 = 第一页）。游标分页自最新
                 *     命中向后走（VL limit/offset 语义）。
                 */
                cursor?: string;
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
                    "application/json": components["schemas"]["v1SearchLogsResponse"];
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
    LogsService_GetLogsBackend: {
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
                    "application/json": components["schemas"]["v1GetLogsBackendResponse"];
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
    LogsService_SetLogsBackend: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["v1SetLogsBackendRequest"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1SetLogsBackendResponse"];
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
    SystemService_GetJoinGuide: {
        parameters: {
            query?: {
                /**
                 * @description worker 节点的公网 IP（防火墙规则按它生成精确放行文本；空 = 输出
                 *     规则模板、IP 位以 <worker-ip> 占位）。
                 */
                worker_ip?: string;
                /**
                 * @description manager 可达地址覆盖（advertise 为私网而 worker 跨公网的场景；空 =
                 *     取 swarm advertise addr）。
                 */
                manager_addr?: string;
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
                    "application/json": components["schemas"]["v1GetJoinGuideResponse"];
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
    SystemService_RotateJoinToken: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["v1RotateJoinTokenRequest"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1RotateJoinTokenResponse"];
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
    SystemService_GetS3Settings: {
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
                    "application/json": components["schemas"]["v1GetS3SettingsResponse"];
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
    SystemService_UpdateS3Settings: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["v1UpdateS3SettingsRequest"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1UpdateS3SettingsResponse"];
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
    SystemService_TestS3Connection: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["v1TestS3ConnectionRequest"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1TestS3ConnectionResponse"];
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
    PlacementService_UpdatePlacement: {
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
                "application/json": components["schemas"]["PlacementServiceUpdatePlacementBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1UpdatePlacementResponse"];
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
    PlacementService_GetPlacementMigrationPlan: {
        parameters: {
            query?: {
                /** @description 目标节点（唯一显示名或平台 ID）。 */
                to?: string;
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
                    "application/json": components["schemas"]["v1GetPlacementMigrationPlanResponse"];
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
    PlacementService_ListVolumes: {
        parameters: {
            query?: {
                /** @description 状态过滤（active|orphaned|discarded；空 = 输出全部）。 */
                status?: string;
                /** @description 残留过滤（true = 只输出 residual 行）。 */
                residual?: boolean;
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
                    "application/json": components["schemas"]["v1ListVolumesResponse"];
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
    CronService_ListCronRuns: {
        parameters: {
            query?: {
                /** @description 收窄到单 schedule（空 = 全部）。 */
                service?: string;
                /** @description 行数上限（缺省 20；≤20 的量级面，无分页游标）。 */
                limit?: number;
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
                    "application/json": components["schemas"]["v1ListCronRunsResponse"];
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
    CronService_TriggerCronRun: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 应用名（compose 应用名）。 */
                app: string;
                /** @description compose 服务名（必须声明 fleetly.cron，否则 404 语义）。 */
                service: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CronServiceTriggerCronRunBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1TriggerCronRunResponse"];
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
    DatabaseService_ListDatabases: {
        parameters: {
            query?: {
                /** @description 行数上限（缺省 100）。 */
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
                    "application/json": components["schemas"]["v1ListDatabasesResponse"];
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
    DatabaseService_CreateDatabase: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["v1CreateDatabaseRequest"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1CreateDatabaseResponse"];
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
    DatabaseService_GetDatabase: {
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
                    "application/json": components["schemas"]["v1GetDatabaseResponse"];
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
    DatabaseService_DeleteDatabase: {
        parameters: {
            query?: {
                /**
                 * @description 破坏性确认 = 实例名原样回传（mismatch → 400——与卷-节点 409 前哨同
                 *     型的数据安全面；引用 app 在册 → 409 E_DB_REFERENCED 先行）。
                 */
                confirm?: string;
                /** @description 删除数据卷（默认 false = 保留转 orphaned；true = 不可逆删除）。 */
                delete_volumes?: boolean;
            };
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
                    "application/json": components["schemas"]["v1DeleteDatabaseResponse"];
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
    DatabaseService_ListDatabaseBackups: {
        parameters: {
            query?: {
                /** @description 行数上限（缺省 20）。 */
                limit?: number;
            };
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
                    "application/json": components["schemas"]["v1ListDatabaseBackupsResponse"];
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
    DatabaseService_TriggerDatabaseBackup: {
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
                "application/json": components["schemas"]["DatabaseServiceTriggerDatabaseBackupBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1TriggerDatabaseBackupResponse"];
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
    DatabaseService_RevealDatabaseCredentials: {
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
                    "application/json": components["schemas"]["v1RevealDatabaseCredentialsResponse"];
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
    DatabaseService_RestoreDatabaseBackup: {
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
                "application/json": components["schemas"]["DatabaseServiceRestoreDatabaseBackupBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1RestoreDatabaseBackupResponse"];
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
    DatabaseService_ResumeDatabase: {
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
                "application/json": components["schemas"]["DatabaseServiceResumeDatabaseBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1ResumeDatabaseResponse"];
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
    DatabaseService_RetryDatabase: {
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
                "application/json": components["schemas"]["DatabaseServiceRetryDatabaseBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1RetryDatabaseResponse"];
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
    DatabaseService_RotateDatabaseCredentials: {
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
                "application/json": components["schemas"]["DatabaseServiceRotateDatabaseCredentialsBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1RotateDatabaseCredentialsResponse"];
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
    DatabaseService_UpdateDatabaseSettings: {
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
                "application/json": components["schemas"]["DatabaseServiceUpdateDatabaseSettingsBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1UpdateDatabaseSettingsResponse"];
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
    DatabaseService_SuspendDatabase: {
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
                "application/json": components["schemas"]["DatabaseServiceSuspendDatabaseBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1SuspendDatabaseResponse"];
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
    DatabaseService_UpgradeDatabase: {
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
                "application/json": components["schemas"]["DatabaseServiceUpgradeDatabaseBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1UpgradeDatabaseResponse"];
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
    SecretsService_ListSecrets: {
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
                    "application/json": components["schemas"]["v1ListSecretsResponse"];
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
    SecretsService_SetSecret: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 归属 app 名。 */
                app: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SecretsServiceSetSecretBody"];
            };
        };
        responses: {
            /** @description A successful response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["v1SetSecretResponse"];
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
    SecretsService_RemoveSecret: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                app: string;
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
                    "application/json": components["schemas"]["v1RemoveSecretResponse"];
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

