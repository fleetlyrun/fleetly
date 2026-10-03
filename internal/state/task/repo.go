// Package task 是 Task 聚合 repo（CONTEXT.md Task 词条：程序化工作负载，
// 双形态 one-shot/resident；ADR-0012/0025）。行本身即受理真源（spec 冻结
// protojson）；状态迁移一律经 Transit CAS（四件一拍的"状态"件，Outbox/审计
// 由调用方同事务组合）。TTL 与 Owner Lease 的绝对 deadline 语义在 Run 行
// （ADR-0018 墙钟续算）。
package task

import (
	"context"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// State 是 Task 状态机值（ADR-0012：双形态生命周期）。
type State string

const (
	StateActive    State = "active"    // 保温中（resident 补足维持 desired；one-shot 等其唯一 Run 终态）
	StateDraining  State = "draining"  // 排空中：停止补足，存量 Run 按起因收口
	StateCompleted State = "completed" // 终态（one-shot 的 Run 自然完成）
	StateFailed    State = "failed"    // 终态（one-shot 的 Run 失败）
	StateDrained   State = "drained"   // 终态（排空完成：lease_expired / owner_revoked / 用户排空）
	StateDeleted   State = "deleted"   // 终态 tombstone（显式删除；载体已拆）
)

// Terminal 报告是否终态（终态行不再参与驱动）。
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateDrained, StateDeleted:
		return true
	}
	return false
}

// Form 是双形态值（CONTEXT.md Task 词条；kebab-case 与存储一致）。
const (
	FormOneShot  = "one-shot"
	FormResident = "resident"
)

// Task 是聚合行（Task 行不删——审计单位；deleted 是 tombstone）。
type Task struct {
	ID                 string
	ProjectID          string
	Name               string
	Form               string // one-shot | resident（spec.Form 同值）
	State              State
	Spec               []byte // 冻结 TaskSpec（protojson）
	OwnerTokenID       string // 属主 Token 行引用（非明文，P1-8）
	DesiredConcurrency int64
	NetworkGroup       string
	DNSName            string // per-Task 池级稳定 DNS（engine 铸名）
	LeaseDeadline      string // RFC3339 绝对 deadline（空 = 无租约约束）
	CreatedAt          string
	UpdatedAt          string
	FinishedAt         string
}

// Repo 是 Task 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行（活跃名唯一索引防项目内重名 → ErrAlreadyExists）。
func (r *Repo) Create(ctx context.Context, run state.Runner, t *Task) error {
	now := state.FormatTime(r.clock.Now())
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO tasks
			(id, project_id, name, form, state, spec, owner_token_id,
			 desired_concurrency, network_group, dns_name, lease_deadline,
			 created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		t.ID, t.ProjectID, t.Name, t.Form, string(t.State), t.Spec, t.OwnerTokenID,
		t.DesiredConcurrency, t.NetworkGroup, t.DNSName, t.LeaseDeadline,
		t.CreatedAt, t.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: an active task named %q already exists in project %s", state.ErrAlreadyExists, t.Name, t.ProjectID)
	}
	return err
}

// Get 按 ID 直读（tombstone 行照常返回——终态事实可见）。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Task, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanTask(row.Scan)
}

// GetByName 按项目内名直读（活跃名唯一；无 → ErrNotFound）。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*Task, error) {
	row := run.QueryRowContext(ctx,
		selectCols+" WHERE project_id = ? AND name = ? AND state != 'deleted'", projectID, name)
	return scanTask(row.Scan)
}

// ListByProject 返回项目内 Task（新→旧 + after 游标；ADR-0026 after_* + limit
// 惯例——游标 = ULID 创建序）。
func (r *Repo) ListByProject(ctx context.Context, run state.Runner, projectID, afterID string, limit int) ([]Task, error) {
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

// ListDriving 返回全部待驱动行（active/draining；tombstone 与终态不拾取）。
func (r *Repo) ListDriving(ctx context.Context, run state.Runner) ([]Task, error) {
	return r.query(ctx, run,
		selectCols+" WHERE state IN ('active', 'draining') ORDER BY id")
}

// ListRecentlyFinished 返回收口时刻在窗内的终态 Task（收尾批 E29-2 残留
// 载体清扫的候选集）。deleted 不在列——其载体拆除是 DeleteTask 自身的
// Remove 步（先收口后落账），tombstone 无残留面。finished_at 是 RFC3339
// UTC 串（state.FormatTime），字典序即时序。limit 钳制与 ListByProject
// 同口径（<=0 或超上限回落/钳缺省）。新→旧序（finished_at DESC）：近期
// 收口的行才是残留的实际所在（崩溃/失败窗口），旧行近乎必然已收敛。
func (r *Repo) ListRecentlyFinished(ctx context.Context, run state.Runner, since string, limit int) ([]Task, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	return r.query(ctx, run,
		selectCols+" WHERE state IN ('completed', 'failed', 'drained')"+
			" AND finished_at != '' AND finished_at >= ?"+
			" ORDER BY finished_at DESC LIMIT ?", since, limit)
}

// ListByOwner 返回属主 Token 名下活跃行（吊销排空的拉式扫描面，P1-8）。
func (r *Repo) ListByOwner(ctx context.Context, run state.Runner, tokenID string) ([]Task, error) {
	return r.query(ctx, run,
		selectCols+" WHERE owner_token_id = ? AND state IN ('active', 'draining') ORDER BY id", tokenID)
}

// StatsByProject 返回 Project 内非终态 Task 的配额口径统计（ADR-0017
// 附录 A.1）：活跃行数与 desired_concurrency 之和（受理位与引擎配额检查
// 共用，事务内读）。
func (r *Repo) StatsByProject(ctx context.Context, run state.Runner, projectID string) (active int, desiredSum int64, err error) {
	row := run.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(desired_concurrency), 0)
		FROM tasks
		WHERE project_id = ? AND state NOT IN ('completed', 'failed', 'drained', 'deleted')`,
		projectID)
	if err := row.Scan(&active, &desiredSum); err != nil {
		return 0, 0, state.MapScanErr(err)
	}
	return active, desiredSum, nil
}

// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
// lease_deadline/desired_concurrency 等字段）。前置不符 → ErrConflict；
// 行不存在 → ErrNotFound。UPDATE 带 state 前置条件作并发防御纵深。
func (r *Repo) Transit(ctx context.Context, run state.Runner, id string, from []State, to State, mut func(*Task)) error {
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
func (r *Repo) UpdateLease(ctx context.Context, run state.Runner, id, deadline string) error {
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
	SELECT id, project_id, name, form, state, spec, owner_token_id,
	       desired_concurrency, network_group, dns_name, lease_deadline,
	       created_at, updated_at, finished_at
	FROM tasks`

func (r *Repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]Task, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func scanTask(scan func(dest ...any) error) (*Task, error) {
	var t Task
	var stateStr string
	err := scan(&t.ID, &t.ProjectID, &t.Name, &t.Form, &stateStr, &t.Spec, &t.OwnerTokenID,
		&t.DesiredConcurrency, &t.NetworkGroup, &t.DNSName, &t.LeaseDeadline,
		&t.CreatedAt, &t.UpdatedAt, &t.FinishedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	t.State = State(stateStr)
	return &t, nil
}

func stateIn(set []State, s State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

func joinStates(set []State) string {
	parts := make([]string, len(set))
	for i, s := range set {
		parts[i] = string(s)
	}
	return strings.Join(parts, "|")
}
