// Package fleetlygrpc 实现五上下文 API 面（structure/delivery/runtime/
// proxy/telemetry）。读路径直读聚合 repo；写路径四件一拍（engine 或 repo
// 事务内组合 outbox/audit）；错误一律 apperr 信封（映射 helper 在
// mapping.go）。scope 注解已在 proto 写好；enforcement 随账号批接管。
package fleetlygrpc

import (
	"log/slog"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
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
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	apprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	appvariablerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/appvariable"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	backuprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/backup"
	browserepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/browse"
	buildrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/build"
	catalogrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/catalog"
	cfgrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/config"
	dbrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/database"
	deploymentrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/deployment"
	freezerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/freeze"
	githubapprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/githubapp"
	hookrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/hook"
	invitationrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/invitation"
	membershiprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/membership"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	networkpeerrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/networkpeer"
	noderepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/node"
	outboxrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/outbox"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	revisionrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/revision"
	rolerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/role"
	routerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/route"
	runrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/run"
	schedulerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/schedule"
	secretrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/secret"
	sharedvariablerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/sharedvariable"
	sourcerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/sourceupload"
	taskrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/task"
	teamrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/team"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/token"
	userrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/user"
	volumerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/volume"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/upload"
)

// Services 聚合全部 API 依赖（装配注入；Runtime 面收窄为 Enrollment 消费）。
type Services struct {
	DB      *persistence.DB
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

	// ScopeVocabulary 是 scope 词表（CreateRole 校验用；assembly 单一源
	// 注入——服务面不自带词表）。
	ScopeVocabulary []string

	// GatewayPort 是 REST gateway 端口（ADR-0049：EnrollNode 的 RelayCommand
	// 拼装锚——装配期从配置注入，默认空 = 无中继装载脚本面）。
	GatewayPort string

	Log *slog.Logger
}

// NewServices 构造（repos 从 DB 时钟派生；vocab 是 scope 词表单一源；
// dataRoot 是上传产物 blob 根；anchor 的 repo 集在此构造注入——组合根
// 落位，anchor 零持久化依赖）。
func NewServices(db *persistence.DB, e *engine.Engine, c *material.Cipher, rt capability.Runtime, dataRoot string, vocab []string, log *slog.Logger) *Services {
	clock := db.Clock()
	schedules := schedulerepo.New(clock)
	freezes := freezerepo.New(clock)
	nodes := noderepo.New(clock)
	hooks := hookrepo.New(clock)
	ghApps := githubapprepo.New(clock)
	uploads := sourcerepo.New(clock)
	databases := dbrepo.New(clock)
	browseSessions := browserepo.New(clock)
	backups := backuprepo.New(clock)
	projects := projectrepo.New(clock)
	apps := apprepo.New(clock)
	tasks := taskrepo.New(clock)
	networks := networkrepo.New(clock)
	peers := networkpeerrepo.New(clock)
	routes := routerepo.New(clock)
	volumes := volumerepo.New(clock)
	runs := runrepo.New(clock)
	deployments := deploymentrepo.New(clock)
	builds := buildrepo.New(clock)
	return &Services{
		DB:              db,
		Engine:          e,
		Cipher:          c,
		Runtime:         rt,
		Logging:         e.LoggingProvider(),
		Metrics:         e.MetricsProvider(),
		Projects:        projects,
		Apps:            apps,
		Deployments:     deployments,
		Revisions:       revisionrepo.New(clock),
		Builds:          builds,
		Tasks:           tasks,
		Runs:            runs,
		Schedules:       schedules,
		OutboxEvents:    outboxrepo.New(clock),
		Secrets:         secretrepo.New(clock),
		Configs:         cfgrepo.New(clock),
		SharedVariables: sharedvariablerepo.New(clock),
		AppVariables:    appvariablerepo.New(clock),
		Volumes:         volumes,
		Networks:        networks,
		NetworkPeers:    peers,
		Freezes:         freezes,
		Routes:          routes,
		Nodes:           nodes,
		Audits:          auditrepo.New(clock),
		Users:           userrepo.New(clock),
		Teams:           teamrepo.New(clock),
		Roles:           rolerepo.New(clock),
		Memberships:     membershiprepo.New(clock),
		Tokens:          tokenrepo.New(clock),
		Invitations:     invitationrepo.New(clock),
		Hooks:           hooks,
		GitHubApps:      ghApps,
		GitHub:          github.NewClient(nil, nil),
		Uploads:         uploads,
		UploadStore:     upload.NewStore(dataRoot, 0, 0),
		Databases:       databases,
		Browse:          browseSessions,
		Backups:         backups,
		Catalog:         catalogrepo.New(clock),
		Anchor: anchor.New(anchor.Repos{
			Projects:    projects,
			Apps:        apps,
			Tasks:       tasks,
			Schedules:   schedules,
			Networks:    networks,
			Peers:       peers,
			Hooks:       hooks,
			Routes:      routes,
			Databases:   databases,
			Volumes:     volumes,
			Runs:        runs,
			Deployments: deployments,
			Builds:      builds,
			GitHubApps:  ghApps,
		}),
		eventTickets:    newEventTicketStore(clock),
		ScopeVocabulary: vocab,
		Log:             log,
	}
}
