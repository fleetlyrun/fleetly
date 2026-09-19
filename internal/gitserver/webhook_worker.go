package gitserver

// D1（S17 类 D：超时与取消闭环）——webhook 最小异步化：受理与执行分离。
// 背景：GitHub webhook 投递有 10s 硬超时，原同步链「拉源(FetchRemote) →
// 入队(DeployFromCommit)」全程绑 r.Context()——大仓库 fetch 必被客户端
// 掐断，且 GitHub 对 5xx 无限重投，形成永久失败循环。裁决：
//   - 安全面（验签/时间窗/重放占坑/分支过滤/去重）保持在响应前同步完成
//     （ServeHTTP 不变），受理即回 202 {status:"accepted"}；
//   - fetch + 入队移交本文件的后台 worker：带界队列（chan 32，满则受理侧
//     503 + 撤坑——背压可见），单 worker 串行消费，per-item 预算
//     context.WithTimeout(Background, 30min)（不绑请求 ctx，也不绑服务
//     ctx——停机排空语义见 loop）；
//   - 结果披露走事件流与审计（官方不会重投 202）：fetch 失败 → 事件
//     app.webhook_fetch_failed + 审计 + 撤坑（同 delivery ID 的手动
//     redeliver 可重试）；入队拒绝沿用既有审计路径；成功即既有
//     deployment.queued 事件。

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// webhookQueueCapacity 是受理队列的硬上界（D1）：满则受理侧 503——背压
// 可见（v0.1 常量：32 足够吸收瞬时重投峰，worker 串行消费）。
const webhookQueueCapacity = 32

// webhookJobTimeout 是单个后台任务的执行预算（D1）：30 分钟上界覆盖最慢
// 的首次大仓库 fetch；v0.1 常量，配置面随实际需要引入。
const webhookJobTimeout = 30 * time.Minute

// webhookJob 是受理后移交后台执行的投递项（安全面校验已在 ServeHTTP
// 全部通过；此结构不含 secret/body 等敏感材料）。
type webhookJob struct {
	appID      string
	app        string
	deliveryID string
	sha        string
	ref        string
}

// webhookRunner 承载 webhook 异步执行面（带界队列 + 单 worker + idle 等待
// 点）。零值不可用，由 NewGitTriggers 经 init 构造；生命周期由服务壳驱动
// （cmd/fleetlyd git_service 的 lynx Start/Stop → StartWebhookWorker/
// StopWebhookWorker）。
type webhookRunner struct {
	mu       sync.Mutex
	cond     *sync.Cond
	queue    chan webhookJob
	pending  int           // 已受理未完成的任务数（idle 判定 = pending == 0）
	stopping atomic.Bool   // 停机排水位：受理侧见此位即 503
	done     chan struct{} // worker 排空退出信号（nil = 未启动）
}

// init 构造队列与 idle 条件变量。
func (r *webhookRunner) init() {
	r.queue = make(chan webhookJob, webhookQueueCapacity)
	r.cond = sync.NewCond(&r.mu)
}

// start 启动 worker（幂等：已启动 no-op——服务壳单次启停纪律）。worker
// 生命周期绑 ctx：ctx 取消进入停机排空（先立排水位拒绝新任务，再处理完
// 队列既有项——在处理项的 per-item 预算独立于 ctx，不被取消打断）。
func (r *webhookRunner) start(ctx context.Context, process func(webhookJob)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done != nil {
		return
	}
	done := make(chan struct{})
	r.done = done
	go r.loop(ctx, done, process)
}

// stop 等待 worker 排空退出；ctx 先到则让位返回（后台排空继续，进程退出
// 兜底）。未启动为 no-op。
func (r *webhookRunner) stop(ctx context.Context) error {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// accept 把受理的投递入队（D1 受理侧唯一入口）：停机排水期或队列满 →
// false（调用方 503 + 撤坑）。pending 先于入队自增——waitIdle 的同步点
// 因此覆盖「202 已回但 worker 尚未拾起」的窗口。
func (r *webhookRunner) accept(job webhookJob) bool {
	if r.stopping.Load() {
		return false
	}
	r.mu.Lock()
	r.pending++
	r.mu.Unlock()
	select {
	case r.queue <- job:
		return true
	default:
		r.mu.Lock()
		r.pending--
		r.mu.Unlock()
		return false
	}
}

// waitIdle 阻塞至已受理任务全部处理完成（测试同步点——替代 sleep 轮询，
// 消除竞态；生产路径不调用）。
func (r *webhookRunner) waitIdle() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.pending > 0 {
		r.cond.Wait()
	}
}

// loop 是 worker 主循环：运行期消费队列；ctx 取消后立排水位并排空剩余项
// 再退出（close(done) 是 StopWebhookWorker 的等待点）。
func (r *webhookRunner) loop(ctx context.Context, done chan struct{}, process func(webhookJob)) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			r.stopping.Store(true)
			r.drain(process)
			return
		case job := <-r.queue:
			r.run(process, job)
		}
	}
}

// drain 排空队列既有项（停机路径；此时受理侧已转 503，队列只减不增）。
func (r *webhookRunner) drain(process func(webhookJob)) {
	for {
		select {
		case job := <-r.queue:
			r.run(process, job)
		default:
			return
		}
	}
}

// run 包裹单任务执行：完成后扣 pending，归零时广播唤醒 waitIdle。
func (r *webhookRunner) run(process func(webhookJob), job webhookJob) {
	defer func() {
		r.mu.Lock()
		r.pending--
		if r.pending == 0 {
			r.cond.Broadcast()
		}
		r.mu.Unlock()
	}()
	process(job)
}

// StartWebhookWorker 启动 webhook 后台 worker（D1）：随 git.ssh 服务壳的
// lynx Start 启动（webhook 面是 gateway 原生端点、独立于 SSH enabled 开关，
// 服务壳恒承担 worker 生命周期）；ctx 取消后 worker 排空队列退出，
// StopWebhookWorker 是排空完成等待点。
func (s *GitTriggers) StartWebhookWorker(ctx context.Context) {
	s.hooks.start(ctx, s.runWebhookJob)
}

// StopWebhookWorker 等待 worker 排空退出（D1）；未启动为 no-op。
func (s *GitTriggers) StopWebhookWorker(ctx context.Context) error {
	return s.hooks.stop(ctx)
}

// runWebhookJob 执行单个受理投递（原同步链的拉源+入队两步原样移入）：
// per-item 预算独立于请求与服务 ctx——D1 的要点（fetch 不再被 GitHub
// 10s 投递超时掐断，停机排空也不打断在处理项）。
func (s *GitTriggers) runWebhookJob(job webhookJob) {
	ctx, cancel := context.WithTimeout(context.Background(), webhookJobTimeout)
	defer cancel()

	// 拉源（fetch 失败 → 披露三件套，见 discloseFetchFailure）。
	if err := s.FetchRemote(ctx, job.app); err != nil {
		if !errors.Is(err, ErrFetchFailed) {
			// 非 ErrFetchFailed 形态（理论不可达：FetchRemote 全路径包装）：
			// 原文只进日志（B2 口径）。
			s.log.Warn("gitserver: webhook fetch failed with unexpected error", "app", job.app, "error", err.Error())
		}
		s.discloseFetchFailure(ctx, job)
		return
	}

	rec, _, err := s.DeployFromCommit(ctx, DeployInput{
		App:         job.app,
		SHA:         job.sha,
		Ref:         job.ref,
		AuditAction: "git.webhook_deploy",
	})
	if err != nil {
		// 与原同步路径同语义：审计 rejected；仅 5xx 形态撤坑（4xx 校验拒绝
		// 如 compose 词形错是确定性终局，保持已占坑）。异步后无 HTTP 回执
		// 可写——本审计即结果披露（事件面沿用既有 deployment.* 词汇，compose
		// 拒绝无专门事件）。
		s.webhookAuditErr(ctx, job.appID, job.app, job.deliveryID, "deploy rejected: "+err.Error())
		status := http.StatusInternalServerError
		var ae *apperr.Error
		if errors.As(err, &ae) {
			status = ae.HTTPStatus()
		}
		if status >= http.StatusInternalServerError {
			s.replay.Unmark(job.deliveryID)
		}
		return
	}
	s.log.Info("gitserver: webhook deployment enqueued",
		"app", job.app, "sha", job.sha, "ref", job.ref, "deployment", rec.ID)
}

// discloseFetchFailure 落拉源失败披露三件套（D1）：事件
// app.webhook_fetch_failed + 审计（detail 只记错误码与阶段——B2 出站字节
// 口径）+ 撤坑（同 delivery ID 的官方 redeliver 可重试；受理期已回 202，
// 失败只能走事件流披露——官方不会自动重投 202）。
func (s *GitTriggers) discloseFetchFailure(ctx context.Context, job webhookJob) {
	payload := state.DiffSummary("app", job.app, "delivery", job.deliveryID,
		"sha", job.sha, "code", "E_RUNTIME_UNAVAILABLE", "stage", "fetch")
	if err := s.st.InTx(ctx, func(tx *state.Tx) error {
		_, err := tx.AppendEvent(ctx, state.Event{
			Name:    "app.webhook_fetch_failed",
			Subject: "app:" + job.appID,
			Payload: payload,
		})
		return err
	}); err != nil {
		s.log.Warn("gitserver: webhook fetch-failed event write failed", "app", job.app, "error", err.Error())
	}
	s.webhookAuditErr(ctx, job.appID, job.app, job.deliveryID, "fetch failed: code=E_RUNTIME_UNAVAILABLE, stage=fetch")
	s.replay.Unmark(job.deliveryID)
}

// webhookAuditErr 写 webhook 拒绝审计（handler 同步拒绝路径与 worker 异步
// 失败路径共用；result=error，action 词根独立可分）。
func (s *GitTriggers) webhookAuditErr(ctx context.Context, appID, app, deliveryID, detail string) {
	if err := s.st.InTx(ctx, func(tx *state.Tx) error {
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "system",
			Action:      "app.webhook_rejected",
			Target:      "app:" + appID,
			Result:      "error",
			DiffSummary: auditDiff(app, deliveryID, "rejected", detail),
		})
	}); err != nil {
		s.log.Warn("gitserver: webhook audit write failed", "app", app, "error", err.Error())
	}
}
