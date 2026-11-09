package engine

import (
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/backups"
	"github.com/fleetlyrun/fleetly/internal/application/browses"
	"github.com/fleetlyrun/fleetly/internal/application/dataservices"
	"github.com/fleetlyrun/fleetly/internal/application/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/execs"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/intake"
	"github.com/fleetlyrun/fleetly/internal/application/logpipes"
	"github.com/fleetlyrun/fleetly/internal/application/manageds"
	"github.com/fleetlyrun/fleetly/internal/application/metrics"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// 引擎构造面（P3 收官批 8 壳收口：Repos/Deps/New 自 engine.go 逐字迁移
// ——循环表与 init 链零漂移，仅 Options 按域切片与 buildLimits 更名的机械
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
		pubsubLoop:      NewLoop("pubsub", log),
		bucketstoreLoop: NewLoop("bucketstore", log),
		backupLoop:      NewLoop("backup", log),
		loggingLoop:     NewLoop("logging", log),
		metricsLoop:     NewLoop("metrics", log),
		taskLoop:        NewLoop("task", log),
		scheduleLoop:    NewLoop("schedule", log),
		driftLoop:       NewLoop("drift", log),
		browseLoop:      NewLoop("browse", log),
		nodeLeftSeen:    map[string]bool{},
		nodeAbsentSince: map[string]time.Time{},
	}
	// Route 发布门默认开放（H7）：只有 Start 的基线重放窗会临时关闭它——
	// 手动驱动/测试形态零行为变化。
	e.routeReady.Store(true)
	// intake 应用模块装配（P5 wave-5 拆包形态，ADR-0088 决策 2/3/8）：嵌入已
	// 解开——应用模块独立成包，核经构造注入持有；域自有事实（builders/
	// buildOpts/ghClient——单一真源随域，ghClient 现铸在应用模块 New）与状态
	//（build.builds 嵌套保留，包内私有）随应用模块持有；依赖显式逐字段注入 +
	// 内核端口绑定为核方法值/闭包（rootCtx/spawn 与 logpipes/metrics 同款
	//——Add/Done 配对单源留核；transitBuild 端口 = 核四件一、终态副作用
	// 随应用模块 transitBuildRelease 包装——冻结边裁决见 transition.go 注；
	// failDeployment/loadSpec 是受理线例外边端口归位；registry 端点四函数
	// 绑 resolve.go 解析器 resolver 端口，定义位置零改；dataRoot 闭包逐拍读
	// 核 opts——通则五配置快照端口；github/upload 分层豁免随应用模块平移）。
	e.intake = intake.New(intake.Deps{
		Builders:         deps.Builders,
		Registry:         deps.Registry,
		Logging:          deps.Logging,
		Cipher:           deps.Cipher,
		Runtime:          deps.Runtime,
		Repos:            deps.Stores,
		Tx:               deps.Tx,
		Log:              log,
		BuildConcurrency: opts.Build.BuildConcurrency,
		BuildTimeout:     opts.Build.BuildTimeout,
		DataRoot:         func() string { return e.opts.Data.DataRoot },
		RootCtx:          e.buildRootCtx,
		Spawn: func(fn func()) {
			e.wg.Add(1)
			go func() {
				defer e.wg.Done()
				fn()
			}() //nolint:gosec // 构建 goroutine 生命周期 = Build 行写者（step 起拍、Stop 有界排水）
		},
		TransitBuild:             e.transitBuild,
		TransitAndReload:         e.transitAndReload,
		FailDeployment:           e.failDeployment,
		LoadSpec:                 e.loadSpec,
		CommitWrite:              e.commitWrite,
		EventTeamOfProject:       e.eventTeamOfProject,
		ProjectTeam:              e.projectTeam,
		RegistryEndpoint:         e.registryEndpoint,
		RegistryProjectEndpoints: e.registryProjectEndpoints,
		RegistryEndpointFor:      e.registryEndpointFor,
		ManagedEndpoint:          e.managedEndpoint,
		KickBuild:                e.buildLoop.Kick,
	})
	// logpipes 应用模块装配（P5 wave-1 拆包形态，ADR-0088 决策 2/3）：嵌入已
	// 解开——应用模块独立成包，核经构造注入持有；依赖显式逐字段注入 + 机制
	// 端口绑定为核方法值/闭包（rootCtx/spawn——Add/Done 配对单源留核，
	// 应用模块结构性无法破坏排水）。应用模块与核同名端口副本双持（logging/
	// runtime/repos/log/clock）的 P4 过渡形态随拆包自然消解（各持各包）。
	e.logpipes = logpipes.New(logpipes.Deps{
		Logging:  deps.Logging,
		Runtime:  deps.Runtime,
		Proxy:    deps.Proxy,
		Registry: deps.Registry,
		Repos:    deps.Stores,
		Log:      log,
		Clock:    clock,
		Cursors:  deps.Stores.LogCursors,
		RootCtx:  e.buildRootCtx,
		Spawn: func(fn func()) {
			e.wg.Add(1)
			go func() {
				defer e.wg.Done()
				fn()
			}() //nolint:gosec // 采集 goroutine 生命周期 = 引擎进程（step 起拍、Stop 有界排水）
		},
	})
	// execs 应用模块装配（P5 wave-2 拆包形态，ADR-0088 决策 2/3）：嵌入已解开
	// ——应用模块独立成包，核经构造注入持有；依赖显式逐字段注入 + emitLedger
	// 内核端口绑定为核方法值（签名单源 contract.EmitLedger）。
	e.execs = execs.New(execs.Deps{
		Log:        log,
		ExecFace:   capability.FacesOf(deps.Runtime).Exec,
		Repos:      deps.Stores,
		Clock:      clock,
		EmitLedger: e.emitLedger,
	})
	// backups 应用模块装配（P5 wave-3 拆包形态，ADR-0088 决策 2/3/4）：嵌入已
	// 解开——应用模块独立成包，核经构造注入持有；执行链装配面（objectStore/
	// utility）与单飞锁（platformBackupMu 随域走）随应用模块持有；内核端口绑定
	// 为核方法值（lockMaintenance/commitWrite/emitObservation 名对齐——
	// maintenanceguard 锁入口名集与 enginefuncregistry emit*/commit* 前缀
	// 判据零漂移；commitWrite 签名单源 contract.CommitWrite）；facts
	// 端口五族绑 resolve.go 解析器（定义位置零改，resolutionface 锚不动）；
	// 域配置快照（Backup 段 + DataRoot）随应用模块自持（P4 嵌入期的核 Options
	// 副本形态随拆包消解——构造后冻结语义零漂移）。
	e.backups = backups.New(backups.Deps{
		ObjectStore: deps.ObjectStore,
		Utility:     capability.FacesOf(deps.Runtime).Utility,
		Repos:       deps.Stores,
		DB:          db,
		Tx:          deps.Tx,
		Options: backups.Options{
			BackupTimeout:  opts.Backup.BackupTimeout,
			PlatformBackup: opts.Backup.PlatformBackup,
			ResticPath:     opts.Backup.ResticPath,
			DataRoot:       opts.Data.DataRoot,
		},
		Log:                          log,
		Clock:                        clock,
		LockMaintenance:              e.lockMaintenance,
		CommitWrite:                  e.commitWrite,
		EmitObservation:              e.emitObservation,
		EventTeamOfProject:           e.eventTeamOfProject,
		ProjectTeamOf:                e.projectTeamOf,
		ProjectNetworkFactsNamesOnly: e.projectNetworkFactsNamesOnly,
		ControlPlaneNode:             e.controlPlaneNode,
		EnsureDatabaseVolume:         e.ensureDatabaseVolume,
		DatabasePassword:             e.databasePassword,
		KickDatabases:                e.KickDatabases,
		PubSubPassword:               e.pubsubPassword,
	})
	// metrics 应用模块装配（P5 wave-2 拆包形态，ADR-0088 决策 2/3）：嵌入已
	// 解开——应用模块独立成包，核经构造注入持有。Metrics capability 与域自有
	// repo（alertRules/channels）随应用模块持有（capability 消费经核门面
	// MetricsProvider 委托）；offsiteConfigured 闭包逐拍读核 opts（live 读
	// 语义——测试经 opts 写驱动 s3 配置迁移沿）；观测窄接口闭包族绑核方
	// 法值——appOwnerOf 是 ownerOf/ownerTeam 两端口的 P5 wave-2 基础类型
	// 化收敛（App 域过滤在绑定闭包内裁决：应用模块只消费 App 域归属，域判别
	// 留核）；rootCtx/spawn 与 logpipes 同款（Add/Done 配对单源留核）。
	e.metrics = metrics.New(metrics.Deps{
		Metrics:    deps.Metrics,
		Runtime:    deps.Runtime,
		Repos:      deps.Stores,
		AlertRules: deps.Stores.AlertRules,
		Channels:   deps.Stores.Channels,
		Cipher:     deps.Cipher,
		Log:        log,
		Clock:      clock,
		OffsiteConfigured: func() bool {
			return e.opts.Backup.PlatformBackup != nil && e.opts.Backup.PlatformBackup.S3 != nil
		},
		EmitObservation: e.emitObservation,
		EventTeamOfApp:  e.eventTeamOfApp,
		AppOwnerOf: func(wid string) (string, bool) {
			owner, ok := e.obs.ownerOf(wid)
			if !ok || owner.domain != ownerApp {
				return "", false // 非 App 域载体不进诊断/通知面（与 container_exited 同口径）
			}
			return owner.id, true
		},
		WorkloadProcess: e.WorkloadProcess,
		RootCtx:         e.buildRootCtx,
		Spawn: func(fn func()) {
			e.wg.Add(1)
			go func() {
				defer e.wg.Done()
				fn()
			}() //nolint:gosec // 派发 goroutine 生命周期 = 引擎进程（spawn 端口排水纪律，logpipes 同款）
		},
	})
	// browses 应用模块装配（P5 wave-3 拆包形态，ADR-0088 决策 2/3）：嵌入已
	// 解开——应用模块独立成包，核经构造注入持有；browseConfig 闭包逐拍读核
	// opts（live 读语义，通则五——测试经 opts 写驱动面启停）；ensure cache 端口绑
	// ensureMu 族方法值（签名单源 engine/contract，锁单源留核）且 ensure cache 表本体
	// 在组合根铸造注入（metastatewrite D3：表只在组合根/meta init 出生）；
	// 观测窄接口窄化为会话轴签名——browseOwner 铸造（ownerBrowse 域判别）
	// 留核绑定闭包，应用模块只消费会话 ID（workloadOwner 不全域下沉的 wave-3
	// 裁定，appOwnerOf 基础类型化同款思路）；kickBrowse/publishRoutesNow
	// 留核消费（Loop 挂载与路由发布真源都在核）。
	e.browses = browses.New(browses.Deps{
		Runtime:                      deps.Runtime,
		Repos:                        deps.Stores,
		Cipher:                       deps.Cipher,
		Log:                          log,
		Clock:                        clock,
		BrowseConfig:                 func() BrowseConfig { return e.opts.Data.Browse },
		LockMaintenance:              e.lockMaintenance,
		BoundedStep:                  e.boundedStep,
		EnsureFresh:                  e.ensureFresh,
		EnsureRemember:               e.ensureRemember,
		EnsureForget:                 e.ensureForget,
		ProjectTeam:                  e.projectTeam,
		ProjectNetworkFactsNamesOnly: e.projectNetworkFactsNamesOnly,
		RecordOwners: func(sessionID string, gen uint64, ws []capability.Workload) {
			e.obs.recordOwners(gen, ws, func(capability.Workload) workloadOwner {
				return browseOwner(sessionID)
			})
		},
		RecordExpected: func(sessionID string, gen uint64) {
			e.expect.recordExpected(browseOwner(sessionID), gen)
		},
		KickBrowse:       e.KickBrowse,
		PublishRoutesNow: e.PublishRoutesNow,
		EnsureMemo:       make(map[string]contract.Memo),
	})
	// dataservices 应用模块装配（B2e 应用模块归一，ADR-0096 决策 1 单应用模块包——自
	// databases 应用模块〔P5 wave-4 拆包形态，ADR-0088 决策 2/3/4〕整包更名，
	// Deps 注入面逐字段零改）：rotateMu/ensure cache 表随域走（表本体组合根铸造
	// 注入，metastatewrite D3）；utility 与 backups 同源双持（ExtensionsOf 探测
	// 单次、两副本各持——P5 过渡形态通则二）；volumePinner 端口绑 intake
	// 应用模块提升方法（e.intake.PinVolumes/ApplyVolumePinning：真源随应用模块，
	// 绑定=组合根方向）；Database 投影前段 ProjectDatabase 随应用模块包
	//（dataservices/projection.go，唯一消费者——egress 过滤公式真源
	// contract.EgressSubset 与核投影面同源）；观测窄接口窄化为 database
	// 轴签名——databaseOwner 铸造（ownerDatabase 域判别）留核绑定闭包，应用模块
	// 只消费 databaseID（workloadOwner 不全域下沉的 P5 wave-4 重裁）；
	// kickDatabases 留核消费（KickDatabases 留核，Loop 挂载核持有；kick 面
	// 分类型——pubsub/bucketstore 的 kick 随 B4b/B5）。kind descriptor 不经
	// Deps 注入：注册表住 domain（dataservicetemplate），应用模块分类型步直读。
	e.dataservices = dataservices.New(dataservices.Deps{
		Runtime:                      deps.Runtime,
		Repos:                        deps.Stores,
		Cipher:                       deps.Cipher,
		Utility:                      capability.FacesOf(deps.Runtime).Utility,
		Log:                          log,
		Clock:                        clock,
		LockMaintenance:              e.lockMaintenance,
		BoundedStep:                  e.boundedStep,
		EnsureFresh:                  e.ensureFresh,
		EnsureRemember:               e.ensureRemember,
		EnsureForget:                 e.ensureForget,
		AppliedRemember:              e.appliedRemember,
		AppliedForget:                e.appliedForget,
		AppliedDigestOf:              e.appliedDigestOf,
		ProjectTeam:                  e.projectTeam,
		ProjectTeamOf:                e.projectTeamOf,
		ProjectNetworkFacts:          e.projectNetworkFacts,
		ProjectNetworkFactsNamesOnly: e.projectNetworkFactsNamesOnly,
		EnsureDatabaseVolume:         e.ensureDatabaseVolume,
		DatabasePassword:             e.databasePassword,
		PinVolumes:                   e.intake.PinVolumes,
		ApplyVolumePinning:           e.intake.ApplyVolumePinning,
		GuardVolumeCarriers:          e.GuardVolumeCarriers,
		ControlPlaneNode:             e.controlPlaneNode,
		KickDatabases:                e.KickDatabases,
		RecordOwners: func(databaseID string, gen uint64, ws []capability.Workload) {
			e.obs.recordOwners(gen, ws, func(capability.Workload) workloadOwner {
				return databaseOwner(databaseID)
			})
		},
		RecordExpected: func(databaseID string, gen uint64) {
			e.expect.recordExpected(databaseOwner(databaseID), gen)
		},
		ForgetExpected: func(databaseID string) {
			e.expect.forgetExpected(databaseOwner(databaseID))
		},
		ForgetObservation: e.obs.forget,
		ObservationOf:     e.obs.observationOf,
		EnsureMemo:        make(map[string]contract.Memo),
		// dynamic 热应用 ensure cache 表本体（B2d，ADR-0095 决策 6）：组合根铸造
		// 注入（metastatewrite D3——锁协议留核 ensureMu，应用模块经端口消费）。
		AppliedMemo: make(map[string]string),
		// pubsub 类型端口（B4b，ADR-0096 决策 1）：观测窄接口的 pubsub 轴窄化
		// ——pubsubOwner 铸造（ownerPubSub 域判别）留核绑定闭包；ensure cache 表本体
		// 组合根铸造注入（类型状态面分持）。
		EnsurePubSubVolume: e.ensurePubSubVolume,
		PubSubPassword:     e.pubsubPassword,
		KickPubSubs:        e.KickPubSubs,
		RecordPubSubOwners: func(pubsubID string, gen uint64, ws []capability.Workload) {
			e.obs.recordOwners(gen, ws, func(capability.Workload) workloadOwner {
				return pubsubOwner(pubsubID)
			})
		},
		RecordPubSubExpected: func(pubsubID string, gen uint64) {
			e.expect.recordExpected(pubsubOwner(pubsubID), gen)
		},
		ForgetPubSubExpected: func(pubsubID string) {
			e.expect.forgetExpected(pubsubOwner(pubsubID))
		},
		EnsurePubSubMemo: make(map[string]contract.Memo),
		// bucketstore 类型端口（B5，ADR-0096 决策 1）：观测窄接口的 bucketstore
		// 轴窄化——bucketStoreOwner 铸造（ownerBucketStore 域判别）留核绑定
		// 闭包；ensure cache 表本体组合根铸造注入（类型状态面分持）。
		EnsureBucketStoreVolume: e.ensureBucketStoreVolume,
		BucketStoreSecretKey:    e.bucketStoreSecretKey,
		KickBucketStores:        e.KickBucketStores,
		RecordBucketStoreOwners: func(bucketStoreID string, gen uint64, ws []capability.Workload) {
			e.obs.recordOwners(gen, ws, func(capability.Workload) workloadOwner {
				return bucketStoreOwner(bucketStoreID)
			})
		},
		RecordBucketStoreExpected: func(bucketStoreID string, gen uint64) {
			e.expect.recordExpected(bucketStoreOwner(bucketStoreID), gen)
		},
		ForgetBucketStoreExpected: func(bucketStoreID string) {
			e.expect.forgetExpected(bucketStoreOwner(bucketStoreID))
		},
		EnsureBucketStoreMemo: make(map[string]contract.Memo),
		// 子资源对账段确保缓存表本体（B8，design §4.7——两类型共用一键空间）。
		SubResourceMemo: make(map[string]contract.Memo),
	})
	// manageds 应用模块装配（P5 wave-4 拆包形态，ADR-0088 决策 2/3）：嵌入已
	// 解开——应用模块独立成包，核经构造注入持有；ensure cache 表随域走（表本体组合
	// 根铸造注入，metastatewrite D3）；proxy/registry/logging 副本双持
	//（消费面裁定：核消费占优，随域迁入会强制核改写访问器——metrics
	// 撞名先例不适用）；metricsProvider 绑 metrics 应用模块的提升访问器（窄
	// 事实端口闭包，通则五）；reconcileNodes/publishRoutes 是设计稿 §4
	// 例外边的端口归位（真源留核、Step 单点编排消费）；gensFor 留核
	//（ensureMu 锁与 gens map 配对裁定——managedGens 表本体随核，应用模块经
	// 端口读写，ManagedGen 类型随应用模块包）；观测窄接口窄化为 system 轴
	// 签名——systemOwner 铸造（ownerSystem 域判别）留核绑定闭包，应用模块
	// 只消费 gen/ws 与 process（workloadOwner 不全域下沉的 P5 wave-4
	// 重裁，browses 会话轴同思路）。
	e.manageds = manageds.New(manageds.Deps{
		Proxy:                 deps.Proxy,
		Registry:              deps.Registry,
		Logging:               deps.Logging,
		Runtime:               deps.Runtime,
		Log:                   log,
		Clock:                 clock,
		LockMaintenance:       e.lockMaintenance,
		BoundedStep:           e.boundedStep,
		EnsureFresh:           e.ensureFresh,
		EnsureRemember:        e.ensureRemember,
		EnsureForget:          e.ensureForget,
		ActiveProjectNetworks: e.activeProjectNetworks,
		ActiveProjectIDs:      e.activeProjectIDs,
		ControlPlaneNode:      e.controlPlaneNode,
		GensFor:               e.gensFor,
		ReconcileNodes:        e.reconcileNodes,
		PublishRoutes:         e.publishRoutes,
		MetricsProvider:       e.MetricsProvider,
		RecordOwners: func(gen uint64, ws []capability.Workload) {
			e.obs.recordOwners(gen, ws, func(w capability.Workload) workloadOwner {
				return systemOwner(w.Process)
			})
		},
		RecordExpected: func(process string, gen uint64) {
			e.expect.recordExpected(systemOwner(process), gen)
		},
		EnsureMemo: make(map[string]contract.Memo),
	})
	e.managedGens = make(map[string]*manageds.ManagedGen)
	// diagnosis 应用模块装配（P5 wave-6 拆包形态，ADR-0088 决策 1/2/3——终态
	// 第 9 个应用模块，九个应用模块对账闭合）：读时装配器（无环无写面），观测导出面端口
	// 绑核方法值（ObservationSnapshot/WorkloadProcess——obs 私有缓存唯一
	// 合法出口，metrics 的 WorkloadProcess 端口同款）；事件账本行读绑
	// repos.Outbox；告警行读绑核门面 AlertRuleRepo（repo 持有权随 metrics
	// 应用模块，P5 wave-2——组合根方向绑定，模块间零 import）；部署行读绑
	// repos.Deployments（第五事实源，立案③残留缺口批——repo 端口与 Outbox
	// 同款直绑）。
	e.diagnosis = diagnosis.New(diagnosis.Deps{
		Clock:               clock,
		Outbox:              deps.Stores.Outbox,
		AlertRules:          e.AlertRuleRepo(),
		Deployments:         deps.Stores.Deployments,
		ObservationSnapshot: e.ObservationSnapshot,
		WorkloadProcess:     e.WorkloadProcess,
	})
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
	// 循环表（表序 = DriveOnce 手动驱动序：schedule 先于 task——同一轮内
	// 铸出的 Task 即刻进补足链；goroutine 挂载/Kick 顺序随表，环间独立
	// 无依赖）。B2e（ADR-0096 决策 1，红队 B-击二翻案）：数据服务三种数据服务类型
	// 各一循环（database 保留名 / pubsub / bucketstore，10→12），step 同源
	// 调 dataservices 应用模块分类型步 StepFor(kind)——每类型一循环 = 每类型一写者 +
	// 故障域隔离（族 panic 只死类型循环，loop.go loop failure 教义原样执法）；
	// pubsub/bucketstore 随 B4b/B5 descriptor 登记实职化（登记前为 no-op
	// 占位循环的机制保留给未来类型）。
	e.rings = []ring{
		{name: "deployment", loop: e.loop, step: e.step},
		{name: "build", loop: e.buildLoop, step: contract.StepOf(e.intake, "build")},
		{name: "managed", loop: e.managedLoop, step: contract.StepOf(e.manageds, "managed")},
		{name: "database", loop: e.databaseLoop, step: contract.StepOf(e.dataservices, "database")},
		{name: "pubsub", loop: e.pubsubLoop, step: contract.StepOf(e.dataservices, "pubsub")},
		{name: "bucketstore", loop: e.bucketstoreLoop, step: contract.StepOf(e.dataservices, "bucketstore")},
		{name: "backup", loop: e.backupLoop, step: contract.StepOf(e.backups, "backup")},
		{name: "logging", loop: e.loggingLoop, step: contract.StepOf(e.logpipes, "logging")},
		{name: "metrics", loop: e.metricsLoop, step: contract.StepOf(e.metrics, "metrics")},
		{name: "schedule", loop: e.scheduleLoop, step: e.scheduleStep},
		{name: "task", loop: e.taskLoop, step: e.taskStep},
		{name: "browse", loop: e.browseLoop, step: contract.StepOf(e.browses, "browse")},
	}
	return e
}
