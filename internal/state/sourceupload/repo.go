// Package sourceupload 是上传产物聚合 repo（ADR-0019 附录 A，F1.10）：
// 行按 (project_id, digest) 唯一——同项目同内容重传返回同一行；blob 是
// 全局内容寻址文件（DataRoot/uploads/<sha256hex>，internal/upload 管辖），
// 跨项目行共享同一 blob，refcount=行数。
package sourceupload

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Upload 是上传产物行（无状态机——上传是一次性动作，流中断=tmp 丢弃无行）。
type Upload struct {
	ID        string
	ProjectID string
	Digest    string // sha256 hex
	SizeBytes int64
	CreatedAt string
}

// Repo 是上传产物聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

const selectCols = "id, project_id, digest, size_bytes, created_at"

// Create 落行；同 (project, digest) 已有行 → ErrAlreadyExists（finalize 的
// 去重路径先 FindByDigest，此形态只在并发同内容上传时可达）。
func (r *Repo) Create(ctx context.Context, run state.Runner, u *Upload) error {
	u.CreatedAt = state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		INSERT INTO source_uploads (id, project_id, digest, size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.ProjectID, u.Digest, u.SizeBytes, u.CreatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: project %q already holds an upload with digest %s", state.ErrAlreadyExists, u.ProjectID, u.Digest)
	}
	return err
}

// Get 按 ID 读行。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Upload, error) {
	row := run.QueryRowContext(ctx,
		"SELECT "+selectCols+" FROM source_uploads WHERE id = ?", id)
	return scanUpload(row)
}

// FindByDigest 找项目内同内容行（重传去重路径；无行 → ErrNotFound）。
func (r *Repo) FindByDigest(ctx context.Context, run state.Runner, projectID, digest string) (*Upload, error) {
	row := run.QueryRowContext(ctx,
		"SELECT "+selectCols+" FROM source_uploads WHERE project_id = ? AND digest = ?", projectID, digest)
	return scanUpload(row)
}

// ListByProject 返回项目上传行（新→旧 + after 游标；ADR-0026 惯例）。
func (r *Repo) ListByProject(ctx context.Context, run state.Runner, projectID, afterID string, limit int) ([]Upload, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := "SELECT " + selectCols + " FROM source_uploads WHERE project_id = ?"
	args := []any{projectID}
	if afterID != "" {
		q += " AND id < ?"
		args = append(args, afterID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Upload
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
func (r *Repo) BytesByProject(ctx context.Context, run state.Runner, projectID string) (int64, error) {
	var sum int64
	err := run.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(size_bytes), 0) FROM source_uploads WHERE project_id = ?", projectID).
		Scan(&sum)
	return sum, err
}

// CountByDigest 数共享同一 blob 的行数（末行删除时 blob 一并删除）。
func (r *Repo) CountByDigest(ctx context.Context, run state.Runner, digest string) (int64, error) {
	var n int64
	err := run.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM source_uploads WHERE digest = ?", digest).Scan(&n)
	return n, err
}

// Delete 落删除（清扫路径）。
func (r *Repo) Delete(ctx context.Context, run state.Runner, id string) error {
	_, err := run.ExecContext(ctx, "DELETE FROM source_uploads WHERE id = ?", id)
	return err
}

// SweepUnreferenced 清扫过保留窗且未被任何 Revision 引用的行（引用=spec
// 体内含 upload id 字面量——26 字符 ULID 的碰撞面可忽略；Revision 冻结先于
// engine.Submit，部署路径的上传天然受保护）。返回被删行（含 digest，供
// blob refcount GC）。
func (r *Repo) SweepUnreferenced(ctx context.Context, run state.Runner, cutoff string) ([]Upload, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT `+selectCols+` FROM source_uploads u
		WHERE u.created_at < ?
		  AND NOT EXISTS (SELECT 1 FROM revisions rv WHERE rv.spec LIKE '%' || u.id || '%')
		ORDER BY u.id ASC`, cutoff)
	if err != nil {
		return nil, err
	}
	var stale []Upload
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

func scanUpload(row scanner) (*Upload, error) {
	var u Upload
	if err := row.Scan(&u.ID, &u.ProjectID, &u.Digest, &u.SizeBytes, &u.CreatedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &u, nil
}
