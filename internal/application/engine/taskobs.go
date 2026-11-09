package engine

// Run 观测的装配与执行（F1.5/F1.6，C3 观测 verdict owner + P1-7 缓存分
// 家）：裁决权在 Run 状态机——观测裁决表单源 domain/automation/run 的
// Adjudicate（P3a 批第 2 单元下沉，ADR-0087 决策 7），本文件是观测两轨
// 的 Instance 级装配与行写执行，与 App 部署面（observ.go 的五 map +
// L1/L2/L3 门）分轨。缓存组独立（workloadRun/runObs）；per-Run 终态即回
// 收（janitor 不背债）。

import (
	"context"
	"errors"
	"sync"

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
	ensure      map[string]ensureMemo               // taskID → 期望集签名确保缓存（2026-10-03 收编通用 ensureMemo）
	locks       sync.Map                            // taskID → *sync.Mutex（驱动环/DeleteTask 收口共享；appLocks 同款不清退裁决 E30）
}

// handleRunObservation 是 Run 观测的裁决入口（装配 → 域裁决 → 执行）：
// 观测缓存刷新 → 直读 Run 行 → run.Adjudicate 域裁决 → 行写执行
// （transitRunFour 四件一：CAS + 事件发射 + 审计）→ 终态回收（P1-7）→
// Kick（Task 环收口镜像/补足）。裁决表单源在 domain/automation/run
// （六行对账表下沉，ADR-0087 决策 7；切分契约：裁决表归域，eventcode
// 发射随四件一、观测缓存/终态回收/Kick 留 engine）。
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
	v := m.Adjudicate(ev)
	if v.Transit {
		if err := e.transitRunFour(ctx, m, v.From, v.To, v.Mutation()); err != nil {
			e.log.Error("run observe: transit", "run", runID, "to", v.To, "err", err)
		}
		if v.To.Terminal() {
			e.task.recycleObs(runID) // per-Run 终态回收（P1-7：随裁决，不等 janitor）
		}
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
