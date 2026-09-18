package api

import "strings"

// 方法级 scope 映射（T2.17 鉴权矩阵的唯一登记点）：
//
//	read   — 全部只读面（List/Get/Show/Watch/Follow/History/Status）
//	deploy — 部署/回滚/取消、env 写（env-set 影响下次部署）、构建触发、
//	         漂移收敛与 opt-in 置位（写运行域/影响下次部署语义）
//	admin  — token 管理、env 明文读、app 删除（破坏性）
//
// 纪律：新增 RPC 必须在此登记；未登记方法在拦截器按 admin 拒绝
// （fail-closed，见 auth.go）。
var methodScopes = map[string]string{
	// SystemService
	"/fleetly.server.v1.SystemService/GetSystemStatus":  ScopeRead,
	"/fleetly.server.v1.SystemService/ListNodes":        ScopeRead,
	"/fleetly.server.v1.SystemService/GetIngressStatus": ScopeRead,
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
	"/fleetly.server.v1.BuildsService/TriggerBuild": ScopeDeploy,
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
	"/fleetly.server.v1.LogsService/FollowLogs":      ScopeRead,
	"/fleetly.server.v1.LogsService/ListHistoryLogs": ScopeRead,
	// EventsService
	"/fleetly.server.v1.EventsService/WatchEvents": ScopeRead,
	// PlacementService
	"/fleetly.server.v1.PlacementService/ShowPlacement": ScopeRead,
	// TokensService（管理面整体 admin）
	"/fleetly.server.v1.TokensService/CreateToken": ScopeAdmin,
	"/fleetly.server.v1.TokensService/ListTokens":  ScopeAdmin,
	"/fleetly.server.v1.TokensService/RevokeToken": ScopeAdmin,
	// GitKeysService（git 公钥管理面整体 admin——SSH push 认证凭据，
	// T2.19）
	"/fleetly.server.v1.GitKeysService/AddGitKey":    ScopeAdmin,
	"/fleetly.server.v1.GitKeysService/ListGitKeys":  ScopeAdmin,
	"/fleetly.server.v1.GitKeysService/RemoveGitKey": ScopeAdmin,
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
