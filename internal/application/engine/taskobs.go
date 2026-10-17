package engine

// Run 观测裁决（F1.5/F1.6，C3 观测 verdict owner + P1-7 缓存分家）：Run
// 观测的裁决权在 Run 状态机（本文件），与 App 部署面（observ.go 的五 map
// + L1/L2/L3 门）分轨。缓存组独立（workloadRun/runObs）；per-Run 终态即回
// 收（janitor 不背债）。

import (
	"context"
	"errors"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// tasks 是 Task 域状态（行为 task.go/taskverbs.go/taskobs.go/taskview.go；
// P1-7 缓存分家口径不变；per-Run 终态回收随 handleRunObservation——恢复
// 真源是 Task/Run 行，不抄 rebuildBaselines）。P3 批 3 收编：taskDomain →
// tasks（复数普通名词零后缀，ADR-0075 命名法则）；Engine 字段名 task 保持
// 不变。P3 收官批 8 收编：类型体自 engine.go 归位 taskobs.go（壳收口）。
type tasks struct {
	obsMu       sync.RWMutex
	workloadRun map[string]string                   // workloadID(=runID) → taskID（观测路由/归属）
	runObs      map[string]capability.WorkloadEvent // runID → 最新观测
	ensure      map[string]ensureMemo               // taskID → 期望集签名备忘（2026-10-03 收编通用 ensureMemo）
	locks       sync.Map                            // taskID → *sync.Mutex（驱动环/DeleteTask 收口共享；appLocks 同款不清退裁决 E30）
}

// handleRunObservation 是 Run 观测的裁决入口：观测缓存刷新 → Run 状态机
// 迁移（观测两轨的 Instance 级，ADR-0025 决策 3）→ 终态回收（P1-7）→
// Kick（Task 环收口镜像/补足）。
//
// 裁决表（终态观测与 Run 行态的对账）：
//
//	观测 running        + 行 pending            → running
//	观测 completed      + 行 pending/running    → stopped/completed（退出码 0）
//	观测 failed         + 行 pending/running    → failed/failed（退出码非 0）
//	观测 stopped        + 行 stopping           → stopped/<stopping 起因>
//	观测 stopped        + 行 pending/running    → stopped/platform_drained
//	（载体被平台侧移除：节点排空/人工拆载体——诚实归类，不伪装成败）
func (e *Engine) handleRunObservation(ctx context.Context, runID string, ev capability.WorkloadEvent) {
	e.task.obsMu.Lock()
	e.task.runObs[runID] = ev
	e.task.obsMu.Unlock()

	m, err := e.repos.Runs.Get(ctx, runID)
	if err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			e.log.Error("run observe: get row", "run", runID, "err", err)
		}
		return // 行不在（删除收口后的迟到观测）：丢弃
	}
	if m.State.Terminal() {
		e.task.recycleObs(runID) // 终态回收（迟到观测不重开状态机）
		return
	}
	switch {
	case ev.State == capability.WorkloadRunning && m.State == run.StatePending:
		if err := e.transitRunFour(ctx, m, []run.State{run.StatePending}, run.StateRunning, nil); err != nil {
			e.log.Error("run observe: running", "run", runID, "err", err)
		}
	case ev.State == capability.WorkloadCompleted && m.State.Active():
		if err := e.transitRunFour(ctx, m,
			run.ActiveStates, run.StateStopped,
			func(r *run.Run) { r.StopReason = run.ReasonCompleted; r.ExitCode = ev.ExitCode }); err != nil {
			e.log.Error("run observe: completed", "run", runID, "err", err)
		}
		e.task.recycleObs(runID)
	case ev.State == capability.WorkloadFailed && m.State.Active():
		if err := e.transitRunFour(ctx, m,
			run.ActiveStates, run.StateFailed,
			func(r *run.Run) { r.StopReason = run.ReasonFailed; r.ExitCode = ev.ExitCode }); err != nil {
			e.log.Error("run observe: failed", "run", runID, "err", err)
		}
		e.task.recycleObs(runID)
	case ev.State == capability.WorkloadStopped && m.State == run.StateStopping:
		// stopping 起因已在行上（stopRunRow 写入）；观测确认即终态。
		if err := e.transitRunFour(ctx, m,
			[]run.State{run.StateStopping}, run.StateStopped,
			func(r *run.Run) { r.ExitCode = ev.ExitCode }); err != nil {
			e.log.Error("run observe: stopped", "run", runID, "err", err)
		}
		e.task.recycleObs(runID)
	case ev.State == capability.WorkloadStopped && m.State.Active():
		if err := e.transitRunFour(ctx, m,
			run.ActiveStates, run.StateStopped,
			func(r *run.Run) { r.StopReason = run.ReasonPlatformDrained; r.ExitCode = ev.ExitCode }); err != nil {
			e.log.Error("run observe: platform drained", "run", runID, "err", err)
		}
		e.task.recycleObs(runID)
	}
	e.taskLoop.Kick()
}

// recycleObs 回收终态 Run 的观测缓存条目（P1-7：per-Run 终态回收随
// 裁决，不等 janitor 扫描；归属映射保留至 Task 收口——期间迟到观测按
// 终态行幂等丢弃）。
func (x *tasks) recycleObs(runID string) {
	x.obsMu.Lock()
	delete(x.runObs, runID)
	x.obsMu.Unlock()
}

// observation 返回 Run 的最新观测（诊断/测试面；不存在返回零值）。
func (x *tasks) observation(runID string) (capability.WorkloadEvent, bool) {
	x.obsMu.RLock()
	defer x.obsMu.RUnlock()
	ev, ok := x.runObs[runID]
	return ev, ok
}

// drainObs 清理 Task 名下全部观测缓存（workloadRun 归属映射 +
// runObs 观测槽；DeleteTask 消费——workload ID 即 run ID，一键双清）。
func (x *tasks) drainObs(taskID string) {
	x.obsMu.Lock()
	for wid, owner := range x.workloadRun {
		if owner == taskID {
			delete(x.workloadRun, wid)
			delete(x.runObs, wid)
		}
	}
	x.obsMu.Unlock()
}
