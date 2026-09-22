package api

import "strings"

// 方法级 scope 映射（T2.17 鉴权矩阵的唯一登记点）：
//
//	read   — 全部只读面（List/Get/Show/Watch/Follow/History/Status）
//	deploy — 部署/回滚/取消、env 写（env-set 影响下次部署）、漂移收敛与
//	         opt-in 置位（写运行域/影响下次部署语义）
//	admin  — token 管理、env 明文读、app 删除（破坏性）、构建触发（H14
//	         整改：TriggerBuild 的 base_dir 可指向宿主任意目录，构建会把
//	         整目录打进镜像——宿主文件系统读取面与 env 明文同级信任，
//	         不再随 deploy scope 下放）
//
// 纪律：新增 RPC 必须在此登记；未登记方法在拦截器按 admin 拒绝
// （fail-closed，见 auth.go）。
var methodScopes = map[string]string{
	// SystemService
	"/fleetly.server.v1.SystemService/GetSystemStatus":  ScopeRead,
	"/fleetly.server.v1.SystemService/ListNodes":        ScopeRead,
	"/fleetly.server.v1.SystemService/GetIngressStatus": ScopeRead,
	// join 向导面（E1-8，multi-node §2.3）：guide 响应含 join token 材料 =
	// admin；token 轮换 = admin（安全面写操作，D-MN-1）。
	"/fleetly.server.v1.SystemService/GetJoinGuide":    ScopeAdmin,
	"/fleetly.server.v1.SystemService/RotateJoinToken": ScopeAdmin,
	// 备份面（T2.22）：台账只读；手动触发 = 写面语义（与升级编排的
	// pre_upgrade 快照共用入口），取 deploy scope。
	"/fleetly.server.v1.SystemService/ListBackups":   ScopeRead,
	"/fleetly.server.v1.SystemService/TriggerBackup": ScopeDeploy,
	// S3 设置面（E3-2，对象存储 §5.1）：整体 admin——端点/桶/凭证指纹属
	// 平台敏感配置，secret 明文只写（Update）与凭证解密（Test）是平台
	// 信任面，与 env 明文读同级，不随 deploy/read 下放。
	"/fleetly.server.v1.SystemService/GetS3Settings":    ScopeAdmin,
	"/fleetly.server.v1.SystemService/UpdateS3Settings": ScopeAdmin,
	"/fleetly.server.v1.SystemService/TestS3Connection": ScopeAdmin,
	// AppsService
	"/fleetly.server.v1.AppsService/ListApps":  ScopeRead,
	"/fleetly.server.v1.AppsService/GetApp":    ScopeRead,
	"/fleetly.server.v1.AppsService/DeleteApp": ScopeAdmin,
	// webhook/git 触发配置面（T2.19；secret 与认证材料写面 = admin——
	// 验签是该端点的唯一认证，材料属平台敏感面）。
	"/fleetly.server.v1.AppsService/SetAppWebhookSecret": ScopeAdmin,
	"/fleetly.server.v1.AppsService/ShowAppWebhook":      ScopeAdmin,
	"/fleetly.server.v1.AppsService/SetAppSource":        ScopeAdmin,
	// DeploymentsService
	"/fleetly.server.v1.DeploymentsService/ListDeployments":    ScopeRead,
	"/fleetly.server.v1.DeploymentsService/GetDeployment":      ScopeRead,
	"/fleetly.server.v1.DeploymentsService/Deploy":             ScopeDeploy,
	"/fleetly.server.v1.DeploymentsService/CancelDeployment":   ScopeDeploy,
	"/fleetly.server.v1.DeploymentsService/RollbackDeployment": ScopeDeploy,
	// DeployFromGit（T2.19）：post-receive 钩子经 hook token（deploy
	// scope）回调——最小权限，与 Deploy 同级。
	"/fleetly.server.v1.DeploymentsService/DeployFromGit": ScopeDeploy,
	// RevisionsService
	"/fleetly.server.v1.RevisionsService/ListRevisions":   ScopeRead,
	"/fleetly.server.v1.RevisionsService/GetRevisionSpec": ScopeRead,
	// BuildsService
	// TriggerBuild = admin（H14 宿主目录信任边界）：base_dir 显式提供时可
	// 指向宿主任意目录（SQLite 库、age 密钥材料同位），构建把整目录打进
	// 镜像再经部署外带——比 deploy 多出宿主文件系统逃逸面，与 env 明文
	// 读取（GetEnv=admin）同级信任。读面（GetBuild/ListBuilds）不变。
	"/fleetly.server.v1.BuildsService/TriggerBuild": ScopeAdmin,
	"/fleetly.server.v1.BuildsService/GetBuild":     ScopeRead,
	"/fleetly.server.v1.BuildsService/ListBuilds":   ScopeRead,
	// DriftService
	"/fleetly.server.v1.DriftService/ShowDrift":        ScopeRead,
	"/fleetly.server.v1.DriftService/ConvergeDrift":    ScopeDeploy,
	"/fleetly.server.v1.DriftService/SetDriftConverge": ScopeDeploy,
	// DomainsService
	"/fleetly.server.v1.DomainsService/ListAppDomains":   ScopeRead,
	"/fleetly.server.v1.DomainsService/VerifyAppDomains": ScopeRead,
	// EnvService
	"/fleetly.server.v1.EnvService/ListEnv":   ScopeRead,
	"/fleetly.server.v1.EnvService/SetEnv":    ScopeDeploy,
	"/fleetly.server.v1.EnvService/RemoveEnv": ScopeDeploy,
	// GetEnv（明文）= admin——取舍注记见 env.proto。
	"/fleetly.server.v1.EnvService/GetEnv": ScopeAdmin,
	// LogsService
	// SearchLogs（E6 W5-S1）：read——与 FollowLogs 同级（能看直播就能看
	// 检索，E6 设计 §3.1 权限原文）。
	"/fleetly.server.v1.LogsService/FollowLogs":      ScopeRead,
	"/fleetly.server.v1.LogsService/ListHistoryLogs": ScopeRead,
	"/fleetly.server.v1.LogsService/SearchLogs":      ScopeRead,
	// 日志后端面（E6 W5-S1）：show = read（运行视图）；set = deploy
	//（写运行域语义——切换触发 duty 收敛与采集路由翻转，与
	// SetDriftConverge 的 opt-in 置位同级；无凭据材料，不到 admin）。
	"/fleetly.server.v1.LogsService/GetLogsBackend": ScopeRead,
	"/fleetly.server.v1.LogsService/SetLogsBackend": ScopeDeploy,
	// MetricsService（E6 W5-S3，D-W5-2 opt-in）：查询与状态 = read
	//（PromQL 透传是操作员工具——设计 §4.2；能看日志检索就能查指标）；
	// 模式切换 = deploy（写运行域语义——opt-in 置位触发三件套部署/移除，
	// 与 SetLogsBackend 同级理由；无凭据材料，不到 admin）。
	"/fleetly.server.v1.MetricsService/SearchMetrics":    ScopeRead,
	"/fleetly.server.v1.MetricsService/GetMetricsStatus": ScopeRead,
	"/fleetly.server.v1.MetricsService/SetMetricsMode":   ScopeDeploy,
	// EventsService
	"/fleetly.server.v1.EventsService/WatchEvents": ScopeRead,
	// PlacementService
	// ShowPlacement/ListVolumes/GetPlacementMigrationPlan = read（只读面，
	// E1-7）；UpdatePlacement = admin（破坏性确认路径，multi-node §2.6）。
	"/fleetly.server.v1.PlacementService/ShowPlacement":             ScopeRead,
	"/fleetly.server.v1.PlacementService/UpdatePlacement":           ScopeAdmin,
	"/fleetly.server.v1.PlacementService/ListVolumes":               ScopeRead,
	"/fleetly.server.v1.PlacementService/GetPlacementMigrationPlan": ScopeRead,
	// TokensService（管理面整体 admin）
	"/fleetly.server.v1.TokensService/CreateToken": ScopeAdmin,
	"/fleetly.server.v1.TokensService/ListTokens":  ScopeAdmin,
	"/fleetly.server.v1.TokensService/RevokeToken": ScopeAdmin,
	// GitKeysService（git 公钥管理面整体 admin——SSH push 认证凭据，
	// T2.19）
	"/fleetly.server.v1.GitKeysService/AddGitKey":    ScopeAdmin,
	"/fleetly.server.v1.GitKeysService/ListGitKeys":  ScopeAdmin,
	"/fleetly.server.v1.GitKeysService/RemoveGitKey": ScopeAdmin,
	// CronService（E5 Cron）：手动触发 = 写面语义（与 Deploy 同级——触发
	// 的是应用自身的 compose 声明，不新增权限面）；台账读面 = read。
	"/fleetly.server.v1.CronService/TriggerCronRun": ScopeDeploy,
	"/fleetly.server.v1.CronService/ListCronRuns":   ScopeRead,
	// DatabaseService（E4 数据库托管，managed-databases §2.3）：get/list =
	// read（连接投影脱敏——明文 reveal 属 admin 更严面，随 S4/S6）；生命周期
	// 与设置写面（create/delete/suspend/resume/retry/settings）= admin
	// ——delete 是数据安全破坏性操作（引用守卫 + confirm 两段式），settings
	// 直改资源限额/备份计划，与 app 删除同级信任，不随 deploy 下放。
	"/fleetly.server.v1.DatabaseService/GetDatabase":            ScopeRead,
	"/fleetly.server.v1.DatabaseService/ListDatabases":          ScopeRead,
	"/fleetly.server.v1.DatabaseService/CreateDatabase":         ScopeAdmin,
	"/fleetly.server.v1.DatabaseService/DeleteDatabase":         ScopeAdmin,
	"/fleetly.server.v1.DatabaseService/SuspendDatabase":        ScopeAdmin,
	"/fleetly.server.v1.DatabaseService/ResumeDatabase":         ScopeAdmin,
	"/fleetly.server.v1.DatabaseService/RetryDatabase":          ScopeAdmin,
	"/fleetly.server.v1.DatabaseService/UpdateDatabaseSettings": ScopeAdmin,
	// E4 W4-S4（managed-databases §2.5）：rotate = 破坏性两段式数据安全操作
	// （引用 app 被自动重部署），admin 与 delete 同级；reveal = 密码明文显式
	// 展开（admin 更严面——与 env GetEnv 同级信任）。
	"/fleetly.server.v1.DatabaseService/RotateDatabaseCredentials": ScopeAdmin,
	"/fleetly.server.v1.DatabaseService/RevealDatabaseCredentials": ScopeAdmin,
	// E4 W4-S5（managed-databases §2.6）：备份列表 = read（台账只读事实面）；
	// 备份触发/恢复/升级 = admin（恢复与升级是破坏性两段式数据安全操作——
	// 原地重放覆盖数据卷、受控重建有停机窗口，与 delete 同级；备份触发直写
	// 远端 repo，写面语义与平台备份 TriggerBackup 同口径）。
	"/fleetly.server.v1.DatabaseService/ListDatabaseBackups":   ScopeRead,
	"/fleetly.server.v1.DatabaseService/TriggerDatabaseBackup": ScopeAdmin,
	"/fleetly.server.v1.DatabaseService/RestoreDatabaseBackup": ScopeAdmin,
	"/fleetly.server.v1.DatabaseService/UpgradeDatabase":       ScopeAdmin,
	// SecretsService（E4 W4-S4，D-DB-7）：set/remove = admin（密钥写面与
	// webhook secret/env 明文同级信任）；list = read（只出名称/指纹——与
	// ListEnv 同口径，值零出现）。
	"/fleetly.server.v1.SecretsService/SetSecret":    ScopeAdmin,
	"/fleetly.server.v1.SecretsService/ListSecrets":  ScopeRead,
	"/fleetly.server.v1.SecretsService/RemoveSecret": ScopeAdmin,
}

// RequiredScope 返回方法所需 scope（未登记返回 false——调用方按 admin
// 拒绝路径处理）。
func RequiredScope(fullMethod string) (string, bool) {
	s, ok := methodScopes[fullMethod]
	return s, ok
}

// methodAction 是审计 action 的方法级词根（api.<Service>.<Method>——审计
// action 记 token/方法级，不发明新事件名）。
func methodAction(fullMethod string) string {
	trimmed := strings.TrimPrefix(fullMethod, "/fleetly.server.v1.")
	return "api." + strings.ReplaceAll(trimmed, "/", ".")
}
