// Package revision 是 Revision 聚合 repo 的 SQLite 实现：行类型与 Repo
// 接口住 domain/delivery/revision（ADR-0075 Repo 接口层），本包只持不导出
// 实现与构造。
package revision

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain revision.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain revision.Repo）。
func New(clock state.Clock) revision.Repo { return &repo{clock: clock} }

// Create 落一行（digest 由 spec 派生；同 App 序号唯一、同 App 同内容唯一
// ——重复冻结同内容 → state.ErrAlreadyExists，调用方应改走 FindByDigest
// 复用）。ID 空则由 app id + per-App 序号派生（web-42-r7，ADR-0070 派生
// 类——per-App 修订号即 UI 面既有语义，确定性无随机）。
func (r *repo) Create(ctx context.Context, run state.Runner, rev *revision.Revision) error {
	now := state.FormatTime(r.clock.Now())
	rev.CreatedAt = now
	rev.Digest = revision.Digest(rev.Spec)
	if rev.ID == "" {
		rev.ID = entityid.Revision(rev.AppID, rev.Seq)
	}
	_, err := run.ExecContext(ctx, `
		INSERT INTO revisions (id, app_id, seq, digest, spec, source_text, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rev.ID, rev.AppID, rev.Seq, rev.Digest, rev.Spec, rev.SourceText, rev.CreatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: revision with identical spec already frozen for app", state.ErrAlreadyExists)
	}
	return err
}

// FindByDigest 按 App + 内容指纹找既有冻结体（内容寻址复用：无变化重
// 部署复用 Rn 而非新冻结 Rn+1）。
func (r *repo) FindByDigest(ctx context.Context, run state.Runner, appID, digest string) (*revision.Revision, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, app_id, seq, digest, spec, source_text, created_at
		FROM revisions WHERE app_id = ? AND digest = ?`, appID, digest)
	return scanRevision(row.Scan)
}

// Get 按 ID 直读。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*revision.Revision, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, app_id, seq, digest, spec, source_text, created_at
		FROM revisions WHERE id = ?`, id)
	return scanRevision(row.Scan)
}

// GetBySeq 按 App 内序号读（R1..Rn 对外形态）。
func (r *repo) GetBySeq(ctx context.Context, run state.Runner, appID string, seq int64) (*revision.Revision, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, app_id, seq, digest, spec, source_text, created_at
		FROM revisions WHERE app_id = ? AND seq = ?`, appID, seq)
	return scanRevision(row.Scan)
}

// Latest 返回 App 最新 Revision（无 → state.ErrNotFound；"上一成功"由
// 调用方结合 Deployment 终态判定，本 repo 不持部署语义）。
func (r *repo) Latest(ctx context.Context, run state.Runner, appID string) (*revision.Revision, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, app_id, seq, digest, spec, source_text, created_at
		FROM revisions WHERE app_id = ? ORDER BY seq DESC LIMIT 1`, appID)
	return scanRevision(row.Scan)
}

// NextSeq 返回 App 下一序号（首条 = 1；单调推进）。
func (r *repo) NextSeq(ctx context.Context, run state.Runner, appID string) (int64, error) {
	var seq int64
	err := run.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) FROM revisions WHERE app_id = ?`, appID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	return seq + 1, nil
}

// ListByApp 返回 App 全部 Revision（R1..Rn 序号升序 + after 游标；
// ADR-0026 after_* + limit 惯例——游标轴 = App 内单调 seq，升序即既有
// 响应序，events 面同款形态）。limit<=0 或 >200 回落/钳制缺省 50。
func (r *repo) ListByApp(ctx context.Context, run state.Runner, appID string, afterSeq int64, limit int) ([]revision.Revision, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := `
		SELECT id, app_id, seq, digest, spec, source_text, created_at
		FROM revisions WHERE app_id = ?`
	args := []any{appID}
	if afterSeq > 0 {
		q += " AND seq > ?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY seq ASC LIMIT ?"
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []revision.Revision
	for rows.Next() {
		rev, err := scanRevision(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *rev)
	}
	return out, rows.Err()
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

func scanRevision(scan func(dest ...any) error) (*revision.Revision, error) {
	var rev revision.Revision
	if err := scan(&rev.ID, &rev.AppID, &rev.Seq, &rev.Digest, &rev.Spec, &rev.SourceText, &rev.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &rev, nil
}
