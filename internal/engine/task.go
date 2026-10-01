package engine

// Task/Run 驱动（F1.5/F1.6，ADR-0012/0025）：Task 域单写者收敛环 + 观测
// 裁决。与部署链的分界（C3 观测 verdict owner + P1-7 缓存分家，批 0.5 裁决
// 台账 C3 行）：
//
//   - 部署链的观测缓存五 map（workloadApp/observations/ensuredGen/
//     ensuredSpec/expected）是"部署形状"——键按 App 投影、恢复真源是
//     succeeded 部署重放；Run 粒度混入即 gen 语义错位、per-Run 条目只增
//     不清。
//   - Task 域用独立缓存组（workloadRun/runObs + per-Task Ensure 签名），
//     恢复真源是 Task/Run 行与绝对 deadline（ADR-0018 墙钟续算），重启后
//     下一拍收敛自愈，不抄 rebuildBaselines。
//   - 观测裁决权分轨：handleObservation 按 workloadRun 归属路由——Run 观
//     测进 Run 状态机（handleRunObservation），App 观测走原部署面。
//
// 混合池拓扑（ADR-0025 决策 7/R-6）：resident 池 = per-Run service（控制
// 面 TTL/排空/per-Run DNS 的载体单元），池级 DNS 由全部 Run 的别名共同
// 铸出（DNS RR 行为 e2e 实证项）；swarm API 压力（Ensure ×N）以 torchwood
// 池规模压测锚点收口。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/run"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// Task 域哨兵错误（API 层映射 errcode）。
var (
	// ErrTaskNotRenewable 是 RenewTask 目标形态不可续期（one-shot 无租约）。
	ErrTaskNotRenewable = errors.New("engine: task carries no owner lease")
	// ErrTaskTerminal 是生命周期动词命中终态行（终态事实不可改写）。
	ErrTaskTerminal = errors.New("engine: task is in a terminal state")
	// ErrNotResident 是仅 resident 形态适用的动词面（ScaleTask）。
	ErrNotResident = errors.New("engine: verb applies to resident tasks only")
)

// taskDrivingStates 是 Run 驱动集合（Ensure 期望集来源）。
var taskDrivingStates = []run.State{run.StatePending, run.StateRunning, run.StateStopping}

// taskStep 是 Task 收敛环的单次推进：属主吊销拉式扫描 → 逐 Task 驱动
// （janitor/补足/Ensure/终态收口）。单写者（Loop 串行）；观测裁决在
// consumeWatch goroutine 经 CAS 并发写行，互不阻塞。
func (e *Engine) taskStep(ctx context.Context) {
	tasks, err := e.tasks.ListDriving(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("task step: list driving", "err", err)
		return
	}
	if err := e.sweepRevokedOwners(ctx, tasks); err != nil {
		e.log.Error("task step: sweep revoked owners", "err", err)
	}
	for i := range tasks {
		e.driveTask(ctx, &tasks[i])
	}
}

// sweepRevokedOwners 是属主吊销排空的拉式扫描（P1-8：吊销排空走 task 环
// 周期扫 revoked 属主——拉式、重启安全；Task 行记 owner_token_id 引用）。
// 默认宽限排空（宽限停止存量 Run）；TaskOwnerRevokedRunToTTL 配置跑完 TTL。
func (e *Engine) sweepRevokedOwners(ctx context.Context, tasks []task.Task) error {
	byToken := map[string][]*task.Task{}
	for i := range tasks {
		t := &tasks[i]
		if t.OwnerTokenID == "" || t.State != task.StateActive {
			continue
		}
		byToken[t.OwnerTokenID] = append(byToken[t.OwnerTokenID], t)
	}
	for tokenID, owned := range byToken {
		tok, err := e.tokens.Get(ctx, e.db.Runner(), tokenID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue // 行不存在防御：Token 行只吊销不删
			}
			return fmt.Errorf("lookup owner token %s: %w", tokenID, err)
		}
		if !tok.Revoked {
			continue
		}
		for _, t := range owned {
			e.drainTask(ctx, t, run.ReasonOwnerRevoked)
		}
	}
	return nil
}

// driveTask 驱动单个 Task 一拍：lease 到期排空 → janitor（TTL/停止兜底）→
// 终态镜像/排空收口 → 补足 → Ensure（期望集收敛）。持有 Task 级互斥
// （DeleteTask 的载体收口与本环互斥，与 appLocks 同款）。
func (e *Engine) driveTask(ctx context.Context, t *task.Task) {
	mu := e.lockTask(t.ID)
	mu.Lock()
	defer mu.Unlock()

	spec, err := loadTaskSpec(t.Spec)
	if err != nil {
		e.log.Error("task drive: load spec", "task", t.ID, "err", err)
		return
	}
	now := e.clock.Now()

	// lease_expired（resident + active + 绝对 deadline 超宽限，ADR-0018 墙钟
	// 口径）：排空（停补足 + 宽限停止存量 Run）+ lease.expired 事件；属主
	// 续期可复活（RenewTask 的补足半边）。
	if t.State == task.StateActive && t.Form == task.FormResident && t.LeaseDeadline != "" {
		if deadline := parseDeadline(t.LeaseDeadline); deadline != nil && now.After(deadline.Add(e.opts.TaskLeaseGrace)) {
			e.drainTask(ctx, t, run.ReasonLeaseExpired)
			if err := e.outboxAppend(ctx, eventLeaseExpired, "task", t.ID,
				leaseEventPayloadJSON(t, "lease deadline exceeded grace")); err != nil {
				e.log.Error("task drive: lease.expired event", "task", t.ID, "err", err)
			}
		}
	}

	runs, err := e.runs.ListByTaskStates(ctx, e.db.Runner(), t.ID, taskDrivingStates)
	if err != nil {
		e.log.Error("task drive: list runs", "task", t.ID, "err", err)
		return
	}

	// janitor：TTL 到期 → stopping/ttl_expired（deadline 覆写为停止收口
	// 兜底）；stopping 超兜底 → stopped（观测缺位时的收口保底，不悬挂）。
	for i := range runs {
		m := &runs[i]
		if m.State.Active() && m.Deadline != "" {
			if deadline := parseDeadline(m.Deadline); deadline != nil && now.After(*deadline) {
				if err := e.stopRunRow(ctx, m, run.ReasonTTLExpired, now); err != nil {
					e.log.Error("task drive: ttl expire", "run", m.ID, "err", err)
				}
				continue
			}
		}
		if m.State == run.StateStopping && m.Deadline != "" {
			if deadline := parseDeadline(m.Deadline); deadline != nil && now.After(*deadline) {
				if err := e.transitRunFour(ctx, m,
					[]run.State{run.StateStopping}, run.StateStopped, nil); err != nil && !errors.Is(err, state.ErrConflict) {
					e.log.Error("task drive: stop fallback", "run", m.ID, "err", err)
				}
			}
		}
	}

	// 终态镜像与排空收口（行事实 → Task 态）。
	if t.State == task.StateActive && t.Form == task.FormOneShot {
		if e.oneshotTerminal(ctx, t) {
			// 唯一 Run 已终态：Task 镜像其成败，Ensure 空集收口残留载体
			//（one-shot 完成后 service 仍驻留 swarm——收敛移除）。
			e.ensureTaskWorkloads(ctx, t, nil)
			return
		}
	}
	if t.State == task.StateDraining && len(runs) == 0 {
		// 排空完成：残留载体收敛移除（Ensure 空集）→ drained。
		e.ensureTaskWorkloads(ctx, t, nil)
		if err := e.transitTaskFour(ctx, t,
			[]task.State{task.StateDraining}, task.StateDrained, nil); err != nil && !errors.Is(err, state.ErrConflict) {
			e.log.Error("task drive: drained", "task", t.ID, "err", err)
		}
		return
	}

	// 补足（active only）：one-shot 期望 1；resident 期望 desired_concurrency。
	// 活槽位 = pending/running（stopping 是收口态不占位）。突增上限防一拍
	// 海量创建（swarm API 压力面，决策 7 压测锚）。
	if t.State == task.StateActive {
		want := t.DesiredConcurrency
		if t.Form == task.FormOneShot {
			want = 1
		}
		live := 0
		for i := range runs {
			if runs[i].State.Active() {
				live++
			}
		}
		created := false
		for n := live; n < int(want) && n-live < e.opts.TaskReplenishBurst; n++ {
			if err := e.createRun(ctx, t, spec, now); err != nil {
				e.log.Error("task drive: replenish", "task", t.ID, "err", err)
				break
			}
			created = true
		}
		if created {
			// 补足后重列：新 Run 同拍进期望集（否则首拍 Ensure 空集——
			// 对新建 Task 是空域无害，对补位拍会白白推迟一拍收敛）。
			if refreshed, err := e.runs.ListByTaskStates(ctx, e.db.Runner(), t.ID, taskDrivingStates); err == nil {
				runs = refreshed
			} else {
				e.log.Error("task drive: relist runs", "task", t.ID, "err", err)
			}
		}
	}

	// Ensure：期望集 = 驱动 Run 的 Workload（pending/running → 1 副本，
	// stopping → 0 副本承载 SIGTERM+StopGrace）；域内收敛移除终态 Run 的
	// 残留载体。签名比对 + 周期强制重放控制 API 压力。
	ws := make([]capability.Workload, 0, len(runs))
	for i := range runs {
		w, _, err := ProjectTask(spec, taskTeam(t), runs[i].ID, runs[i].State != run.StateStopping)
		if err != nil {
			e.log.Error("task drive: project run", "run", runs[i].ID, "err", err)
			continue
		}
		w.StopGrace = e.opts.TaskStopGrace
		ws = append(ws, w)
	}
	e.ensureTaskWorkloads(ctx, t, ws)
}

// oneshotTerminal 判定 one-shot Task 的唯一 Run 是否已终态并镜像其成败
// （completed/failed）。返回 true = 镜像已落（或此前已落）。
func (e *Engine) oneshotTerminal(ctx context.Context, t *task.Task) bool {
	all, err := e.runs.ListByTaskStates(ctx, e.db.Runner(), t.ID, []run.State{run.StateStopped, run.StateFailed})
	if err != nil {
		e.log.Error("task drive: list terminal runs", "task", t.ID, "err", err)
		return false
	}
	if len(all) == 0 {
		return false
	}
	latest := &all[0] // ListByTaskStates 新→旧：首行 = 唯一 Run
	to := task.StateCompleted
	if latest.State == run.StateFailed {
		to = task.StateFailed
	}
	if t.State == to {
		return true // 镜像已落（幂等重入）
	}
	if err := e.transitTaskFour(ctx, t, []task.State{task.StateActive}, to, nil); err != nil {
		e.log.Error("task drive: mirror terminal", "task", t.ID, "err", err)
		return false
	}
	return true
}

// ensureTaskWorkloads 以签名比对 + 周期强制收口下发 Task 域期望集（签名
// 未变且未到期 = 跳过，控制 per-tick swarm API 压力；失败清签名下拍重试；
// 重启后缓存丢失 → 首拍全量重放自愈）。
func (e *Engine) ensureTaskWorkloads(ctx context.Context, t *task.Task, ws []capability.Workload) {
	sig := workloadSetSignature(ws)
	e.taskEnsuredMu.Lock()
	lastSig, ensured := e.taskEnsured[t.ID]
	lastAt := e.taskLastEnsure[t.ID]
	e.taskEnsuredMu.Unlock()
	if ensured && lastSig == sig && e.clock.Now().Before(lastAt.Add(e.opts.TaskReconcileInterval)) {
		return
	}
	ns := capability.NamespaceRef{Team: taskTeam(t), Project: t.ProjectID, Task: t.ID}
	materials, err := e.resolveTaskMaterials(ctx, t)
	if err != nil {
		e.log.Error("task ensure: materials", "task", t.ID, "err", err)
		return
	}
	if err := e.runtime.Ensure(ctx, ns, ws, capability.Generation(1), materials); err != nil {
		e.log.Error("task ensure", "task", t.ID, "err", err)
		e.taskEnsuredMu.Lock()
		delete(e.taskEnsured, t.ID) // 失败清签名：下拍重试
		e.taskEnsuredMu.Unlock()
		return
	}
	// 归属登记（观测路由：Run 观测 → Run 状态机）。
	e.taskObsMu.Lock()
	for _, w := range ws {
		e.workloadRun[w.ID] = t.ID
	}
	e.taskObsMu.Unlock()
	now := e.clock.Now()
	e.taskEnsuredMu.Lock()
	e.taskEnsured[t.ID] = sig
	e.taskLastEnsure[t.ID] = now
	e.taskEnsuredMu.Unlock()
}

// drainTask 发起排空：task → draining（task.draining 事件携带起因）；
// 默认宽限停止存量 Run（stopping/<reason>），跑完 TTL 模式只停补足。
func (e *Engine) drainTask(ctx context.Context, t *task.Task, reason string) {
	if err := e.transitTaskFour(ctx, t, []task.State{task.StateActive}, task.StateDraining, nil); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return // 并发已迁移（观测路径/重复拍）：以行现状为准
		}
		e.log.Error("task drain", "task", t.ID, "err", err)
		return
	}
	if reason == run.ReasonOwnerRevoked && e.opts.TaskOwnerRevokedRunToTTL {
		return // 跑完 TTL 模式：存量 Run 自然收口
	}
	now := e.clock.Now()
	runs, err := e.runs.ListByTaskStates(ctx, e.db.Runner(), t.ID, []run.State{run.StatePending, run.StateRunning})
	if err != nil {
		e.log.Error("task drain: list runs", "task", t.ID, "err", err)
		return
	}
	for i := range runs {
		if err := e.stopRunRow(ctx, &runs[i], reason, now); err != nil {
			e.log.Error("task drain: stop run", "run", runs[i].ID, "err", err)
		}
	}
}

// stopRunRow 把一条活跃 Run 迁入 stopping（起因即写入，终态携带；deadline
// 覆写为停止收口兜底 = StopGrace × 2 + 观测余量）。
func (e *Engine) stopRunRow(ctx context.Context, m *run.Run, reason string, now time.Time) error {
	fallback := state.FormatTime(now.Add(2 * e.opts.TaskStopGrace).Add(10 * time.Second))
	return e.transitRunFour(ctx, m,
		[]run.State{run.StatePending, run.StateRunning}, run.StateStopping,
		func(r *run.Run) { r.StopReason, r.Deadline = reason, fallback })
}

// createRun 落一条 pending Run（四件一拍：行 + run.created 事件 + 审计）。
// TTL 绝对 deadline 按 ADR-0018 墙钟落库。
func (e *Engine) createRun(ctx context.Context, t *task.Task, spec *specv1.TaskSpec, now time.Time) error {
	id := ulid.Make().String()
	deadline := ""
	if ttl := spec.GetTtlSeconds(); ttl > 0 {
		deadline = state.FormatTime(now.Add(time.Duration(ttl) * time.Second))
	}
	m := &run.Run{
		ID: id, TaskID: t.ID, ProjectID: t.ProjectID,
		State: run.StatePending, WorkloadID: id, DNSName: RunDNSName(id),
		Deadline: deadline,
	}
	return e.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.runs.Create(ctx, tx, m); err != nil {
			return err
		}
		if _, err := e.outbox.Append(ctx, tx, eventRunState(run.StatePending), "run", m.ID, runEventPayloadJSON(m)); err != nil {
			return err
		}
		return e.audits.Append(ctx, tx, &audit.Entry{
			ID: ulid.Make().String(), Source: audit.SourceSystem,
			Action: "run.create", Resource: "run/" + m.ID,
		})
	})
}

// RenewTask 续期 Owner Lease（F1.6）：deadline 推进 TaskLeaseInterval；
// 排空态（draining/drained）可复活——续期即属主在场的显式声明，补足随
// active 恢复（"排空并补足"的补足半边）。
func (e *Engine) RenewTask(ctx context.Context, id string) (*task.Task, error) {
	var out *task.Task
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		t, err := e.tasks.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.Form != task.FormResident {
			return fmt.Errorf("%w: task %s is %s (one-shot tasks carry no lease)", ErrTaskNotRenewable, id, t.Form)
		}
		if t.State.Terminal() && t.State != task.StateDrained {
			return fmt.Errorf("%w: task %s is %s", ErrTaskTerminal, id, t.State)
		}
		deadline := state.FormatTime(e.clock.Now().Add(e.opts.TaskLeaseInterval))
		if t.State != task.StateActive {
			// 复活：draining/drained → active，随后原地续 lease（复活迁移的
			// task.active 事件随本事务显式落——repo Transit 不自带事件）。
			if err := e.tasks.Transit(ctx, tx, id,
				[]task.State{task.StateDraining, task.StateDrained}, task.StateActive, nil); err != nil {
				return err
			}
			revived, err := e.tasks.Get(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := e.outbox.Append(ctx, tx, eventTaskState(task.StateActive), "task", id, taskEventPayloadJSON(revived, "")); err != nil {
				return err
			}
		}
		if err := e.tasks.UpdateLease(ctx, tx, id, deadline); err != nil {
			return err
		}
		fresh, err := e.tasks.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		out = fresh
		_, err = e.outbox.Append(ctx, tx, eventLeaseRenewed, "task", id, leaseEventPayloadJSON(fresh, ""))
		return err
	})
	if err != nil {
		return nil, err
	}
	e.taskLoop.Kick()
	return out, nil
}

// StopTask 排空停止（F1.5）：停止补足；force=false 存量 Run 自然收口
// （完成/TTL），force=true 宽限停止（StopGrace 路径）。draining 收口后
// Task → drained。
func (e *Engine) StopTask(ctx context.Context, id string, force bool) (*task.Task, error) {
	var out *task.Task
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		t, err := e.tasks.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.State.Terminal() {
			return fmt.Errorf("%w: task %s is %s", ErrTaskTerminal, id, t.State)
		}
		if t.State == task.StateActive {
			if err := e.tasks.Transit(ctx, tx, id,
				[]task.State{task.StateActive}, task.StateDraining, nil); err != nil {
				return err
			}
		}
		fresh, err := e.tasks.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		out = fresh
		_, err = e.outbox.Append(ctx, tx, eventTaskDraining, "task", id,
			taskEventPayloadJSON(fresh, run.ReasonStoppedByUser))
		return err
	})
	if err != nil {
		return nil, err
	}
	if force {
		now := e.clock.Now()
		runs, rerr := e.runs.ListByTaskStates(ctx, e.db.Runner(), id, []run.State{run.StatePending, run.StateRunning})
		if rerr != nil {
			e.log.Error("task stop: list runs", "task", id, "err", rerr) // 行已 draining；停止收口交下一拍
		}
		for i := range runs {
			if err := e.stopRunRow(ctx, &runs[i], run.ReasonStoppedByUser, now); err != nil {
				e.log.Error("task stop: stop run", "run", runs[i].ID, "err", err)
			}
		}
	}
	e.taskLoop.Kick()
	return out, nil
}

// ScaleTask 调整 resident 期望并发（原地迁移 + task.updated 事件）。
func (e *Engine) ScaleTask(ctx context.Context, id string, desired int64) (*task.Task, error) {
	var out *task.Task
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		t, err := e.tasks.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.Form != task.FormResident {
			return fmt.Errorf("%w: scaling applies to resident tasks (task %s is %s)", ErrNotResident, id, t.Form)
		}
		if t.State.Terminal() {
			return fmt.Errorf("%w: task %s is %s", ErrTaskTerminal, id, t.State)
		}
		if err := e.tasks.Transit(ctx, tx, id,
			[]task.State{task.StateActive}, task.StateActive,
			func(m *task.Task) { m.DesiredConcurrency = desired }); err != nil {
			return err
		}
		fresh, err := e.tasks.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		out = fresh
		_, err = e.outbox.Append(ctx, tx, eventTaskUpdated, "task", id, taskEventPayloadJSON(fresh, ""))
		return err
	})
	if err != nil {
		return nil, err
	}
	e.taskLoop.Kick()
	return out, nil
}

// DeleteTask 删除 Task（ADR-0023 同款"先收口后落账"）：拆载体 → 存量 Run
// 终态化（stopped_by_user）→ tombstone。持有 Task 级互斥（与驱动环共享）。
func (e *Engine) DeleteTask(ctx context.Context, id string) error {
	mu := e.lockTask(id)
	mu.Lock()
	defer mu.Unlock()

	t, err := e.tasks.Get(ctx, e.db.Runner(), id)
	if err != nil {
		return err
	}
	if t.State == task.StateDeleted {
		return nil // 幂等
	}
	ns := capability.NamespaceRef{Team: taskTeam(t), Project: t.ProjectID, Task: t.ID}
	if err := e.runtime.Remove(ctx, ns); err != nil {
		return fmt.Errorf("runtime remove: %w", err)
	}
	// 缓存收口（P1-7 分家面：Task 域缓存组按 Task/Run 键清理）。
	e.drainRunObsForTask(id)
	e.taskEnsuredMu.Lock()
	delete(e.taskEnsured, id)
	delete(e.taskLastEnsure, id)
	e.taskEnsuredMu.Unlock()

	return e.db.Tx(ctx, func(tx *sql.Tx) error {
		driving, err := e.runs.ListDriving(ctx, tx)
		if err != nil {
			return err
		}
		for i := range driving {
			if driving[i].TaskID != id {
				continue
			}
			if err := e.runs.Transit(ctx, tx, driving[i].ID,
				taskDrivingStates, run.StateStopped,
				func(r *run.Run) { r.StopReason = run.ReasonStoppedByUser; r.Deadline = "" }); err != nil && !errors.Is(err, state.ErrConflict) {
				return err
			}
		}
		if err := e.tasks.Transit(ctx, tx, id,
			[]task.State{task.StateActive, task.StateDraining, task.StateCompleted, task.StateFailed, task.StateDrained},
			task.StateDeleted, nil); err != nil {
			return err
		}
		fresh, err := e.tasks.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := e.outbox.Append(ctx, tx, eventTaskDeleted, "task", id, taskEventPayloadJSON(fresh, "")); err != nil {
			return err
		}
		return e.audits.Append(ctx, tx, &audit.Entry{
			ID: ulid.Make().String(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "task.delete", Resource: "task/" + id,
		})
	})
}

// StopRun 停止单条 Run（stopped_by_user；Task 补足在下一拍补位）。
func (e *Engine) StopRun(ctx context.Context, id string) (*run.Run, error) {
	m, err := e.runs.Get(ctx, e.db.Runner(), id)
	if err != nil {
		return nil, err
	}
	if m.State.Terminal() {
		return m, nil // 幂等
	}
	if err := e.stopRunRow(ctx, m, run.ReasonStoppedByUser, e.clock.Now()); err != nil {
		return nil, err
	}
	e.taskLoop.Kick()
	return e.runs.Get(ctx, e.db.Runner(), id)
}

// transitRunFour 是 Run 状态迁移的四件一拍（CAS + run.<state> 事件 + 审计；
// 行不删）。
func (e *Engine) transitRunFour(ctx context.Context, m *run.Run, from []run.State, to run.State, mut func(*run.Run)) error {
	return e.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.runs.Transit(ctx, tx, m.ID, from, to, mut); err != nil {
			return err
		}
		fresh, err := e.runs.Get(ctx, tx, m.ID)
		if err != nil {
			return err
		}
		*m = *fresh
		if _, err := e.outbox.Append(ctx, tx, eventRunState(to), "run", m.ID, runEventPayloadJSON(m)); err != nil {
			return err
		}
		return e.audits.Append(ctx, tx, &audit.Entry{
			ID: ulid.Make().String(), Source: audit.SourceSystem,
			Action: "run.transit", Resource: "run/" + m.ID, AfterFP: string(to),
		})
	})
}

// transitTaskFour 是 Task 状态迁移的四件一拍（CAS + task.<state> 事件 + 审计）。
func (e *Engine) transitTaskFour(ctx context.Context, t *task.Task, from []task.State, to task.State, mut func(*task.Task)) error {
	return e.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.tasks.Transit(ctx, tx, t.ID, from, to, mut); err != nil {
			return err
		}
		fresh, err := e.tasks.Get(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		*t = *fresh
		_, err = e.outbox.Append(ctx, tx, eventTaskState(to), "task", t.ID, taskEventPayloadJSON(t, ""))
		return err
	})
}

// resolveTaskMaterials 装配 Task 域 Ensure 材料（镜像 resolveMaterials 的
// 单进程版：镜像凭证 + Secret 注入；ADR-0014 值不落 Spec/日志）。
func (e *Engine) resolveTaskMaterials(ctx context.Context, t *task.Task) (capability.Materials, error) {
	spec, err := loadTaskSpec(t.Spec)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("load task spec: %w", err)
	}
	return e.materialsForProcess(ctx, spec.GetProcess(), t.ProjectID)
}

// taskTeam 是 Task 域归属轴（N0 单团队默认；Team 轴接实随 F1.8，P1-10）。
func taskTeam(t *task.Task) string { return "default" }

// loadTaskSpec 反序列化 Task 行冻结体（protojson blob）。
func loadTaskSpec(blob []byte) (*specv1.TaskSpec, error) {
	spec := &specv1.TaskSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(blob, spec); err != nil {
		return nil, fmt.Errorf("unmarshal task spec: %w", err)
	}
	return spec, nil
}

// workloadSetSignature 计算期望集签名（幂等收敛的跳过判定；逐字节稳定：
// 按 Workload ID 排序，env 键值对排序消除 map 遍历序）。
func workloadSetSignature(ws []capability.Workload) string {
	sorted := append([]capability.Workload(nil), ws...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	type sigEntry struct {
		ID       string            `json:"id"`
		Image    string            `json:"image"`
		Replicas int64             `json:"replicas"`
		Command  []string          `json:"command,omitempty"`
		Env      map[string]string `json:"env"`
	}
	out := make([]sigEntry, len(sorted))
	for i, w := range sorted {
		env := make(map[string]string, len(w.Env))
		for k, v := range w.Env {
			env[k] = v
		}
		out[i] = sigEntry{ID: w.ID, Image: w.Image, Replicas: w.Replicas, Command: w.Command, Env: env}
	}
	b, _ := json.Marshal(out) //nolint:errcheck // 纯标量结构，Marshal 不失败
	return string(b)
}

// outboxAppend 是事件落库的便捷面（返回错误供调用方日志）。
func (e *Engine) outboxAppend(ctx context.Context, name, aggregate, id string, payload []byte) error {
	_, err := e.outbox.Append(ctx, e.db.Runner(), name, aggregate, id, payload)
	return err
}
