// revisions.go 是 function.RevisionRepo 的 SQLite 实现（冻结体子表；
// Prune 语义与 delivery/revision 同律——ADR-0101 全域统一律）。
package function

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// revisions 是 domain function.RevisionRepo 的 SQLite 实现。
type revisions struct {
	clock state.Clock
	run   state.Runner
}

// NewRevisions 构造冻结体 repo。
func NewRevisions(clock state.Clock, run state.Runner) function.RevisionRepo {
	return &revisions{clock: clock, run: run}
}

// Create 落一行（ID = <fn id>-r<n> 确定性派生，entityid.Revision 同式）。
func (r *revisions) Create(ctx context.Context, rev *function.Revision) error {
	rev.CreatedAt = state.FormatTime(r.clock.Now())
	rev.Digest = function.Digest(rev.Spec)
	if rev.ID == "" {
		rev.ID = entityid.Revision(rev.FunctionID, rev.Seq)
	}
	_, err := r.run.ExecContext(ctx, `
		INSERT INTO function_revisions (id, function_id, seq, digest, spec, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		rev.ID, rev.FunctionID, rev.Seq, rev.Digest, rev.Spec, rev.CreatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: identical spec already frozen for function", state.ErrAlreadyExists)
	}
	return err
}

// Get 按 ID 直读。
func (r *revisions) Get(ctx context.Context, id string) (*function.Revision, error) {
	row := r.run.QueryRowContext(ctx, selectRevisionCols+`
		FROM function_revisions WHERE id = ?`, id)
	return scanRevision(row.Scan)
}

// GetBySeq 按函数内序号读（r1..rn 对外形态）。
func (r *revisions) GetBySeq(ctx context.Context, functionID string, seq int64) (*function.Revision, error) {
	row := r.run.QueryRowContext(ctx, selectRevisionCols+`
		FROM function_revisions WHERE function_id = ? AND seq = ?`, functionID, seq)
	return scanRevision(row.Scan)
}

// FindByDigest 按函数 + 内容指纹找既有冻结体（内容寻址复用）。
func (r *revisions) FindByDigest(ctx context.Context, functionID, digest string) (*function.Revision, error) {
	row := r.run.QueryRowContext(ctx, selectRevisionCols+`
		FROM function_revisions WHERE function_id = ? AND digest = ?`, functionID, digest)
	return scanRevision(row.Scan)
}

// NextSeq 返回函数下一序号（首条 = 1；单调）。
func (r *revisions) NextSeq(ctx context.Context, functionID string) (int64, error) {
	var seq int64
	err := r.run.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) FROM function_revisions WHERE function_id = ?`, functionID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	return seq + 1, nil
}

// ListByFunction 返回保留窗内全部 Revision（升序 + after 游标）。
func (r *revisions) ListByFunction(ctx context.Context, functionID string, afterSeq int64, limit int) ([]function.Revision, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectRevisionCols + ` FROM function_revisions WHERE function_id = ?`
	args := []any{functionID}
	if afterSeq > 0 {
		q += " AND seq > ?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY seq ASC LIMIT ?"
	args = append(args, limit)
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return persistence.Collect(rows, collectRevision)
}

// Prune 是保留窗清理（ADR-0101，与 delivery/revision.Prune 同律同式）：
// 保留最新 keep 行 + protect 名单，删其余最旧。窗口阈值 = 第 keep 新行
// seq；protect 行即使在窗外也保留（软上限）。
func (r *revisions) Prune(ctx context.Context, functionID string, protect []string, keep int) (int64, error) {
	q := `
		DELETE FROM function_revisions
		WHERE function_id = ? AND seq < COALESCE((
			SELECT MIN(seq) FROM (
				SELECT seq FROM function_revisions WHERE function_id = ? ORDER BY seq DESC LIMIT ?
			)
		), 0)`
	args := []any{functionID, functionID, keep}
	if len(protect) > 0 {
		ph := make([]string, len(protect))
		for i, id := range protect {
			ph[i] = "?"
			args = append(args, id)
		}
		q += " AND id NOT IN (" + strings.Join(ph, ",") + ")"
	}
	res, err := r.run.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAllByFunction 是聚合终局全清（ADR-0101 决策 4）。
func (r *revisions) DeleteAllByFunction(ctx context.Context, functionID string) error {
	_, err := r.run.ExecContext(ctx, `DELETE FROM function_revisions WHERE function_id = ?`, functionID)
	return err
}

const selectRevisionCols = `
	SELECT id, function_id, seq, digest, spec, created_at`

func scanRevision(scan func(dest ...any) error) (*function.Revision, error) {
	var rev function.Revision
	if err := scan(&rev.ID, &rev.FunctionID, &rev.Seq, &rev.Digest, &rev.Spec, &rev.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &rev, nil
}

// collectRevision 是 persistence.Collect 的行函数（scanRevision 解引用形态）。
func collectRevision(rows *sql.Rows) (function.Revision, error) {
	rev, err := scanRevision(rows.Scan)
	if err != nil {
		return function.Revision{}, err
	}
	return *rev, nil
}
