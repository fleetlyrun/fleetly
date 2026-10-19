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

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"

	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/github"
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

	// Backup 域（F2.2，ADR-0039）：台账 repo + 备份环 + 执行链装配面
	//（ObjectStore 端口 = 产物承载；Utility 子面 = 工具容器执行，装配期
	// 经 FacesOf 从 Runtime 探测）。
	objectStore capability.ObjectStore
	utility     capability.RuntimeUtility
	// execFace 是 exec 子面 typed 持有（F3.2，ADR-0049；nil = 受理诚实
	// 失败 E_EXEC_UNSUPPORTED——无降级路径）。
	execFace capability.RuntimeExec
	// exec 是会话域实例态（受理/路由/生命周期；进程内活体，零持久化）。
	exec execs

	// Browse 会话域实例态（F3.6，ADR-0051）：会话注册表进程内活体。
	browse browses

	// Logging 域（F2.4，ADR-0040）：受管日志存储 Provider + 采集环。
	logging capability.Logging
	logpipe logpipes

	metrics    capability.Metrics
	metricsDom metrics
	alertRules alertrule.Repo
	channels   channel.Repo

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
	cancel       context.CancelFunc
	wg           sync.WaitGroup

	// 域状态（行为住各域文件，状态随域绑定——加环/加域不再横穿 Engine
	// 结构体；域类型定义随域文件，P3 收官批 8 壳收口归位。锁语义零漂移：
	// ensure 备忘族仍共用 ensureMu 单锁（五张 memo map 分住各域子结构）；
	// 观测/漂移/期望/收敛各域锁照旧）。
	task      tasks
	obs       observations
	drift     drifts
	expect    expects
	converge  converges // ADR-0082 缺省收敛域（双通道 memo/签名生命周期/冷却记账/缺席投影缓存）
	managed   manageds
	database  databases
	delivery  deploys
	proxyMemo proxies
	build     builds

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
	// （排队语义，见 lockMaintenance）。
	maintenanceMu sync.RWMutex

	// platformBackupMu 是 Platform Backup 的引擎级单飞锁（H6）：快照
	// （VACUUM INTO）与仓外步骤不在 restic 仓锁内——并发触发可产出损坏
	// 但 restic check 通过的恢复点，也会让手动触发误报失败。runPlatformBackup
	// 入口 TryLock，拿不到即 ErrPlatformBackupInProgress（节拍环静默跳过、
	// 手动面映射 E_CONFLICT）；不阻塞排队，触发者立即得到诚实反馈。
	platformBackupMu sync.Mutex

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

	// 构建面（F0.9/F1.14）：Builder 家族（名→Provider，spec 路由）。
	builders  map[string]capability.Builder
	buildOpts buildLimits

	// Proxy 面（F0.15）：受管 Provider reconciler（Route 全量发布真源在
	// repos.Routes——读写同源，ADR-0080 目录化）。
	proxy capability.Proxy

	// Registry 面（F1.11，ADR-0019 附录 B）：构建推送目标 + from_build
	// 引用组合 + 平台拉取凭证 + 受管 zot 自宿。
	registry capability.Registry

	// 材料面（F0.17/18，ADR-0014）：age 信封（Secret/Config/Volume repo
	// 在 repos——读写同源，ADR-0080 目录化；networks 同）。
	cipher *material.Cipher

	// GitHub App 接入材料（ADR-0073 阶段 B）：聚合 repo 在 repos.GitHubApps
	//（ADR-0080 目录化）+ 最小客户端——clone 前按 host+属主解析并现铸
	// installation token（gitmaterial.go）。
	ghClient *github.Client

	// rotateMu 串行化 Database 凭证轮换（IA v3 二期⑤b）：并发轮换的
	// utility/Secret 写交错可能落下 Secret 与数据面不一致的终态——全域
	// 单飞是罕见运维动作的最小诚实防线（rotate.go 注）。
	rotateMu sync.Mutex
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
