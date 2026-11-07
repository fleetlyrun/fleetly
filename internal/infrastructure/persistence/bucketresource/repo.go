// Package bucketresource 是 Bucket 子资源 repo 的 SQLite 实现：行类型与
// Repo 接口住 domain/data/bucketresource（ADR-0075 Repo 接口层；
// persistence/topic 同形范本），本包只持不导出实现与构造。表名
// bucket_resources（migration 00042——bucket 词保留给 Bucket Store 实体域，
// 表名分形避撞同律）。
package bucketresource

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketresource"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain bucketresource.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain bucketresource.Repo）。
func New(clock state.Clock, run state.Runner) bucketresource.Repo {
	return &repo{clock: clock, run: run}
}

// Create 落一行；同父实例内同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, b *bucketresource.Bucket) error {
	seq, err := persistence.NextSeq(ctx, r.run, "bucket_resources")
	if err != nil {
		return err
	}
	b.Seq = seq
	if b.ID == "" {
		b.ID = entityid.Mint(r.clock.Now())
	}
	now := state.FormatTime(r.clock.Now())
	b.CreatedAt, b.UpdatedAt, b.Status = now, now, bucketresource.StatusPending
	b.StatusErr = ""
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO bucket_resources (id, seq, project_id, bucket_store_id, name, quota_bytes,
			status, status_err, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, '')`,
		b.ID, b.Seq, b.ProjectID, b.BucketStoreID, b.Name, b.QuotaBytes,
		b.Status, b.CreatedAt, b.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: bucket name %q already exists on the bucket store instance", state.ErrAlreadyExists, b.Name)
	}
	return err
}

// Get 按 ID 读行（ADR-0023 统一口径：tombstone 后一律不存在）。
func (r *repo) Get(ctx context.Context, id string) (*bucketresource.Bucket, error) {
	row := r.run.QueryRowContext(ctx, selectCols+` WHERE id = ? AND deleted_at = ''`, id)
	return scanBucket(row.Scan)
}

// ListByBucketStore 返回父实例下全部行（含 tombstone——ensure 面 + GC 面
// 共用）。
func (r *repo) ListByBucketStore(ctx context.Context, bucketStoreID string) ([]bucketresource.Bucket, error) {
	return r.query(ctx, selectCols+` WHERE bucket_store_id = ? ORDER BY seq`, bucketStoreID)
}

// CountByBucketStore 返回父实例下活跃行数（per 实例配额口径：buckets 50）。
func (r *repo) CountByBucketStore(ctx context.Context, bucketStoreID string) (int, error) {
	var n int
	err := r.run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bucket_resources WHERE bucket_store_id = ? AND deleted_at = ''`, bucketStoreID).Scan(&n)
	if err != nil {
		return 0, persistence.MapScanErr(err)
	}
	return n, nil
}

// UpdateDesired 落期望态面（quota 全量替换）。
func (r *repo) UpdateDesired(ctx context.Context, id string, quotaBytes *int64) error {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE bucket_resources SET quota_bytes = ?, status = ?, status_err = '', updated_at = ?
		WHERE id = ? AND deleted_at = ''`, quotaBytes, bucketresource.StatusPending, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("update bucket %s: %w", id, state.ErrNotFound)
	}
	return nil
}

// SetStatus 落对账状态（同值写入不迁 updated_at；命中 0 行静默返回）。
func (r *repo) SetStatus(ctx context.Context, id, status, statusErr string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE bucket_resources SET status = ?, status_err = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND (status != ? OR status_err != ?)`,
		status, statusErr, now, id, status, statusErr)
	return err
}

// SoftDelete 落 tombstone（= 声明式 GC 挂起；幂等口径与 Get 一致）。
func (r *repo) SoftDelete(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE bucket_resources SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, now, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return state.ErrNotFound
	}
	return nil
}

// Delete 物理删行（收敛段 GC 成功路径；幂等：不存在静默成功）。
func (r *repo) Delete(ctx context.Context, id string) error {
	_, err := r.run.ExecContext(ctx, `DELETE FROM bucket_resources WHERE id = ?`, id)
	return err
}

// DeleteByBucketStore 物理删父实例下全部行（父实例删除的级联收口；幂等）。
func (r *repo) DeleteByBucketStore(ctx context.Context, bucketStoreID string) error {
	_, err := r.run.ExecContext(ctx, `DELETE FROM bucket_resources WHERE bucket_store_id = ?`, bucketStoreID)
	return err
}

const selectCols = `
	SELECT id, seq, project_id, bucket_store_id, name, quota_bytes,
		status, status_err, created_at, updated_at, deleted_at
	FROM bucket_resources`

func (r *repo) query(ctx context.Context, q string, args ...any) ([]bucketresource.Bucket, error) {
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []bucketresource.Bucket
	for rows.Next() {
		b, err := scanBucket(rows.Scan)
		if err != nil {
			return nil, persistence.MapScanErr(err)
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func scanBucket(scan func(dest ...any) error) (*bucketresource.Bucket, error) {
	var b bucketresource.Bucket
	err := scan(&b.ID, &b.Seq, &b.ProjectID, &b.BucketStoreID, &b.Name, &b.QuotaBytes,
		&b.Status, &b.StatusErr, &b.CreatedAt, &b.UpdatedAt, &b.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &b, nil
}
