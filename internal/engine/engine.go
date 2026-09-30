package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	configrepo "github.com/fleetlyrun/fleetly/internal/state/config"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/node"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
	"github.com/fleetlyrun/fleetly/internal/state/route"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
)

// 哨兵错误（API 层映射 errcode；engine 自身不依赖 apperr）。
var (
	// ErrQueueFull 是 admission 排队容量已满（ADR-0016：queue 满显式反馈，
	// 不是 409——请求没错，是背压）。
	ErrQueueFull = errors.New("engine: deployment queue is full")
	// ErrNotCancellable 是目标 Deployment 已终态（取消只作用于排队/在途）。
	ErrNotCancellable = errors.New("engine: deployment already finished")
)

// Options 是引擎参数（装配注入；测试覆盖默认值）。
type Options struct {
	// QueueCapacity 是每 App 排队上限（默认 8；满即 ErrQueueFull）。
	QueueCapacity int
	// ObserveWindow 是 L3 观察窗（默认 60s，F0.12 验收口径）。
	ObserveWindow time.Duration
	// ReleaseTimeout 是 L1 就绪等待上限（默认 120s；超时即失败回滚）。
	ReleaseTimeout time.Duration
	// Tick 是收敛循环兜底节拍（默认 1s）。
	Tick time.Duration
	// BuildConcurrency 是并发构建上限（默认 2，F0.9）。
	BuildConcurrency int
	// BuildTimeout 是单次构建硬超时（默认 15m；超时=expired 看门狗）。
	BuildTimeout time.Duration
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
	if o.Tick <= 0 {
		o.Tick = time.Second
	}
	if o.BuildConcurrency <= 0 {
		o.BuildConcurrency = 2
	}
	if o.BuildTimeout <= 0 {
		o.BuildTimeout = 15 * time.Minute
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

	loop        *Loop
	buildLoop   *Loop
	managedLoop *Loop
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	// 构建面（F0.9）：Builder 端口、并发上限、构建输入登记与最近日志缓冲。
	builder   capability.Builder
	buildOpts buildOptions
	buildLogs *logBuffer

	// Edge 面（F0.15）：Route 全量发布 + 受管 Provider reconciler。
	edge   capability.Edge
	routes *route.Repo

	// 材料面（F0.17/18，ADR-0014）：Secret/Config/Volume repo 与 age 信封。
	cipher  *material.Cipher
	secrets *secret.Repo
	configs *configrepo.Repo
	volumes *volume.Repo

	buildInputMu sync.Mutex
	buildInputs  map[string]capability.BuildRequest // buildID → 登记输入（重启丢失即回 queued 重放）

	obsMu        sync.RWMutex
	observations map[string]capability.WorkloadEvent // workloadID → 最新观测
	workloadApp  map[string]string                   // workloadID → appID（归属缓存，Ensure 时刷新）
	ensuredGen   map[string]uint64                   // workloadID → 最近 Ensure 的 Generation（就绪门集合界定）

	expectMu sync.Mutex
	expected map[string]uint64 // appID → 最近 Ensure 的 Generation（Drift 对照锚）

	driftMu sync.Mutex
	drift   map[string]string // workloadID → 最后 drift 签名（去抖）
}

// Deps 是引擎依赖（装配注入；可选依赖为 nil 时对应能力停用并给出精确
// 反馈，不静默）。
type Deps struct {
	DB      *state.DB
	Runtime capability.Runtime
	Builder capability.Builder // 可空：构建链停用（镜像直投不受影响）
	Edge    capability.Edge    // 可空：Route 发布与受管自宿停用
	Cipher  *material.Cipher   // 可空：Secret 面停用（引用 Secret 的部署得精确错误）
	Logger  *slog.Logger
}

// New 构造引擎（不启动；Start 后进入驱动）。
func New(deps Deps, opts Options) *Engine {
	opts.fill()
	db := deps.DB
	log := deps.Logger
	clock := db.Clock()
	return &Engine{
		builder:      deps.Builder,
		edge:         deps.Edge,
		cipher:       deps.Cipher,
		runtime:      deps.Runtime,
		db:           db,
		log:          log,
		clock:        clock,
		opts:         opts,
		projects:     project.New(clock),
		apps:         app.New(clock),
		revisions:    revision.New(clock),
		deployments:  deployment.New(clock),
		builds:       build.New(clock),
		outbox:       outbox.New(clock),
		nodes:        node.New(clock),
		audits:       audit.New(clock),
		loop:         NewLoop("deployment", log),
		buildLoop:    NewLoop("build", log),
		managedLoop:  NewLoop("managed", log),
		routes:       route.New(clock),
		secrets:      secret.New(clock),
		configs:      configrepo.New(clock),
		volumes:      volume.New(clock),
		buildOpts:    buildOptions{Concurrency: opts.BuildConcurrency, Timeout: opts.BuildTimeout},
		buildLogs:    newLogBuffer(500),
		buildInputs:  make(map[string]capability.BuildRequest),
		observations: make(map[string]capability.WorkloadEvent),
		workloadApp:  make(map[string]string),
		ensuredGen:   make(map[string]uint64),
		expected:     make(map[string]uint64),
		drift:        make(map[string]string),
	}
}

// Start 进入驱动：收敛循环 + Watch 消费。重复 Start 幂等（第二次为 no-op）。
func (e *Engine) Start(ctx context.Context) {
	if e.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.cancel = cancel
	e.resetOrphanBuilds(runCtx)
	e.wg.Add(4)
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
		e.consumeWatch(runCtx)
	}()
	e.loop.Kick() // 启动即收敛：进程重启后按 Generation 幂等重放（场景 1）
	e.buildLoop.Kick()
	e.managedLoop.Kick()
}

// resetOrphanBuilds 把崩溃遗留的 building 行重置 queued（重放：buildkit
// 缓存幂等；无输入登记的孤儿在 executeBuild 内再回 queued 等登记）。
func (e *Engine) resetOrphanBuilds(ctx context.Context) {
	if e.builder == nil {
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
// 返回。in-flight Ensure 可安全中断重放（ADR-0005 优雅退出）。
func (e *Engine) Stop(ctx context.Context) error {
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
