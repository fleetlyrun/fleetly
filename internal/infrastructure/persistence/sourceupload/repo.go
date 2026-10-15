// Package sourceupload 是上传产物聚合 repo 的 SQLite 实现：行类型与
// Repo 接口住 domain/delivery/sourceupload（ADR-0075 Repo 接口层），本包
// 只持不导出实现与构造。
package sourceupload

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain sourceupload.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain sourceupload.Repo）。
func New(clock state.Clock) sourceupload.Repo { return &repo{clock: clock} }

const selectCols = "id, seq, project_id, digest, size_bytes, created_at"

// Create 落行；同 (project, digest) 已有行 → ErrAlreadyExists（finalize 的
// 去重路径先 FindByDigest，此形态只在并发同内容上传时可达）。
func (r *repo) Create(ctx context.Context, run state.Runner, u *sourceupload.Upload) error {
	seq, err := persistence.NextSeq(ctx, run, "source_uploads")
	if err != nil {
		return err
	}
	u.Seq = seq
	if u.ID == "" {
		u.ID = entityid.Join("upload", seq)
	}
	u.CreatedAt = state.FormatTime(r.clock.Now())
	_, err = run.ExecContext(ctx, `
		INSERT INTO source_uploads (id, seq, project_id, digest, size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Seq, u.ProjectID, u.Digest, u.SizeBytes, u.CreatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: project %q already holds an upload with digest %s", state.ErrAlreadyExists, u.ProjectID, u.Digest)
	}
	return err
}

// Get 按 ID 读行。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*sourceupload.Upload, error) {
	row := run.QueryRowContext(ctx,
		"SELECT "+selectCols+" FROM source_uploads WHERE id = ?", id)
	return scanUpload(row)
}

// FindByDigest 找项目内同内容行（重传去重路径；无行 → ErrNotFound）。
func (r *repo) FindByDigest(ctx context.Context, run state.Runner, projectID, digest string) (*sourceupload.Upload, error) {
	row := run.QueryRowContext(ctx,
		"SELECT "+selectCols+" FROM source_uploads WHERE project_id = ? AND digest = ?", projectID, digest)
	return scanUpload(row)
}

// ListByProject 返回项目上传行（新→旧 + after 游标；ADR-0026 惯例）。
func (r *repo) ListByProject(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]sourceupload.Upload, error) {
	limit = persistence.ClampLimit(limit, 50, 200)
	q := "SELECT " + selectCols + " FROM source_uploads WHERE project_id = ?"
	args := []any{projectID}
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
	var out []sourceupload.Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// BytesByProject 汇总项目上传存量（(project, digest) 唯一使 SUM 即去重值；
// 配额读在 finalize 事务内——ADR-0024 TOCTOU 口径）。
func (r *repo) BytesByProject(ctx context.Context, run state.Runner, projectID string) (int64, error) {
	var sum int64
	err := run.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(size_bytes), 0) FROM source_uploads WHERE project_id = ?", projectID).
		Scan(&sum)
	return sum, err
}

// CountByDigest 数共享同一 blob 的行数（末行删除时 blob 一并删除）。
func (r *repo) CountByDigest(ctx context.Context, run state.Runner, digest string) (int64, error) {
	var n int64
	err := run.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM source_uploads WHERE digest = ?", digest).Scan(&n)
	return n, err
}

// Delete 落删除（清扫路径）。
func (r *repo) Delete(ctx context.Context, run state.Runner, id string) error {
	_, err := run.ExecContext(ctx, "DELETE FROM source_uploads WHERE id = ?", id)
	return err
}

// SweepUnreferenced 清扫过保留窗且未被任何 Revision 引用的行（引用=spec
// 体内含 upload id 字面量——26 字符 ULID 的碰撞面可忽略；Revision 冻结先于
// engine.Submit，部署路径的上传天然受保护）。返回被删行（含 digest，供
// blob refcount GC）。
func (r *repo) SweepUnreferenced(ctx context.Context, run state.Runner, cutoff string) ([]sourceupload.Upload, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT `+selectCols+` FROM source_uploads u
		WHERE u.created_at < ?
		  AND NOT EXISTS (SELECT 1 FROM revisions rv WHERE rv.spec LIKE '%' || u.id || '%')
		ORDER BY u.seq ASC`, cutoff)
	if err != nil {
		return nil, err
	}
	var stale []sourceupload.Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			rows.Close() //nolint:gosec,errcheck // 扫描失败路径的收尾关闭
			return nil, err
		}
		stale = append(stale, *u)
	}
	if err := rows.Err(); err != nil {
		rows.Close() //nolint:gosec,errcheck // 扫描失败路径的收尾关闭
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return stale, nil
}

// scanner 兼容 *sql.Row 与 *sql.Rows。
type scanner interface{ Scan(dest ...any) error }

func scanUpload(row scanner) (*sourceupload.Upload, error) {
	var u sourceupload.Upload
	if err := row.Scan(&u.ID, &u.Seq, &u.ProjectID, &u.Digest, &u.SizeBytes, &u.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &u, nil
}
