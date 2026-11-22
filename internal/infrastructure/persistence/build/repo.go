// Package build 是 Build 聚合 repo 的 SQLite 实现：行类型、状态机词表与
// Repo 接口住 domain/delivery/build（ADR-0075 Repo 接口层），本包只持
// 不导出实现与构造。
package build

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain build.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain build.Repo）。
func New(clock state.Clock, run state.Runner) build.Repo { return &repo{clock: clock, run: run} }

// Create 落一行。ID 空则装配 "<app id>-b<seq>"（ADR-0092 派生律）；Seq 零值则由 seq_counters 单调计数器铸造
// （ADR-0070）。
func (r *repo) Create(ctx context.Context, b *build.Build) error {
	now := state.FormatTime(r.clock.Now())
	b.CreatedAt, b.UpdatedAt = now, now
	seq, err := persistence.NextSeq(ctx, r.run, "builds")
	if err != nil {
		return err
	}
	b.Seq = seq
	if b.OwnerKind == "" {
		b.OwnerKind = build.OwnerApp
	}
	if b.ID == "" {
		// 兜底派生（受理面恒铸 ID——防御轨）：owner kind 分支锚（function
		// 行无 App 归属——Derive 空 id 会铸退化键）。
		if b.OwnerKind == build.OwnerFunction {
			b.ID = entityid.Derive(b.FunctionID, "fb", seq)
		} else {
			b.ID = entityid.Derive(b.AppID, "b", seq)
		}
	}
	// app_id 空串归一 NULL（function 构建行无 App 归属——SQLite FK 对
	// NULL 跳过检查；owner kind = app 恒非空由调用方保证）。
	appID := any(b.AppID)
	if appID == "" {
		appID = nil
	}
	// digest 建行可带（App 侧 queued 空值沿 Transit mut 补写；函数域测试
	// 与直建 succeeded 形态需要建行即有产物锚——列恒在，空串缺省）。
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO builds (id, seq, app_id, function_id, owner_kind, revision_id, state, digest, repo, error, created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, '')`,
		b.ID, b.Seq, appID, b.FunctionID, string(b.OwnerKind), b.RevisionID, string(b.State), b.Digest, b.Repo, b.CreatedAt, b.UpdatedAt)
	return err
}

// Get 按 ID 直读。
func (r *repo) Get(ctx context.Context, id string) (*build.Build, error) {
	row := r.run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanBuild(row.Scan)
}

// ListByApp 返回 App 全部 Build（新→旧 + after 游标；ADR-0026 after_* +
// limit 惯例——游标 = seq 创建序）。limit<=0 或 >200 回落/钳制缺省 50。
func (r *repo) ListByApp(ctx context.Context, appID string, afterSeq int64, limit int) ([]build.Build, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectCols + " WHERE app_id = ?"
	args := []any{appID}
	if afterSeq > 0 {
		q += " AND seq < ?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return persistence.Collect(rows, collectBuild)
}

// ListByRevision 返回 Revision 的全部 Build 行（新→旧；Revision 级构建
// 一次，历史行应只有一条——含终态供部署驱动判定复用或失败）。
func (r *repo) ListByRevision(ctx context.Context, revisionID string) ([]build.Build, error) {
	rows, err := r.run.QueryContext(ctx,
		selectCols+" WHERE revision_id = ? ORDER BY seq DESC", revisionID)
	if err != nil {
		return nil, err
	}
	return persistence.Collect(rows, collectBuild)
}

// ListActive 返回全部活跃 Build（构建循环拾取面）。
func (r *repo) ListActive(ctx context.Context) ([]build.Build, error) {
	rows, err := r.run.QueryContext(ctx,
		selectCols+" WHERE state IN (?, ?) ORDER BY seq", string(build.StateQueued), string(build.StateBuilding))
	if err != nil {
		return nil, err
	}
	return persistence.Collect(rows, collectBuild)
}

// Transit 是状态 CAS（同 deployment.Transit 口径；mut 补充 digest/error）。
func (r *repo) Transit(ctx context.Context, id string, from []build.State, to build.State, mut func(*build.Build)) error {
	cur, err := r.Get(ctx, id)
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
	res, err := r.run.ExecContext(ctx, `
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
	SELECT id, seq, app_id, function_id, owner_kind, revision_id, state, digest, repo, error, created_at, updated_at, finished_at
	FROM builds`

func scanBuild(scan func(dest ...any) error) (*build.Build, error) {
	var b build.Build
	var stateStr, ownerStr string
	// app_id 经 sql.NullString 读（function 构建行 NULL——空串归一给
	// 消费面，扫描面不因 NULL 断）。
	var appID sql.NullString
	err := scan(&b.ID, &b.Seq, &appID, &b.FunctionID, &ownerStr, &b.RevisionID, &stateStr, &b.Digest, &b.Repo, &b.Error,
		&b.CreatedAt, &b.UpdatedAt, &b.FinishedAt)
	if err == nil {
		b.AppID = appID.String
		b.OwnerKind = build.OwnerKind(ownerStr)
	}
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	b.State = build.State(stateStr)
	return &b, nil
}

// collectBuild 是 persistence.Collect 的行函数（scanBuild 的解引用形态，
// 错误原样上抛）。
func collectBuild(rows *sql.Rows) (build.Build, error) {
	b, err := scanBuild(rows.Scan)
	if err != nil {
		return build.Build{}, err
	}
	return *b, nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)
