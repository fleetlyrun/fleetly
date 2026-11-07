// Package bucketstore 是 Bucket Store 聚合 repo 的 SQLite 实现：行类型与
// Repo 接口住 domain/data/bucketstore（ADR-0075 Repo 接口层；
// persistence/pubsub 同形范本），本包只持不导出实现与构造。
package bucketstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain bucketstore.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain bucketstore.Repo）。
func New(clock state.Clock, run state.Runner) bucketstore.Repo {
	return &repo{clock: clock, run: run}
}

// Create 落一行；同 Project 内同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, b *bucketstore.BucketStore) error {
	seq, err := persistence.NextSeq(ctx, r.run, "bucket_stores")
	if err != nil {
		return err
	}
	b.Seq = seq
	if b.ID == "" {
		b.ID = entityid.Mint(r.clock.Now())
	}
	settings, err := marshalSettings(b.Settings)
	if err != nil {
		return err
	}
	now := state.FormatTime(r.clock.Now())
	b.CreatedAt, b.UpdatedAt, b.Status = now, now, bucketstore.StatusPending
	b.Generation, b.SpecFingerprint = 0, ""
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO bucket_stores (id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb, credentials_ref,
			generation, spec_fingerprint, backup_interval_secs, backup_retention_secs, status,
			last_backup_at, restore_from_backup, restore_error, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?, ?, '', '', '', ?, ?, '')`,
		b.ID, b.Seq, b.ProjectID, b.Name, b.Engine, b.Version, settings, b.CPUMillis, b.MemoryMB, b.CredentialsRef,
		b.BackupIntervalSecs, b.BackupRetentionSecs, b.Status, b.CreatedAt, b.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: bucket store name %q already exists in project", state.ErrAlreadyExists, b.Name)
	}
	return err
}

// Get 按 ID 读活跃行（ADR-0023 统一口径：tombstone 后一律不存在）。
func (r *repo) Get(ctx context.Context, id string) (*bucketstore.BucketStore, error) {
	row := r.run.QueryRowContext(ctx, selectCols+` WHERE id = ? AND deleted_at = ''`, id)
	return scanBucketStore(row.Scan)
}

// GetByName 在 Project 内按名读活跃行。
func (r *repo) GetByName(ctx context.Context, projectID, name string) (*bucketstore.BucketStore, error) {
	row := r.run.QueryRowContext(ctx, selectCols+`
		WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanBucketStore(row.Scan)
}

// ListByProject 新→旧分页（after_seq 游标 = seq 创建序）。
func (r *repo) ListByProject(ctx context.Context, projectID string, afterSeq int64, limit int) ([]bucketstore.BucketStore, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectCols + ` WHERE project_id = ? AND deleted_at = ''`
	args := []any{projectID}
	if afterSeq > 0 {
		q += ` AND seq < ?`
		args = append(args, afterSeq)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	return r.query(ctx, q, args...)
}

// List 返回全部活跃行（收敛环的枚举面；按 seq 升序稳定遍历）。
func (r *repo) List(ctx context.Context) ([]bucketstore.BucketStore, error) {
	return r.query(ctx, selectCols+` WHERE deleted_at = '' ORDER BY seq`)
}

// CountByProject 返回 Project 内活跃行数（受理位配额口径）。
func (r *repo) CountByProject(ctx context.Context, projectID string) (int, error) {
	var n int
	err := r.run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bucket_stores WHERE project_id = ? AND deleted_at = ''`, projectID).Scan(&n)
	if err != nil {
		return 0, persistence.MapScanErr(err)
	}
	return n, nil
}

// EnsureGeneration 是收敛环的下发编号口（database 同律：指纹一致 → 幂等
// 重放同号；变化 → gen+1 与新指纹同事务落行；Ensure 失败不回退）。
func (r *repo) EnsureGeneration(ctx context.Context, id, fingerprint string) (uint64, error) {
	now := state.FormatTime(r.clock.Now())
	var gen uint64
	err := r.run.QueryRowContext(ctx, `
		UPDATE bucket_stores SET generation = generation + 1, spec_fingerprint = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND spec_fingerprint != ?
		RETURNING generation`, fingerprint, now, id, fingerprint).Scan(&gen)
	if err == nil {
		return gen, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		row := r.run.QueryRowContext(ctx,
			`SELECT generation FROM bucket_stores WHERE id = ? AND deleted_at = ''`, id)
		if serr := row.Scan(&gen); serr != nil {
			return 0, persistence.MapScanErr(serr)
		}
		return gen, nil
	}
	return 0, persistence.MapScanErr(err)
}

// SetStatus 落观测状态（同值写入不迁 updated_at；命中 0 行静默返回）。
func (r *repo) SetStatus(ctx context.Context, id, status string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE bucket_stores SET status = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND status != ?`, status, now, id, status)
	return err
}

// UpdateDesired 落期望态面（settings/resources 全量替换；nil 指针 = 该轴
// 无限制；已删/不存在返回 state.ErrNotFound）。
func (r *repo) UpdateDesired(ctx context.Context, id string, settings map[string]string, cpuMillis, memoryMB *int32) error {
	column, err := marshalSettings(settings)
	if err != nil {
		return err
	}
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE bucket_stores SET settings = ?, cpu_millis = ?, memory_mb = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, column, cpuMillis, memoryMB, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("update bucket store %s: %w", id, state.ErrNotFound)
	}
	return nil
}

// SoftDelete 落 tombstone（幂等口径与 Get 一致：已删 = 不存在 → 再删
// state.ErrNotFound）。
func (r *repo) SoftDelete(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE bucket_stores SET deleted_at = ?, updated_at = ?
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

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb, credentials_ref,
		generation, spec_fingerprint, backup_interval_secs, backup_retention_secs, status,
		last_backup_at, restore_from_backup, restore_error, created_at, updated_at, deleted_at
	FROM bucket_stores`

func (r *repo) query(ctx context.Context, q string, args ...any) ([]bucketstore.BucketStore, error) {
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []bucketstore.BucketStore
	for rows.Next() {
		b, err := scanBucketStore(rows.Scan)
		if err != nil {
			return nil, persistence.MapScanErr(err)
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func scanBucketStore(scan func(dest ...any) error) (*bucketstore.BucketStore, error) {
	var b bucketstore.BucketStore
	var settings string
	err := scan(&b.ID, &b.Seq, &b.ProjectID, &b.Name, &b.Engine, &b.Version, &settings,
		&b.CPUMillis, &b.MemoryMB, &b.CredentialsRef,
		&b.Generation, &b.SpecFingerprint, &b.BackupIntervalSecs, &b.BackupRetentionSecs, &b.Status,
		&b.LastBackupAt, &b.RestoreFromBackup, &b.RestoreError,
		&b.CreatedAt, &b.UpdatedAt, &b.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	b.Settings, err = unmarshalSettings(settings)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// marshalSettings 把旋钮子集序列化为列形 JSON（键序稳定——同值恒同字节；
// pubsub repo 同形）。
func marshalSettings(settings map[string]string) (string, error) {
	if len(settings) == 0 {
		return "{}", nil
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type kv struct {
		Key   string `json:"k"`
		Value string `json:"v"`
	}
	pairs := make([]kv, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, kv{Key: k, Value: settings[k]})
	}
	b, err := json.Marshal(pairs)
	if err != nil {
		return "", fmt.Errorf("marshal bucket store settings: %w", err)
	}
	return string(b), nil
}

// unmarshalSettings 反序列化旋钮列（宽容读：空/畸形态落 nil）。
func unmarshalSettings(column string) (map[string]string, error) {
	if column == "" || column == "{}" {
		return nil, nil
	}
	var pairs []struct {
		Key   string `json:"k"`
		Value string `json:"v"`
	}
	if err := json.Unmarshal([]byte(column), &pairs); err != nil {
		return nil, nil
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		out[p.Key] = p.Value
	}
	return out, nil
}
