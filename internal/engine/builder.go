package engine

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/build"
)

// 构建循环参数（Options 扩展；BuildConcurrency 上限可配，F0.9）。
type buildOptions struct {
	Concurrency int           // 并发构建上限（默认 2）
	Timeout     time.Duration // 单次构建硬超时（默认 15m；超时=expired）
}

// logBuffer 是构建日志最近缓冲（诚实边界："仅实时+最近缓冲"，F0.9/F0.25
// 同口径；持久化检索 N2）。有界环形，最新优先快照供读取面。
type logBuffer struct {
	mu     sync.Mutex
	frames map[string][]capability.LogFrame
	cap    int
}

func newLogBuffer(capacity int) *logBuffer {
	if capacity <= 0 {
		capacity = 500
	}
	return &logBuffer{frames: map[string][]capability.LogFrame{}, cap: capacity}
}

func (b *logBuffer) write(buildID string, f capability.LogFrame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	buf := append(b.frames[buildID], f)
	if len(buf) > b.cap {
		buf = buf[len(buf)-b.cap:]
	}
	b.frames[buildID] = buf
}

// recent 返回最近缓冲快照（旧→新）。
func (b *logBuffer) recent(buildID string) []capability.LogFrame {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]capability.LogFrame, len(b.frames[buildID]))
	copy(out, b.frames[buildID])
	return out
}

// bufferWriter 适配 LogWriter → 环形缓冲。
type bufferWriter struct {
	engine  *Engine
	buildID string
}

func (w *bufferWriter) WriteLog(_ context.Context, f capability.LogFrame) error {
	f.WorkloadID = w.buildID
	w.engine.buildLogs.write(w.buildID, f)
	return nil
}

// buildStep 是构建循环的收敛步：拾取 queued（并发余量内）→ 起 goroutine
// 执行（per-Build 单写者：行级 CAS，goroutine 生命周期 = 该行写者）。
func (e *Engine) buildStep(ctx context.Context) {
	active, err := e.builds.ListActive(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("build step: list active", "err", err)
		return
	}
	running := 0
	for _, b := range active {
		if b.State == build.StateBuilding {
			running++
		}
	}
	for _, b := range active {
		if b.State != build.StateQueued || running >= e.buildOpts.Concurrency {
			continue
		}
		// CAS queued→building（四件一拍）后立即返回；执行在 goroutine（构建
		// 生命周期绑定进程而非收敛步——step ctx 取消不等构建，优雅退出由
		// executeBuild 内的 shutdown 路径回 queued 重放）。
		fresh, err := e.transitBuild(ctx, &b,
			[]build.State{build.StateQueued}, build.StateBuilding, nil)
		if err != nil {
			e.log.Error("build step: transit", "build", b.ID, "err", err)
			continue
		}
		running++
		go e.executeBuild(fresh) //nolint:gosec // 构建goroutine 生命周期即 Build 行写者（架构 §6 per-线单写者）
	}
}

// executeBuild 执行一次构建（goroutine；ctx = 进程生命期，超时独立控制）。
// 幂等重放：崩溃遗留的 building 行由 Start 重置 queued 重跑（buildkit 缓存
// 保证重放成本可控）。
func (e *Engine) executeBuild(b *build.Build) {
	runCtx, cancel := context.WithTimeout(context.Background(), e.buildOpts.Timeout)
	defer cancel()

	// 构建输入在 building 前由部署驱动备好（buildInputs 落在行外的内存
	// 登记表；重启丢失时按 queued 重放路径重建）。
	e.buildInputMu.Lock()
	input, ok := e.buildInputs[b.ID]
	e.buildInputMu.Unlock()
	if !ok {
		// 无输入登记（重启遗留）：回 queued 等部署驱动重新登记。
		if _, err := e.transitBuild(runCtx, b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); err != nil {
			e.log.Error("build: reset orphan to queued", "build", b.ID, "err", err)
		}
		return
	}

	result, err := e.builder.Build(runCtx, input, &bufferWriter{engine: e, buildID: b.ID})
	switch {
	case err == nil:
		if _, terr := e.transitBuild(runCtx, b,
			[]build.State{build.StateBuilding}, build.StateSucceeded,
			func(m *build.Build) { m.Digest = result.Digest }); terr != nil {
			e.log.Error("build: transit succeeded", "build", b.ID, "err", terr)
		}
	case isContextTimeout(runCtx, err):
		if _, terr := e.transitBuild(context.Background(), b,
			[]build.State{build.StateBuilding}, build.StateExpired,
			func(m *build.Build) { m.Error = "build timed out" }); terr != nil {
			e.log.Error("build: transit expired", "build", b.ID, "err", terr)
		}
	case runCtx.Err() != nil:
		// 进程退出路径（优雅退出）：回 queued，重启后重放。
		if _, terr := e.transitBuild(context.Background(), b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); terr != nil {
			e.log.Error("build: reset on shutdown", "build", b.ID, "err", terr)
		}
	default:
		if _, terr := e.transitBuild(context.Background(), b,
			[]build.State{build.StateBuilding}, build.StateFailed,
			func(m *build.Build) { m.Error = err.Error() }); terr != nil {
			e.log.Error("build: transit failed", "build", b.ID, "err", terr)
		}
	}
	e.buildLoop.Kick() // 释放并发位，立即拾取下一个
}

// transitBuild 是 Build 的四件一拍（CAS + build.<state> 事件 + 审计）。
func (e *Engine) transitBuild(ctx context.Context, b *build.Build, from []build.State, to build.State, mut func(*build.Build)) (*build.Build, error) {
	var fresh *build.Build
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.builds.Transit(ctx, tx, b.ID, from, to, mut); err != nil {
			return err
		}
		var err error
		fresh, err = e.builds.Get(ctx, tx, b.ID)
		if err != nil {
			return err
		}
		if fresh.State != to || !buildStateIn(from, to) {
			if _, err := e.outbox.Append(ctx, tx, eventBuildState(to), "build", b.ID, buildEventPayloadJSON(fresh)); err != nil {
				return err
			}
		}
		return e.audits.Append(ctx, tx, &audit.Entry{
			ID: ulid.Make().String(), Source: audit.SourceSystem,
			Action: "build.transit", Resource: "build/" + b.ID,
			AfterFP: string(fresh.State),
		})
	})
	if err != nil {
		return nil, err
	}
	return fresh, nil
}

func buildStateIn(set []build.State, s build.State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

func isContextTimeout(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (err == context.DeadlineExceeded || strings.Contains(err.Error(), "context deadline exceeded"))
}
