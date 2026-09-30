package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/node"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
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

	loop   *Loop
	cancel context.CancelFunc
	wg     sync.WaitGroup

	obsMu        sync.RWMutex
	observations map[string]capability.WorkloadEvent // workloadID → 最新观测
	workloadApp  map[string]string                   // workloadID → appID（归属缓存，Ensure 时刷新）
	ensuredGen   map[string]uint64                   // workloadID → 最近 Ensure 的 Generation（就绪门集合界定）

	expectMu sync.Mutex
	expected map[string]uint64 // appID → 最近 Ensure 的 Generation（Drift 对照锚）

	driftMu sync.Mutex
	drift   map[string]string // workloadID → 最后 drift 签名（去抖）
}

// New 构造引擎（不启动；Start 后进入驱动）。
func New(db *state.DB, rt capability.Runtime, log *slog.Logger, opts Options) *Engine {
	opts.fill()
	clock := db.Clock()
	return &Engine{
		db:           db,
		log:          log,
		runtime:      rt,
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
	e.wg.Add(2)
	go func() {
		defer e.wg.Done()
		e.loop.Run(runCtx, e.opts.Tick, e.step)
	}()
	go func() {
		defer e.wg.Done()
		e.consumeWatch(runCtx)
	}()
	e.loop.Kick() // 启动即收敛：进程重启后按 Generation 幂等重放（场景 1）
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
