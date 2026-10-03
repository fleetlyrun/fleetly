package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	configrepo "github.com/fleetlyrun/fleetly/internal/state/config"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/state/node"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
	"github.com/fleetlyrun/fleetly/internal/state/route"
	"github.com/fleetlyrun/fleetly/internal/state/run"
	"github.com/fleetlyrun/fleetly/internal/state/schedule"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/state/task"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
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
	// ErrFirstBootNetworkUnknown 是 firstBootJobs 受理命中项目内不存在的
	// 裸网名（B12 P3-5 fail-closed：建网/taskGroup:/project: 引用/删附件；
	// API 层映射 E_INVALID_ARGUMENT）。
	ErrFirstBootNetworkUnknown = errors.New("engine: first boot job attaches an unknown network")
)

// Options 是引擎参数（装配注入；测试覆盖默认值）。
type Options struct {
	// QueueCapacity 是每 App 排队上限（默认 8；满即 ErrQueueFull）。
	QueueCapacity int
	// ObserveWindow 是 L3 观察窗（默认 60s，F0.12 验收口径）。
	ObserveWindow time.Duration
	// ReleaseTimeout 是 L1 就绪等待上限（默认 120s；超时即失败回滚）。
	ReleaseTimeout time.Duration
	// FirstBootWaitGrace 是 firstBootJobs 等待窗在作业 ttl 之上的余量
	//（默认 2m：mint→Run 创建滞后 + 终态观测滞后；镜像拉取计入 Run 自身
	// TTL——deadline 从 Run 创建起算，ADR-0030 决策 4）。
	FirstBootWaitGrace time.Duration
	// Tick 是收敛循环兜底节拍（默认 1s）。
	Tick time.Duration
	// BuildConcurrency 是并发构建上限（默认 2，F0.9）。
	BuildConcurrency int
	// BuildTimeout 是单次构建硬超时（默认 15m；超时=expired 看门狗）。
	BuildTimeout time.Duration
	// ManagedStepTimeout 是受管收敛步硬上限（默认 30s）。staging 实证
	//（2026-09-30）：docker daemon 重启/swarm 重建窗口内的 API hang 会把
	// 无界的单写者循环永久卡死（静默直至进程重启）——受管步一律带界。
	ManagedStepTimeout time.Duration
	// DriftScanInterval 是漂移扫描节拍（默认 30s；ADR-0022 spec 对照 +
	// 稳态看门狗共用此环）。
	DriftScanInterval time.Duration
	// TaskLeaseInterval 是 Owner Lease 续期步长（默认 30s：deadline =
	// 续期时刻 + 步长；宽限叠加在其后，ADR-0012/F1.6）。
	TaskLeaseInterval time.Duration
	// TaskLeaseGrace 是租约过期宽限（默认 90s：超宽限未续期即排空回收）。
	TaskLeaseGrace time.Duration
	// TaskStopGrace 是 Run 停止宽限（默认 30s；SIGTERM 后强制收口前的
	// 等待窗，映射 Workload.StopGrace）。
	TaskStopGrace time.Duration
	// TaskReconcileInterval 是 Task 域强制重放节拍（默认 60s；期望集签名
	// 未变也周期收敛——自愈面，控制 per-tick swarm API 压力）。
	TaskReconcileInterval time.Duration
	// TaskOwnerRevokedRunToTTL 是属主吊销排空模式（ADR-0017 默认宽限排空
	// = false；true = 跑完 TTL：只停补足不停止存量 Run）。
	TaskOwnerRevokedRunToTTL bool
	// ScheduleOverlap 是 Schedule 重叠策略（ADR-0018 A.3 修订 / ADR-0017
	// 附录 A.4）：skip（默认——上一拍 Run 未终态时跳过本拍）或 fire（照常
	// 拍，允许并行拍）。值域由 ParseScheduleOverlap 把守（装配解析配置后
	// 注入；空值回退 skip）。
	ScheduleOverlap string
	// TaskReplenishBurst 是单拍补足创建上限（默认 8；防一拍海量创建打爆
	// swarm API——决策 7 压测锚的稳态面）。
	TaskReplenishBurst int
	// ReconcileReplayInterval 是收敛域签名短路的统一强制重放节拍（默认
	// 60s；Task 域 TaskReconcileInterval 先例的全域推广）：受管/Database
	// Ensure、releasing 等待期物化、Route 发布在签名未变时跳过重活，但
	// 自愈窗口不得无限期关闭——环外变更（人工改载体、载体漂移、Secret
	// 重封装等签名外输入）由本节拍的强制重放兜底收敛，滞后有界。
	ReconcileReplayInterval time.Duration
	// DataRoot 是平台数据根（构建上下文与 git 检出落盘）。
	DataRoot string
}

func (o *Options) fill() {
	if o.QueueCapacity <= 0 {
		o.QueueCapacity = 8
	}
	if o.ObserveWindow <= 0 {
		o.ObserveWindow = 60 * time.Second
	}
	if o.ReleaseTimeout <= 0 {
		o.ReleaseTimeout = 120 * time.Second
	}
	if o.FirstBootWaitGrace <= 0 {
		o.FirstBootWaitGrace = 2 * time.Minute
	}
	if o.Tick <= 0 {
		o.Tick = time.Second
	}
	if o.BuildConcurrency <= 0 {
		o.BuildConcurrency = 2
	}
	if o.BuildTimeout <= 0 {
		o.BuildTimeout = 15 * time.Minute
	}
	if o.ManagedStepTimeout <= 0 {
		o.ManagedStepTimeout = 30 * time.Second
	}
	if o.DriftScanInterval <= 0 {
		o.DriftScanInterval = 30 * time.Second
	}
	if o.TaskLeaseInterval <= 0 {
		o.TaskLeaseInterval = 30 * time.Second
	}
	if o.TaskLeaseGrace <= 0 {
		o.TaskLeaseGrace = 90 * time.Second
	}
	if o.TaskStopGrace <= 0 {
		o.TaskStopGrace = 30 * time.Second
	}
	if o.TaskReconcileInterval <= 0 {
		o.TaskReconcileInterval = 60 * time.Second
	}
	if o.TaskReplenishBurst <= 0 {
		o.TaskReplenishBurst = 8
	}
	if o.ReconcileReplayInterval <= 0 {
		o.ReconcileReplayInterval = 60 * time.Second
	}
	if o.ScheduleOverlap == "" {
		o.ScheduleOverlap = ScheduleOverlapSkip
	}
}

// Schedule 重叠策略值（ADR-0017 附录 A.4 修订 ADR-0018 A.3）。
const (
	// ScheduleOverlapSkip：上一拍铸出的 Task 仍有未终态 Run 时跳过本拍
	//（schedule.skipped reason=overlap）——最少惊异默认。
	ScheduleOverlapSkip = "skip"
	// ScheduleOverlapFire：重叠时照常拍，允许并行拍（torchwood 类并行
	// 派发的实证形态）。
	ScheduleOverlapFire = "fire"
)

// ParseScheduleOverlap 归一配置值：空值/合法值直通，非法值报错
// （fail-fast——配置错误不得静默回退成 skip）。
func ParseScheduleOverlap(s string) (string, error) {
	switch s {
	case "", ScheduleOverlapSkip:
		return ScheduleOverlapSkip, nil
	case ScheduleOverlapFire:
		return ScheduleOverlapFire, nil
	default:
		return "", fmt.Errorf("engine: schedule_overlap_policy must be %q or %q, got %q",
			ScheduleOverlapSkip, ScheduleOverlapFire, s)
	}
}

// Engine 是部署收敛引擎：Deployment 状态机单写者 + admission + Runtime
// Watch 消费。写路径全部四件一拍（CAS + Outbox + 审计；部署记录无
// tombstone）；决策读平台权威表，观测缓存仅作信号（架构 §6）。
type Engine struct {
	db      *state.DB
	log     *slog.Logger
	runtime capability.Runtime
	clock   state.Clock
	opts    Options

	projects    *project.Repo
	apps        *app.Repo
	revisions   *revision.Repo
	deployments *deployment.Repo
	builds      *build.Repo
	outbox      *outbox.Repo
	nodes       *node.Repo
	audits      *audit.Repo

	// Task 域（F1.5/F1.6）：聚合 repo + 单写者环 + 属主 Token 行引用面
	//（吊销排空拉式扫描，P1-8）。
	tasks  *task.Repo
	runs   *run.Repo
	tokens *tokenrepo.Repo

	// Schedule 域（F1.7）：聚合 repo + 到期拍环（fire 铸 one-shot Task 后
	// 交 taskLoop 驱动——Schedule 只拥有"何时拍"）。
	schedules *schedule.Repo

	// 跨 Project peer 声明（F1.8，ADR-0013 附录 A）：批准态真源——
	// 投影翻译（strict/isolate）与撤销隔离的解析面。
	peerDecls *networkpeer.Repo

	// 上传产物行（F1.10，ADR-0019 附录 A）：构建输入解析面（upload id →
	// digest → blob 解包）。
	uploads *sourceupload.Repo

	// Database 域（F1.12，ADR-0029）：聚合 repo + 用户域受管收敛环
	//（第三条部署轨——挂项目网供 App 连，与 App 部署链/受管域分立）。
	databases    *dbrepo.Repo
	databaseLoop *Loop

	loop         *Loop
	buildLoop    *Loop
	managedLoop  *Loop
	taskLoop     *Loop // Task/Run 收敛环（janitor/补足/Ensure/收口）
	scheduleLoop *Loop // Schedule 到期拍环（F1.7）
	driftLoop    *Loop // ADR-0022 漂移扫描环（spec 对照 + 稳态看门狗）
	cancel       context.CancelFunc
	wg           sync.WaitGroup

	// lifecycleMu 同步引擎生命周期（B12 P3-6）：Start 全程持锁（cancel
	// 判空/赋值 + wg.Add）与 Stop 全程持锁（cancel 置 nil + wg.Wait 排水）
	// 互斥——裸读写 cancel 是数据竞争，Add/Wait 重叠是 WaitGroup 竞争，
	// 并发双 Start 会双发全套收敛循环 goroutine。
	lifecycleMu sync.Mutex

	// runCtx 是 Start 物化的进程生命期 ctx（Q-7）：构建 goroutine 的派生
	// 根——Stop 取消它即有界排水（executeBuild 的优雅退出路径接管）。
	runCtxMu sync.Mutex
	runCtx   context.Context

	// managedGen 是受管域 Generation 实例态（C5：指纹/gen 不再是包级
	// 全局——多 Engine 实例互不污染）。
	managedGen managedGenState

	// nodeLeftSeen 是 node.left 去抖实例态（C5：节点回归即清签名）。
	nodeLeftSeen map[string]bool

	// appLocks 是 App 级互斥（admission/基线重放/收口共享，N0.1 P1-3）：
	// 重放持有锁期间 Submit 排队——admission 落行与重放的 ActiveByApp
	// 复查被串行化，不存在"复查后落行、重放再 Ensure 旧 Generation 与
	// 驱动器对翻载体标签"的窗口（健康部署被 L1 误判回滚的根因）。
	appLocks sync.Map // appID → *sync.Mutex

	// taskLocks 是 Task 级互斥（驱动环与 DeleteTask 载体收口共享——
	// appLocks 同款形态；深审裁决表 #16 的同构互斥）。
	taskLocks sync.Map // taskID → *sync.Mutex

	// Task 域观测缓存（P1-7 缓存分家：独立缓存组，与部署形状五 map 隔离；
	// per-Run 终态回收随 handleRunObservation——恢复真源是 Task/Run 行，
	// 不抄 rebuildBaselines）。
	taskObsMu   sync.RWMutex
	workloadRun map[string]string                   // workloadID(=runID) → taskID（观测路由/归属）
	runObs      map[string]capability.WorkloadEvent // runID → 最新观测

	// Task 域 Ensure 签名（幂等收敛跳过 + 周期强制重放；失败清空下拍重试）。
	taskEnsuredMu  sync.Mutex
	taskEnsured    map[string]string    // taskID → 期望集签名
	taskLastEnsure map[string]time.Time // taskID → 最近 Ensure 时刻

	// 收敛域签名短路备忘（N1 C16/C17：每拍全量无条件重活的跳过判定面）。
	// 记忆"上次成功 Ensure 的完整签名 + 时刻"：签名未变且未到强制重放节拍
	// 即跳过本拍；Ensure 失败不落/清签名（下拍重试）；重启丢失首拍全量自愈。
	// ensureMemo 的域内键与签名字面由各域定义（受管=namespace，Database=行
	// ID，releasing=Deployment 行锚，Route 发布单槽）。
	ensureMu      sync.Mutex
	managedEnsure map[string]ensureMemo // namespace → 受管域上次成功 Ensure
	dbEnsure      map[string]ensureMemo // databaseID → Database 域上次成功 Ensure
	releaseEnsure map[string]ensureMemo // deploymentID → releasing 物化上次成功
	routesPubSig  string                // Route 发布：上次全量发布的行集指纹
	routesPubAt   time.Time             // Route 发布：上次全量发布时刻（强制重放节拍锚）
	routesPubNow  atomic.Bool           // PublishRoutesNow 即时发布信号（消费即清）

	// 构建面（F0.9/F1.14）：Builder 家族（名→Provider，spec 路由）、并发
	// 上限、构建输入登记与最近日志缓冲（ADR-0032）。
	builders  map[string]capability.Builder
	buildOpts buildOptions
	buildLogs *logBuffer

	// Edge 面（F0.15）：Route 全量发布 + 受管 Provider reconciler。
	edge   capability.Edge
	routes *route.Repo

	// Registry 面（F1.11，ADR-0019 附录 B）：构建推送目标 + from_build
	// 引用组合 + 平台拉取凭证 + 受管 zot 自宿。
	registry capability.Registry

	// 材料面（F0.17/18，ADR-0014）：Secret/Config/Volume repo 与 age 信封。
	cipher   *material.Cipher
	secrets  *secret.Repo
	configs  *configrepo.Repo
	volumes  *volume.Repo
	networks *networkrepo.Repo // 受管 Edge 挂网真源（活跃 Project 网络全量）

	buildInputMu sync.Mutex
	buildInputs  map[string]capability.BuildRequest // buildID → 登记输入（重启丢失即回 queued 重放）

	obsMu        sync.RWMutex
	observations map[string]capability.WorkloadEvent // workloadID → 最新观测
	workloadApp  map[string]string                   // workloadID → appID（归属缓存，Ensure 时刷新）
	ensuredGen   map[string]uint64                   // workloadID → 最近 Ensure 的 Generation（就绪门集合界定）
	ensuredSpec  map[string]capability.Workload      // workloadID → 最近 Ensure 的投影 spec（ADR-0022 spec 对照 drift）

	expectMu sync.Mutex
	expected map[string]uint64 // appID → 最近 Ensure 的 Generation（Drift 对照锚）

	driftMu    sync.Mutex
	drift      map[string]string // workloadID → 最后 drift 签名（去抖）
	stoppedMu  sync.Mutex
	stoppedSig map[string]string // workloadID → 稳态 stopped 签名（去抖，ADR-0022）
}

// Deps 是引擎依赖（装配注入；可选依赖为 nil 时对应能力停用并给出精确
// 反馈，不静默）。
type Deps struct {
	DB       *state.DB
	Runtime  capability.Runtime
	Builders map[string]capability.Builder // 可空/空 map：构建链停用（镜像直投不受影响；ADR-0032 spec 路由家族）
	Edge     capability.Edge               // 可空：Route 发布与受管自宿停用
	Registry capability.Registry           // 可空：build 源部署精确失败（附录 B.5①）
	Cipher   *material.Cipher              // 可空：Secret 面停用（引用 Secret 的部署得精确错误）
	Logger   *slog.Logger
}

// New 构造引擎（不启动；Start 后进入驱动）。
func New(deps Deps, opts Options) *Engine {
	opts.fill()
	db := deps.DB
	log := deps.Logger
	clock := db.Clock()
	return &Engine{
		builders:       deps.Builders,
		edge:           deps.Edge,
		registry:       deps.Registry,
		cipher:         deps.Cipher,
		runtime:        deps.Runtime,
		db:             db,
		log:            log,
		clock:          clock,
		opts:           opts,
		projects:       project.New(clock),
		apps:           app.New(clock),
		revisions:      revision.New(clock),
		deployments:    deployment.New(clock),
		builds:         build.New(clock),
		outbox:         outbox.New(clock),
		nodes:          node.New(clock),
		audits:         audit.New(clock),
		tasks:          task.New(clock),
		runs:           run.New(clock),
		tokens:         tokenrepo.New(clock),
		schedules:      schedule.New(clock),
		peerDecls:      networkpeer.New(clock),
		uploads:        sourceupload.New(clock),
		databases:      dbrepo.New(clock),
		loop:           NewLoop("deployment", log),
		buildLoop:      NewLoop("build", log),
		managedLoop:    NewLoop("managed", log),
		databaseLoop:   NewLoop("database", log),
		taskLoop:       NewLoop("task", log),
		scheduleLoop:   NewLoop("schedule", log),
		driftLoop:      NewLoop("drift", log),
		nodeLeftSeen:   map[string]bool{},
		routes:         route.New(clock),
		secrets:        secret.New(clock),
		configs:        configrepo.New(clock),
		volumes:        volume.New(clock),
		networks:       networkrepo.New(clock),
		buildOpts:      buildOptions{Concurrency: opts.BuildConcurrency, Timeout: opts.BuildTimeout},
		buildLogs:      newLogBuffer(500),
		buildInputs:    make(map[string]capability.BuildRequest),
		observations:   make(map[string]capability.WorkloadEvent),
		workloadApp:    make(map[string]string),
		ensuredGen:     make(map[string]uint64),
		ensuredSpec:    make(map[string]capability.Workload),
		expected:       make(map[string]uint64),
		drift:          make(map[string]string),
		stoppedSig:     make(map[string]string),
		workloadRun:    make(map[string]string),
		runObs:         make(map[string]capability.WorkloadEvent),
		taskEnsured:    make(map[string]string),
		taskLastEnsure: make(map[string]time.Time),
		managedEnsure:  make(map[string]ensureMemo),
		dbEnsure:       make(map[string]ensureMemo),
		releaseEnsure:  make(map[string]ensureMemo),
	}
}

// lockTask 取 Task 级互斥（惰性建；驱动环/DeleteTask 共享）。
func (e *Engine) lockTask(taskID string) *sync.Mutex {
	mu, _ := e.taskLocks.LoadOrStore(taskID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// boundedStep 给受管收敛步派生 ManagedStepTimeout 硬上限 ctx（统一入口：
// managed.go/database.go/drift.go 既有 stepCtx 形态的共用底座）。engine.go
// Options.ManagedStepTimeout 注释的实证背景——docker API hang 卡死单写者
// 环，TTL janitor/租约排空/停止兜底全住环上，卡死即全部时间看门狗失明。
// 收敛步本就是失败下拍重试语义，带界无损。
func (e *Engine) boundedStep(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
}

// ensureMemo 是收敛域"上次成功 Ensure"的备忘（N1 C16/C17）：sig 是该域
// Ensure 全部输入的签名字面（域内定义），at 驱动周期强制重放（签名未变也
// 周期收敛——自愈面，Task 域 TaskReconcileInterval 先例同款节律），gen 备
// 短路拍需要下发编号的消费面（Database 状态推进）。
type ensureMemo struct {
	sig string
	gen uint64
	at  time.Time
}

// ensureFresh 报告键上备忘是否存在且（签名未变 + 未到强制重放节拍）——
// 双条件同时成立才允许跳过本拍重活。
func (e *Engine) ensureFresh(memo map[string]ensureMemo, key, sig string, now time.Time) (ensureMemo, bool) {
	e.ensureMu.Lock()
	rec, ok := memo[key]
	e.ensureMu.Unlock()
	if !ok || rec.sig != sig || now.After(rec.at.Add(e.opts.ReconcileReplayInterval)) {
		return ensureMemo{}, false
	}
	return rec, true
}

// ensureRemember 落/刷键上备忘（仅成功路径调用）。
func (e *Engine) ensureRemember(memo map[string]ensureMemo, key string, rec ensureMemo) {
	e.ensureMu.Lock()
	memo[key] = rec
	e.ensureMu.Unlock()
}

// ensureForget 清键上备忘（Ensure 失败/域收口；缺失为 no-op）。
func (e *Engine) ensureForget(memo map[string]ensureMemo, key string) {
	e.ensureMu.Lock()
	delete(memo, key)
	e.ensureMu.Unlock()
}

// lockApp 取 App 级互斥（惰性建；admission/基线重放/收口共享）。
func (e *Engine) lockApp(appID string) *sync.Mutex {
	mu, _ := e.appLocks.LoadOrStore(appID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// Start 进入驱动：收敛循环 + Watch 消费。重复 Start 幂等（第二次为
// no-op）。全程持 lifecycleMu（B12 P3-6）：判空/赋 cancel 与 wg.Add 对
// 并发 Stop 的 wg.Wait 互斥——裸读写是数据竞争，且并发双 Start 会双发
// 全套收敛循环 goroutine。Stop 后重启（cancel 已置 nil）沿旧语义放行。
func (e *Engine) Start(ctx context.Context) {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	if e.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.cancel = cancel
	e.runCtxMu.Lock()
	e.runCtx = runCtx // 构建goroutine 派生根（Q-7：Stop 取消 → 有界排水）
	e.runCtxMu.Unlock()
	e.resetOrphanBuilds(runCtx)
	e.wg.Add(6)
	go func() {
		defer e.wg.Done()
		e.loop.Run(runCtx, e.opts.Tick, e.step)
	}()
	go func() {
		defer e.wg.Done()
		e.buildLoop.Run(runCtx, e.opts.Tick, e.buildStep)
	}()
	go func() {
		defer e.wg.Done()
		e.managedLoop.Run(runCtx, e.opts.Tick, e.managedStep)
	}()
	go func() {
		defer e.wg.Done()
		e.databaseLoop.Run(runCtx, e.opts.Tick, e.databaseStep)
	}()
	go func() {
		defer e.wg.Done()
		e.taskLoop.Run(runCtx, e.opts.Tick, e.taskStep)
	}()
	go func() {
		defer e.wg.Done()
		e.scheduleLoop.Run(runCtx, e.opts.Tick, e.scheduleStep)
	}()
	e.StartWatch(runCtx)
	e.loop.Kick() // 启动即收敛：进程重启后按 Generation 幂等重放（场景 1）
	e.buildLoop.Kick()
	e.managedLoop.Kick()
	e.databaseLoop.Kick() // 启动即收敛：数据库行 + 行上指纹是重放真源
	e.taskLoop.Kick()     // 启动即收敛：Task/Run 行 + 绝对 deadline 是恢复真源（P1-7）
	e.scheduleLoop.Kick() // 启动即收敛：next_fire_at 是绝对时刻——错过窗口补跑一拍（ADR-0018 附录 A）
	// ADR-0022：启动基线重放（异步；重建归属/期望缓存）+ 漂移扫描环
	//（Loop.Run 阻塞至 ctx 取消——与其他环同款 goroutine 形态）。重放
	// goroutine 计入 wg（N0.1 P2-11）：Stop 排水覆盖重放，不再裸奔。
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.rebuildBaselines(runCtx)
	}()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.driftScanLoop(runCtx)
	}()
}

// StartWatch 只启动 Watch 消费（手动驱动形态配套：收敛循环不启动，观测
// 缓存仍需进——apitest golden 的确定性路径）。
func (e *Engine) StartWatch(ctx context.Context) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.consumeWatch(ctx)
	}()
}

// buildRootCtx 返回构建 goroutine 的派生根：Start 后是进程生命期 runCtx
// （Stop 取消 → executeBuild 优雅退出回 queued，排水有界）；未 Start 的
// 手动驱动形态（测试 DriveOnce）无 Stop 排水语义，退化为 Background 根
// （构建 goroutine 生命周期 = Build 行写者，独立于驱动调用的 ctx）。
func (e *Engine) buildRootCtx() context.Context {
	e.runCtxMu.Lock()
	defer e.runCtxMu.Unlock()
	if e.runCtx != nil {
		return e.runCtx
	}
	return context.Background()
}

// resetOrphanBuilds 把崩溃遗留的 building 行重置 queued（重放：buildkit
// 缓存幂等）。输入登记不在此重建——driveBuilding 见到在途行时幂等重建
// （D-4）；归属部署已终态的孤儿行由 buildStep 前置检一跳到终态。
func (e *Engine) resetOrphanBuilds(ctx context.Context) {
	if len(e.builders) == 0 {
		return
	}
	active, err := e.builds.ListActive(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("build: reset orphans", "err", err)
		return
	}
	for _, b := range active {
		if b.State != build.StateBuilding {
			continue
		}
		if _, err := e.transitBuild(ctx, &b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); err != nil {
			e.log.Error("build: reset orphan", "build", b.ID, "err", err)
		}
	}
}

// Stop 有界排空：取消循环并等待在途 step（Ensure 由 step 内 ctx 收口）
// 返回。in-flight Ensure 可安全中断重放（ADR-0005 优雅退出）。全程持
// lifecycleMu（B12 P3-6）：排水等待与并发 Start 的 wg.Add 互斥，双 Stop
// 串行在锁上（后者见 cancel=nil 幂等返回 nil）；Start 未完整（cancel 尚
// 未落）同样安全 no-op。
func (e *Engine) Stop(ctx context.Context) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	if e.cancel == nil {
		return nil
	}
	e.cancel()
	e.cancel = nil
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("engine drain exceeded deadline: %w", ctx.Err())
	}
}

// Loop 暴露收敛循环（apitest 与装配层 Kick 用；只读面）。
func (e *Engine) Loop() *Loop { return e.loop }

// KickTasks 唤醒 Task 收敛环（API 受理面消费：创建/缩放/停止后立即驱动）。
func (e *Engine) KickTasks() { e.taskLoop.Kick() }

// KickSchedules 唤醒 Schedule 到期拍环（API 受理面消费：创建后立即判定）。
func (e *Engine) KickSchedules() { e.scheduleLoop.Kick() }

// DriveOnce 手动驱动一轮收敛（部署/构建/受管/Database/Schedule/Task 六线
// 各一步；apitest 手动形态消费——golden 确定性：不依赖真实节拍）。scheduleStep 先于
// taskStep：同一轮 Drive 内铸出的 Task 即刻进补足链。
func (e *Engine) DriveOnce(ctx context.Context) {
	e.step(ctx)
	e.buildStep(ctx)
	e.managedStep(ctx)
	e.databaseStep(ctx)
	e.scheduleStep(ctx)
	e.taskStep(ctx)
}
