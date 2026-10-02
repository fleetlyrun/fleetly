package engine

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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
//
// 帧带 per-Build 单调序列号（N0.1 P2-1）：回绕丢帧后读取面按 seq 续流，
// 不依赖 len(frames) 位置——"游标=长度"假设在环形截断后静默停发。
// 终态 Build 的缓冲保留最近 retainedTerminalBuilds 个，其余回收（frames
// map 只增不清会随时间泄漏）。
type logBuffer struct {
	mu       sync.Mutex
	frames   map[string][]seqFrame
	nextSeq  map[string]int64
	terminal []string // 终态完成序（旧→新）
	cap      int
}

// seqFrame 是带序列号的日志帧。
type seqFrame struct {
	seq int64
	f   capability.LogFrame
}

// retainedTerminalBuilds 是终态 Build 日志缓冲保留数（最近的可回读，更早
// 的回收；活跃 Build 不回收）。
const retainedTerminalBuilds = 8

func newLogBuffer(capacity int) *logBuffer {
	if capacity <= 0 {
		capacity = 500
	}
	return &logBuffer{
		frames:  map[string][]seqFrame{},
		nextSeq: map[string]int64{},
		cap:     capacity,
	}
}

func (b *logBuffer) write(buildID string, f capability.LogFrame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	seq := b.nextSeq[buildID]
	b.nextSeq[buildID] = seq + 1
	buf := append(b.frames[buildID], seqFrame{seq: seq, f: f})
	if len(buf) > b.cap {
		buf = buf[len(buf)-b.cap:]
	}
	b.frames[buildID] = buf
}

// recentAfter 返回 seq 大于 after 的帧（旧→新）与本次见到的最新 seq（无
// 新帧时原样回传 after——游标不动）。
func (b *logBuffer) recentAfter(buildID string, after int64) ([]capability.LogFrame, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []capability.LogFrame
	last := after
	for _, sf := range b.frames[buildID] {
		if sf.seq <= after {
			continue
		}
		out = append(out, sf.f)
		last = sf.seq
	}
	return out, last
}

// recent 返回最近缓冲快照（旧→新；首个消费面，apitest/golden 同形态）。
func (b *logBuffer) recent(buildID string) []capability.LogFrame {
	out, _ := b.recentAfter(buildID, -1)
	if out == nil {
		return []capability.LogFrame{}
	}
	return out
}

// markTerminal 登记终态完成并回收超龄终态缓冲（幂等：重复登记同 ID 忽略）。
func (b *logBuffer) markTerminal(buildID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n := len(b.terminal); n > 0 && b.terminal[n-1] == buildID {
		return
	}
	b.terminal = append(b.terminal, buildID)
	if len(b.terminal) > retainedTerminalBuilds {
		drop := b.terminal[0]
		b.terminal = b.terminal[1:]
		delete(b.frames, drop)
		delete(b.nextSeq, drop)
	}
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

// RecentBuildLogs 返回构建日志最近缓冲快照（旧→新；B4 读面消费）。
func (e *Engine) RecentBuildLogs(buildID string) []capability.LogFrame {
	return e.buildLogs.recent(buildID)
}

// RecentBuildLogsAfter 返回 seq 大于 after 的日志帧（旧→新）与最新 seq
// （follow 续流游标；N0.1 P2-1）。
func (e *Engine) RecentBuildLogsAfter(buildID string, after int64) ([]capability.LogFrame, int64) {
	return e.buildLogs.recentAfter(buildID, after)
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
		// 拾取前置检（D-4）：无进程内登记的 queued 行不允许空拾取——无输入
		// 的执行只会弹跳或误终态。归它或等登记，见 buildPickable。
		if !e.buildPickable(ctx, &b) {
			continue
		}
		// CAS queued→building（四件一拍）后立即返回；执行在 goroutine（构建
		// 生命周期绑定进程而非收敛步——step ctx 取消不等构建，优雅退出由
		// executeBuild 内的 shutdown 路径回 queued 重放）。goroutine 计入
		// wg（Q-7）：Stop 取消 runCtx 即有界排水，不默等构建超时。
		fresh, err := e.transitBuild(ctx, &b,
			[]build.State{build.StateQueued}, build.StateBuilding, nil)
		if err != nil {
			e.log.Error("build step: transit", "build", b.ID, "err", err)
			continue
		}
		running++
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			e.executeBuild(fresh)
		}() //nolint:gosec // 构建 goroutine 生命周期即 Build 行写者（架构 §6 per-线单写者）
	}
}

// buildPickable 是 queued 行的拾取前置检（D-4）：无进程内输入登记的行
// 只有两种来路——①归属部署仍活跃（重启后待 driveBuilding 幂等重建登记，
// 本拍跳过等登记落地）；②归属部署已终态（取消/抢占后遗留的孤儿行）→
// 一跳到终态 cancelled（同步、无 goroutine），禁止拾取执行形成
// queued↔building 振荡。已登记的行正常放行。
func (e *Engine) buildPickable(ctx context.Context, b *build.Build) bool {
	e.buildInputMu.Lock()
	_, registered := e.buildInputs[b.ID]
	e.buildInputMu.Unlock()
	if registered {
		return true
	}
	owner, err := e.buildOwnerActive(ctx, b)
	if err != nil {
		// 归属判定失败（存储故障）：保守跳过本拍——不基于不确定的探测销毁行。
		e.log.Error("build step: probe owner", "build", b.ID, "err", err)
		return false
	}
	if owner {
		return false // 部署驱动将在 driveBuilding 内重建输入（下拍拾取）
	}
	if _, err := e.transitBuild(ctx, b,
		[]build.State{build.StateQueued}, build.StateCancelled,
		func(m *build.Build) { m.Error = "orphaned build: owning deployment is no longer active" }); err != nil {
		e.log.Error("build step: cancel orphan", "build", b.ID, "err", err)
	}
	return false
}

// buildOwnerActive 报告 Build 的归属部署是否仍活跃（同 App 且目标
// Revision 一致：该部署进入 building 态时 driveBuilding 会重建输入登记）。
func (e *Engine) buildOwnerActive(ctx context.Context, b *build.Build) (bool, error) {
	active, err := e.deployments.ActiveByApp(ctx, e.db.Runner(), b.AppID)
	if err != nil {
		return false, err
	}
	for i := range active {
		if active[i].ToRevision == b.RevisionID {
			return true, nil
		}
	}
	return false, nil
}

// executeBuild 执行一次构建（goroutine；根 = 引擎进程生命期 runCtx，构建
// 超时独立包裹其上）。幂等重放：崩溃遗留的 building 行由 Start 重置
// queued 重跑（buildkit 缓存保证重放成本可控）。
func (e *Engine) executeBuild(b *build.Build) {
	// Q-7：根取引擎 runCtx（Start 物化；Stop 取消 → 下方 runCtx.Err() 分支
	// 优雅回 queued，排水有界），超时独立包裹（Stop 不默等 15m 看门狗）。
	runCtx, cancel := context.WithTimeout(e.buildRootCtx(), e.buildOpts.Timeout)
	defer cancel()
	// 终态落库 ctx：runCtx 超时/被取消后仍需写库（脱离取消链、保留值链）。
	finishCtx := context.WithoutCancel(runCtx)

	// 构建输入在 building 前由部署驱动备好（buildInputs 是行外内存登记表；
	// 重启丢失由 driveBuilding 幂等重建，拾取前置检已拦无登记行——到达
	// 此分支即防御纵深）。
	e.buildInputMu.Lock()
	input, ok := e.buildInputs[b.ID]
	e.buildInputMu.Unlock()
	if !ok {
		// 无输入登记且无人会再登记（孤儿形态：部署已不在/输入丢失）→
		// 一跳到终态 cancelled。不得回 queued 弹跳——那正是 queued↔building
		// 无限振荡的根因（D-4）。
		if _, err := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateCancelled,
			func(m *build.Build) { m.Error = "build input lost: no registered input for this build" }); err != nil {
			e.log.Error("build: cancel without input", "build", b.ID, "err", err)
		}
		e.buildLoop.Kick()
		return
	}

	// 路由解析（ADR-0032）：input.Builder 是 Revision 冻结体的分派键；未知
	// 名（未装配的 Provider 或漂移名）→ 精确终态失败（不 panic、不弹回
	// queued 重试——重试不会让未装配的 Provider 出现）。
	builder, ok := e.builders[input.Builder]
	if !ok {
		if _, terr := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateFailed,
			func(m *build.Build) {
				m.Error = fmt.Sprintf("builder %q is not wired on this platform (wired: %s)", input.Builder, strings.Join(sortedBuilderNames(e.builders), ", "))
			}); terr != nil {
			e.log.Error("build: transit unwired-builder failure", "build", b.ID, "err", terr)
		}
		e.buildLoop.Kick()
		return
	}

	result, err := builder.Build(runCtx, input, &bufferWriter{engine: e, buildID: b.ID})
	switch {
	case err == nil:
		if _, terr := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateSucceeded,
			func(m *build.Build) { m.Digest = result.Digest }); terr != nil {
			e.log.Error("build: transit succeeded", "build", b.ID, "err", terr)
		}
	case isContextTimeout(runCtx, err):
		if _, terr := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateExpired,
			func(m *build.Build) { m.Error = "build timed out" }); terr != nil {
			e.log.Error("build: transit expired", "build", b.ID, "err", terr)
		}
	case runCtx.Err() != nil:
		// 进程退出路径（优雅退出）：回 queued，重启后重放。
		if _, terr := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); terr != nil {
			e.log.Error("build: reset on shutdown", "build", b.ID, "err", terr)
		}
	default:
		if _, terr := e.transitBuild(finishCtx, b,
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
	if fresh.State.Terminal() {
		// 终态登记触发超龄缓冲回收（frames map 只增不清会泄漏，N0.1 P2-1）。
		e.buildLogs.markTerminal(b.ID)
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

// sortedBuilderNames 返回在册 builder 名（排序稳定，供失败文本与日志）。
func sortedBuilderNames(builders map[string]capability.Builder) []string {
	names := make([]string, 0, len(builders))
	for name := range builders {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
