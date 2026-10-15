package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/schedule"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/browse"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/githubapp"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/token"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	appvariable "github.com/fleetlyrun/fleetly/internal/domain/structure/appvariable"
	configrepo "github.com/fleetlyrun/fleetly/internal/domain/structure/config"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	sharedvariable "github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/logcursor"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/github"
)

// 引擎构造面（P3 收官批 8 壳收口：Repos/Deps/New 自 engine.go 逐字迁移
// ——环表与 init 链零漂移，仅 Options 按域切片与 buildLimits 更名的机械
// 消费点改写）。

// Repos 是引擎的聚合 repo 集（组合根构造注入——P1b C 批构造上提：engine
// 包零持久化构造，repo 实现选择住 assembly；字段分组与 Engine 同名消费面
// 对齐，时钟注入在组合根完成——各 repo 以同一 DB 时钟构造）。
type Repos struct {
	// 部署链主干（F0.x）：结构/交付聚合 + Outbox 事件 + 审计 + 节点。
	Projects    project.Repo
	Apps        app.Repo
	Revisions   revision.Repo
	Deployments deployment.Repo
	Builds      build.Repo
	Outbox      outbox.Repo
	Nodes       node.Repo
	Audits      audit.Repo

	// 规格受理咽喉的三级合成消费面（P3-7 下沉批）：SharedVariable/
	// AppVariable 的冻结前装载。
	SharedVariables sharedvariable.Repo
	AppVariables    appvariable.Repo

	// Task 域（F1.5/F1.6）：聚合 repo 与属主 Token 行引用面（吊销排空
	// 拉式扫描，P1-8）。
	Tasks  task.Repo
	Runs   run.Repo
	Tokens token.Repo

	// Schedule 域（F1.7）：到期拍环的消费面（fire 铸 one-shot Task）。
	Schedules schedule.Repo

	// 跨 Project peer 声明（F1.8，ADR-0013 附录 A）：批准态真源。
	PeerDecls networkpeer.Repo

	// 上传产物行（F1.10，ADR-0019 附录 A）：构建输入解析面。
	Uploads sourceupload.Repo

	// Database 域（F1.12，ADR-0029）：第三条部署轨的台账。
	Databases database.Repo

	// Browse 会话域（F3.6，ADR-0051）：回收台账 repo。
	Browse browse.Repo

	// Backup 域（F2.2，ADR-0039）：备份台账 repo。
	Backups backup.Repo

	// 日志采集游标（F2.4，ADR-0040）：域流对账锚。
	LogCursors logcursor.Repo

	// 告警面（F2.5，ADR-0041）：规则与通道。
	AlertRules alertrule.Repo
	Channels   channel.Repo

	// Proxy 面（F0.15）：Route 全量发布真源。
	Routes route.Repo

	// 材料面（F0.17/18，ADR-0014）：Secret/Config/Volume/Network。
	Secrets  secret.Repo
	Configs  configrepo.Repo
	Volumes  volume.Repo
	Networks network.Repo

	// GitHub App 接入材料（ADR-0073 阶段 B）。
	GitHubApps githubapp.Repo
}

// Deps 是引擎依赖（装配注入；可选依赖为 nil 时对应能力停用并给出精确
// 反馈，不静默）。
type Deps struct {
	DB state.DB
	// Repos 是聚合 repo 集（组合根构造注入；P1b C 批起构造上提 assembly，
	// 本包不再引任何 persistence 子包）。
	Repos       Repos
	Runtime     capability.Runtime
	Builders    map[string]capability.Builder // 可空/空 map：构建链停用（镜像直投不受影响；ADR-0032 spec 路由家族）
	Proxy       capability.Proxy              // 可空：Route 发布与受管自宿停用
	Registry    capability.Registry           // 可空：build 源部署精确失败（附录 B.5①）
	Logging     capability.Logging            // 可空：采集/检索面停用——logs 回退 Runtime 实时路径、build 日志回退环形缓冲（ADR-0040）
	Metrics     capability.Metrics            // 可空：零采集/零告警、查询精确失败（ADR-0041）
	ObjectStore capability.ObjectStore        // 可空：备份链停用（ADR-0039；本地目标零配置在册，缺席 = 装配缺口）
	Cipher      *material.Cipher              // 可空：Secret 面停用（引用 Secret 的部署得精确错误）
	Logger      *slog.Logger
}

// New 构造引擎（不启动；Start 后进入驱动）。
func New(deps Deps, opts Options) *Engine {
	opts.fill()
	db := deps.DB
	log := deps.Logger
	clock := db.Clock()
	repos := deps.Repos
	e := &Engine{
		builders:        deps.Builders,
		proxy:           deps.Proxy,
		registry:        deps.Registry,
		logging:         deps.Logging,
		metrics:         deps.Metrics,
		objectStore:     deps.ObjectStore,
		utility:         capability.FacesOf(deps.Runtime).Utility,
		execFace:        capability.FacesOf(deps.Runtime).Exec,
		cipher:          deps.Cipher,
		runtime:         deps.Runtime,
		db:              db,
		log:             log,
		clock:           clock,
		opts:            opts,
		projects:        repos.Projects,
		apps:            repos.Apps,
		revisions:       repos.Revisions,
		deployments:     repos.Deployments,
		builds:          repos.Builds,
		outbox:          repos.Outbox,
		nodes:           repos.Nodes,
		audits:          repos.Audits,
		tasks:           repos.Tasks,
		runs:            repos.Runs,
		tokens:          repos.Tokens,
		schedules:       repos.Schedules,
		peerDecls:       repos.PeerDecls,
		uploads:         repos.Uploads,
		browseRepo:      repos.Browse,
		databases:       repos.Databases,
		backups:         repos.Backups,
		sharedVariables: repos.SharedVariables,
		appVariables:    repos.AppVariables,
		loop:            NewLoop("deployment", log),
		buildLoop:       NewLoop("build", log),
		managedLoop:     NewLoop("managed", log),
		databaseLoop:    NewLoop("database", log),
		backupLoop:      NewLoop("backup", log),
		loggingLoop:     NewLoop("logging", log),
		metricsLoop:     NewLoop("metrics", log),
		taskLoop:        NewLoop("task", log),
		scheduleLoop:    NewLoop("schedule", log),
		driftLoop:       NewLoop("drift", log),
		browseLoop:      NewLoop("browse", log),
		nodeLeftSeen:    map[string]bool{},
		routes:          repos.Routes,
		secrets:         repos.Secrets,
		configs:         repos.Configs,
		volumes:         repos.Volumes,
		networks:        repos.Networks,
		ghApps:          repos.GitHubApps,
		ghClient:        github.NewClient(nil, nil),
		buildOpts:       buildLimits{Concurrency: opts.Build.BuildConcurrency, Timeout: opts.Build.BuildTimeout},
	}
	e.build.logs = newLogBuffer(500)
	e.build.inputs = make(map[string]capability.BuildRequest)
	e.logpipe.streams = make(map[string]context.CancelFunc)
	e.logpipe.batches = make(map[string]*nsBatch)
	e.logpipe.cursors = repos.LogCursors
	e.metricsDom.above = make(map[string]time.Time)
	e.metricsDom.lastCPU = make(map[string]cpuCounter)
	e.metricsDom.cores = make(map[string]float64)
	e.alertRules = repos.AlertRules
	e.channels = repos.Channels
	e.obs.init()
	e.exec.init()
	e.exec.log = log
	e.expect.expected = make(map[workloadOwner]uint64)
	e.drift.sig = make(map[string]string)
	e.drift.rolloutSig = make(map[string]string)
	e.drift.stoppedSig = make(map[string]string)
	e.task.workloadRun = make(map[string]string)
	e.task.runObs = make(map[string]capability.WorkloadEvent)
	e.task.ensure = make(map[string]ensureMemo)
	e.managed.ensure = make(map[string]ensureMemo)
	e.managed.gens = make(map[string]*managedGen)
	e.database.ensure = make(map[string]ensureMemo)
	e.delivery.release = make(map[string]ensureMemo)
	e.proxyMemo.pub = make(map[string]ensureMemo)
	e.browse.sessions = make(map[string]*browseSession)
	e.browse.ensure = make(map[string]ensureMemo)
	// 环表（表序 = DriveOnce 手动驱动序：schedule 先于 task——同一轮内
	// 铸出的 Task 即刻进补足链；goroutine 挂载/Kick 顺序随表，环间独立
	// 无依赖）。
	e.rings = []ring{
		{name: "deployment", loop: e.loop, step: e.step},
		{name: "build", loop: e.buildLoop, step: e.buildStep},
		{name: "managed", loop: e.managedLoop, step: e.managedStep},
		{name: "database", loop: e.databaseLoop, step: e.databaseStep},
		{name: "backup", loop: e.backupLoop, step: e.backupStep},
		{name: "logging", loop: e.loggingLoop, step: e.loggingStep},
		{name: "metrics", loop: e.metricsLoop, step: e.metricsStep},
		{name: "schedule", loop: e.scheduleLoop, step: e.scheduleStep},
		{name: "task", loop: e.taskLoop, step: e.taskStep},
		{name: "browse", loop: e.browseLoop, step: e.browseStep},
	}
	return e
}
