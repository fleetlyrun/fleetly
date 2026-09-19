package build

// 构建队列（T2.8）：CLI 入队（builds 行，跨进程通道）→ daemon 队列 worker
// 扫描认领执行。并发上限经 semaphore 承载（架构 §4.2 容量边界「并发构建
// 2」；配置上限天花板 MaxConcurrency）。排队可见性 = builds 行 status=queued
// （fleetly builds list）；认领（queued→building）是行级谓词原子操作，多
// worker/多 daemon 竞争下恰好一个 claimer 胜出。

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// errCodeBuildFailed 是构建失败终态的注册表错误码（internal/errcode 既有
// 项——中断复位/超时兜底路径复用该码、以审计 reason 文案区分场景，不新增
// 码避免注册表膨胀）。
const errCodeBuildFailed = "E_BUILD_FAILED"

// buildInterruptedReason 是启动复位 building 行的审计 reason 文案（崩溃/
// 关停遗留行的失败归因）。
const buildInterruptedReason = "构建被中断（daemon 重启/关停）"

// Queue 是构建队列调度器（dispatcher 单 goroutine + 信号量限并发执行）。
type Queue struct {
	store        *state.Store
	exec         Executor
	sem          chan struct{}
	wake         chan struct{}
	pollInterval time.Duration
	timeout      time.Duration
	log          *slog.Logger
}

// NewQueue 构建队列。concurrency ≤0 回落缺省 2、pollInterval/timeout ≤0
// 回落各自缺省（Normalize 语义）。
func NewQueue(store *state.Store, exec Executor, concurrency int, pollInterval, timeout time.Duration, log *slog.Logger) *Queue {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency > MaxConcurrency {
		concurrency = MaxConcurrency
	}
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Queue{
		store:        store,
		exec:         exec,
		sem:          make(chan struct{}, concurrency),
		wake:         make(chan struct{}, 1),
		pollInterval: pollInterval,
		timeout:      timeout,
		log:          log,
	}
}

// Concurrency 返回并发上限（诊断用）。
func (q *Queue) Concurrency() int { return cap(q.sem) }

// Enqueue 入队一条构建（builds queued 行 + 唤醒信号加速同进程拾取）。
func (q *Queue) Enqueue(ctx context.Context, rec state.BuildRecord) (state.BuildRecord, error) {
	created, err := q.store.CreateBuild(ctx, rec)
	if err != nil {
		return state.BuildRecord{}, fmtErr("enqueue build: %w", err)
	}
	q.Wake()
	return created, nil
}

// Wake 非阻塞唤醒扫描（通道容量 1，合并连续唤醒）。
func (q *Queue) Wake() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Run 是调度主循环（阻塞到 ctx 取消；lynx.Service.Start 的 actor 形态）。
// 启动先复位中断构建（daemon 重启恢复，见 resetInterrupted），再进入扫描。
func (q *Queue) Run(ctx context.Context) error {
	q.resetInterrupted(ctx)
	ticker := time.NewTicker(q.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-q.wake:
			q.drainOnce(ctx)
		case <-ticker.C:
			q.drainOnce(ctx)
		}
	}
}

// resetInterrupted 启动复位：上一进程崩溃/关停在途的 building 行收敛为
// failed 终态（无人接手的 building 行永不重跑——NextQueuedBuilds 只扫
// queued；等待它的部署在引擎侧空转到发布超时）。queued 行不动：重启后
// 队列自然重扫认领。复位失败只告警不阻塞调度（queued 流量不受影响，遗留
// 行待下次重启再试）。
func (q *Queue) resetInterrupted(ctx context.Context) {
	n, err := q.store.ResetInterruptedBuilds(ctx, errCodeBuildFailed, buildInterruptedReason)
	if err != nil {
		q.log.Error("reset interrupted builds", "error", err)
		return
	}
	if n > 0 {
		q.log.Warn("reset interrupted builds to failed", "count", n, "reason", buildInterruptedReason)
	}
}

// drainOnce 一轮扫描：在信号量有空位时持续认领最早 queued 记录并派发；
// 信号量满或无待处理记录即返回（不阻塞调度循环）。
func (q *Queue) drainOnce(ctx context.Context) {
	for {
		select {
		case q.sem <- struct{}{}:
		default:
			return // 并发已满：排队中记录保持 queued（排队可见）
		}
		recs, err := q.store.NextQueuedBuilds(ctx, 1)
		if err != nil {
			<-q.sem
			if ctx.Err() != nil {
				return
			}
			q.log.Error("scan queued builds", "error", err)
			return
		}
		if len(recs) == 0 {
			<-q.sem
			return
		}
		rec := recs[0]
		if err := q.store.ClaimBuild(ctx, rec.ID); err != nil {
			<-q.sem
			if !errors.Is(err, state.ErrBuildStateTransition) {
				// 非 claim 竞争（库错误等）：停止本轮避免热循环。
				if ctx.Err() != nil {
					return
				}
				q.log.Error("claim build", "build", rec.ID, "error", err)
				return
			}
			continue // 被其他 worker 抢先：看下一条
		}
		claimed, err := q.store.GetBuild(ctx, rec.ID)
		if err != nil {
			<-q.sem
			q.log.Error("read claimed build", "build", rec.ID, "error", err)
			return
		}
		go func(claimed state.BuildRecord) {
			defer func() { <-q.sem }()
			q.Wake() // 槽位释放后立即补位扫描（避免等 tick）
			// per-build 超时预算（config build.timeout_seconds，缺省 30min）：
			// 挂起的执行（网络挂起/buildkitd 半死）到点取消——信号量槽不再被
			// 永久占用（并发 2 时两个挂起即堵死全队列）。执行器契约：ctx
			// 取消即返回（buildkit 客户端随 ctx 终止 solve）。
			execCtx, cancel := context.WithTimeout(ctx, q.timeout)
			defer cancel()
			if _, err := q.exec.Execute(execCtx, claimed); err != nil {
				q.log.Error("execute build", "build", claimed.ID, "error", err)
				q.convergeStranded(execCtx, claimed.ID)
			}
		}(claimed)
	}
}

// convergeStranded 兜底终态：执行器返回错误后行仍停留 building（超时取消
// 后未收敛、异常路径直接返回等）→ 队列侧收敛 failed，防 builds 行永久停留
// building。终态写用 WithoutCancel——execCtx 此时多半已取消（超时/关停），
// 兜底写必达（保留 trace/log 上下文，不继承取消与 deadline）。
func (q *Queue) convergeStranded(execCtx context.Context, buildID string) {
	finCtx := context.WithoutCancel(execCtx)
	row, err := q.store.GetBuild(finCtx, buildID)
	if err != nil {
		q.log.Error("read stranded build", "build", buildID, "error", err)
		return
	}
	if row.Status != state.BuildBuilding {
		return // 执行器已收敛终态（正常失败路径）
	}
	var reason string
	switch {
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		reason = "构建超时：超出 " + q.timeout.String() + " 预算（config build.timeout_seconds）"
	case execCtx.Err() != nil:
		reason = "构建被中断（daemon 关停，执行器未收敛终态）"
	default:
		reason = "构建执行器异常退出（未收敛终态）"
	}
	if err := q.store.FailStrandedBuild(finCtx, buildID, errCodeBuildFailed, reason); err != nil {
		q.log.Error("converge stranded build", "build", buildID, "error", err)
		return
	}
	q.log.Warn("converged stranded build to failed", "build", buildID, "reason", reason)
}
