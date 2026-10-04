package identity

// 内置角色（F0.5）：owner/admin/member 三角色是平台级模板（team_id 空、
// builtin=1、固定 slug ID）。资源清单由调用方注入（assembly 的
// scopeResources 单一事实源），本包不复制词表——漂移在构造期爆掉。

import (
	"sort"
)

// 内置角色固定 ID（slug 形态，可 grep；启动种子幂等锚）。
const (
	RoleOwnerID  = "builtin-owner"
	RoleAdminID  = "builtin-admin"
	RoleMemberID = "builtin-member"

	// DefaultTeamID 是种子 default Team 的固定 ID（Project 归属缺省值，
	// 与 structure 服务的缺省一致）。
	DefaultTeamID = "default"

	// BootstrapTokenName 是首启引导 Token 的保留名（journal 去重与吊销
	// 面的识别锚）。
	BootstrapTokenName = "bootstrap"
)

// memberWriteResources 是 member 角色的写面：部署与材料（"能部署不能拆
// 家、不能管人"——小微团队的开发者形态）。不含 projects/apps 的建删、
// nodes、identity 面与 audit。
var memberWriteResources = map[string]bool{
	"deployments": true,
	"builds":      true,
	"tasks":       true,
	"secrets":     true,
	"configs":     true,
	// 共享变量是部署材料（ADR-0043）：与 secrets/configs 同桶——member
	// 可写不可拆家（project/app 建删仍不开放）。
	"shared_variables": true,
	"volumes":          true,
	"networks":         true,
	"routes":           true,
}

// memberExcludedResources 是 member 角色完全不获得的资源集（"不能管人、
// 不能拆家"）：用户/角色/Token/邀请/审计是管理面（读权也不给）；platform
// 是集群面（活 join token 等价集群成员权——C3 收紧，member 与自定义
// nodes 域角色都不得沾）。这不是词表复制：词表演进时未知资源一律按
// 业务面处理。
var memberExcludedResources = map[string]bool{
	"users": true, "teams": true, "roles": true,
	"tokens": true, "invitations": true, "audit": true,
	"platform": true,
}

// BuiltinRole 是一条内置角色定义。
type BuiltinRole struct {
	ID     string
	Name   string
	Scopes []Scope
}

// BuiltinRoles 依资源清单构造三条内置角色：
//   - owner  = `*`（全权）
//   - admin  = 全资源 `:admin`（audit 只 `:read`——审计只读面，admin 无
//     额外语义）
//   - member = 业务资源 `:read` + memberWriteResources 的 `:write`；
//     identity 资源（users/teams/roles/tokens/invitations/audit）不进入
//     member——管理面读权也不给（WhoAmI 是 PUBLIC 档不受影响）
func BuiltinRoles(resources []string) []BuiltinRole {
	sorted := append([]string(nil), resources...)
	sort.Strings(sorted)
	admin, member := make([]Scope, 0, len(sorted)), make([]Scope, 0, len(sorted))
	for _, res := range sorted {
		if res == "audit" {
			admin = append(admin, Scope{Resource: res, Action: "read"})
		} else {
			admin = append(admin, Scope{Resource: res, Action: "admin"})
		}
		if memberExcludedResources[res] {
			continue
		}
		member = append(member, Scope{Resource: res, Action: "read"})
		if memberWriteResources[res] {
			member = append(member, Scope{Resource: res, Action: "write"})
		}
	}
	return []BuiltinRole{
		{ID: RoleOwnerID, Name: "owner", Scopes: []Scope{{Resource: "*", Action: "*"}}},
		{ID: RoleAdminID, Name: "admin", Scopes: admin},
		{ID: RoleMemberID, Name: "member", Scopes: member},
	}
}
