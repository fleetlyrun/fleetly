// Package dataservices 是托管数据服务单应用模块包（ADR-0096 决策 1「统一托管
// 服务机制 + 类型特化」；design 2026-11-04 §4.1 L3 / §4.3）：database/pubsub/
// bucketstore 三种数据服务类型共用一颗应用模块承载收敛生命周期（行 → 投影 → 材料 →
// Ensure → 观测 → status 推进；Update 双车道；凭证轮换；级联拆除），类型
// 差异经 domain 侧 kind descriptor 注册表（dataservicetemplate.RegisterKind/
// LookupKind）与各类型模板包承载——受管域「一个通用 reconciler + N 个
// Describe()」（ADR-0004）的用户域对称形态。
//
// 循环表形态（红队 B-击二翻案定形）：**单应用模块包 + 循环表三条目**——engine
// construct.go 的 database（保留名）/pubsub/bucketstore 三循环各经
// StepFor(kind) 挂载本应用模块的分类型步 Step(ctx, kind)；注册表缺席的类型
// 对应循环步 no-op 立返（机制先行占位：pubsub/bucketstore 随 B4b/B5 登记
// descriptor 即活，循环表零改）。三循环并发调 Step：每类型一循环 = 每类型一写者，
// 类型状态面各自分持（本批只有 database 类型——ensure/applied ensure cache 表、
// utilityMu/replayFails/rotateMu）；loop.go「panic → loop failure」教义原样执法
// （类型级 bug 只死类型循环、进 health 面，不引入类型步 recover）。
//
// 承继：本包自 internal/application/databases 整包更名而来（B2e 应用模块归一批，
// 2026-11-06——databases 应用模块是 P5 wave-4 拆包形态〔ADR-0088 决策 2〕；
// 迁移形态取整包改名而非 re-export 包：应用模块两两禁 import〔层内互引矩阵〕
// 使「dataservices 包 re-export databases 包」结构性不可行，且 ADR-0096 立
// 法的即是单应用模块包）。明示漂移清单（design §4.3，红队 C-AT-02 口径——不
// 宣称零漂移）：①ensure cache 签名第四段（B2d 已落）；②循环 step 指向（database 循环
// e.databases.Step → e.dataservices.StepFor(KindDatabase)）；③应用模块包名
// （databases → dataservices，engine 字段/门面委托/gRPC import 同批改道）。
// 不变面：循环名 database、health 面（HealthComponents/DeadRings 只列死循环）、
// KickDatabases 分类型 kick、gRPC/CLI/Console 可观测行为（databases golden/
// apitest 零漂移是本批核心验收）。
//
// 内核契约共享面（ensure cache 协议/铸名与指纹公式）在 internal/application/
// engine/contract；禁回引 *engine.Engine（enginebackref 跨包执法）；观测窄接口
// 窄化为 database 轴签名（databaseOwner 铸造留核绑定闭包）。
package dataservices

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// kindStep 是一类数据服务的收敛步：kind 词面与实现的单源配对。
// Loops()（循环表）与 Step（分类型分发）共用本表——新增类型只改
// kindSteps 初始化一处，不再维护 switch + 循环表两份清单。
type kindStep struct {
	kind string
	step func(context.Context)
}

// DataServices 是托管数据服务应用模块实例态（ADR-0096 决策 1 单应用模块包；B2e 自
// databases.Databases 更名——接收者短名 d 不动，P4 wave-2a 三个应用模块裁定承继）。
// 状态面按类型分持：下列字段全部属 database 类型（B2e 唯一在册类型）；pubsub/
// bucketstore 的类型状态随 B4b/B5 以各自字段/子结构加入，**跨类型共享的可变
// 态必须自带锁**（三循环并发调 Step 的并发安全约定——ensure cache 端口单锁留核
// ensureMu、utilityMu per-entity、replayMu/rotateMu 随域，皆既有）。依赖
// 显式逐字段 + 内核端口注入（construct.go 装配）。ensure cache 表 ensure/applied
// 随域持有（map 本体在组合根铸造注入——metastatewrite D3 的「表只在组合
// 根/meta init 出生」纪律）。禁挂 *Engine 回引（enginebackref 契约测试执法）。
type DataServices struct {
	caches contract.EnsureCaches // ensure cache 句柄铸造面（五表各自铸造——互斥量共享单锁）
	ensure contract.EnsureCache  // databaseID → 上次成功 Ensure
	// ensurePub 是 pubsub 类型的 per-entity Ensure 确保缓存表句柄（B4b；类型
	// 状态面分持——三循环并发调 Step，各类型一句柄）。
	ensurePub contract.EnsureCache
	// ensureBkt 是 bucketstore 类型的 per-entity Ensure 确保缓存表句柄（B5；
	// 每类型一循环=每类型一写者）。
	ensureBkt contract.EnsureCache
	// applied 是 dynamic 旋钮热应用的 per-entity 确保缓存表句柄（databaseID
	// → 已应用 dynamic 子集摘要；B2d，ADR-0095 决策 6）。
	applied contract.AppliedCache
	// utilityMu 是 dynamic 旋钮热应用动作族的 per-entity 互斥（B2d，
	// ADR-0095 决策 5 单写边界，红队 B-击一）：Update API 热应用与循环重放
	// 共持——Update 全程持锁使 apply→ensure cache→行三连原子化。与 rotateMu 分界
	// 见 update.go 包注（凭证面 vs 配置面，无共享写面不共锁）。
	utilityMu utilityLocks
	// replayFails 是循环重放失败退避的 per-entity 锚点（databaseID → 最近
	// 失败时刻；一窗内不重试，防持续故障面的工具容器 churn——红队 A7）。
	replayMu    sync.Mutex
	replayFails map[string]time.Time
	// rotateMu 串行化 Database 凭证轮换（IA v3 二期⑤b）：并发轮换的
	// utility/Secret 写交错可能落下 Secret 与数据面不一致的终态——全域
	// 单飞是罕见运维动作的最小诚实防线（rotate.go 注）。P4 wave-2a 随域
	// 迁入（ADR-0088 决策 4：rotateMu→databases）。
	rotateMu sync.Mutex
	// subMemo 是子资源对账段的 per-父实例确保缓存表句柄（B8，design §4.7：
	// 行集期望态摘要 + 60s 强制重放窗；键 = 类型前缀+父实例 id）。
	subMemo contract.EnsureCache
	// kindSteps 是分类型收敛步表（kind 词面 → 实现；Loops 与 Step 共用，
	// 固定序 database/pubsub/bucketstore——循环表注册序稳定）。
	kindSteps []kindStep

	// 显式依赖（实测消费面，P4 wave-2a 逐字段注入）：runtime 是载体动词
	// 消费面（Ensure/Remove——核副本双持，P5 过渡形态）；repos 是行读写
	// 聚合目录（Databases 行集/Secrets 凭证——transact.Stores 整体注入，
	// ADR-0088 决策 3）；cipher 是凭证 Secret 解封/重封（核副本双持）；
	// utility 是 Runtime 工具容器扩展接口（rotate 的数据面改密消费——装配期
	// 经 ExtensionsOf 探测，与 backups 应用模块同名副本双持，P5 过渡形态通则二；
	// 旋转恢复链的 backups 消费经提升读应用模块自有副本=各自单一真源）；
	// log/clock 域内日志与节拍。
	runtime capability.Runtime
	repos   transact.Stores
	cipher  *material.Cipher
	utility capability.RuntimeUtility
	log     *slog.Logger
	clock   state.Clock

	// 内核端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// lockMaintenance 是 maintenance 端口读半边（Ensure/Remove 与网络重建
	// 串行化，ADR-0046；端口名对齐锁入口方法名集——maintenanceguard 判据
	// 继续命中）；boundedStep 是步带界端口（ManagedStepTimeout 事实面，
	// ensure.go 统一入口）；ensureFresh/ensureRemember/ensureForget 是
	// ensure cache 端口（签名单源 engine/contract——ensureMu 单锁留核、ensure cache map 随域，
	// ADR-0088 决策 4）；appliedRemember/appliedForget/appliedDigestOf 是
	// dynamic 热应用 ensure cache 端口（B2d，ADR-0095 决策 6——同锁同纪律的轻
	// 半边：map[string]string 表随域、锁留核）；projectTeam/projectTeamOf/projectNetworkFacts/
	// projectNetworkFactsNamesOnly 是 resolver 端口（解析器五族住核 resolve.go，
	// resolutionface 锚不动）；ensureDatabaseVolume/databasePassword 是
	// resolver 端口的卷登记/凭证解析族（同面；卷登记自 ADR-0097 返回行——
	// 投影载体键取行 id）；pinVolumes/applyVolumePinning 是 volumePinner
	// 端口（卷钉住双动词挂 intake 应用模块提升方法——组合根方向绑定）；
	// guardVolumeCarriers 是存量 adoption 契约测试端口（ADR-0097 决策 3，真源
	// 核 volumes.go——materialize 与本域单源）；kickDatabases 是轮换/Update
	// 后的收敛循环唤醒窄句柄（批 2 生长点实化 Kicker(name)——只含 Kick 一面；
	// kick 面分类型——pubsub/bucketstore 的 kick 随 B4b/B5）；obsDatabase/obsPubSub/obsBucketStore 是观测域句柄
	// 三 kind 各一枚（批 1①b，BindDomain 铸造——域判别随句柄收口，模块只
	// 消费裸行 ID；观测缓存键 = 投影 Workload ID = 行 ID 的恒等在此轴上
	// 成立）；forgetObservation/observationOf 是域无关的观测读/忘口。
	lockMaintenance              func() func()
	boundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	projectTeam                  func(ctx context.Context, projectID string) (string, error)
	projectTeamOf                func(projectID string) string
	projectNetworkFacts          func(ctx context.Context, projectID string) ([]string, map[string]bool)
	projectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	ensureDatabaseVolume         func(ctx context.Context, row *database.Database) (*volume.Volume, error)
	databasePassword             func(ctx context.Context, row *database.Database) (string, error)
	pinVolumes                   func(ctx context.Context, ws []capability.Workload, projectID string) error
	applyVolumePinning           func(ctx context.Context, ws []capability.Workload, projectID string) error
	guardVolumeCarriers          func(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, projectID string) error
	controlPlaneNode             func(ctx context.Context) (string, error)
	kickDatabase                 contract.Kicker
	obsDatabase                  contract.Observations
	forgetObservation            func(databaseID string)
	observationOf                func(databaseID string) (capability.WorkloadEvent, bool)
	// pubsub 类型端口（B4b，ADR-0096 决策 1）：卷登记/凭证解析/kick。
	ensurePubSubVolume func(ctx context.Context, row *pubsub.PubSub) (*volume.Volume, error)
	pubsubPassword     func(ctx context.Context, row *pubsub.PubSub) (string, error)
	kickPubSub         contract.Kicker
	obsPubSub          contract.Observations
	// bucketstore 类型端口（B5，ADR-0096 决策 1）：卷登记/凭证解析/kick。
	ensureBucketStoreVolume func(ctx context.Context, row *bucketstore.BucketStore) (*volume.Volume, error)
	bucketStoreSecretKey    func(ctx context.Context, row *bucketstore.BucketStore) (string, error)
	kickBucketStore         contract.Kicker
	obsBucketStore          contract.Observations
}

// Deps 是应用模块依赖与内核端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：核经构造注入持有应用模块；依赖显式逐字段注入 +
// 内核端口绑定为核方法值/闭包；ensure cache 表本体随句柄未导出（批 1 句柄化，memoMapRegistry 登记制退役）。
type Deps struct {
	Runtime capability.Runtime
	Repos   transact.Stores
	Cipher  *material.Cipher
	Utility capability.RuntimeUtility
	Log     *slog.Logger
	Clock   state.Clock

	LockMaintenance              func() func()
	BoundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	Caches                       contract.EnsureCaches
	ProjectTeam                  func(ctx context.Context, projectID string) (string, error)
	ProjectTeamOf                func(projectID string) string
	ProjectNetworkFacts          func(ctx context.Context, projectID string) ([]string, map[string]bool)
	ProjectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	EnsureDatabaseVolume         func(ctx context.Context, row *database.Database) (*volume.Volume, error)
	DatabasePassword             func(ctx context.Context, row *database.Database) (string, error)
	PinVolumes                   func(ctx context.Context, ws []capability.Workload, projectID string) error
	ApplyVolumePinning           func(ctx context.Context, ws []capability.Workload, projectID string) error
	GuardVolumeCarriers          func(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, projectID string) error
	ControlPlaneNode             func(ctx context.Context) (string, error)
	DatabaseKick                 contract.Kicker
	// 观测域句柄三 kind 各铸一枚（批 1①b，BindDomain 铸造——域判别随
	// 句柄收口）；ForgetObservation/ObservationOf 是域无关的观测读/忘口
	//（缓存键 = 投影 Workload ID = 行 ID 的恒等跨类型成立，三 kind 复用）。
	DatabaseObs       contract.Observations
	PubSubObs         contract.Observations
	BucketStoreObs    contract.Observations
	ForgetObservation func(databaseID string)
	ObservationOf     func(databaseID string) (capability.WorkloadEvent, bool)

	// pubsub 类型端口（B4b）。
	EnsurePubSubVolume func(ctx context.Context, row *pubsub.PubSub) (*volume.Volume, error)
	PubSubPassword     func(ctx context.Context, row *pubsub.PubSub) (string, error)
	PubSubKick         contract.Kicker
	// bucketstore 类型端口（B5）。
	EnsureBucketStoreVolume func(ctx context.Context, row *bucketstore.BucketStore) (*volume.Volume, error)
	BucketStoreSecretKey    func(ctx context.Context, row *bucketstore.BucketStore) (string, error)
	BucketStoreKick         contract.Kicker
}

// New 构造数据服务应用模块（不启动——循环步由 engine 核驱动：循环表三条目各经
// StepFor(kind) 挂载分类型步）。
func New(deps Deps) *DataServices {
	d := &DataServices{
		runtime:                      deps.Runtime,
		repos:                        deps.Repos,
		cipher:                       deps.Cipher,
		utility:                      deps.Utility,
		log:                          deps.Log,
		clock:                        deps.Clock,
		caches:                       deps.Caches,
		ensure:                       deps.Caches.NewEnsureCache(),
		ensurePub:                    deps.Caches.NewEnsureCache(),
		ensureBkt:                    deps.Caches.NewEnsureCache(),
		applied:                      deps.Caches.NewAppliedCache(),
		subMemo:                      deps.Caches.NewEnsureCache(),
		lockMaintenance:              deps.LockMaintenance,
		boundedStep:                  deps.BoundedStep,
		projectTeam:                  deps.ProjectTeam,
		projectTeamOf:                deps.ProjectTeamOf,
		projectNetworkFacts:          deps.ProjectNetworkFacts,
		projectNetworkFactsNamesOnly: deps.ProjectNetworkFactsNamesOnly,
		ensureDatabaseVolume:         deps.EnsureDatabaseVolume,
		databasePassword:             deps.DatabasePassword,
		pinVolumes:                   deps.PinVolumes,
		applyVolumePinning:           deps.ApplyVolumePinning,
		guardVolumeCarriers:          deps.GuardVolumeCarriers,
		controlPlaneNode:             deps.ControlPlaneNode,
		kickDatabase:                 deps.DatabaseKick,
		obsDatabase:                  deps.DatabaseObs,
		forgetObservation:            deps.ForgetObservation,
		observationOf:                deps.ObservationOf,
		ensurePubSubVolume:           deps.EnsurePubSubVolume,
		pubsubPassword:               deps.PubSubPassword,
		kickPubSub:                   deps.PubSubKick,
		obsPubSub:                    deps.PubSubObs,
		ensureBucketStoreVolume:      deps.EnsureBucketStoreVolume,
		bucketStoreSecretKey:         deps.BucketStoreSecretKey,
		kickBucketStore:              deps.BucketStoreKick,
		obsBucketStore:               deps.BucketStoreObs,
	}
	// 分类型收敛步的单源表（Loops 与 Step 共用；新增类型只改这一处）。
	d.kindSteps = []kindStep{
		{kind: dataservicetemplate.KindDatabase, step: d.stepDatabase},
		{kind: dataservicetemplate.KindPubSub, step: d.stepPubSub},
		{kind: dataservicetemplate.KindBucketStore, step: d.stepBucketStore},
	}
	return d
}

// Step 是数据服务应用模块的分类型收敛步（ADR-0096 决策 1 循环表三条目：database/
// pubsub/bucketstore 三循环各以自家 kind 调本面）。判序：
//
//  1. descriptor 注册表缺席 → no-op 立返（机制先行占位——pubsub/
//     bucketstore 在 B4b/B5 登记 descriptor 前恒走此路，循环表零改即活）；
//  2. 在册类型 → 分发到该类型的收敛步（单写者；逐行收敛，错误逐行记日志
//     不阻断——类型内行为与归一前逐字节一致）；
//  3. 在册而本应用模块无该类型收敛实现 → panic（装配缺口是编程错误：loop.go
//     「panic → loop failure」教义把它显式送进 health 面——与红队 B-击二
//     「类型级 bug 必须进 health」口径同向，不在此 recover 收窄）。
//
// 并发：三循环并发调用；类型分发后只触该类型状态面（见 DataServices 注）。
func (d *DataServices) Step(ctx context.Context, kind string) {
	if _, ok := dataservicetemplate.LookupKind(kind); !ok {
		return
	}
	for _, ks := range d.kindSteps {
		if ks.kind == kind {
			ks.step(ctx)
			return
		}
	}
	panic("dataservices: kind " + kind + " is registered but the satellite carries no convergence step for it (wire the kind step in the same batch as its descriptor)")
}

// StepFor 返回绑定 kind 的循环步形态（construct.go 循环表 step 的挂载面：
// `step: e.dataservices.StepFor(dataservicetemplate.KindDatabase)`——ringregistry
// 契约测试按调用链 + 常量选择子登记冻结，每类型一循环机械可查）。闭包逐拍转调
// Step：注册表判在调用时点，登记落批后占位循环即活。
func (d *DataServices) StepFor(kind string) func(context.Context) {
	return func(ctx context.Context) { d.Step(ctx, kind) }
}

// resourcesOf 把行资源面折为载体上限（rowResources/pubsubResources/
// bucketStoreResources 三域同构段的共用核——三域行的 CPUMillis/MemoryMB 均
// *int32；nil = 无限制 = 现状，ADR-0095 决策 7；static 语义——指纹进
// ManagedFingerprint，资源变更即受监督滚动）。
func resourcesOf(cpu, mem *int32) *capability.Resources {
	if cpu == nil && mem == nil {
		return nil
	}
	res := &capability.Resources{}
	if cpu != nil {
		res.CPUMillis = int64(*cpu)
	}
	if mem != nil {
		res.MemoryMB = int64(*mem)
	}
	return res
}

// advanceRowStatus 是三域行状态推进的共用核（advanceDatabaseStatus/
// advancePubSubStatus/advanceBucketStoreStatus 同构段单源）：从观测缓存取
// 事件，gen 匹配且与行现值不同才落 SetStatus（方法值直接适配——三 repo 的
// SetStatus(ctx, id, status) 同签名）。观测缓存键 = 投影 Workload ID = 行 ID。
// 无迁移动作返回 nil；SetStatus 错误原样上抛，日志留调用方（行轴标签随域）。
func (d *DataServices) advanceRowStatus(ctx context.Context, id, current string, setStatus func(context.Context, string, string) error, gen uint64) error {
	ev, seen := d.observationOf(id)
	if status, ok := dbStatusFromObservation(ev, gen); ok && seen && status != current {
		return setStatus(ctx, id, status)
	}
	return nil
}
