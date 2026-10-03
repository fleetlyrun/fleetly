package engine

// Schedule 驱动环（F1.7，ADR-0018）：到期判定按墙钟解析比较（next_fire_at
// 是绝对时刻——控制面重启后按墙钟续算，不依赖进程内计时器）。到期拍从
// 冻结 TaskSpec 模板铸一条 one-shot Task，此后补足/观测/TTL/终态镜像全走
// taskStep 既有链（F1.5 机制复用——Schedule 只拥有"何时拍"，不另立执行
// 机制；CONTEXT.md Run 词条：Schedule 的执行也是 Run）。
//
// 两条显式裁决（ADR-0018 附录 A）：
//
//   - 错过窗口补跑一拍：重启/停机跨过 next_fire_at 后，恢复的第一拍把错过
//     的窗口补跑一次（sched.Next(now) 从当前时刻续算——不按过期拍点追补
//     多次，也不静默跳过；一拍最多一个 Run）；
//   - 重叠 skip（默认）：上一拍铸出的 Task 仍有未终态 Run（pending/
//     running/stopping）时跳过本拍（schedule.skipped 事件携带
//     reason=overlap），next_fire_at 照常推进。策略经
//     FLEETLY_ENGINE_SCHEDULE_OVERLAP_POLICY 可配置（skip|fire——fire
//     照常拍允许并行拍；ADR-0018 A.3 修订 / ADR-0017 附录 A.4）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/schedule"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// Schedule 域哨兵错误（API 层映射 errcode）。
var (
	// ErrScheduleTerminal 是生命周期动词命中终态行（tombstone 事实不可改写）。
	ErrScheduleTerminal = errors.New("engine: schedule is in a terminal state")
	// ErrScheduleOverlapping 是手动触发时上一拍 Run 未终态（先停上一拍或等其收口）。
	ErrScheduleOverlapping = errors.New("engine: schedule's previous run is still in flight")
)

// scheduleStep 是 Schedule 收敛环的单次推进：活跃行逐条按墙钟判到期，
// 到期即拍（fire/skip）。单写者（Loop 串行）；手动触发经 TriggerSchedule
// 与本环共享 Fire/RecordFire CAS 防御。
func (e *Engine) scheduleStep(ctx context.Context) {
	rows, err := e.schedules.ListDriving(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("schedule step: list driving", "err", err)
		return
	}
	now := e.clock.Now()
	for i := range rows {
		s := &rows[i]
		if s.NextFireAt == "" {
			continue
		}
		due := parseDeadline(s.NextFireAt)
		if due == nil || now.Before(*due) {
			continue
		}
		e.fireSchedule(ctx, s, now)
	}
}

// fireSchedule 处理一次到期：重叠判定 → skip（事件 + 推进）或 fire（铸
// Task + Fire CAS + 事件，同事务）。解析失败是创建面校验的不可达防御：
// 记日志不推进（下一拍重试，不静默跳过到期窗口）。
func (e *Engine) fireSchedule(ctx context.Context, s *schedule.Schedule, now time.Time) {
	sched, err := schedule.ParseCron(s.CronExpr, s.Timezone)
	if err != nil {
		e.log.Error("schedule fire: parse cron", "schedule", s.ID, "err", err)
		return
	}
	// 错过窗口补跑一拍：next 从当前时刻续算（含本拍在内不再追补第二次）。
	next := state.FormatTime(sched.Next(now))

	overlapping, err := e.scheduleOverlapping(ctx, s.LastTaskID)
	if err != nil {
		e.log.Error("schedule fire: overlap check", "schedule", s.ID, "err", err)
		return
	}
	if overlapping && e.opts.ScheduleOverlap != ScheduleOverlapFire {
		e.skipSchedule(ctx, s, next, scheduleSkipReasonOverlap)
		return
	}
	if _, err := e.spawnScheduleTask(ctx, s, ScheduleSourceCron, next); err != nil {
		if errors.Is(err, ErrTaskQuota) {
			// 超配额 skip（ADR-0017 附录 A.1）：与重叠 skip 同形——不静默
			// 丢拍、不追补、schedule.skipped 事件可观测，节奏照常推进。
			e.skipSchedule(ctx, s, next, scheduleSkipReasonQuota)
			return
		}
		e.log.Error("schedule fire: spawn task", "schedule", s.ID, "err", err)
	}
}

// skipSchedule 处理一次跳拍：Fire CAS 推进 next_fire_at + schedule.skipped
// 事件（同事务；CAS 失败 = 另一拍先落，静默让位）。
func (e *Engine) skipSchedule(ctx context.Context, s *schedule.Schedule, next, reason string) {
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.schedules.Fire(ctx, tx, s.ID, s.NextFireAt, next, s.LastTaskID); err != nil {
			return err
		}
		skipped := *s
		skipped.NextFireAt = next
		_, err := e.outbox.Append(ctx, tx, eventScheduleSkipped, "schedule", s.ID,
			scheduleEventPayloadJSON(&skipped, "", reason))
		return err
	})
	if err != nil && !errors.Is(err, state.ErrConflict) {
		e.log.Error("schedule fire: skip", "schedule", s.ID, "reason", reason, "err", err)
	}
}

// TriggerSchedule 手动触发（RunNow 语义）：立即铸一拍 Task，next_fire_at
// 不动（cron 节奏不被手动拍打乱）；重叠时诚实拒绝（E_CONFLICT 面——先停
// 上一拍 Run 或等其收口）。
func (e *Engine) TriggerSchedule(ctx context.Context, id string) (*schedule.Schedule, error) {
	s, err := e.schedules.Get(ctx, e.db.Runner(), id)
	if err != nil {
		return nil, err
	}
	if s.State.Terminal() {
		return nil, fmt.Errorf("%w: schedule %s is %s", ErrScheduleTerminal, id, s.State)
	}
	overlapping, err := e.scheduleOverlapping(ctx, s.LastTaskID)
	if err != nil {
		return nil, err
	}
	if overlapping {
		return nil, fmt.Errorf("%w: schedule %s previous task %s still has live runs", ErrScheduleOverlapping, id, s.LastTaskID)
	}
	if _, err := e.spawnScheduleTask(ctx, s, ScheduleSourceManual, s.NextFireAt); err != nil {
		return nil, mapTriggerConflict(err, id)
	}
	e.taskLoop.Kick()
	return e.schedules.Get(ctx, e.db.Runner(), id)
}

// mapTriggerConflict 把铸 Task 事务的 CAS 冲突归一为重叠语义（B12 P3-4：
// 输家识别到赢家已铸——赢家铸的即"上一拍"，对重叠判定的并发窗口补位）。
// 其余错误（配额/存储）原样上抛。
func mapTriggerConflict(err error, id string) error {
	if errors.Is(err, state.ErrConflict) {
		return fmt.Errorf("%w: schedule %s was fired concurrently", ErrScheduleOverlapping, id)
	}
	return err
}

// scheduleOverlapping 判定上一拍 Task 是否仍有未终态 Run（重叠 skip 锚：
// Run 状态是真源——Task 行的终态镜像随 taskStep 有拍延迟，以 Run 为准）。
func (e *Engine) scheduleOverlapping(ctx context.Context, lastTaskID string) (bool, error) {
	if lastTaskID == "" {
		return false, nil
	}
	runs, err := e.runs.ListByTaskStates(ctx, e.db.Runner(), lastTaskID, taskDrivingStates)
	if err != nil {
		return false, err
	}
	return len(runs) > 0, nil
}

// spawnScheduleTask 从冻结模板铸一条 one-shot Task 并同事务记账/发事件：
// 任务行 + task.created + 审计 + Fire CAS（到期拍：next_fire_at 推进）或
// RecordFire（手动拍：只记 last_task_id）+ schedule.fired + 审计。CAS 失败
// 整单回滚（双发防御——Task 行不落孤账）。Name 留空：终态 Task 永久占名
// （tasks 唯一索引含终态行），拍出的 Task 靠 schedule.last_task_id 与事件
// 导航，不占项目名位。
func (e *Engine) spawnScheduleTask(ctx context.Context, s *schedule.Schedule, source, nextFireAt string) (*task.Task, error) {
	tmpl, err := loadTaskSpec(s.Spec)
	if err != nil {
		return nil, fmt.Errorf("load schedule template: %w", err)
	}
	taskID := ulid.Make().String()
	tmpl.Task = &specv1.TaskRef{Id: taskID, Project: s.ProjectID}
	body, err := protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}.Marshal(tmpl)
	if err != nil {
		return nil, fmt.Errorf("marshal fired task spec: %w", err)
	}
	row := &task.Task{
		ID: taskID, ProjectID: s.ProjectID, Name: "",
		Form: task.FormOneShot, State: task.StateActive, Spec: body,
		DesiredConcurrency: 1, NetworkGroup: tmpl.GetNetworkGroup(),
		DNSName: TaskDNSName(taskID),
	}
	fired := *s
	fired.NextFireAt, fired.LastTaskID = nextFireAt, taskID
	err = e.db.Tx(ctx, func(tx *sql.Tx) error {
		// 配额检查（含本拍自占一位；ADR-0017 附录 A.1）：超限返回哨兵——
		// 到期拍调用方转 skip，手动拍调用方诚实拒绝。
		active, sum, err := e.tasks.StatsByProject(ctx, tx, s.ProjectID)
		if err != nil {
			return err
		}
		if active >= MaxTasksPerProject || sum+1 > MaxTaskConcurrencyPerProject {
			return fmt.Errorf("%w: project %s active tasks %d, desired concurrency %d + 1",
				ErrTaskQuota, s.ProjectID, active, sum)
		}
		if err := e.tasks.Create(ctx, tx, row); err != nil {
			return err
		}
		if _, err := e.outbox.Append(ctx, tx, EventTaskCreated, "task", row.ID, TaskCreatedEventJSON(row)); err != nil {
			return err
		}
		if err := e.audits.Append(ctx, tx, &audit.Entry{
			ID: ulid.Make().String(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "task.create", Resource: "task/" + row.ID, AfterFP: row.Form,
		}); err != nil {
			return err
		}
		if source == ScheduleSourceCron {
			if err := e.schedules.Fire(ctx, tx, s.ID, s.NextFireAt, nextFireAt, taskID); err != nil {
				return err
			}
		} else {
			// 手动拍的 CAS 锚（B12 P3-4）：fromLastTaskID = 触发时读行的旧
			// 值——并发双拍输家的锚失配 → ErrConflict 整单回滚（Task 行不
			// 落孤账），至多一铸。
			if err := e.schedules.RecordFire(ctx, tx, s.ID, s.LastTaskID, taskID); err != nil {
				return err
			}
		}
		if _, err := e.outbox.Append(ctx, tx, eventScheduleFired, "schedule", s.ID,
			scheduleEventPayloadJSON(&fired, source, "")); err != nil {
			return err
		}
		return e.audits.Append(ctx, tx, &audit.Entry{
			ID: ulid.Make().String(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "schedule.fire", Resource: "schedule/" + s.ID, AfterFP: taskID,
		})
	})
	if err != nil {
		return nil, err
	}
	e.taskLoop.Kick() // 新 Task 即刻驱动（补足 Run 不等节拍）
	return row, nil
}
