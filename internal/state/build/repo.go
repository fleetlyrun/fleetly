// Package build 是 Build 聚合 repo（CONTEXT.md Build 词条：从 Source 产出
// 镜像的过程记录，输出不可变 digest）。状态机 queued → building →
// succeeded | failed | cancelled | expired（超时看门狗）由 engine 持有，
// 本 repo 只承接持久化与 CAS。
package build

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// State 是 Build 状态机值（领域模型 §4）。
type State string

const (
	StateQueued    State = "queued"
	StateBuilding  State = "building"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	StateExpired   State = "expired" // 超时看门狗收口
)

// Terminal 报告是否终态。
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled, StateExpired:
		return true
	}
	return false
}

// Build 是聚合行（构建记录永不删）。
type Build struct {
	ID         string
	AppID      string
	RevisionID string
	State      State
	Digest     string
	Error      string
	CreatedAt  string
	UpdatedAt  string
	FinishedAt string
}

// Repo 是 Build 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行。
func (r *Repo) Create(ctx context.Context, run state.Runner, b *Build) error {
	now := state.FormatTime(r.clock.Now())
	b.CreatedAt, b.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO builds (id, app_id, revision_id, state, digest, error, created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, '', '', ?, ?, '')`,
		b.ID, b.AppID, b.RevisionID, string(b.State), b.CreatedAt, b.UpdatedAt)
	return err
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Build, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanBuild(row.Scan)
}

// ListByApp 返回 App 全部 Build（新→旧）。
func (r *Repo) ListByApp(ctx context.Context, run state.Runner, appID string) ([]Build, error) {
	rows, err := run.QueryContext(ctx, selectCols+" WHERE app_id = ? ORDER BY id DESC", appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Build
	for rows.Next() {
		b, err := scanBuild(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ListByRevision 返回 Revision 的全部 Build 行（新→旧；Revision 级构建
// 一次，历史行应只有一条——含终态供部署驱动判定复用或失败）。
func (r *Repo) ListByRevision(ctx context.Context, run state.Runner, revisionID string) ([]Build, error) {
	rows, err := run.QueryContext(ctx,
		selectCols+" WHERE revision_id = ? ORDER BY id DESC", revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Build
	for rows.Next() {
		b, err := scanBuild(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ListActive 返回全部活跃 Build（构建循环拾取面）。
func (r *Repo) ListActive(ctx context.Context, run state.Runner) ([]Build, error) {
	rows, err := run.QueryContext(ctx,
		selectCols+" WHERE state IN (?, ?) ORDER BY id", string(StateQueued), string(StateBuilding))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Build
	for rows.Next() {
		b, err := scanBuild(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// Transit 是状态 CAS（同 deployment.Transit 口径；mut 补充 digest/error）。
func (r *Repo) Transit(ctx context.Context, run state.Runner, id string, from []State, to State, mut func(*Build)) error {
	cur, err := r.Get(ctx, run, id)
	if err != nil {
		return err
	}
	if !stateIn(from, cur.State) {
		return fmt.Errorf("%w: build %s is %s", state.ErrConflict, id, cur.State)
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
		UPDATE builds SET state = ?, digest = ?, error = ?, updated_at = ?, finished_at = ?
		WHERE id = ? AND state = ?`,
		string(cur.State), cur.Digest, cur.Error, cur.UpdatedAt, cur.FinishedAt,
		id, string(origState))
	if err != nil {
		return err
	}
	// CAS 纵深防御（Q-5）：前置 Get 与 UPDATE 之间状态被并发迁移时
	// RowsAffected=0——显式归一 ErrConflict，不静默当成功（对照
	// secret/hook repo 的 RowsAffected 正例）。
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: build %s state changed concurrently", state.ErrConflict, id)
	}
	return nil
}

const selectCols = `
	SELECT id, app_id, revision_id, state, digest, error, created_at, updated_at, finished_at
	FROM builds`

func scanBuild(scan func(dest ...any) error) (*Build, error) {
	var b Build
	var stateStr string
	err := scan(&b.ID, &b.AppID, &b.RevisionID, &stateStr, &b.Digest, &b.Error,
		&b.CreatedAt, &b.UpdatedAt, &b.FinishedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	b.State = State(stateStr)
	return &b, nil
}

func stateIn(set []State, s State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}
