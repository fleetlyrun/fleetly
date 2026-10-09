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
	// 旗标解析错误保持 UsageError 类型（退出码 64 契约靠类型分支；裸
	// fmt.Errorf 会把用法错误降级成普通错误退 1——A5 断言钉死）。
	app.FlagError = func(verb string, err error) error {
		return &commands.UsageError{Usage: verb, Err: fmt.Errorf("%s: bad arguments: %v", verb, err)}
	}
	// 错误信封渲染：apperr 还原出 errcode + 处置提示 + docs 链接（Agent
	// 与人类共用的可行动 stderr）；非信封错误默认单行。
	app.RenderError = renderErrorFor
	app.ExitCode = exitCodeFor

	app.Register(
		newVersionCmd(info),
		newStatusCmd(),
		newDoctorVerb(),
		// 能力自描述（F1.4，架构 §7：Agent 的零文档发现面）。
		newSchemaCmd(),
		newExplainVerb(),
		// 身份与访问（Identity 上下文）。
		newInitVerb(),
		newLoginVerb(),
		newWhoamiVerb(),
		groupVerb("tokens", "manage tokens (secrets shown once at creation)", newTokensCreateVerb(), newTokensListVerb(), newTokensRevokeVerb()),
		groupVerb("users", "manage users and invitations", newUsersCreateVerb(), newUsersListVerb(), newUsersSetPasswordVerb(), newUsersInviteVerb(), newUsersAcceptVerb()),
		groupVerb("roles", "manage roles (builtin owner/admin/member plus custom)", newRolesCreateVerb(), newRolesListVerb()),
		groupVerb("teams", "manage teams", newTeamsCreateVerb(), newTeamsListVerb()),
		newAuditVerb(),
		// Governance 上下文（F1.9：Change Freeze 治理刹车）。
		groupVerb("freeze", "manage change freezes (refuse change verbs with a reason until lifted)",
			newFreezeSetVerb(), newFreezeLiftVerb(), newFreezeListVerb()),
		// Structure 上下文（动词组：嵌套 Dispatch）。
		groupVerb("projects", "manage projects", newProjectsCreateVerb(), newProjectsListVerb(), newProjectsDeleteVerb()),
		groupVerb("apps", "manage apps", newAppsCreateVerb(), newAppsListVerb(), newAppsSpecVerb(), newAppsDeleteVerb()),
		groupVerb("secrets", "manage project secrets (values never returned)", newSecretsPutVerb(), newSecretsListVerb()),
		groupVerb("configs", "manage versioned config files", newConfigsPutVerb(), newConfigsListVerb()),
		// 共享变量（F2.9，ADR-0043）：Project 级变量层——归一化期合成进
		// 部署（App 层 env 覆盖同键）；改共享变量需重部署才生效（响应提示
		// 受影响 App）。
		groupVerb("shared-variables", "manage project shared variables (merged under app-level env at deploy; values are returned)",
			newSharedVarsPutVerb(), newSharedVarsListVerb(), newSharedVarsDeleteVerb()),
		groupVerb("volumes", "manage volumes", newVolumesCreateVerb()),
		groupVerb("networks", "manage project networks and cross-project peer attachments",
			newNetworksCreateVerb(), newNetworksListVerb(), newNetworksRebuildVerb(), newNetworksDeclareVerb(), newNetworksApproveVerb(),
			newNetworksRevokeVerb(), newNetworksPeersVerb()),
		// 托管数据服务（F1.12，ADR-0029；备份动词 F2.2/ADR-0039）。
		groupVerb("databases", "manage managed data services (postgres/pgvector/redis/mysql/mongo templates; credential values never shown)",
			newDatabasesCreateVerb(), newDatabasesListVerb(), newDatabasesGetVerb(), newDatabasesDeleteVerb(),
			newDatabasesBackupVerb(), newDatabasesBackupsVerb(), newDatabasesVerifyVerb(), newDatabasesBrowseVerb()),
		// Delivery 上下文。
		newDeployVerb(),
		groupVerb("deployments", "inspect, wait for and cancel deployments", newDeploymentsListVerb(), newDeploymentsGetVerb(), newDeploymentsWaitVerb(), newDeploymentsCancelVerb()),
		newRollbackVerb(),
		groupVerb("revisions", "inspect frozen revisions", newRevisionsListVerb(), newRevisionsDiffVerb()),
		groupVerb("builds", "inspect and wait for builds, stream build logs", newBuildsListVerb(), newBuildsWaitVerb(), newBuildsLogsVerb()),
		groupVerb("uploads", "upload and list build source directories (content-addressed; re-uploads deduplicate)",
			newUploadsPutVerb(), newUploadsListVerb()),
		groupVerb("hooks", "manage per-app git triggers (secrets shown once at mint/rotate)", newHooksSetVerb(), newHooksGetVerb(), newHooksRotateVerb()),
		// Automation 上下文（F1.5/F1.6/F1.7：Task 双形态 + Owner Lease +
		// WaitRun + 时区 cron Schedule）。
		groupVerb("tasks", "manage programmatic workloads (one-shot executions and resident instance pools)",
			newTasksCreateVerb(), newTasksListVerb(), newTasksGetVerb(), newTasksScaleVerb(),
			newTasksStopVerb(), newTasksDeleteVerb(), newTasksRenewVerb()),
		groupVerb("runs", "inspect and control task runs",
			newRunsListVerb(), newRunsGetVerb(), newRunsStopVerb(), newRunsWaitVerb()),
		groupVerb("schedules", "manage timezone-aware cron schedules firing one-shot tasks",
			newSchedulesCreateVerb(), newSchedulesListVerb(), newSchedulesGetVerb(),
			newSchedulesTriggerVerb(), newSchedulesDeleteVerb()),
		// Proxy / Runtime 上下文。
		newQuickstartVerb(),
		// App 模板目录（F3.3，ADR-0050）：目录读面 + 一键部署 + 操作员刷新。
		groupVerb("templates", "manage the app template catalog (one-click deploy; values for secret variables are platform-generated)",
			newTemplatesListVerb(), newTemplatesShowVerb(), newTemplatesInstantiateVerb(), newTemplatesRefreshVerb()),
		newCreateFromDokployVerb(),
		groupVerb("routes", "manage routes", newRoutesCreateVerb(), newRoutesListVerb()),
		groupVerb("nodes", "inspect cluster nodes and administer scheduling", newNodesListVerb(), newNodesEnrollVerb(),
			newNodesDrainVerb(), newNodesCordonVerb(), newNodesUncordonVerb()),
		// Telemetry 上下文。
		groupVerb("events", "list and follow platform events from the outbox", newEventsListVerb(), newEventsFollowVerb()),
		newLogsVerb(),
		// Exec 子面（F3.2，ADR-0049）：one-shot 命令（退出码透传）与交互
		// TTY——机器与人机同面。
		newExecVerb(),
		newShellVerb(),
		// Metrics/Alerting 上下文（F2.5，ADR-0041）。
		groupVerb("metrics", "query the managed metrics store (PromQL pass-through)", newMetricsQueryVerb()),
		groupVerb("channels", "manage notification channels for alerting (credentials are write-only)",
			newChannelsCreateVerb(), newChannelsTestVerb(), newChannelsListVerb(), newChannelsDeleteVerb()),
		groupVerb("alerts", "manage threshold alert rules and list current alert states",
			groupVerb("rules", "manage threshold alert rules",
				newAlertsRulesCreateVerb(), newAlertsRulesListVerb(), newAlertsRulesDeleteVerb()),
			newAlertsListVerb()),
		// Platform 上下文（F2.3，ADR-0039 决策 10）：平台级操作面——
		// Platform Backup 手动触发/列举（升级序前置动词）。
		groupVerb("platform", "platform-level operations (backup before upgrades, list snapshots)",
			newPlatformBackupVerb(), newPlatformBackupsVerb()),
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
	// exec 子面退出码透传（F3.2，ADR-0049）：载体进程退出码即 CLI 退出码
	//（机器编排的成败锚——`fleetly exec app/web -- make test` 的 $?）。
	var exec exitCodeError
	if errors.As(err, &exec) {
		return int(exec.code)
	}
	var unknown *commands.UnknownVerbError
	var usage *commands.UsageError
	if errors.As(err, &unknown) || errors.As(err, &usage) {
		return exitUsage
	}
	return commands.ExitError
}
