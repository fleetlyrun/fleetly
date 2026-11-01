package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/governance"
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
	// ConfirmTickets 是 B1 Confirm Ticket 消费面（可空 = 引擎半边缺席：
	// 无 confirm 入参的受理不受影响；带票受理在缺席面下按不可辨票拒绝
	// ——拦截器半边独立执法 REQUIRED，正常装配恒注入）。
	ConfirmTickets governance.ConfirmTicketStore
	Logger         *slog.Logger
}

// New 构造引擎（不启动；Start 后进入驱动）。
func New(deps Deps, opts Options) *Engine {
	opts.fill()
	db := deps.DB
	log := deps.Logger
	clock := db.Clock()
	e := &Engine{
		builders:        deps.Builders,
		proxy:           deps.Proxy,
		registry:        deps.Registry,
		logging:         deps.Logging,
		cipher:          deps.Cipher,
		runtime:         deps.Runtime,
		db:              db,
		log:             log,
		clock:           clock,
		opts:            opts,
		repos:           deps.Stores,
		tx:              deps.Tx,
		confirmTickets:  deps.ConfirmTickets,
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
		nodeAbsentSince: map[string]time.Time{},
		ghClient:        github.NewClient(nil, nil),
		buildOpts:       buildLimits{Concurrency: opts.Build.BuildConcurrency, Timeout: opts.Build.BuildTimeout},
	}
	e.build.logs = newLogBuffer(500)
	e.build.inputs = make(map[string]capability.BuildRequest)
	// Route 发布门默认开放（H7）：只有 Start 的基线重放窗会临时关闭它——
	// 手动驱动/测试形态零行为变化。
	e.routeReady.Store(true)
	// 卫星装配（P4 wave-1，ADR-0088 决策 2/3）：依赖显式逐字段注入 +
	// 机制端口绑定为方法值/闭包。嵌入字段不可入复合字面量，装配走卫星
	// 指针逐字段赋值（经局部指针限定——卫星多持与 Engine 同名端口的副本
	//（logging/runtime/repos/log/clock），裸提升写会静默落到核字段上）。
	logs := &e.logpipes
	logs.streams = make(map[string]context.CancelFunc)
	logs.batches = make(map[string]*nsBatch)
	logs.cursors = deps.Stores.LogCursors
	logs.logging = deps.Logging
	logs.runtime = deps.Runtime
	logs.proxy = deps.Proxy
	logs.registry = deps.Registry
	logs.repos = deps.Stores
	logs.log = log
	logs.clock = clock
	logs.rootCtx = e.buildRootCtx
	logs.spawn = func(fn func()) {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			fn()
		}() //nolint:gosec // 采集 goroutine 生命周期 = 引擎进程（step 起拍、Stop 有界排水）
	}
	exec := &e.execs
	exec.init()
	exec.log = log
	exec.execFace = capability.FacesOf(deps.Runtime).Exec
	exec.repos = deps.Stores
	exec.clock = clock
	exec.emitLedger = e.emitLedger
	// backups 卫星装配（P4 wave-1b，ADR-0088 决策 2/3/4）：执行链装配面
	//（objectStore/utility）与单飞锁（platformBackupMu 随域走）随卫星持有；
	// 机制端口绑定为核方法值（lockMaintenance/commitWrite/emitObservation
	// 名对齐——maintenanceguard 锁入口名集与 enginefuncregistry emit*/
	// commit* 前缀判据零漂移）；facts 端口五族绑 resolve.go 解析面（定义
	// 位置零改，resolutionface 锚不动）。
	bk := &e.backups
	bk.objectStore = deps.ObjectStore
	bk.utility = capability.FacesOf(deps.Runtime).Utility
	bk.repos = deps.Stores
	bk.log = log
	bk.clock = clock
	bk.db = db
	bk.tx = deps.Tx
	bk.opts = opts
	bk.lockMaintenance = e.lockMaintenance
	bk.commitWrite = e.commitWrite
	bk.emitObservation = e.emitObservation
	bk.eventTeamOfProject = e.eventTeamOfProject
	bk.projectTeamOf = e.projectTeamOf
	bk.projectNetworkFactsNamesOnly = e.projectNetworkFactsNamesOnly
	bk.controlPlaneNode = e.controlPlaneNode
	bk.ensureDatabaseVolume = e.ensureDatabaseVolume
	bk.databasePassword = e.databasePassword
	bk.kickDatabases = e.KickDatabases
	// metrics 卫星装配（P4 wave-1b）：Metrics capability 与域自有 repo
	//（alertRules/channels）随卫星持有（capability 消费经提升
	// MetricsProvider() 访问器）；offsiteConfigured 闭包逐拍读核 opts
	//（live 读语义——测试经 opts 写驱动 s3 配置迁移沿）；rootCtx/spawn
	// 与 logpipes 同款（Add/Done 配对单源留核）。
	m := &e.metrics
	m.lastCPU = make(map[string]cpuCounter)
	m.cores = make(map[string]float64)
	m.metrics = deps.Metrics
	m.runtime = deps.Runtime
	m.repos = deps.Stores
	m.alertRules = deps.Stores.AlertRules
	m.channels = deps.Stores.Channels
	m.cipher = deps.Cipher
	m.log = log
	m.clock = clock
	m.offsiteConfigured = func() bool {
		return e.opts.Backup.PlatformBackup != nil && e.opts.Backup.PlatformBackup.S3 != nil
	}
	m.emitObservation = e.emitObservation
	m.eventTeamOfApp = e.eventTeamOfApp
	m.ownerTeam = e.ownerTeam
	m.ownerOf = e.obs.ownerOf
	m.workloadProcess = e.WorkloadProcess
	m.rootCtx = e.buildRootCtx
	m.spawn = func(fn func()) {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			fn()
		}() //nolint:gosec // 派发 goroutine 生命周期 = 引擎进程（spawn 端口排水纪律，logpipes 同款）
	}
	// browses 卫星装配（P4 wave-2a，ADR-0088 决策 2/3）：browseConfig 闭包
	// 逐拍读核 opts（live 读语义，通则五——测试经 opts 写驱动面启停）；
	// memo 端口绑 ensureMu 族方法值（锁单源留核）；观测窄接口绑 obs/expect
	// 域登记函数；kickBrowse/publishRoutesNow 留核消费（Loop 挂载与路由
	// 发布真源都在核）。
	br := &e.browses
	br.runtime = deps.Runtime
	br.repos = deps.Stores
	br.cipher = deps.Cipher
	br.log = log
	br.clock = clock
	br.browseConfig = func() BrowseConfig { return e.opts.Data.Browse }
	br.lockMaintenance = e.lockMaintenance
	br.boundedStep = e.boundedStep
	br.ensureFresh = e.ensureFresh
	br.ensureRemember = e.ensureRemember
	br.ensureForget = e.ensureForget
	br.projectTeam = e.projectTeam
	br.projectNetworkFactsNamesOnly = e.projectNetworkFactsNamesOnly
	br.recordOwners = e.obs.recordOwners
	br.recordExpected = e.expect.recordExpected
	br.kickBrowse = e.KickBrowse
	br.publishRoutesNow = e.PublishRoutesNow
	br.sessions = make(map[string]*browseSession)
	br.ensure = make(map[string]ensureMemo)
	// databases 卫星装配（P4 wave-2a）：rotateMu 随域（决策 4，零值锁）；
	// utility 与 backups 同源双持（FacesOf 探测单次、两副本各持——P5
	// 过渡形态通则二）；volumePinner 端口绑核 pinVolumes/applyVolumePinning
	//（materials.go 留核至 wave-2b intake 卫星化——白名单实体的物理首形，
	// ADR-0088 决策 7）；观测窄接口绑 obs/expect 域登记函数与单点读；
	// kickDatabases 留核消费（KickDatabases 留核）。
	dbs := &e.databases
	dbs.runtime = deps.Runtime
	dbs.repos = deps.Stores
	dbs.cipher = deps.Cipher
	dbs.utility = capability.FacesOf(deps.Runtime).Utility
	dbs.log = log
	dbs.clock = clock
	dbs.lockMaintenance = e.lockMaintenance
	dbs.boundedStep = e.boundedStep
	dbs.ensureFresh = e.ensureFresh
	dbs.ensureRemember = e.ensureRemember
	dbs.ensureForget = e.ensureForget
	dbs.projectTeam = e.projectTeam
	dbs.projectTeamOf = e.projectTeamOf
	dbs.projectNetworkFacts = e.projectNetworkFacts
	dbs.projectNetworkFactsNamesOnly = e.projectNetworkFactsNamesOnly
	dbs.ensureDatabaseVolume = e.ensureDatabaseVolume
	dbs.databasePassword = e.databasePassword
	dbs.pinVolumes = e.pinVolumes
	dbs.applyVolumePinning = e.applyVolumePinning
	dbs.kickDatabases = e.KickDatabases
	dbs.recordOwners = e.obs.recordOwners
	dbs.recordExpected = e.expect.recordExpected
	dbs.forgetExpected = e.expect.forgetExpected
	dbs.forgetObservation = e.obs.forget
	dbs.observationOf = e.obs.observationOf
	dbs.ensure = make(map[string]ensureMemo)
	// manageds 卫星装配（P4 wave-2a）：proxy/registry/logging 副本双持
	//（消费面裁定：核消费占优，随域迁入会强制核改写访问器——metrics
	// 撞名先例不适用）；metricsProvider 绑 metrics 卫星的提升访问器（窄
	// 事实端口闭包，通则五）；reconcileNodes/publishRoutes 是设计稿 §4
	// 例外边的端口归位（真源留核、managedStep 单点编排消费）；gensFor
	// 留核（ensureMu 锁与 gens map 配对裁定，managed.go 注）。
	mg := &e.manageds
	mg.proxy = deps.Proxy
	mg.registry = deps.Registry
	mg.logging = deps.Logging
	mg.runtime = deps.Runtime
	mg.log = log
	mg.clock = clock
	mg.lockMaintenance = e.lockMaintenance
	mg.boundedStep = e.boundedStep
	mg.ensureFresh = e.ensureFresh
	mg.ensureRemember = e.ensureRemember
	mg.ensureForget = e.ensureForget
	mg.activeProjectNetworks = e.activeProjectNetworks
	mg.activeProjectIDs = e.activeProjectIDs
	mg.controlPlaneNode = e.controlPlaneNode
	mg.gensFor = e.gensFor
	mg.reconcileNodes = e.reconcileNodes
	mg.publishRoutes = e.publishRoutes
	mg.metricsProvider = e.MetricsProvider
	mg.recordOwners = e.obs.recordOwners
	mg.recordExpected = e.expect.recordExpected
	mg.ensure = make(map[string]ensureMemo)
	mg.gens = make(map[string]*managedGen)
	e.obs.init()
	e.converge.init()
	e.expect.expected = make(map[workloadOwner]uint64)
	e.drift.sig = make(map[string]string)
	e.drift.rolloutSig = make(map[string]string)
	e.drift.stoppedSig = make(map[string]string)
	e.task.workloadRun = make(map[string]string)
	e.task.runObs = make(map[string]capability.WorkloadEvent)
	e.task.ensure = make(map[string]ensureMemo)
	e.delivery.release = make(map[string]ensureMemo)
	e.proxyMemo.pub = make(map[string]ensureMemo)
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
