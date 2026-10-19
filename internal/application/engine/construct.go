package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/github"
)

// 引擎构造面（P3 收官批 8 壳收口：Repos/Deps/New 自 engine.go 逐字迁移
// ——环表与 init 链零漂移，仅 Options 按域切片与 buildLimits 更名的机械
// 消费点改写）。

// Deps 是引擎依赖（装配注入；可选依赖为 nil 时对应能力停用并给出精确
// 反馈，不静默）。
type Deps struct {
	DB state.DB
	// Stores 是聚合目录的 db 绑定形态（组合根经 Binder 构造注入；P1b C 批
	// 构造上提，本包零持久化构造）。读径直调；事务作用域经 Tx.Do 的闭包
	// s（ADR-0080）。
	Stores transact.Stores
	// Tx 是事务入口（组合根装配 db + Binder）。
	Tx          transact.Tx
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
	e := &Engine{
		builders:     deps.Builders,
		proxy:        deps.Proxy,
		registry:     deps.Registry,
		logging:      deps.Logging,
		metrics:      deps.Metrics,
		objectStore:  deps.ObjectStore,
		utility:      capability.FacesOf(deps.Runtime).Utility,
		execFace:     capability.FacesOf(deps.Runtime).Exec,
		cipher:       deps.Cipher,
		runtime:      deps.Runtime,
		db:           db,
		log:          log,
		clock:        clock,
		opts:         opts,
		repos:        deps.Stores,
		tx:           deps.Tx,
		alertRules:   deps.Stores.AlertRules,
		channels:     deps.Stores.Channels,
		loop:         NewLoop("deployment", log),
		buildLoop:    NewLoop("build", log),
		managedLoop:  NewLoop("managed", log),
		databaseLoop: NewLoop("database", log),
		backupLoop:   NewLoop("backup", log),
		loggingLoop:  NewLoop("logging", log),
		metricsLoop:  NewLoop("metrics", log),
		taskLoop:     NewLoop("task", log),
		scheduleLoop: NewLoop("schedule", log),
		driftLoop:    NewLoop("drift", log),
		browseLoop:   NewLoop("browse", log),
		nodeLeftSeen: map[string]bool{},
		ghClient:     github.NewClient(nil, nil),
		buildOpts:    buildLimits{Concurrency: opts.Build.BuildConcurrency, Timeout: opts.Build.BuildTimeout},
	}
	e.build.logs = newLogBuffer(500)
	e.build.inputs = make(map[string]capability.BuildRequest)
	// Route 发布门默认开放（H7）：只有 Start 的基线重放窗会临时关闭它——
	// 手动驱动/测试形态零行为变化。
	e.routeReady.Store(true)
	e.logpipe.streams = make(map[string]context.CancelFunc)
	e.logpipe.batches = make(map[string]*nsBatch)
	e.logpipe.cursors = deps.Stores.LogCursors
	e.metricsDom.above = make(map[string]time.Time)
	e.metricsDom.lastCPU = make(map[string]cpuCounter)
	e.metricsDom.cores = make(map[string]float64)
	e.obs.init()
	e.converge.init()
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
	e.ringDeaths = make(map[string]bool)
	e.peerIsolations = make(map[peerIsolationRequest]bool)
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
