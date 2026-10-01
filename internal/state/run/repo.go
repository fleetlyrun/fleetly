// Package run 是 Run 聚合 repo（CONTEXT.md Run 词条：Task 的一次执行，
// 产出结果、日志与停止原因）。状态机 pending → running → stopping →
// stopped | failed（ADR-0012）；终态携带停止原因七枚举。TTL 与停止收口
// 兜底共用绝对 deadline 列（ADR-0018 墙钟续算；stopping 迁移时覆写为
// 停止收口截止）。
package run

import (
	"context"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// State 是 Run 状态机值（ADR-0012）。
type State string

const (
	StatePending  State = "pending"  // 行已落，载体未下发
	StateRunning  State = "running"  // 载体观测 running
	StateStopping State = "stopping" // 停止已请求（SIGTERM + StopGrace 路径）
	StateStopped  State = "stopped"  // 终态（携带起因）
	StateFailed   State = "failed"   // 终态（进程失败）
)

// Terminal 报告是否终态。
func (s State) Terminal() bool {
	return s == StateStopped || s == StateFailed
}

// Active 报告是否活跃态（pending/running；stopping 是收口态——不再计入
// 期望并发的活槽位，但仍在驱动集合内）。
func (s State) Active() bool {
	return s == StatePending || s == StateRunning
}

// StopReason 是停止原因七枚举（ADR-0012；终态携带，snake_case 冻结）。
const (
	ReasonCompleted       = "completed"        // 自然退出（退出码 0）
	ReasonFailed          = "failed"           // 进程失败（退出码非 0）
	ReasonStoppedByUser   = "stopped_by_user"  // 用户/API 停止
	ReasonTTLExpired      = "ttl_expired"      // TTL 到期
	ReasonLeaseExpired    = "lease_expired"    // Owner Lease 失联超宽限
	ReasonOwnerRevoked    = "owner_revoked"    // 属主 Token 吊销 → 宽限排空
	ReasonPlatformDrained = "platform_drained" // 平台排空（节点排空/载体被平台移除）
)

// Run 是聚合行（Run 行不删——审计单位；终态行的载体由收敛移除）。
type Run struct {
	ID         string
	TaskID     string
	ProjectID  string
	State      State
	StopReason string // 七枚举；stopping 起因即写入，终态携带
	ExitCode   *int   // 终态退出码（nil = 未观测）
	WorkloadID string // Run 载体 Workload ID（观测对账锚）
	DNSName    string // per-Run 稳定 DNS（engine 铸名）
	Deadline   string // 双语义绝对 deadline：活跃 = TTL 截止；stopping = 停止收口兜底截止
	CreatedAt  string
	UpdatedAt  string
	StartedAt  string
	FinishedAt string
}

// Repo 是 Run 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行。
func (r *Repo) Create(ctx context.Context, run state.Runner, m *Run) error {
	now := state.FormatTime(r.clock.Now())
	m.CreatedAt, m.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO runs
			(id, task_id, project_id, state, stop_reason, exit_code,
			 workload_id, dns_name, deadline, created_at, updated_at,
			 started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, '', '')`,
		m.ID, m.TaskID, m.ProjectID, string(m.State), m.StopReason,
		m.WorkloadID, m.DNSName, m.Deadline, m.CreatedAt, m.UpdatedAt)
	return err
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Run, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanRun(row.Scan)
}

// ListByTask 返回 Task 名下 Run（新→旧 + after 游标；ADR-0026 惯例）。
func (r *Repo) ListByTask(ctx context.Context, run state.Runner, taskID, afterID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := selectCols + " WHERE task_id = ?"
	args := []any{taskID}
	if afterID != "" {
		q += " AND id < ?"
		args = append(args, afterID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	return r.query(ctx, run, q, args...)
}

// ListDriving 返回全部待驱动行（pending/running/stopping）。
func (r *Repo) ListDriving(ctx context.Context, run state.Runner) ([]Run, error) {
	return r.query(ctx, run,
		selectCols+" WHERE state IN ('pending', 'running', 'stopping') ORDER BY id")
}

// ListByTaskStates 返回 Task 名下指定状态的 Run（补足判定的活槽位计数）。
func (r *Repo) ListByTaskStates(ctx context.Context, run state.Runner, taskID string, states []State) ([]Run, error) {
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
		selectCols+" WHERE task_id = ? AND state IN ("+placeholders(len(states))+") ORDER BY id", args...)
}

// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
// stop_reason/exit_code/deadline 等字段）。前置不符 → ErrConflict；行不
// 存在 → ErrNotFound。UPDATE 带 state 前置条件作并发防御纵深。
func (r *Repo) Transit(ctx context.Context, run state.Runner, id string, from []State, to State, mut func(*Run)) error {
	cur, err := r.Get(ctx, run, id)
	if err != nil {
		return err
	}
	if !stateIn(from, cur.State) {
		return fmt.Errorf("%w: run %s is %s, want one of %s", state.ErrConflict, id, cur.State, joinStates(from))
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
	if to == StateRunning && cur.StartedAt == "" {
		cur.StartedAt = cur.UpdatedAt
	}
	res, err := run.ExecContext(ctx, `
		UPDATE runs SET
			state = ?, stop_reason = ?, exit_code = ?, deadline = ?,
			updated_at = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND state = ?`,
		string(cur.State), cur.StopReason, cur.ExitCode, cur.Deadline,
		cur.UpdatedAt, cur.StartedAt, cur.FinishedAt,
		id, string(origState))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: run %s state changed concurrently, want one of %s",
			state.ErrConflict, id, joinStates(from))
	}
	return nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, task_id, project_id, state, stop_reason, exit_code,
	       workload_id, dns_name, deadline, created_at, updated_at,
	       started_at, finished_at
	FROM runs`

func (r *Repo) query(ctx context.Context, run state.Runner, q string, args ...any) ([]Run, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Run
	for rows.Next() {
		m, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func scanRun(scan func(dest ...any) error) (*Run, error) {
	var m Run
	var stateStr string
	var exitCode any
	err := scan(&m.ID, &m.TaskID, &m.ProjectID, &stateStr, &m.StopReason, &exitCode,
		&m.WorkloadID, &m.DNSName, &m.Deadline,
		&m.CreatedAt, &m.UpdatedAt, &m.StartedAt, &m.FinishedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	m.State = State(stateStr)
	if exitCode != nil {
		if code, ok := exitCode.(int64); ok {
			c := int(code)
			m.ExitCode = &c
		}
	}
	return &m, nil
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

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
