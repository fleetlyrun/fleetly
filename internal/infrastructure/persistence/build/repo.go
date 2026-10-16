// Package build 是 Build 聚合 repo 的 SQLite 实现：行类型、状态机词表与
// Repo 接口住 domain/delivery/build（ADR-0075 Repo 接口层），本包只持
// 不导出实现与构造。
package build

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain build.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain build.Repo）。
func New(clock state.Clock) build.Repo { return &repo{clock: clock} }

// Create 落一行。ID 空则装配 "<app id>-<seq>"；Seq 零值则事务内取 MAX+1
// （ADR-0070）。
func (r *repo) Create(ctx context.Context, run state.Runner, b *build.Build) error {
	now := state.FormatTime(r.clock.Now())
	b.CreatedAt, b.UpdatedAt = now, now
	seq, err := persistence.NextSeq(ctx, run, "builds")
	if err != nil {
		return err
	}
	b.Seq = seq
	if b.ID == "" {
		b.ID = entityid.Derive(b.AppID, "b", seq)
	}
	_, err = run.ExecContext(ctx, `
		INSERT INTO builds (id, seq, app_id, revision_id, state, digest, repo, error, created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, '', ?, '', ?, ?, '')`,
		b.ID, b.Seq, b.AppID, b.RevisionID, string(b.State), b.Repo, b.CreatedAt, b.UpdatedAt)
	return err
}

// Get 按 ID 直读。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*build.Build, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanBuild(row.Scan)
}

// ListByApp 返回 App 全部 Build（新→旧 + after 游标；ADR-0026 after_* +
// limit 惯例——游标 = seq 创建序）。limit<=0 或 >200 回落/钳制缺省 50。
func (r *repo) ListByApp(ctx context.Context, run state.Runner, appID string, afterSeq int64, limit int) ([]build.Build, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectCols + " WHERE app_id = ?"
	args := []any{appID}
	if afterSeq > 0 {
		q += " AND seq < ?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []build.Build
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
func (r *repo) ListByRevision(ctx context.Context, run state.Runner, revisionID string) ([]build.Build, error) {
	rows, err := run.QueryContext(ctx,
		selectCols+" WHERE revision_id = ? ORDER BY seq DESC", revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []build.Build
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
func (r *repo) ListActive(ctx context.Context, run state.Runner) ([]build.Build, error) {
	rows, err := run.QueryContext(ctx,
		selectCols+" WHERE state IN (?, ?) ORDER BY seq", string(build.StateQueued), string(build.StateBuilding))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []build.Build
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
func (r *repo) Transit(ctx context.Context, run state.Runner, id string, from []build.State, to build.State, mut func(*build.Build)) error {
	cur, err := r.Get(ctx, run, id)
	if err != nil {
		return err
	}
	if !persistence.StateIn(from, cur.State) {
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
		`+persistence.TransitWhere,
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
	SELECT id, seq, app_id, revision_id, state, digest, repo, error, created_at, updated_at, finished_at
	FROM builds`

func scanBuild(scan func(dest ...any) error) (*build.Build, error) {
	var b build.Build
	var stateStr string
	err := scan(&b.ID, &b.Seq, &b.AppID, &b.RevisionID, &stateStr, &b.Digest, &b.Repo, &b.Error,
		&b.CreatedAt, &b.UpdatedAt, &b.FinishedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	b.State = build.State(stateStr)
	return &b, nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)
