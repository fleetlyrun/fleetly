// Package run 是 Run 聚合 repo 的 SQLite 实现：行类型、状态机词表与 Repo
// 接口住 domain/automation/run（ADR-0075 Repo 接口层），本包只持不导出
// 实现与构造。
package run

import (
	"context"
	"fmt"

	domainrun "github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain run.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain run.Repo）。
func New(clock state.Clock) domainrun.Repo { return &repo{clock: clock} }

// Create 落一行。
func (r *repo) Create(ctx context.Context, run state.Runner, m *domainrun.Run) error {
	seq, err := persistence.NextSeq(ctx, run, "runs")
	if err != nil {
		return err
	}
	m.Seq = seq
	if m.ID == "" {
		m.ID = entityid.Derive(m.TaskID, "r", seq)
	}
	now := state.FormatTime(r.clock.Now())
	m.CreatedAt, m.UpdatedAt = now, now
	_, err = run.ExecContext(ctx, `
		INSERT INTO runs (id, seq, task_id, project_id, state, stop_reason, exit_code,
			 workload_id, dns_name, deadline, created_at, updated_at,
			 started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, '', '')`,
		m.ID, m.Seq, m.TaskID, m.ProjectID, string(m.State), m.StopReason,
		m.WorkloadID, m.DNSName, m.Deadline, m.CreatedAt, m.UpdatedAt)
	return err
}

// Get 按 ID 直读。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*domainrun.Run, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanRun(row.Scan)
}

// ListByTask 返回 Task 名下 Run（新→旧 + after 游标；ADR-0026 惯例）。
func (r *repo) ListByTask(ctx context.Context, run state.Runner, taskID string, afterSeq int64, limit int) ([]domainrun.Run, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectCols + " WHERE task_id = ?"
	args := []any{taskID}
	if afterSeq > 0 {
		q += " AND seq < ?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// ListByProject 返回 Project 内 Run（新→旧 + after 游标；ADR-0035 ListRuns
// 的 project 过滤面——runs 行自带冗余 project_id，免二跳）。
func (r *repo) ListByProject(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]domainrun.Run, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectCols + " WHERE project_id = ?"
	args := []any{projectID}
	if afterSeq > 0 {
		q += " AND seq < ?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// ListDriving 返回全部待驱动行（pending/running/stopping）。
func (r *repo) ListDriving(ctx context.Context, run state.Runner) ([]domainrun.Run, error) {
	return r.query(ctx, run,
		selectCols+" WHERE state IN ('pending', 'running', 'stopping') ORDER BY seq")
}

// ListDrivingOfTerminalTasks 返回终态 Task 名下的 driving 态 Run（僵尸
// 收口面：正常链路不产生，pre-fix 存量行在此显形——Schedule 重叠判定以
// Run 行为真源，僵尸行会让该 Schedule 永久 skip）。终态词表单源引用
// task.TerminalStateStrings（deployment.ActiveStateStrings 同款先例——
// 词表变更编译期咬合，不再手抄腐化）。
func (r *repo) ListDrivingOfTerminalTasks(ctx context.Context, run state.Runner) ([]domainrun.Run, error) {
	states := task.TerminalStateStrings()
	args := make([]any, len(states))
	for i, s := range states {
		args[i] = s
	}
	return r.query(ctx, run,
		selectCols+" WHERE state IN ('pending', 'running', 'stopping')"+
			" AND task_id IN (SELECT id FROM tasks WHERE state IN ("+persistence.Placeholders(len(states))+"))"+
			" ORDER BY seq", args...)
}

// ListByTaskStates 返回 Task 名下指定状态的 Run（新→旧，与 ListByTask
// 同一惯例；顺序是过量排空的方向锚——升序会把"停新保老"倒置成停老保新，
// 2026-10-02 实证缺陷）。
func (r *repo) ListByTaskStates(ctx context.Context, run state.Runner, taskID string, states []domainrun.State) ([]domainrun.Run, error) {
	if len(states) == 0 {
		return nil, nil
	}
	strs := make([]string, len(states))
	args := make([]any, 0, len(states)+1)
	args = append(args, taskID)
	for i, s := range states {
		strs[i] = string(s)
		args = append(args, string(s))
	}
	return r.query(ctx, run,
		selectCols+" WHERE task_id = ? AND state IN ("+persistence.Placeholders(len(states))+") ORDER BY seq DESC", args...)
}

// CountActiveByTasks 返回各 Task 的活 Run 计数（pending/running；列表投影
// 的 active_run_count 单查询面——避免 N+1）。
func (r *repo) CountActiveByTasks(ctx context.Context, run state.Runner) (map[string]int, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT task_id, COUNT(*) FROM runs
		WHERE state IN ('pending', 'running') GROUP BY task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读聚合，关闭错误无处置面
	out := map[string]int{}
	for rows.Next() {
		var taskID string
		var n int
		if err := rows.Scan(&taskID, &n); err != nil {
			return nil, err
		}
		out[taskID] = n
	}
	return out, rows.Err()
}

// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
// stop_reason/exit_code/deadline 等字段）。前置不符 → ErrConflict；行不
// 存在 → ErrNotFound。UPDATE 带 state 前置条件作并发防御纵深。
func (r *repo) Transit(ctx context.Context, run state.Runner, id string, from []domainrun.State, to domainrun.State, mut func(*domainrun.Run)) error {
	cur, err := r.Get(ctx, run, id)
	if err != nil {
		return err
	}
	if !persistence.StateIn(from, cur.State) {
		return fmt.Errorf("%w: run %s is %s, want one of %s", state.ErrConflict, id, cur.State, persistence.JoinStates(from))
	}
	origState := cur.State
	if mut != nil {
		mut(cur)
	}
	cur.State = to
	cur.UpdatedAt = state.FormatTime(r.clock.Now())
	if to.Terminal() && cur.FinishedAt == "" {
		cur.FinishedAt = cur.UpdatedAt
	}
	if to == domainrun.StateRunning && cur.StartedAt == "" {
		cur.StartedAt = cur.UpdatedAt
	}
	res, err := run.ExecContext(ctx, `
		UPDATE runs SET
			state = ?, stop_reason = ?, exit_code = ?, deadline = ?,
			updated_at = ?, started_at = ?, finished_at = ?
		`+persistence.TransitWhere,
		string(cur.State), cur.StopReason, cur.ExitCode, cur.Deadline,
		cur.UpdatedAt, cur.StartedAt, cur.FinishedAt,
		id, string(origState))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: run %s state changed concurrently, want one of %s",
			state.ErrConflict, id, persistence.JoinStates(from))
	}
	return nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, seq, task_id, project_id, state, stop_reason, exit_code,
	       workload_id, dns_name, deadline, created_at, updated_at,
	       started_at, finished_at
	FROM runs`

func (r *repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]domainrun.Run, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []domainrun.Run
	for rows.Next() {
		m, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func scanRun(scan func(dest ...any) error) (*domainrun.Run, error) {
	var m domainrun.Run
	var stateStr string
	var exitCode any
	err := scan(&m.ID, &m.Seq, &m.TaskID, &m.ProjectID, &stateStr, &m.StopReason, &exitCode,
		&m.WorkloadID, &m.DNSName, &m.Deadline,
		&m.CreatedAt, &m.UpdatedAt, &m.StartedAt, &m.FinishedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	m.State = domainrun.State(stateStr)
	if exitCode != nil {
		if code, ok := exitCode.(int64); ok {
			c := int(code)
			m.ExitCode = &c
		}
	}
	return &m, nil
}
