// Package cmd 承载 fleetly CLI 全部动词。机器契约：全命令 --json（root
// bool flag，protojson snake_case 输出）、稳定退出码（0 成功/无变化、
// 1 错误、2 有变化（diff/plan 类）、64 用法错误）、错误信封 stderr 渲染
// （errcode + 处置提示 + docs）。
package cmd

import (
	"errors"
	"fmt"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/buildinfo"
)

// 退出码四态（归档仓验证的脚本可分支约定）：
//
//	0  成功（含 diff/plan 无变化）
//	1  命令错误（RPC 失败、校验失败等）
//	2  有变化（diff/plan 检测到漂移；仅 diff 类动词）
//	64 用法错误（EX_USAGE：未知动词/旗标解析失败/缺参数）
const (
	exitChanges = 2
	exitUsage   = 64
)

// errChanges 标记 diff/plan 类动词检测到变化（渲染照常，退出码 2）。
var errChanges = errors.New("changes detected")

// NewApp 装配 CLI 根应用。--json 是全局 root bool flag（commands 框架
// 在任意位置剥取，落 Environment.RootBools["json"]）。
func NewApp(info buildinfo.BuildInfo) *commands.App {
	app := commands.New()
	app.RootBoolFlags = []string{"json"}
	app.HelpHeader = "fleetly - lightweight PaaS for humans and agents"
	app.VerbTitle = "commands:"
	app.HelpFooter = fmt.Sprintf("fleetly %s (run 'fleetly <command> --help' for details)", displayVersion(info))
	app.FlagError = func(verb string, err error) error {
		return fmt.Errorf("%s: bad arguments: %v", verb, err)
	}
	// 错误信封渲染：apperr 还原出 errcode + 处置提示 + docs 链接（Agent
	// 与人类共用的可行动 stderr）；非信封错误默认单行。
	app.RenderError = renderErrorFor
	app.ExitCode = exitCodeFor

	app.Register(
		newVersionCmd(info),
		newStatusCmd(),
		newDoctorVerb(),
		// 身份与访问（Identity 上下文）。
		newLoginVerb(),
		newWhoamiVerb(),
		groupVerb("tokens", "manage tokens (secrets shown once at creation)", newTokensCreateVerb(), newTokensListVerb(), newTokensRevokeVerb()),
		groupVerb("users", "manage users and invitations", newUsersCreateVerb(), newUsersListVerb(), newUsersInviteVerb(), newUsersAcceptVerb()),
		groupVerb("roles", "manage roles (builtin owner/admin/member plus custom)", newRolesCreateVerb(), newRolesListVerb()),
		groupVerb("teams", "manage teams", newTeamsCreateVerb(), newTeamsListVerb()),
		newAuditVerb(),
		// Structure 上下文（动词组：嵌套 Dispatch）。
		groupVerb("projects", "manage projects", newProjectsCreateVerb(), newProjectsListVerb()),
		groupVerb("apps", "manage apps", newAppsCreateVerb(), newAppsListVerb()),
		groupVerb("secrets", "manage project secrets (values never returned)", newSecretsPutVerb(), newSecretsListVerb()),
		groupVerb("configs", "manage versioned config files", newConfigsPutVerb(), newConfigsListVerb()),
		groupVerb("volumes", "manage volumes", newVolumesCreateVerb()),
		groupVerb("networks", "manage project networks", newNetworksCreateVerb()),
		// Delivery 上下文。
		newDeployVerb(),
		groupVerb("deployments", "inspect deployments", newDeploymentsListVerb()),
		newRollbackVerb(),
		groupVerb("revisions", "inspect frozen revisions", newRevisionsListVerb(), newRevisionsDiffVerb()),
		groupVerb("builds", "inspect builds", newBuildsListVerb()),
		groupVerb("hooks", "manage per-app git triggers (secrets shown once at mint/rotate)", newHooksSetVerb(), newHooksGetVerb(), newHooksRotateVerb()),
		// Edge / Runtime 上下文。
		groupVerb("routes", "manage routes", newRoutesCreateVerb(), newRoutesListVerb()),
		groupVerb("nodes", "inspect cluster nodes and enrollment", newNodesListVerb(), newNodesEnrollVerb()),
		// Telemetry 上下文。
		groupVerb("events", "list platform events from the outbox", newEventsListVerb()),
		newLogsVerb(),
	)
	return app
}

// exitCodeFor 把动词错误映射为稳定退出码（机器契约见包注释）。
func exitCodeFor(err error) int {
	if err == nil {
		return commands.ExitOK
	}
	if errors.Is(err, errChanges) {
		return exitChanges
	}
	var unknown *commands.UnknownVerbError
	var usage *commands.UsageError
	if errors.As(err, &unknown) || errors.As(err, &usage) {
		return exitUsage
	}
	return commands.ExitError
}
