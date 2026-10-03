// Package schedule 是 Schedule 聚合 repo（CONTEXT.md Schedule 词条：周期
// 触发规则，按时生成 Run；Avoid: cron 作实体名/timer）。本包同时拥有带
// 时区 cron 的解释面（ParseCron，ADR-0018）：IANA 时区名随行持久化，
// 跨夏令时由 cron 库按墙钟解释；next_fire_at 是绝对时刻（控制面重启后
// 按墙钟续算，不依赖进程内计时器）。到期拍由 engine 驱动环判定（对比在
// Go 侧解析后进行——墙钟口径与 runs.deadline 一致，不做 SQL 字符串比较）。
package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"
	// 嵌入 IANA 时区库（~450KB）：时区解释是控制面正确性面——容器形态
	//（无系统 zoneinfo）与裸 Windows 测试环境都不得随部署环境漂移。
	_ "time/tzdata"

	"github.com/robfig/cron/v3"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// State 是 Schedule 状态机值（无中间态：活跃行到期即触发；删除是
// tombstone——在途的已铸 Task 不受影响，跑完自然收口）。
type State string

const (
	StateActive  State = "active"
	StateDeleted State = "deleted" // 终态 tombstone
)

// Terminal 报告是否终态。
func (s State) Terminal() bool { return s == StateDeleted }

// Schedule 是聚合行。
type Schedule struct {
	ID         string
	ProjectID  string
	Name       string
	State      State
	CronExpr   string // 5 字段表达式（minute hour dom month dow）
	Timezone   string // IANA 时区名（空按 UTC 解释；创建面归一为显式值）
	Spec       []byte // 冻结 TaskSpec 模板（protojson；task ref 由 fire 时铸造）
	NextFireAt string // RFC3339 绝对时刻（空 = 不触发）
	LastTaskID string // 最近一拍铸出的 Task（重叠 skip 判定锚）
	CreatedAt  string
	UpdatedAt  string
}

// Repo 是 Schedule 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行（活跃名唯一索引防项目内重名 → ErrAlreadyExists）。
func (r *Repo) Create(ctx context.Context, run state.Runner, s *Schedule) error {
	now := state.FormatTime(r.clock.Now())
	s.CreatedAt, s.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO schedules
			(id, project_id, name, state, cron_expr, timezone, spec,
			 next_fire_at, last_task_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.ProjectID, s.Name, string(s.State), s.CronExpr, s.Timezone, s.Spec,
		s.NextFireAt, s.LastTaskID, s.CreatedAt, s.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: an active schedule named %q already exists in project %s", state.ErrAlreadyExists, s.Name, s.ProjectID)
	}
	return err
}

// Get 按 ID 直读（tombstone 行照常返回——终态事实可见）。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Schedule, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanSchedule(row.Scan)
}

// GetByName 按项目内名直读（活跃名唯一；无 → ErrNotFound）。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*Schedule, error) {
	row := run.QueryRowContext(ctx,
		selectCols+" WHERE project_id = ? AND name = ? AND state != 'deleted'", projectID, name)
	return scanSchedule(row.Scan)
}

// ListByProject 返回项目内 Schedule（新→旧 + after 游标；ADR-0026 after_* +
// limit 惯例——游标 = ULID 创建序）。
func (r *Repo) ListByProject(ctx context.Context, run state.Runner, projectID, afterID string, limit int) ([]Schedule, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := selectCols + " WHERE project_id = ?"
	args := []any{projectID}
	if afterID != "" {
		q += " AND id < ?"
		args = append(args, afterID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// ListDriving 返回全部活跃行（到期判定由调用方按墙钟解析比较）。
func (r *Repo) ListDriving(ctx context.Context, run state.Runner) ([]Schedule, error) {
	return r.query(ctx, run, selectCols+" WHERE state = 'active' ORDER BY id")
}

// Fire 是到期拍/跳过后推进 next_fire_at 的 CAS：仅当行活跃且 next_fire_at
// 仍为 from 时落 next 与 last_task_id。前置不符 → ErrConflict（并发已拍/
// 已删——与 Task 落行同事务时整体回滚，双发防御纵深）；行不存在 →
// ErrNotFound。
func (r *Repo) Fire(ctx context.Context, run state.Runner, id, from, next, lastTaskID string) error {
	res, err := run.ExecContext(ctx, `
		UPDATE schedules SET next_fire_at = ?, last_task_id = ?, updated_at = ?
		WHERE id = ? AND state = 'active' AND next_fire_at = ?`,
		next, lastTaskID, state.FormatTime(r.clock.Now()), id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := r.Get(ctx, run, id); gerr != nil {
			return gerr
		}
		return fmt.Errorf("%w: schedule %s next_fire_at changed concurrently", state.ErrConflict, id)
	}
	return nil
}

// RecordFire 是手动触发的记账面：last_task_id 推进、next_fire_at 不动
// （cron 节奏不被手动拍打乱）。fromLastTaskID 是 CAS 锚（调用方读行时的
// 旧值，B12 P3-4）：并发双拍时输家的锚失配 → ErrConflict——与 Task 落行
// 同事务整体回滚，双铸防御。行不活跃 → ErrConflict；不存在 → ErrNotFound。
func (r *Repo) RecordFire(ctx context.Context, run state.Runner, id, fromLastTaskID, lastTaskID string) error {
	res, err := run.ExecContext(ctx, `
		UPDATE schedules SET last_task_id = ?, updated_at = ?
		WHERE id = ? AND state = 'active' AND last_task_id = ?`,
		lastTaskID, state.FormatTime(r.clock.Now()), id, fromLastTaskID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, gerr := r.Get(ctx, run, id)
		if gerr != nil {
			return gerr
		}
		if cur.State != StateActive {
			return fmt.Errorf("%w: schedule %s is not active", state.ErrConflict, id)
		}
		return fmt.Errorf("%w: schedule %s last_task_id changed concurrently", state.ErrConflict, id)
	}
	return nil
}

// Transit 是状态 CAS（active → deleted 的 tombstone 面；单前置态——
// Schedule 无中间态迁移）。
func (r *Repo) Transit(ctx context.Context, run state.Runner, id string, from, to State) error {
	res, err := run.ExecContext(ctx, `
		UPDATE schedules SET state = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		string(to), state.FormatTime(r.clock.Now()), id, string(from))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := r.Get(ctx, run, id); gerr != nil {
			return gerr
		}
		return fmt.Errorf("%w: schedule %s is not %s", state.ErrConflict, id, from)
	}
	return nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, project_id, name, state, cron_expr, timezone, spec,
	       next_fire_at, last_task_id, created_at, updated_at
	FROM schedules`

func (r *Repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]Schedule, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Schedule
	for rows.Next() {
		s, err := scanSchedule(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func scanSchedule(scan func(dest ...any) error) (*Schedule, error) {
	var s Schedule
	var stateStr string
	err := scan(&s.ID, &s.ProjectID, &s.Name, &stateStr, &s.CronExpr, &s.Timezone, &s.Spec,
		&s.NextFireAt, &s.LastTaskID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	s.State = State(stateStr)
	return &s, nil
}

// ---- 带时区 cron 解释面（ADR-0018） ----

// cronParser 是 5 字段解析器（不支持 @descriptor——区间语义非墙钟，与其
// 让两种时间语义并存不如显式拒绝；CRON_TZ 前缀由 Parser.Parse 原生处理）。
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ParseCron 解析带时区的 5 字段 cron 表达式（timezone 是 IANA 时区名，空
// 按 UTC）。跨夏令时由 robfig/cron 按墙钟解释（SpecSchedule.Next 在目标
// 时区做墙钟算术——春跳不存在的时刻按 time.Date 归一，秋跳重叠时刻取
// 前一义）。表达式与库选型结论见 ADR-0018 附录 A。
func ParseCron(expr, timezone string) (cron.Schedule, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, fmt.Errorf("cron expression must not be empty")
	}
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, fmt.Errorf("timezone %q is not a valid IANA name: %v", timezone, err)
	}
	sched, err := cronParser.Parse("CRON_TZ=" + timezone + " " + strings.TrimSpace(expr))
	if err != nil {
		return nil, fmt.Errorf("invalid 5-field cron expression (minute hour day-of-month month day-of-week): %v", err)
	}
	return sched, nil
}
