package api

import "strings"

// 方法级 scope 映射（T2.17 鉴权矩阵的唯一登记点）：
//
//	read   — 全部只读面（List/Get/Show/Watch/Follow/History/Status）
//	deploy — 部署/回滚/取消、env 写（env-set 影响下次部署）
//	admin  — token 管理、env 明文读、app 删除（破坏性）
//
// 纪律：新增 RPC 必须在此登记；未登记方法在拦截器按 admin 拒绝
// （fail-closed，见 auth.go）。
var methodScopes = map[string]string{
	// SystemService
	"/fleetly.server.v1.SystemService/GetSystemStatus": ScopeRead,
	// AppsService
	"/fleetly.server.v1.AppsService/ListApps":  ScopeRead,
	"/fleetly.server.v1.AppsService/GetApp":    ScopeRead,
	"/fleetly.server.v1.AppsService/DeleteApp": ScopeAdmin,
	// DeploymentsService
	"/fleetly.server.v1.DeploymentsService/ListDeployments":    ScopeRead,
	"/fleetly.server.v1.DeploymentsService/GetDeployment":      ScopeRead,
	"/fleetly.server.v1.DeploymentsService/Deploy":             ScopeDeploy,
	"/fleetly.server.v1.DeploymentsService/CancelDeployment":   ScopeDeploy,
	"/fleetly.server.v1.DeploymentsService/RollbackDeployment": ScopeDeploy,
	// RevisionsService
	"/fleetly.server.v1.RevisionsService/ListRevisions": ScopeRead,
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
