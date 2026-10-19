// Package fleetlygrpc 实现五上下文 API 面（structure/delivery/runtime/
// proxy/telemetry）。读路径直读聚合 repo；写路径四件一拍（engine 或 repo
// 事务内组合 outbox/audit）；错误一律 apperr 信封（映射 helper 在
// mapping.go）。scope 注解已在 proto 写好；enforcement 随账号批接管。
package fleetlygrpc

import (
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/schedule"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/browse"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/catalog"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/githubapp"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/invitation"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/membership"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/role"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/team"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/token"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/user"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/freeze"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/appvariable"
	configrepo "github.com/fleetlyrun/fleetly/internal/domain/structure/config"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/github"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/upload"
)

// Services 聚合全部 API 依赖（装配注入；Runtime 面收窄为 Enrollment 消费）。
type Services struct {
	DB state.DB
	// Tx 是事务入口（组合根装配 db + Binder，ADR-0080）。
	Tx      transact.Tx
	Engine  *engine.Engine
	Cipher  *material.Cipher
	Runtime capability.Runtime
	// Logging 是受管日志存储（可空 = Logging 面停用：text 检索路径精确
	// 失败、logs 回退 Runtime 实时路径——ADR-0040 双径）。
	Logging capability.Logging
	// Metrics 是受管指标存储（可空 = Metrics 面停用：查询精确失败、零采集
	// 零告警——ADR-0041）。
	Metrics capability.Metrics

	Projects     project.Repo
	Apps         app.Repo
	Deployments  deployment.Repo
	Revisions    revision.Repo
	Builds       build.Repo
	Tasks        task.Repo
	Runs         run.Repo
	Schedules    schedule.Repo
	OutboxEvents outbox.Repo
	Secrets      secret.Repo
	Configs      configrepo.Repo
	// SharedVariables 是 SharedVariable 聚合 repo（F2.9，ADR-0043：Project
	// 级共享变量——归一化期合成进 Revision 的 Project 层）。
	SharedVariables sharedvariable.Repo
	// AppVariables 是 AppVariable 聚合 repo（ADR-0072 三级合成顶层：
	// App 级平台变量——冻结咽喉合成，同键覆盖源声明 env 与 SharedVariable）。
	AppVariables appvariable.Repo
	Volumes      volume.Repo
	Networks     network.Repo
	NetworkPeers networkpeer.Repo
	Freezes      freeze.Repo
	Routes       route.Repo
	Nodes        node.Repo
	Audits       audit.Repo
	Users        user.Repo
	Teams        team.Repo
	Roles        role.Repo
	Memberships  membership.Repo
	Tokens       token.Repo
	Invitations  invitation.Repo
	Hooks        hook.Repo

	// GitHubApps 是 GitHub App 接入材料聚合 repo（ADR-0073 阶段 B）；
	// GitHub 是最小客户端（installation token 现铸/受理验证/列仓，零
	// 持久配置——api_base 逐行携带，测试指 httptest 即可）。
	GitHubApps githubapp.Repo
	GitHub     *github.Client

	// Uploads 是上传产物行 repo；UploadStore 是 blob 面（内容寻址落盘，
	// ADR-0019 附录 A）。限额走缺省（512MiB/4GiB，ADR 钉值）。
	Uploads     sourceupload.Repo
	UploadStore *upload.Store

	// Databases 是 Database 聚合 repo（F1.12，ADR-0029）。
	Databases database.Repo

	// Browse 是 Browse 会话回收台账 repo（F3.6，ADR-0051 决策 1：受理
	// 事务落行；engine browseLoop 消费——注册表是进程内活体，行是
	// 重启恢复与到点回收的锚）。
	Browse browse.Repo

	// Backups 是 Backup 台账 repo（F2.2，ADR-0039；对象面经 Engine——
	// ObjectStore 端口是 engine 执行链的装配物）。
	Backups backup.Repo

	// Catalog 是 App 模板目录快照 repo（F3.3，ADR-0050：RefreshTemplates
	// 的单行快照；解析序 = 快照在场优先、内嵌目录兜底）。
	Catalog catalog.Repo

	// TemplatesCatalogURL 是目录刷新源（ADR-0050 决策 4：空 = 刷新停用，
	// 内嵌目录即全部；装配期从 server.templates_catalog_url 注入）。
	TemplatesCatalogURL string

	// GitHubManifestBase 是 manifest 自动建流的公网 console 基址（ADR-0073
	// 决策 8：redirect_url/setup_url 由此派生；空 = 流禁用，手动表单兜底；
	// 装配期从 console.host 注入）。
	GitHubManifestBase string

	// GitHubManifestAPIBase 是 conversions 兑换的 GitHub API 基址（空 =
	// github.com 缺省；测试注入假件基址）。
	GitHubManifestAPIBase string

	// Anchor 是「聚合行 → Project → Team」归属解析图（freeze 与行级授权
	// 共用单源，架构评审第二轮候选 3；ADR-0017 附录 A.3 / ADR-0035）。
	Anchor *anchor.Anchor

	// eventTickets 是 SSE 订阅路径的一次性短时票据面（ADR-0026；铸造经
	// IssueEventTicket，兑换限 SSE 原生入口）。
	eventTickets *eventTicketStore

	// loginLimiter 是 Login 口令面的进程内固定窗限速器（R14-M1 限速半边；
	// loginlimit.go 单源）。nil 安全：仅 Login 消费，测试直构 Services 的
	// 面不受影响。
	loginLimiter *loginLimiter

	// ScopeVocabulary 是 scope 词表（CreateRole 校验用；assembly 单一源
	// 注入——服务面不自带词表）。
	ScopeVocabulary []string

	// GatewayPort 是 REST gateway 端口（H10：EnrollNode 定向中继材料的
	// NodeRelayCommand 拼装锚——装配期从配置注入，默认空 = 无中继材料脚本面）。
	GatewayPort string

	// LoginTokenTTL 是 Login 铸造的密码会话 Token 时效（S18-B / R14-M1；
	// 装配期从 auth.login_token_ttl 注入，缺省 720h）。0 = 永不过期
	//（逃生门与零值夹具语义）；只作用于 Login——tokens create/bootstrap
	// 铸造的 Token 保持 ''（永不过期）。
	LoginTokenTTL time.Duration

	Log *slog.Logger
}

// Deps 是 Services 的装配依赖（组合根构造注入——P1b C 批构造上提：本包
// 零持久化构造，repo 实现选择住 assembly；vocab 是 scope 词表单一源，
// GitHub/UploadStore 是客户端注入面）。字段与 Services 同名消费面对齐。
type Deps struct {
	DB state.DB
	// Tx 是事务入口（组合根装配，ADR-0080）。
	Tx          transact.Tx
	Engine      *engine.Engine
	Cipher      *material.Cipher
	Runtime     capability.Runtime
	Logging     capability.Logging
	Metrics     capability.Metrics
	Vocab       []string
	Log         *slog.Logger
	GitHub      *github.Client
	UploadStore *upload.Store

	Projects     project.Repo
	Apps         app.Repo
	Deployments  deployment.Repo
	Revisions    revision.Repo
	Builds       build.Repo
	Tasks        task.Repo
	Runs         run.Repo
	Schedules    schedule.Repo
	OutboxEvents outbox.Repo
	Secrets      secret.Repo
	Configs      configrepo.Repo
	// SharedVariables 是 SharedVariable 聚合 repo（F2.9，ADR-0043）。
	SharedVariables sharedvariable.Repo
	// AppVariables 是 AppVariable 聚合 repo（ADR-0072）。
	AppVariables appvariable.Repo
	Volumes      volume.Repo
	Networks     network.Repo
	NetworkPeers networkpeer.Repo
	Freezes      freeze.Repo
	Routes       route.Repo
	Nodes        node.Repo
	Audits       audit.Repo
	Users        user.Repo
	Teams        team.Repo
	Roles        role.Repo
	Memberships  membership.Repo
	Tokens       token.Repo
	Invitations  invitation.Repo
	Hooks        hook.Repo
	// GitHubApps 是 GitHub App 接入材料聚合 repo（ADR-0073 阶段 B）。
	GitHubApps githubapp.Repo
	// Uploads 是上传产物行 repo（ADR-0019 附录 A）。
	Uploads sourceupload.Repo
	// Databases 是 Database 聚合 repo（F1.12，ADR-0029）。
	Databases database.Repo
	// Browse 是 Browse 会话回收台账 repo（F3.6，ADR-0051）。
	Browse browse.Repo
	// Backups 是 Backup 台账 repo（F2.2，ADR-0039）。
	Backups backup.Repo
	// Catalog 是 App 模板目录快照 repo（F3.3，ADR-0050）。
	Catalog catalog.Repo
	// Anchor 是「聚合行 → Project → Team」归属解析图（freeze 与行级授权
	// 共用单源；组装在组合根，anchor 零持久化依赖）。
	Anchor *anchor.Anchor
}

// NewServices 构造（deps 全量由组合根注入——repo 构造与 anchor 组装上提
// assembly，P1b C 批；eventTickets 以注入 DB 的时钟铸造）。
func NewServices(deps Deps) *Services {
	return &Services{
		DB:              deps.DB,
		Tx:              deps.Tx,
		Engine:          deps.Engine,
		Cipher:          deps.Cipher,
		Runtime:         deps.Runtime,
		Logging:         deps.Logging,
		Metrics:         deps.Metrics,
		Projects:        deps.Projects,
		Apps:            deps.Apps,
		Deployments:     deps.Deployments,
		Revisions:       deps.Revisions,
		Builds:          deps.Builds,
		Tasks:           deps.Tasks,
		Runs:            deps.Runs,
		Schedules:       deps.Schedules,
		OutboxEvents:    deps.OutboxEvents,
		Secrets:         deps.Secrets,
		Configs:         deps.Configs,
		SharedVariables: deps.SharedVariables,
		AppVariables:    deps.AppVariables,
		Volumes:         deps.Volumes,
		Networks:        deps.Networks,
		NetworkPeers:    deps.NetworkPeers,
		Freezes:         deps.Freezes,
		Routes:          deps.Routes,
		Nodes:           deps.Nodes,
		Audits:          deps.Audits,
		Users:           deps.Users,
		Teams:           deps.Teams,
		Roles:           deps.Roles,
		Memberships:     deps.Memberships,
		Tokens:          deps.Tokens,
		Invitations:     deps.Invitations,
		Hooks:           deps.Hooks,
		GitHubApps:      deps.GitHubApps,
		GitHub:          deps.GitHub,
		Uploads:         deps.Uploads,
		UploadStore:     deps.UploadStore,
		Databases:       deps.Databases,
		Browse:          deps.Browse,
		Backups:         deps.Backups,
		Catalog:         deps.Catalog,
		Anchor:          deps.Anchor,
		eventTickets:    newEventTicketStore(deps.DB.Clock()),
		loginLimiter:    newLoginLimiter(deps.DB.Clock()),
		ScopeVocabulary: deps.Vocab,
		Log:             deps.Log,
	}
}
