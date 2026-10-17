// Package deployment 是 Deployment 聚合 repo 的 SQLite 实现：行类型、
// 状态机词表与 Repo 接口住 domain/delivery/deployment（ADR-0075 Repo 接口
// 层），本包只持不导出实现与构造。
package deployment

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain deployment.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain deployment.Repo）。
func New(clock state.Clock, run state.Runner) deployment.Repo { return &repo{clock: clock, run: run} }

// Create 落一行（活跃态幂等键唯一索引防并发重复入队 → ErrAlreadyExists）。
// ID 空则装配 "<app id>-<seq>"；Seq 零值则事务内取 MAX+1（ADR-0070）。
func (r *repo) Create(ctx context.Context, d *deployment.Deployment) error {
	now := state.FormatTime(r.clock.Now())
	d.CreatedAt, d.UpdatedAt = now, now
	seq, err := persistence.NextSeq(ctx, r.run, "deployments")
	if err != nil {
		return err
	}
	d.Seq = seq
	if d.ID == "" {
		d.ID = entityid.Derive(d.AppID, "d", seq)
	}
	// pre_deploy 游标随行落库（提交方直落形态：回滚部署带 done 游标——
	// ADR-0030 决策 5；常规部署为空串 = 未开始）。
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO deployments
			(id, seq, app_id, from_revision, to_revision, state, generation, from_generation,
			 idempotency_key, commit_sha, superseded_by, error, observe_deadline,
			 pre_deploy, created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, ?, '')`,
		d.ID, d.Seq, d.AppID, d.FromRevision, d.ToRevision, string(d.State), d.Generation, d.FromGeneration,
		d.IdempotencyKey, d.CommitSHA, d.PreDeploy, d.CreatedAt, d.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		// 唯一索引作用域是 App 内（B10）：冲突 = 同 App 同键已有活跃行。
		return fmt.Errorf("%w: an active deployment in app %s already holds idempotency key %q",
			state.ErrAlreadyExists, d.AppID, d.IdempotencyKey)
	}
	return err
}

// Get 按 ID 直读。
func (r *repo) Get(ctx context.Context, id string) (*deployment.Deployment, error) {
	row := r.run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanDeployment(row.Scan)
}

// ListByApp 返回 App 全部 Deployment（新→旧 + after 游标；ADR-0026
// after_* + limit 惯例——游标 = seq 创建序）。limit<=0 或 >200 回落/钳制
// 缺省 50。
func (r *repo) ListByApp(ctx context.Context, appID string, afterSeq int64, limit int) ([]deployment.Deployment, error) {
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
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []deployment.Deployment
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
func (r *repo) ActiveByApp(ctx context.Context, appID string) ([]deployment.Deployment, error) {
	states := deployment.ActiveStateStrings()
	args := append([]any{appID}, persistence.ToAny(states)...)
	rows, err := r.run.QueryContext(ctx,
		selectCols+" WHERE app_id = ? AND state IN ("+persistence.Placeholders(len(states))+") ORDER BY seq",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []deployment.Deployment
	for rows.Next() {
		d, err := scanDeployment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// FindActiveByIdempotencyKey 返回该 App 内持该键的活跃 Deployment（无 →
// ErrNotFound）。键的作用域是 App（B10）：异 App 同键不命中——作用域
// 收紧后的正确语义，异 App 的键不在本 App 作用域内；存量迁移窗口若出现
// 跨 App 同键活跃行，对本 App 亦视同无键。
func (r *repo) FindActiveByIdempotencyKey(ctx context.Context, appID, key string) (*deployment.Deployment, error) {
	states := deployment.ActiveStateStrings()
	args := append([]any{appID, key}, persistence.ToAny(states)...)
	row := r.run.QueryRowContext(ctx,
		selectCols+" WHERE app_id = ? AND idempotency_key = ? AND state IN ("+persistence.Placeholders(len(states))+") ORDER BY seq LIMIT 1",
		args...)
	return scanDeployment(row.Scan)
}

// ListDriving 返回全部待驱动行：活跃态 + 待自动回滚的 failed（领域模型
// §4：failed → (自动) rolling-back；rollback_attempted=1 或无 from_revision
// 的 failed 是终态，不拾取）。
func (r *repo) ListDriving(ctx context.Context) ([]deployment.Deployment, error) {
	states := deployment.ActiveStateStrings()
	args := persistence.ToAny(states)
	args = append(args, false)
	rows, err := r.run.QueryContext(ctx,
		selectCols+" WHERE state IN ("+persistence.Placeholders(len(states))+")"+
			" OR (state = 'failed' AND rollback_attempted = ? AND from_revision != '')"+
			" ORDER BY seq",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []deployment.Deployment
	for rows.Next() {
		d, err := scanDeployment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// NextGeneration 返回 App 下一 Generation（单调；Drift 判定与幂等重放的锚）。
func (r *repo) NextGeneration(ctx context.Context, appID string) (uint64, error) {
	var gen uint64
	err := r.run.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(generation), 0) FROM deployments WHERE app_id = ?`, appID).Scan(&gen)
	if err != nil {
		return 0, err
	}
	return gen + 1, nil
}

// LatestSucceeded 返回 App 最近一次 succeeded 的 Deployment（ADR-0022
// 启动基线重放的锚；无成功基线 → ErrNotFound）。
func (r *repo) LatestSucceeded(ctx context.Context, appID string) (*deployment.Deployment, error) {
	row := r.run.QueryRowContext(ctx,
		selectCols+" WHERE app_id = ? AND state = 'succeeded' ORDER BY seq DESC LIMIT 1", appID)
	return scanDeployment(row.Scan)
}

// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
// error/finished_at/generation/superseded_by/observe_deadline 等字段）。
// 前置不符 → ErrConflict；行不存在 → ErrNotFound（四件一拍的"状态"件；
// UPDATE 带 state 前置条件作并发防御纵深，即使未来放开连接池也安全）。
func (r *repo) Transit(ctx context.Context, id string, from []deployment.State, to deployment.State, mut func(*deployment.Deployment)) error {
	cur, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if !persistence.StateIn(from, cur.State) {
		return fmt.Errorf("%w: deployment %s is %s, want one of %s", state.ErrConflict, id, cur.State, persistence.JoinStates(from))
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
		UPDATE deployments SET
			state = ?, generation = ?, from_generation = ?, error = ?, superseded_by = ?,
			observe_deadline = ?, pre_deploy = ?, rollback_attempted = ?,
			to_revision = ?, updated_at = ?, finished_at = ?
		`+persistence.TransitWhere,
		string(cur.State), cur.Generation, cur.FromGeneration, cur.Error, cur.SupersededBy,
		cur.ObserveDeadline, cur.PreDeploy, cur.RollbackAttempted, cur.ToRevision,
		cur.UpdatedAt, cur.FinishedAt,
		id, string(origState))
	if err != nil {
		return err
	}
	// CAS 纵深防御（Q-5）：前置 Get 与 UPDATE 之间状态被并发迁移时
	// RowsAffected=0——必须显式归一 ErrConflict，不得静默当成功（对照
	// secret/hook repo 的 RowsAffected 正例；单连接串行下不可达，放开
	// 连接池后是最后防线）。
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: deployment %s state changed concurrently, want one of %s",
			state.ErrConflict, id, persistence.JoinStates(from))
	}
	return nil
}

const selectCols = `
	SELECT id, seq, app_id, from_revision, to_revision, state, generation, from_generation,
	       idempotency_key, commit_sha, superseded_by, error, observe_deadline,
	       pre_deploy, rollback_attempted, created_at, updated_at, finished_at
	FROM deployments`

func scanDeployment(scan func(dest ...any) error) (*deployment.Deployment, error) {
	var d deployment.Deployment
	var stateStr string
	var rollback int
	err := scan(&d.ID, &d.Seq, &d.AppID, &d.FromRevision, &d.ToRevision, &stateStr, &d.Generation, &d.FromGeneration,
		&d.IdempotencyKey, &d.CommitSHA, &d.SupersededBy, &d.Error, &d.ObserveDeadline,
		&d.PreDeploy, &rollback, &d.CreatedAt, &d.UpdatedAt, &d.FinishedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	d.State = deployment.State(stateStr)
	d.RollbackAttempted = rollback != 0
	return &d, nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)
