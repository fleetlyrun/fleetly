// Package task 是 Task 聚合 repo 的 SQLite 实现：行类型、状态机词表与
// Repo 接口住 domain/automation/task（ADR-0075 Repo 接口层），本包只持
// 不导出实现与构造。
package task

import (
	"context"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain task.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain task.Repo）。
func New(clock state.Clock) task.Repo { return &repo{clock: clock} }

// Create 落一行（活跃名唯一索引防项目内重名 → ErrAlreadyExists）。
func (r *repo) Create(ctx context.Context, run state.Runner, t *task.Task) error {
	now := state.FormatTime(r.clock.Now())
	t.CreatedAt, t.UpdatedAt = now, now
	// minttask 在 TaskSpec 冻结期预铸 seq/id（ADR-0070：Task Mint 词条——
	// 身份注入先于冻结）；此处零值兜底补铸，预置则原样落行。
	if t.Seq == 0 {
		seq, err := state.NextSeq(ctx, run, "tasks")
		if err != nil {
			return err
		}
		t.Seq = seq
	}
	if t.ID == "" {
		t.ID = entityid.Join(t.Name, t.Seq)
	}
	_, err := run.ExecContext(ctx, `
		INSERT INTO tasks
			(id, seq, project_id, name, form, state, spec, owner_token_id,
			 desired_concurrency, network_group, dns_name, lease_deadline,
			 created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		t.ID, t.Seq, t.ProjectID, t.Name, t.Form, string(t.State), t.Spec, t.OwnerTokenID,
		t.DesiredConcurrency, t.NetworkGroup, t.DNSName, t.LeaseDeadline,
		t.CreatedAt, t.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: an active task named %q already exists in project %s", state.ErrAlreadyExists, t.Name, t.ProjectID)
	}
	return err
}

// Get 按 ID 直读（tombstone 行照常返回——终态事实可见）。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*task.Task, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanTask(row.Scan)
}

// GetByName 按项目内名直读（活跃名唯一；无 → ErrNotFound）。
func (r *repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*task.Task, error) {
	row := run.QueryRowContext(ctx,
		selectCols+" WHERE project_id = ? AND name = ? AND state != 'deleted'", projectID, name)
	return scanTask(row.Scan)
}

// ListByProject 返回项目内 Task（新→旧 + after 游标；ADR-0026 after_* + limit
// 惯例——游标 = seq 创建序）。
func (r *repo) ListByProject(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]task.Task, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
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

// ListDriving 返回全部待驱动行（active/draining；tombstone 与终态不拾取）。
func (r *repo) ListDriving(ctx context.Context, run state.Runner) ([]task.Task, error) {
	return r.query(ctx, run,
		selectCols+" WHERE state IN ('active', 'draining') ORDER BY seq")
}

// ListRecentlyFinished 返回收口时刻在窗内的终态 Task（收尾批 E29-2 残留
// 载体清扫的候选集）。deleted 不在列——其载体拆除是 DeleteTask 自身的
// Remove 步（先收口后落账），tombstone 无残留面（词引用类型常量单源，
// 状态词变更不腐化）。finished_at 是 RFC3339
// UTC 串（state.FormatTime），字典序即时序。limit 钳制与 ListByProject
// 同口径（<=0 或超上限回落/钳缺省）。新→旧序（finished_at DESC）：近期
// 收口的行才是残留的实际所在（崩溃/失败窗口），旧行近乎必然已收敛。
func (r *repo) ListRecentlyFinished(ctx context.Context, run state.Runner, since string, limit int) ([]task.Task, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	states := []string{string(task.StateCompleted), string(task.StateFailed), string(task.StateDrained)}
	return r.query(ctx, run,
		selectCols+" WHERE state IN ("+placeholders(len(states))+")"+
			" AND finished_at != '' AND finished_at >= ?"+
			" ORDER BY finished_at DESC LIMIT ?", append(toAny(states), since, limit)...)
}

// ListByOwner 返回属主 Token 名下活跃行（吊销排空的拉式扫描面，P1-8）。
func (r *repo) ListByOwner(ctx context.Context, run state.Runner, tokenID string) ([]task.Task, error) {
	return r.query(ctx, run,
		selectCols+" WHERE owner_token_id = ? AND state IN ('active', 'draining') ORDER BY seq", tokenID)
}

// StatsByProject 返回 Project 内非终态 Task 的配额口径统计（ADR-0017
// 附录 A.1）：活跃行数与 desired_concurrency 之和（受理位与引擎配额检查
// 共用，事务内读）。非终态 = 终态词表取反（TerminalStateStrings 单源）。
func (r *repo) StatsByProject(ctx context.Context, run state.Runner, projectID string) (active int, desiredSum int64, err error) {
	states := task.TerminalStateStrings()
	row := run.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(desired_concurrency), 0)
		FROM tasks
		WHERE project_id = ? AND state NOT IN (`+placeholders(len(states))+`)`,
		append([]any{projectID}, toAny(states)...)...)
	if err := row.Scan(&active, &desiredSum); err != nil {
		return 0, 0, state.MapScanErr(err)
	}
	return active, desiredSum, nil
}

// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
// lease_deadline/desired_concurrency 等字段）。前置不符 → ErrConflict；
// 行不存在 → ErrNotFound。UPDATE 带 state 前置条件作并发防御纵深。
func (r *repo) Transit(ctx context.Context, run state.Runner, id string, from []task.State, to task.State, mut func(*task.Task)) error {
	cur, err := r.Get(ctx, run, id)
	if err != nil {
		return err
	}
	if !stateIn(from, cur.State) {
		return fmt.Errorf("%w: task %s is %s, want one of %s", state.ErrConflict, id, cur.State, joinStates(from))
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
	res, err := run.ExecContext(ctx, `
		UPDATE tasks SET
			state = ?, lease_deadline = ?, desired_concurrency = ?,
			updated_at = ?, finished_at = ?
		WHERE id = ? AND state = ?`,
		string(cur.State), cur.LeaseDeadline, cur.DesiredConcurrency,
		cur.UpdatedAt, cur.FinishedAt,
		id, string(origState))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: task %s state changed concurrently, want one of %s",
			state.ErrConflict, id, joinStates(from))
	}
	return nil
}

// UpdateLease 更新租约 deadline（原地：state 不变，ADR-0024 原地迁移不发
// 事件的同款口径——lease 续期由 lease.renewed 事件显式承载）。
func (r *repo) UpdateLease(ctx context.Context, run state.Runner, id, deadline string) error {
	res, err := run.ExecContext(ctx, `
		UPDATE tasks SET lease_deadline = ?, updated_at = ? WHERE id = ?`,
		deadline, state.FormatTime(r.clock.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return state.ErrNotFound
	}
	return nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, seq, project_id, name, form, state, spec, owner_token_id,
	       desired_concurrency, network_group, dns_name, lease_deadline,
	       created_at, updated_at, finished_at
	FROM tasks`

func (r *repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]task.Task, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []task.Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func scanTask(scan func(dest ...any) error) (*task.Task, error) {
	var t task.Task
	var stateStr string
	err := scan(&t.ID, &t.Seq, &t.ProjectID, &t.Name, &t.Form, &stateStr, &t.Spec, &t.OwnerTokenID,
		&t.DesiredConcurrency, &t.NetworkGroup, &t.DNSName, &t.LeaseDeadline,
		&t.CreatedAt, &t.UpdatedAt, &t.FinishedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	t.State = task.State(stateStr)
	return &t, nil
}

func stateIn(set []task.State, s task.State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

func joinStates(set []task.State) string {
	parts := make([]string, len(set))
	for i, s := range set {
		parts[i] = string(s)
	}
	return strings.Join(parts, "|")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
