// engine.go 是引擎壳：Engine 汇聚结构 + 收敛环注册项（ring）+ 包级哨兵
// 错误 + 驱动暴露面（P3 收官批 8 壳收口）。参数面（options.go）、构造面
// （construct.go）、生命周期（lifecycle.go）、ensure 备忘协议与互斥底座
// （ensure.go）与各域状态 struct（随域文件）自本文件归位——函数体逐字
// 迁移，零行为变更（ADR-0075 application 环内聚形态）。
package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// 哨兵错误（API 层映射 errcode；engine 自身不依赖 apperr）。
var (
	// ErrQueueFull 是 admission 排队容量已满（ADR-0016：queue 满显式反馈，
	// 不是 409——请求没错，是背压）。
	ErrQueueFull = errors.New("engine: deployment queue is full")
	// ErrNotCancellable 是目标 Deployment 已终态（取消只作用于排队/在途）。
	ErrNotCancellable = errors.New("engine: deployment already finished")
	// ErrActiveDeployment 是 DeleteApp 收口的锁内预检命中活跃部署（ADR-0023
	// 修订：拒绝于拆载体等一切副作用之前；API 层映射 E_CONFLICT）。
	ErrActiveDeployment = errors.New("engine: app has active deployments")
	// ErrNoSuccessfulBaseline 是 Rollback 无成功基线（首次部署无回滚对象；
	// API 层映射 E_NO_BASELINE——Q-13：判定走 errors.Is，不靠文案匹配）。
	ErrNoSuccessfulBaseline = errors.New("engine: no successful baseline to roll back to")
	// ErrCrossProjectRefNotApproved 是部署受理命中未批准的跨 Project 网络
	// 引用（ADR-0013 附录 A.3 fail-closed：declare+approve 或移除引用；
	// API 层映射 E_CONFLICT）。
	ErrCrossProjectRefNotApproved = errors.New("engine: cross-project network reference is not approved")
	// ErrPreDeployNetworkUnknown 是 preDeployJobs 受理命中项目内不存在的
	// 裸网名（B12 P3-5 fail-closed：建网/taskGroup:/project: 引用/删附件；
	// API 层映射 E_INVALID_ARGUMENT）。
	ErrPreDeployNetworkUnknown = errors.New("engine: pre-deploy job attaches an unknown network")
	// ErrPlatformBackupInProgress 是 Platform Backup 已有执行在途（H6 引擎级
	// 单飞：快照/仓锁外步骤不得并发，否则可产出损坏但 restic check 通过的
	// 恢复点；API 层映射 E_CONFLICT，节拍环静默跳过）。
	ErrPlatformBackupInProgress = errors.New("engine: platform backup already in progress")
	// ErrInvalidRegistryRepository 是 Registry 读代理的仓名形态非法（R03-M13）：
	// 受理面映射 E_INVALID_ARGUMENT（400），与"形态合法但越纲"的 404
	// （state.ErrNotFound）分层——避免把用户输入错误伪装成运行期 500。
	ErrInvalidRegistryRepository = errors.New("engine: repository is not a valid platform repository name")
)

// ring 是收敛环注册项（Start/DriveOnce 表驱动：goroutine 挂载、启动
// Kick、手动驱动序走同一张表——新环 = 表加一行 + 一个 Loop 字段；表序
// 即 DriveOnce 手动驱动序（schedule 先于 task：同一轮内铸出的 Task 即刻
// 进补足链））。
type ring struct {
	name string
	loop *Loop
	step func(context.Context)
}

// Engine 是部署收敛引擎：Deployment 状态机单写者 + admission + Runtime
// Watch 消费。写路径全部四件一拍（CAS + Outbox + 审计；部署记录无
// tombstone）；决策读平台权威表，观测缓存仅作信号（架构 §6）。
type Engine struct {
	db      state.DB
	log     *slog.Logger
	runtime capability.Runtime
	clock   state.Clock
	opts    Options

	// repos 是本引擎的聚合目录（db 绑定形态——组合根经 Binder 以
	// db.Runner() 造出；读径直调，CQRS-lite）。事务作用域经 transact.Tx.Do
	// 的闭包 s 走同一类型的 tx 绑定形态（ADR-0080）。
	repos transact.Stores
	tx    transact.Tx

	// backups 是 Backup 域卫星（F2.2，ADR-0039：台账 repo + 备份环 +
	// Platform Backup 执行链）。P4 wave-1b 嵌入改形（ADR-0088 决策 2）：
	// 接收者已下域 *backups，方法经嵌入提升（backupStep 仍在环表、gRPC
	// Backup 面/lifecycle 启动清扫零改）；执行链装配面（objectStore 端口
	// = 产物承载；utility 子面 = 工具容器执行，装配期经 FacesOf 从
	// Runtime 探测——rotate 域与 componentrestart 的消费经嵌入提升读零改）
	// 与单飞锁 platformBackupMu（决策 4 随域走）随卫星字段持有、
	// construct.go 装配。KickBackups 留核（Loop 挂载核持有，KickTasks
	// 同款）。
	backups
	// execs 是 exec 会话域卫星（受理/路由/生命周期；进程内活体，零持久
	// 化）。P4 wave-1 嵌入改形（ADR-0088 决策 2）：接收者已下域 *execs，
	// 方法经嵌入提升对核/外部消费面零变化；execFace/exec 时代的域依赖
	// 与机制端口（emitLedger）随卫星字段持有、construct.go 装配——Start
	// 的回环中继挂载（组合根调用）留核、经 e.execs.execFace 读。
	execs

	// browses 是 Browse 会话域卫星（F3.6，ADR-0051：会话注册表进程内活体，
	// 行是台账）。P4 wave-2a 嵌入改形（ADR-0088 决策 2）：接收者已下域
	// *browses，方法经嵌入提升（browseStep 仍在环表、gRPC Browse 受理/
	// grant 面与 routepublish 的 ephemeral 路由合并面零改）；域依赖与机制
	// 端口（facts/观测窄接口/kick/publishRoutesNow 族）随卫星字段持有、
	// construct.go 装配。KickBrowse 留核（Loop 挂载核持有，KickTasks
	// 同款）。
	browses

	// Logging 域（F2.4，ADR-0040）：受管日志存储 Provider（核消费面：build
	// 日志回读/managed 面/组件重启；采集环域经 logpipes 卫星自持）。
	logging capability.Logging
	// logpipes 是采集环卫星。P4 wave-1 嵌入改形（ADR-0088 决策 2）：接收
	// 者已下域 *logpipes，方法经嵌入提升（loggingStep 仍在环表零改）；
	// 域依赖与机制端口（rootCtx/spawn）随卫星字段持有、construct.go 装配。
	logpipes

	// metrics 是指标采集+告警域卫星（F2.5/ADR-0041 采集环 + 告警评估/
	// 通道派发 + crash-loop 通知，五文件 = metrics/alerting/channelnotify/
	// exposition/crashnotify）。P4 wave-1b 嵌入改形（ADR-0088 决策 2）：
	// 接收者已下域 *metrics，方法经嵌入提升（metricsStep 仍在环表、
	// AlertRule/Channel API 面与 observ 的 crash-loop 评估点零改）；
	// Metrics capability 与域自有 repo（alertRules/channels——diagnose 域
	// 的规则只读消费经提升字段读零改）随卫星字段持有。capability 的核内
	// 消费（managed 重启面/componentrestart providers）改经提升的
	// MetricsProvider() 访问器——卫星 struct 类型名与 capability 字段同名
	//（metrics），裸字段提升不可用（e.metrics 恒指卫星自身），本波唯一
	// 核内改点。
	metrics

	loop         *Loop
	buildLoop    *Loop
	managedLoop  *Loop
	databaseLoop *Loop
	backupLoop   *Loop // Backup 环（调度/执行/恢复/保留，ADR-0039）
	loggingLoop  *Loop // 采集环（域流对账，ADR-0040；流本体在 goroutine）
	metricsLoop  *Loop // 指标采集环（scrape/评估，ADR-0041；粗节拍锚在 step 内）
	taskLoop     *Loop // Task/Run 收敛环（janitor/补足/Ensure/收口）
	scheduleLoop *Loop // Schedule 到期拍环（F1.7）
	browseLoop   *Loop // Browse 会话收敛环（F3.6，ADR-0051）
	driftLoop    *Loop // ADR-0022 漂移扫描环（spec 对照 + 稳态看门狗）
	rings        []ring
	// peerIsolationMu/peerIsolations 是撤销 peer 后的待剥离标记集（R16-M4
	// 异步语义：请求路径只标记 + Kick，剥离由漂移扫描拍执行——不再在请求
	// goroutine 内同步 replay/Ensure）。map 在 New 初始化，读写都在
	// peerIsolationMu 内。
	peerIsolationMu sync.Mutex
	peerIsolations  map[peerIsolationRequest]bool
	// ringMu/ringDeaths 是环死亡记账（supervisor 消费 Loop.Done 后按
	// Panicked 落账；HealthComponents/DeadRings 是 health/doctor 查询面，
	// R02-M7）。map 在 New 初始化，读写都在 ringMu 内。
	ringMu     sync.Mutex
	ringDeaths map[string]bool
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	// 域状态（行为住各域文件，状态随域绑定——加环/加域不再横穿 Engine
	// 结构体；域类型定义随域文件，P3 收官批 8 壳收口归位。锁语义零漂移：
	// ensure 备忘族仍共用 ensureMu 单锁（五张 memo map 分住各域子结构）；
	// 观测/漂移/期望/收敛各域锁照旧）。P4 wave-2a：manageds/databases 两
	// 域升裸嵌入（ADR-0088 决策 2——接收者已下域，方法经嵌入提升：
	// managedStep/databaseStep 仍在环表、networkrebuild 的 managedProviders
	// 与 materialize 的 injectDatabaseEnv 等核消费零改）；rotateMu 随
	// databases 域迁出核（决策 4）。P4 wave-2b：intake 卫星裸嵌入（决策
	// 2——builder/buildsource/buildlog/gitmaterial/materials/registrycontent
	// 六文件接收者已下域 *intake，方法经嵌入提升：buildStep 仍在环表、
	// gRPC Registry/Clone 面、materialize 的材料解析/卷钉住面与 transition
	// 终态释放零改）；builders/buildOpts/ghClient 单一真源随域迁入卫星，
	// e.build 选择子经二级提升零改可达（transition.go:443 冻结边，见
	// builder.go intake 注）。
	task     tasks
	obs      observations
	drift    drifts
	expect   expects
	converge converges // ADR-0082 缺省收敛域（双通道 memo/签名生命周期/冷却记账/缺席投影缓存）
	manageds
	databases
	intake
	delivery  deploys
	proxyMemo proxies

	// ensureMu 是收敛域签名短路备忘族的共用锁（五张 memo map 分住各域
	// 子结构，锁保持单点——拆锁随域分化需要时再裁，本批锁语义零漂移）。
	ensureMu sync.Mutex

	// maintenanceMu 是载体网络维护互斥（ADR-0046 网络重建动词的串行化
	// 锚）：RebuildNetwork 持写锁贯穿 detach→rm→create→attach 全序；
	// 全部 Ensure 族调用点（materialize/databaseStep/managedStep/
	// driveEnsure/backup 环的 utility 附着）与 TeardownDatabase（拆载体
	// 是载体写动词，2026-10-05 级联批入册）持读锁——读锁间照旧并发，
	// 只有重建排他（受管 Proxy 单次 Ensure 引用全部活跃项目网络，全局
	// 粒度是对该耦合的诚实取舍，ADR-0046 决策 3）。锁等待不占步预算
	// （排队语义，见 lockMaintenance）。P4 wave-1b：backups 卫星经
	// lockMaintenance 机制端口消费（gate 注入，锁语义零漂移）；
	// platformBackupMu 已随域迁入 backups 卫星（ADR-0088 决策 4）。
	maintenanceMu sync.RWMutex

	// lifecycleMu 同步引擎生命周期（B12 P3-6）：Start 全程持锁（cancel
	// 判空/赋值 + wg.Add）与 Stop 全程持锁（cancel 置 nil + wg.Wait 排水）
	// 互斥——裸读写 cancel 是数据竞争，Add/Wait 重叠是 WaitGroup 竞争，
	// 并发双 Start 会双发全套收敛循环 goroutine。
	lifecycleMu sync.Mutex

	// runCtx 是 Start 物化的进程生命期 ctx（Q-7）：构建 goroutine 的派生
	// 根——Stop 取消它即有界排水（executeBuild 的优雅退出路径接管）。
	runCtxMu sync.Mutex
	runCtx   context.Context

	// nodeLeftSeen 是 node.left 去抖实例态（节点回归即清签名）。
	nodeLeftSeen map[string]bool

	// nodeAbsentSince 是 node.left 宽限窗计时（节点 ID → 首次缺失的引擎时
	// 刻；S11b）：连续缺失达 opts.Node.LeaveGrace 才执行离场清理，窗内回
	// 归即清条目（不删凭证/不断连/不发事件）。
	nodeAbsentSince map[string]time.Time

	// appLocks 是 App 级互斥（admission/基线重放/收口共享，N0.1 P1-3）：
	// 重放持有锁期间 Submit 排队——admission 落行与重放的 ActiveByApp
	// 复查被串行化，不存在"复查后落行、重放再 Ensure 旧 Generation 与
	// 驱动器对翻载体标签"的窗口（健康部署被 L1 误判回滚的根因）。
	//
	// 终态清条目裁决（E30）：不做。理由：①条目量级 = 进程生命期内曾被
	// 锁过的 App/Task 数（1 锁 ≈ 8B + sync.Map 槽位），趋势随受理速率缓
	// 增、无爆发面，v1 规模（N0 小团队）距关切量级几个数量级；②天真的
	// 终态删键不安全——他方 goroutine（API 动词/驱动环/观测路径并发）
	// 正持锁时删 key，新 lockTask 会铸出第二把锁，同 ID 两个临界区并行
	// = 互斥破环，比有界泄漏有害得多；正确的清退需要引头计数/single-
	// flight 复杂度，收益配不上风险。
	appLocks sync.Map // appID → *sync.Mutex

	// 构建面（F0.9/F1.14）：Builder 家族/构建上限/GitHub App 客户端已随
	// intake 卫星迁入（P4 wave-2b，builder.go 注）；受管仓库与 Logging 面
	// 的核消费（drive 前置门/componentrestart providers）留核副本。

	// Proxy 面（F0.15）：受管 Provider reconciler（Route 全量发布真源在
	// repos.Routes——读写同源，ADR-0080 目录化）。
	proxy capability.Proxy

	// routeReady 是 Route 全量发布就绪门（H7）：Start 在启动循环前置 false
	//（基线重放窗），重放返回后置 true 并即时发布一次；重放失败同样置
	// true（否则拉取端点永久 503，失败由既有 drift/受管环自愈）。构造
	// 默认 true——未 Start 的手动驱动形态（测试/apitest 手动）行为不变。
	routeReady atomic.Bool

	// Registry 面（F1.11，ADR-0019 附录 B）：构建推送目标 + from_build
	// 引用组合 + 平台拉取凭证 + 受管 zot 自宿。
	registry capability.Registry

	// 材料面（F0.17/18，ADR-0014）：age 信封（Secret/Config/Volume repo
	// 在 repos——读写同源，ADR-0080 目录化；networks 同）。intake 卫星
	// 持同名副本（P4 wave-2b，通则二 P5 过渡形态）。
	cipher *material.Cipher

	// confirmTickets 是 B1 Confirm Ticket 的消费面（nil = 强制确认引擎
	// 半边缺席——装配缺口由组合根兜底注入，旧测试直构形态按无票豁免；
	// 拦截器半边独立执法 E_CONFIRMATION_REQUIRED，见 confirm.go）。
	confirmTickets governance.ConfirmTicketStore
}

// Loop 暴露收敛循环（apitest 与装配层 Kick 用；只读面）。
func (e *Engine) Loop() *Loop { return e.loop }

// KickTasks 唤醒 Task 收敛环（API 受理面消费：创建/缩放/停止后立即驱动）。
func (e *Engine) KickTasks() { e.taskLoop.Kick() }

// KickSchedules 唤醒 Schedule 到期拍环（API 受理面消费：创建后立即判定）。
func (e *Engine) KickSchedules() { e.scheduleLoop.Kick() }

// DriveOnce 手动驱动一轮收敛（环表序逐步——schedule 先于 task：同一轮
// Drive 内铸出的 Task 即刻进补足链；apitest 手动形态消费，golden 确定性
// 不依赖真实节拍）。
func (e *Engine) DriveOnce(ctx context.Context) {
	for _, r := range e.rings {
		r.step(ctx)
	}
}
