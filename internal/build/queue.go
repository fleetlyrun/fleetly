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

// Queue 是构建队列调度器（dispatcher 单 goroutine + 信号量限并发执行）。
type Queue struct {
	store        *state.Store
	exec         Executor
	sem          chan struct{}
	wake         chan struct{}
	pollInterval time.Duration
	log          *slog.Logger
}

// NewQueue 构建队列。concurrency ≤0 回落缺省 2（Normalize 语义）。
func NewQueue(store *state.Store, exec Executor, concurrency int, pollInterval time.Duration, log *slog.Logger) *Queue {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency > MaxConcurrency {
		concurrency = MaxConcurrency
	}
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	return &Queue{
		store:        store,
		exec:         exec,
		sem:          make(chan struct{}, concurrency),
		wake:         make(chan struct{}, 1),
		pollInterval: pollInterval,
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
func (q *Queue) Run(ctx context.Context) error {
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
			if _, err := q.exec.Execute(ctx, claimed); err != nil {
				q.log.Error("execute build", "build", claimed.ID, "error", err)
			}
		}(claimed)
	}
}
