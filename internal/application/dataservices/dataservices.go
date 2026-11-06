// Package dataservices 是托管数据服务单卫星包（ADR-0096 决策 1「统一托管
// 服务机制 + 家族特化」；design 2026-11-04 §4.1 L3 / §4.3）：database/pubsub/
// bucketstore 三家族共用一颗卫星承载收敛生命周期（行 → 投影 → 材料 →
// Ensure → 观测 → status 推进；Update 双车道；凭证轮换；级联拆除），家族
// 差异经 domain 侧 family descriptor 注册表（enginetemplate.RegisterFamily/
// LookupFamily）与各家族模板包承载——受管域「一个通用 reconciler + N 个
// Describe()」（ADR-0004）的用户域对称形态。
//
// 环表形态（红队 B-击二翻案定形）：**单卫星包 + 环表三条目**——engine
// construct.go 的 database（保留名）/pubsub/bucketstore 三环各经
// StepFor(family) 挂载本卫星的分族步 Step(ctx, family)；注册表缺席的家族
// 对应环步 no-op 立返（机制先行占位：pubsub/bucketstore 随 B4b/B5 登记
// descriptor 即活，环表零改）。三环并发调 Step：每族一环 = 每族一写者，
// 族状态面各自分持（本批只有 database 族——ensure/applied memo 表、
// utilityMu/replayFails/rotateMu）；loop.go「panic → ring dead」教义原样执法
// （族级 bug 只死族环、进 health 面，不引入家族步 recover）。
//
// 承继：本包自 internal/application/databases 整包更名而来（B2e 卫星归一批，
// 2026-11-06——databases 卫星是 P5 wave-4 拆包形态〔ADR-0088 决策 2〕；
// 迁移形态取整包改名而非 re-export 包：卫星两两禁 import〔环内互引矩阵〕
// 使「dataservices 包 re-export databases 包」结构性不可行，且 ADR-0096 立
// 法的即是单卫星包）。明示漂移清单（design §4.3，红队 C-AT-02 口径——不
// 宣称零漂移）：①memo 签名第四段（B2d 已落）；②环 step 指向（database 环
// e.databases.Step → e.dataservices.StepFor(FamilyDatabase)）；③卫星包名
// （databases → dataservices，engine 字段/门面委托/gRPC import 同批改道）。
// 不变面：环名 database、health 面（HealthComponents/DeadRings 只列死环）、
// KickDatabases 分族 kick、gRPC/CLI/Console 可观测行为（databases golden/
// apitest 零漂移是本批核心验收）。
//
// 机制契约共享面（memo 协议/铸名与指纹公式）在 internal/application/
// engineports；禁回引 *engine.Engine（enginebackref 跨包执法）；观测窄接口
// 窄化为 database 轴签名（databaseOwner 铸造留核绑定闭包）。
package dataservices

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// DataServices 是托管数据服务卫星实例态（ADR-0096 决策 1 单卫星包；B2e 自
// databases.Databases 更名——接收者短名 d 不动，P4 wave-2a 三星裁定承继）。
// 状态面按家族分持：下列字段全部属 database 族（B2e 唯一在册族）；pubsub/
// bucketstore 的族状态随 B4b/B5 以各自字段/子结构加入，**跨族共享的可变
// 态必须自带锁**（三环并发调 Step 的并发安全约定——memo 端口单锁留核
// ensureMu、utilityMu per-entity、replayMu/rotateMu 随域，皆既有）。依赖
// 显式逐字段 + 机制端口注入（construct.go 装配）。memo 表 ensure/applied
// 随域持有（map 本体在组合根铸造注入——metastatewrite D3 的「表只在组合
// 根/meta init 出生」纪律）。禁挂 *Engine 回引（enginebackref 守卫执法）。
type DataServices struct {
	ensure map[string]engineports.Memo // databaseID → 上次成功 Ensure
	// applied 是 dynamic 旋钮热应用的 per-entity 备忘（databaseID → 已
	// 应用 dynamic 子集摘要；B2d，ADR-0095 决策 6）。表本体组合根铸造
	// 注入（metastatewrite D3 同纪律）；读写经 appliedRemember/Forget/
	// DigestOf 端口（锁协议留核 ensureMu——结构性无法绕锁直写）。
	applied map[string]string
	// utilityMu 是 dynamic 旋钮热应用动作族的 per-entity 互斥（B2d，
	// ADR-0095 决策 5 单写边界，红队 B-击一）：Update API 热应用与环重放
	// 共持——Update 全程持锁使 apply→memo→行三连原子化。与 rotateMu 分界
	// 见 update.go 包注（凭证面 vs 配置面，无共享写面不共锁）。
	utilityMu utilityLocks
	// replayFails 是环重放失败退避的 per-entity 锚点（databaseID → 最近
	// 失败时刻；一窗内不重试，防持续故障面的工具容器 churn——红队 A7）。
	replayMu    sync.Mutex
	replayFails map[string]time.Time
	// rotateMu 串行化 Database 凭证轮换（IA v3 二期⑤b）：并发轮换的
	// utility/Secret 写交错可能落下 Secret 与数据面不一致的终态——全域
	// 单飞是罕见运维动作的最小诚实防线（rotate.go 注）。P4 wave-2a 随域
	// 迁入（ADR-0088 决策 4：rotateMu→databases）。
	rotateMu sync.Mutex

	// 显式依赖（实测消费面，P4 wave-2a 逐字段注入）：runtime 是载体动词
	// 消费面（Ensure/Remove——核副本双持，P5 过渡形态）；repos 是行读写
	// 聚合目录（Databases 行集/Secrets 凭证——transact.Stores 整体注入，
	// ADR-0088 决策 3）；cipher 是凭证 Secret 解封/重封（核副本双持）；
	// utility 是 Runtime 工具容器子面（rotate 的数据面改密消费——装配期
	// 经 FacesOf 探测，与 backups 卫星同名副本双持，P5 过渡形态通则二；
	// 旋转恢复链的 backups 消费经提升读卫星自有副本=各自单一真源）；
	// log/clock 域内日志与节拍。
	runtime capability.Runtime
	repos   transact.Stores
	cipher  *material.Cipher
	utility capability.RuntimeUtility
	log     *slog.Logger
	clock   state.Clock

	// 机制端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// lockMaintenance 是 maintenance 端口读半边（Ensure/Remove 与网络重建
	// 串行化，ADR-0046；端口名对齐锁入口方法名集——maintenanceguard 判据
	// 继续命中）；boundedStep 是步带界端口（ManagedStepTimeout 事实面，
	// ensure.go 统一入口）；ensureFresh/ensureRemember/ensureForget 是
	// memo 端口（签名单源 engineports——ensureMu 单锁留核、memo map 随域，
	// ADR-0088 决策 4）；appliedRemember/appliedForget/appliedDigestOf 是
	// dynamic 热应用 memo 端口（B2d，ADR-0095 决策 6——同锁同纪律的轻
	// 半边：map[string]string 表随域、锁留核）；projectTeam/projectTeamOf/projectNetworkFacts/
	// projectNetworkFactsNamesOnly 是 facts 端口（解析面五族住核 resolve.go，
	// resolutionface 锚不动）；ensureDatabaseVolume/databasePassword 是
	// facts 端口的卷登记/凭证解析族（同面；卷登记自 ADR-0097 返回行——
	// 投影载体键取行 id）；pinVolumes/applyVolumePinning 是 volumePinner
	// 端口（卷钉住双动词挂 intake 卫星提升方法——组合根方向绑定）；
	// guardVolumeCarriers 是存量 adoption 守卫端口（ADR-0097 决策 3，真源
	// 核 volumes.go——materialize 与本域单源）；kickDatabases 是轮换/Update
	// 后的收敛环唤醒（Loop 挂载留核；kick 面分族——pubsub/bucketstore 的
	// kick 随 B4b/B5）；recordOwners/recordExpected/forgetExpected/
	// forgetObservation/observationOf 是观测窄接口的 database 轴窄化签名
	//（databaseOwner 铸造与域判别留核绑定闭包，卫星只消费 databaseID——
	// 观测缓存键 = 投影 Workload ID = 数据库行 ID 的恒等在此轴上成立）。
	lockMaintenance              func() func()
	boundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	ensureFresh                  engineports.EnsureFresh
	ensureRemember               engineports.EnsureRemember
	ensureForget                 engineports.EnsureForget
	appliedRemember              engineports.AppliedRemember
	appliedForget                engineports.AppliedForget
	appliedDigestOf              engineports.AppliedDigestOf
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
	kickDatabases                func()
	recordOwners                 func(databaseID string, gen uint64, ws []capability.Workload)
	recordExpected               func(databaseID string, gen uint64)
	forgetExpected               func(databaseID string)
	forgetObservation            func(databaseID string)
	observationOf                func(databaseID string) (capability.WorkloadEvent, bool)
}

// Deps 是卫星依赖与机制端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：核经构造注入持有卫星；依赖显式逐字段注入 +
// 机制端口绑定为核方法值/闭包；EnsureMemo/AppliedMemo 是随域 memo 表本体
// ——组合根铸造注入（metastatewrite D3：表只在组合根/meta init 出生））。
type Deps struct {
	Runtime capability.Runtime
	Repos   transact.Stores
	Cipher  *material.Cipher
	Utility capability.RuntimeUtility
	Log     *slog.Logger
	Clock   state.Clock

	LockMaintenance              func() func()
	BoundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	EnsureFresh                  engineports.EnsureFresh
	EnsureRemember               engineports.EnsureRemember
	EnsureForget                 engineports.EnsureForget
	AppliedRemember              engineports.AppliedRemember
	AppliedForget                engineports.AppliedForget
	AppliedDigestOf              engineports.AppliedDigestOf
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
	KickDatabases                func()
	// RecordOwners/RecordExpected/ForgetExpected/ForgetObservation/
	// ObservationOf 是观测窄接口的 database 域窄化端口（核绑定闭包内铸造
	// databaseOwner——域判别留核）。
	RecordOwners      func(databaseID string, gen uint64, ws []capability.Workload)
	RecordExpected    func(databaseID string, gen uint64)
	ForgetExpected    func(databaseID string)
	ForgetObservation func(databaseID string)
	ObservationOf     func(databaseID string) (capability.WorkloadEvent, bool)

	// EnsureMemo 是 per-Database Ensure 备忘表本体（组合根 make 后注入）。
	EnsureMemo map[string]engineports.Memo
	// AppliedMemo 是 per-Database dynamic 热应用摘要表本体（B2d，ADR-0095
	// 决策 6；组合根 make 后注入——metastatewrite D3 同纪律）。
	AppliedMemo map[string]string
}

// New 构造数据服务卫星（不启动——环步由 engine 核驱动：环表三条目各经
// StepFor(family) 挂载分族步）。
func New(deps Deps) *DataServices {
	d := &DataServices{
		runtime:                      deps.Runtime,
		repos:                        deps.Repos,
		cipher:                       deps.Cipher,
		utility:                      deps.Utility,
		log:                          deps.Log,
		clock:                        deps.Clock,
		lockMaintenance:              deps.LockMaintenance,
		boundedStep:                  deps.BoundedStep,
		ensureFresh:                  deps.EnsureFresh,
		ensureRemember:               deps.EnsureRemember,
		ensureForget:                 deps.EnsureForget,
		appliedRemember:              deps.AppliedRemember,
		appliedForget:                deps.AppliedForget,
		appliedDigestOf:              deps.AppliedDigestOf,
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
		kickDatabases:                deps.KickDatabases,
		recordOwners:                 deps.RecordOwners,
		recordExpected:               deps.RecordExpected,
		forgetExpected:               deps.ForgetExpected,
		forgetObservation:            deps.ForgetObservation,
		observationOf:                deps.ObservationOf,
		ensure:                       deps.EnsureMemo,
		applied:                      deps.AppliedMemo,
	}
	return d
}

// Step 是数据服务卫星的分族收敛步（ADR-0096 决策 1 环表三条目：database/
// pubsub/bucketstore 三环各以自家 family 调本面）。判序：
//
//  1. descriptor 注册表缺席 → no-op 立返（机制先行占位——pubsub/
//     bucketstore 在 B4b/B5 登记 descriptor 前恒走此路，环表零改即活）；
//  2. 在册族 → 分发到该族的收敛步（单写者；逐行收敛，错误逐行记日志
//     不阻断——族内行为与归一前逐字节一致）；
//  3. 在册而本卫星无该族收敛实现 → panic（装配缺口是编程错误：loop.go
//     「panic → ring dead」教义把它显式送进 health 面——与红队 B-击二
//     「家族级 bug 必须进 health」口径同向，不在此 recover 收窄）。
//
// 并发：三环并发调用；族分发后只触该族状态面（见 DataServices 注）。
func (d *DataServices) Step(ctx context.Context, family string) {
	if _, ok := enginetemplate.LookupFamily(family); !ok {
		return
	}
	switch family {
	case enginetemplate.FamilyDatabase:
		d.stepDatabase(ctx)
	default:
		panic("dataservices: family " + family + " is registered but the satellite carries no convergence step for it (wire the family step in the same batch as its descriptor)")
	}
}

// StepFor 返回绑定 family 的环步形态（construct.go 环表 step 的挂载面：
// `step: e.dataservices.StepFor(enginetemplate.FamilyDatabase)`——ringregistry
// 守卫按调用链 + 常量选择子登记冻结，每族一环机械可查）。闭包逐拍转调
// Step：注册表判在调用时点，登记落批后占位环即活。
func (d *DataServices) StepFor(family string) func(context.Context) {
	return func(ctx context.Context) { d.Step(ctx, family) }
}
