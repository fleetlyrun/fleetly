// Package deployment 是 Deployment 聚合 repo（CONTEXT.md Deployment 词条：
// 从旧 Revision 到新 Revision 的受监督迁移）。行本身即 admission 持久
// 真源（state=queued，2026-09-30 裁决：库为真源 + 进程内索引，ADR-0016）；
// 状态迁移一律经 Transit CAS（四件一拍的"状态"件，Outbox/审计由调用方
// 同事务组合）。
package deployment

import (
	"context"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// State 是 Deployment 状态机值（领域模型 §4；含 admission 排队态）。
type State string

const (
	StateQueued      State = "queued"       // admission 已受理，等待单写者拾取
	StatePreparing   State = "preparing"    // Spec 归一化/材料装配/前置 Job
	StateBuilding    State = "building"     // 构建中（镜像引用可跳过）
	StateReleasing   State = "releasing"    // 投影 Workload 下发 Runtime（L1 门）
	StateObserving   State = "observing"    // L3 观察窗 + L2 看门狗常驻
	StateSucceeded   State = "succeeded"    // 终态
	StateFailed      State = "failed"       // 终态（自动触发回滚）
	StateRollingBack State = "rolling-back" // Replay 上一成功 Revision
	StateSuperseded  State = "superseded"   // 被显式 supersede 抢占的终态
	StateCancelled   State = "cancelled"    // 排队中/在途被取消的终态
)

// Terminal 报告是否终态（终态行不再参与 admission 与单写者驱动）。
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateSuperseded, StateCancelled:
		return true
	}
	return false
}

// Active 报告是否活跃态（admission 去重窗口：排队 + 在途 + 回滚中）。
func (s State) Active() bool {
	return s != "" && !s.Terminal()
}

// ActiveStates 是全部活跃态（SQL IN 参数形态）。
func ActiveStates() []string {
	return []string{
		string(StateQueued), string(StatePreparing), string(StateBuilding),
		string(StateReleasing), string(StateObserving), string(StateRollingBack),
	}
}

// Deployment 是聚合行（部署记录永不删——审计单位）。
type Deployment struct {
	ID              string
	AppID           string
	FromRevision    string // 上一 Revision ID（首次部署为空）
	ToRevision      string // 目标 Revision ID
	State           State
	Generation      uint64 // 本 Deployment 拟下发的 Generation（单调）
	IdempotencyKey  string
	CommitSHA       string
	SupersededBy    string // 被哪个 Deployment 抢占（终态 superseded 时非空）
	Error           string // 失败原因（英文，用户可见）
	ObserveDeadline string // L3 观察窗绝对截止（RFC3339；空 = 不在观察期）
	CreatedAt       string
	UpdatedAt       string
	FinishedAt      string
}

// Repo 是 Deployment 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行（活跃态幂等键唯一索引防并发重复入队 → ErrConflict）。
func (r *Repo) Create(ctx context.Context, run state.Runner, d *Deployment) error {
	now := state.FormatTime(r.clock.Now())
	d.CreatedAt, d.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO deployments
			(id, app_id, from_revision, to_revision, state, generation,
			 idempotency_key, commit_sha, superseded_by, error, observe_deadline,
			 created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, '')`,
		d.ID, d.AppID, d.FromRevision, d.ToRevision, string(d.State), d.Generation,
		d.IdempotencyKey, d.CommitSHA, d.CreatedAt, d.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: an active deployment already holds idempotency key %q", state.ErrConflict, d.IdempotencyKey)
	}
	return err
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Deployment, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanDeployment(row.Scan)
}

// ListByApp 返回 App 全部 Deployment（新→旧；ULID 主键序 = 创建序）。
func (r *Repo) ListByApp(ctx context.Context, run state.Runner, appID string) ([]Deployment, error) {
	rows, err := run.QueryContext(ctx, selectCols+" WHERE app_id = ? ORDER BY id DESC", appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// ActiveByApp 返回 App 全部活跃态 Deployment（应至多一条在途 + 任意排队）。
func (r *Repo) ActiveByApp(ctx context.Context, run state.Runner, appID string) ([]Deployment, error) {
	rows, err := run.QueryContext(ctx,
		selectCols+" WHERE app_id = ? AND state IN ("+placeholders(len(ActiveStates()))+") ORDER BY id",
		activeArgs(appID)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// FindActiveByIdempotencyKey 返回持该键的活跃 Deployment（无 → ErrNotFound）。
func (r *Repo) FindActiveByIdempotencyKey(ctx context.Context, run state.Runner, key string) (*Deployment, error) {
	states := ActiveStates()
	args := append([]any{key}, toAny(states)...)
	row := run.QueryRowContext(ctx,
		selectCols+" WHERE idempotency_key = ? AND state IN ("+placeholders(len(states))+") ORDER BY id LIMIT 1",
		args...)
	return scanDeployment(row.Scan)
}

// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
// error/finished_at/generation/superseded_by/observe_deadline 等字段）。
// 前置不符 → ErrConflict；行不存在 → ErrNotFound（四件一拍的"状态"件；
// UPDATE 带 state 前置条件作并发防御纵深，即使未来放开连接池也安全）。
func (r *Repo) Transit(ctx context.Context, run state.Runner, id string, from []State, to State, mut func(*Deployment)) error {
	cur, err := r.Get(ctx, run, id)
	if err != nil {
		return err
	}
	if !stateIn(from, cur.State) {
		return fmt.Errorf("%w: deployment %s is %s, want one of %s", state.ErrConflict, id, cur.State, joinStates(from))
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
	_, err = run.ExecContext(ctx, `
		UPDATE deployments SET
			state = ?, generation = ?, error = ?, superseded_by = ?,
			observe_deadline = ?, updated_at = ?, finished_at = ?
		WHERE id = ? AND state = ?`,
		string(cur.State), cur.Generation, cur.Error, cur.SupersededBy,
		cur.ObserveDeadline, cur.UpdatedAt, cur.FinishedAt,
		id, string(origState))
	return err
}

const selectCols = `
	SELECT id, app_id, from_revision, to_revision, state, generation,
	       idempotency_key, commit_sha, superseded_by, error, observe_deadline,
	       created_at, updated_at, finished_at
	FROM deployments`

func scanDeployment(scan func(dest ...any) error) (*Deployment, error) {
	var d Deployment
	var stateStr string
	err := scan(&d.ID, &d.AppID, &d.FromRevision, &d.ToRevision, &stateStr, &d.Generation,
		&d.IdempotencyKey, &d.CommitSHA, &d.SupersededBy, &d.Error, &d.ObserveDeadline,
		&d.CreatedAt, &d.UpdatedAt, &d.FinishedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	d.State = State(stateStr)
	return &d, nil
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

func activeArgs(appID string) []any {
	args := []any{appID}
	for _, s := range ActiveStates() {
		args = append(args, s)
	}
	return args
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
