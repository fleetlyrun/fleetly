package engine

// Task/Run 生命周期动词面（API 受理消费；配额定律 ADR-0017 附录 A.1/
// ADR-0068 单源）。

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Task 域哨兵错误（API 层映射 errcode）。
var (
	// ErrTaskNotRenewable 是 RenewTask 目标形态不可续期（one-shot 无租约）。
	ErrTaskNotRenewable = errors.New("engine: task carries no owner lease")
	// ErrTaskTerminal 是生命周期动词命中终态行（终态事实不可改写）。
	ErrTaskTerminal = errors.New("engine: task is in a terminal state")
	// ErrTaskDraining 是缩容动词命中排空中行（B12 P3-2：排空中期望并发
	// 无意义——先复活或等排空收口；裸 ErrConflict 的"并发改动"文案误导）。
	ErrTaskDraining = errors.New("engine: task is draining")
	// ErrNotResident 是仅 resident 形态适用的动词面（ScaleTask）。
	ErrNotResident = errors.New("engine: this verb targets resident tasks only")
	// ErrTaskQuota 是 per-Project Task 配额命中（ADR-0017 附录 A.1；API 层
	// 映射 E_QUOTA_EXCEEDED）。
	ErrTaskQuota = errors.New("engine: project task quota exceeded")
)

// Task 配额缺省（ADR-0017 附录 A.1，F1.9）：单一数值源——受理位
// （CreateTask）、ScaleTask 增量检查与 Schedule 拍共用；配置面接入
// AppConfig 后可覆盖。
const (
	// MaxTasksPerProject 是 per-Project 非终态 Task 行数上限。
	MaxTasksPerProject = 100
	// MaxTaskConcurrencyPerProject 是 per-Project 非终态 Task 的
	// desired_concurrency 之和上限（期望 Run 总量 = Workload 数量真源口径）。
	MaxTaskConcurrencyPerProject = 200
)

// taskSpawnQuota 是铸 Task 的配额受理检查（ADR-0017 附录 A.1；含本单自
// CheckTaskQuota 是项目 Task 配额的唯一定律（ADR-0017 附录 A.1；ADR-0068
// 收口——此前 spawn/scale/API 受理位三处判定两条文案各自演化）。双上限：
// spawn 模态计活跃行数（铸造占新行位），scale 模态只计并发净增量（原地
// 改 desired 不占新行位）。active/sum 由调用方事务内 StatsByProject 读
// （SQLite 单写串行，无 TOCTOU）；命中返回 ErrTaskQuota 哨兵——API 层
// mapTaskQuotaError 统一转义，engine 域调用方转各自语义（predeploy 有界
// 等待 / 到期拍 skip / 手动拍诚实拒绝）。
func CheckTaskQuota(projectID string, active int, sum, delta int64, spawn bool) error {
	if spawn && active >= MaxTasksPerProject {
		return fmt.Errorf("%w: project %s holds %d active tasks (limit %d); delete or drain tasks first",
			ErrTaskQuota, projectID, active, MaxTasksPerProject)
	}
	if sum+delta > MaxTaskConcurrencyPerProject {
		return fmt.Errorf("%w: project %s desired concurrency %d + %d would exceed the limit %d",
			ErrTaskQuota, projectID, sum, delta, MaxTaskConcurrencyPerProject)
	}
	return nil
}

// taskSpawnQuota 是铸造位的配额受理检查（含本单自占 desired 位）：命中
// 返回哨兵——调用方转有界等待（predeploy）/skip（到期拍）/诚实拒绝（手动
// 拍）。此前在 predeploy 与 schedule 各持一份逐字拷贝，收口为 commitWrite
// 的 checks 段。
func (e *Engine) taskSpawnQuota(ctx context.Context, s transact.Stores, projectID string, desired int64) error {
	active, sum, err := s.Tasks.StatsByProject(ctx, projectID)
	if err != nil {
		return err
	}
	return CheckTaskQuota(projectID, active, sum, desired, true)
}

// RenewTask 续期 Owner Lease（F1.6）：deadline 推进 TaskLeaseInterval；
// 排空态（draining/drained）可复活——续期即属主在场的显式声明，补足随
// active 恢复（"排空并补足"的补足半边）。
func (e *Engine) RenewTask(ctx context.Context, id string) (*task.Task, error) {
	var out *task.Task
	err := e.tx.Do(ctx, func(s transact.Stores) error {
		t, err := s.Tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		if t.Form != task.FormResident {
			return fmt.Errorf("%w: task %s is %s (one-shot tasks carry no lease)", ErrTaskNotRenewable, id, t.Form)
		}
		if t.State.Terminal() && t.State != task.StateDrained {
			return fmt.Errorf("%w: task %s is %s", ErrTaskTerminal, id, t.State)
		}
		deadline := state.FormatTime(e.clock.Now().Add(e.opts.Task.TaskLeaseInterval))
		if t.State != task.StateActive {
			// 复活：draining/drained → active（四件一走唯一序列真源——
			// task.active 事件与 task.renew 用户动词审计随行）。
			if err := e.transitTaskTx(ctx, s, t,
				[]task.State{task.StateDraining, task.StateDrained}, task.StateActive, nil,
				taskTransitOpts{auditAction: "task.renew", auditActorCtx: true}); err != nil {
				return err
			}
		}
		var fresh *task.Task
		err = e.commitWrite(ctx, s, writeFact{
			// 续 lease 是字段更新而非状态迁移：无审计（resident 池持续续期，
			// 逐次留痕是噪音面），lease.renewed 事实事件随行。
			write: func(ctx context.Context, s transact.Stores) error {
				if err := s.Tasks.UpdateLease(ctx, id, deadline); err != nil {
					return err
				}
				var err error
				fresh, err = s.Tasks.Get(ctx, id)
				return err
			},
			facts: []func() writes.FactRow{func() writes.FactRow {
				// 双真偏离（仅流面）：续 lease 无取证孪生——resident 池持续
				// 续期，逐次留痕是噪音面（A 化前亦无审计行，行集合零漂移）。
				return writes.FactRow{
					Name: eventLeaseRenewed, Aggregate: "task", ID: id,
					Payload: leaseEventPayloadJSON(fresh, ""), TeamID: e.eventTeamOfProject(ctx, s, t.ProjectID),
					StreamOnly: true,
				}
			}},
		})
		if err != nil {
			return err
		}
		out = fresh
		return nil
	})
	if err != nil {
		return nil, err
	}
	e.taskLoop.Kick()
	return out, nil
}

// StopTask 排空停止（F1.5）：停止补足；force=false 存量 Run 自然收口
// （完成/TTL），force=true 宽限停止（StopGrace 路径）。draining 收口后
// Task → drained。全程持有 Task 级互斥（DeleteTask 先例）：与持锁的
// driveTask 串行——否则强停列 Run 与补足铸 Run 竞态，新铸 Run 漏停（由
// 排空补停兜底收敛，但锁纪律下窗口本身不存在）。
func (e *Engine) StopTask(ctx context.Context, id string, force bool) (*task.Task, error) {
	mu := e.lockTask(id)
	mu.Lock()
	defer mu.Unlock()

	var out *task.Task
	err := e.tx.Do(ctx, func(s transact.Stores) error {
		t, err := s.Tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		if t.State.Terminal() {
			return fmt.Errorf("%w: task %s is %s", ErrTaskTerminal, id, t.State)
		}
		if t.State == task.StateActive {
			// 排空迁移四件一：task.draining 事件（起因负载）+ task.stop
			// 用户动词审计。已 draining 时无迁移不发事件（事件是状态迁移
			// 的既成事实——此前该路径无迁移也重复发 draining 事件）。
			if err := e.transitTaskTx(ctx, s, t,
				[]task.State{task.StateActive}, task.StateDraining, nil,
				taskTransitOpts{reason: run.ReasonStoppedByUser, auditAction: "task.stop", auditActorCtx: true}); err != nil {
				return err
			}
		}
		fresh, err := s.Tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		out = fresh
		return nil
	})
	if err != nil {
		return nil, err
	}
	if force {
		now := e.clock.Now()
		runs, rerr := e.repos.Runs.ListByTaskStates(ctx, id, run.ActiveStates)
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
	err := e.tx.Do(ctx, func(s transact.Stores) error {
		t, err := s.Tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		if t.Form != task.FormResident {
			return fmt.Errorf("%w: scaling targets resident tasks (task %s is %s)", ErrNotResident, id, t.Form)
		}
		if t.State.Terminal() {
			return fmt.Errorf("%w: task %s is %s", ErrTaskTerminal, id, t.State)
		}
		if t.State == task.StateDraining {
			// 排空中缩容无意义（B12 P3-2）：期望并发被排空忽略，且无补足
			// 半边——诚实拒绝并指路（复活/等收口），不落裸 CAS 冲突文案。
			return fmt.Errorf("%w: task %s is draining; scaling has no effect while the pool is draining",
				ErrTaskDraining, id)
		}
		// 配额增量检查（ADR-0017 附录 A.1）：本单对项目并发总量的净增量 =
		// 新值 − 现值；事务内读（SQLite 单写连接串行，无 TOCTOU）。scale
		// 模态不占新行位——CheckTaskQuota 的 spawn=false 腿（ADR-0068）。
		if delta := desired - t.DesiredConcurrency; delta > 0 {
			_, sum, err := s.Tasks.StatsByProject(ctx, t.ProjectID)
			if err != nil {
				return err
			}
			if err := CheckTaskQuota(t.ProjectID, 0, sum, delta, false); err != nil {
				return err
			}
		}
		// 原地迁移（active→active 仅改 desired）：无状态事件（抑制规则），
		// task.updated 是"变化"事件照发；task.update 用户动词审计随行。
		if err := e.transitTaskTx(ctx, s, t,
			[]task.State{task.StateActive}, task.StateActive,
			func(m *task.Task) { m.DesiredConcurrency = desired },
			taskTransitOpts{
				updateEvent:   eventTaskUpdated,
				auditAction:   "task.update",
				auditAfterFP:  fmt.Sprintf("%s; desired_concurrency=%d", task.StateActive, desired),
				auditActorCtx: true,
			}); err != nil {
			return err
		}
		out = t
		return nil
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

	t, err := e.repos.Tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	if t.State == task.StateDeleted {
		return nil // 幂等
	}
	team, err := e.taskTeam(ctx, t)
	if err != nil {
		return err
	}
	ns := capability.NamespaceRef{Team: team, Project: t.ProjectID, Task: t.ID}
	// 维护互斥读半边（R13-L1 同根因：载体写动词必须持读锁，与 Ensure 族
	// 及网络重建写半边串行化）。锁等待不占步预算（排队语义：读锁在带界
	// ctx 派生之前获取）。
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	// Remove 带界（B15-1，批 3 判定的反转）：API 请求路径的 Remove 挂死会
	// 卡住 API 调用本身（与收敛环卡死同害），且 lockTask 被握死堵住驱动环
	// 同 Task 全部驱动。带 ManagedStepTimeout 硬上限，超时如实上抛。
	rctx, rcancel := e.boundedStep(ctx)
	removeErr := e.runtime.Remove(rctx, ns)
	rcancel()
	if removeErr != nil {
		return fmt.Errorf("runtime remove: %w", removeErr)
	}
	// 缓存收口（P1-7 分家面：Task 域缓存组按 Task/Run 键清理）。
	e.task.drainObs(id)
	e.ensureForget(e.task.ensure, id)

	return e.tx.Do(ctx, func(s transact.Stores) error {
		driving, err := s.Runs.ListDriving(ctx)
		if err != nil {
			return err
		}
		for i := range driving {
			if driving[i].TaskID != id {
				continue
			}
			// 存量 Run 终态化走四件一（run.stopped 事件+审计随行——事件是
			// 状态迁移的既成事实；此前此处静默 CAS 是五拷贝时代的漂移）。
			if err := e.transitRunTx(ctx, s, &driving[i],
				taskDrivingStates, run.StateStopped,
				func(r *run.Run) { r.StopReason = run.ReasonStoppedByUser; r.Deadline = "" }); err != nil && !errors.Is(err, state.ErrConflict) {
				return err
			}
		}
		// tombstone 迁移四件一：task.deleted 事件 + task.delete 用户动词
		// 审计（AfterFP=deleted——此前无指纹，随收口补上）。
		return e.transitTaskTx(ctx, s, t,
			[]task.State{task.StateActive, task.StateDraining, task.StateCompleted, task.StateFailed, task.StateDrained},
			task.StateDeleted, nil,
			taskTransitOpts{auditAction: "task.delete", auditActorCtx: true})
	})
}

// StopRun 停止单条 Run（stopped_by_user；Task 补足在下一拍补位）。
func (e *Engine) StopRun(ctx context.Context, id string) (*run.Run, error) {
	m, err := e.repos.Runs.Get(ctx, id)
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
	return e.repos.Runs.Get(ctx, id)
}

// transitRunFour（Run 四件一）与 transitTaskFour（Task 四件一）均已收口
// 至 transition.go；用户动词（renew/stop/scale/delete）经 transitTaskTx
// 携带起因负载与请求路径审计。

// resolveTaskMaterials 装配 Task 域 Ensure 材料（镜像 resolveMaterials 的
// 单进程版：镜像凭证 + Secret 注入；ADR-0014 值不落 Spec/日志）。
func (e *Engine) resolveTaskMaterials(ctx context.Context, t *task.Task) (capability.Materials, error) {
	spec, err := loadTaskSpec(t.Spec)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("load task spec: %w", err)
	}
	return e.materialsForProcess(ctx, spec.GetProcess(), t.ProjectID)
}

// taskTeam 是 Task 域归属轴（ADR-0028 接实：从 Project 行实取 team_id）。
func (e *Engine) taskTeam(ctx context.Context, t *task.Task) (string, error) {
	return e.projectTeam(ctx, t.ProjectID)
}

// loadTaskSpec 反序列化 Task 行冻结体（protojson blob）。
func loadTaskSpec(blob []byte) (*specv1.TaskSpec, error) {
	spec := &specv1.TaskSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(blob, spec); err != nil {
		return nil, fmt.Errorf("unmarshal task spec: %w", err)
	}
	return spec, nil
}
