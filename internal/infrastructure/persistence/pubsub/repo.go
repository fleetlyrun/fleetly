// Package pubsub 是 PubSub 聚合 repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/data/pubsub（ADR-0075 Repo 接口层；persistence/database 同形范本），
// 本包只持不导出实现与构造。
package pubsub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain pubsub.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain pubsub.Repo）。
func New(clock state.Clock, run state.Runner) pubsub.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同 Project 内同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, p *pubsub.PubSub) error {
	seq, err := persistence.NextSeq(ctx, r.run, "pubsubs")
	if err != nil {
		return err
	}
	p.Seq = seq
	if p.ID == "" {
		p.ID = entityid.Mint(r.clock.Now())
	}
	settings, err := marshalSettings(p.Settings)
	if err != nil {
		return err
	}
	now := state.FormatTime(r.clock.Now())
	p.CreatedAt, p.UpdatedAt, p.Status = now, now, pubsub.StatusPending
	p.Generation, p.SpecFingerprint = 0, ""
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO pubsubs (id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb, credentials_ref,
			generation, spec_fingerprint, backup_interval_secs, backup_retention_secs, status,
			last_backup_at, restore_from_backup, restore_error, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?, ?, '', '', '', ?, ?, '')`,
		p.ID, p.Seq, p.ProjectID, p.Name, p.Engine, p.Version, settings, p.CPUMillis, p.MemoryMB, p.CredentialsRef,
		p.BackupIntervalSecs, p.BackupRetentionSecs, p.Status, p.CreatedAt, p.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: pubsub name %q already exists in project", state.ErrAlreadyExists, p.Name)
	}
	return err
}

// Get 按 ID 读活跃行（ADR-0023 统一口径：tombstone 后一律不存在）。
func (r *repo) Get(ctx context.Context, id string) (*pubsub.PubSub, error) {
	row := r.run.QueryRowContext(ctx, selectCols+` WHERE id = ? AND deleted_at = ''`, id)
	return scanPubSub(row.Scan)
}

// GetByName 在 Project 内按名读活跃行。
func (r *repo) GetByName(ctx context.Context, projectID, name string) (*pubsub.PubSub, error) {
	row := r.run.QueryRowContext(ctx, selectCols+`
		WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanPubSub(row.Scan)
}

// ListByProject 新→旧分页（after_seq 游标 = seq 创建序）。
func (r *repo) ListByProject(ctx context.Context, projectID string, afterSeq int64, limit int) ([]pubsub.PubSub, error) {
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
func (r *repo) List(ctx context.Context) ([]pubsub.PubSub, error) {
	return r.query(ctx, selectCols+` WHERE deleted_at = '' ORDER BY seq`)
}

// ListRestorePending 返回恢复挂起中的活跃行（备份环恢复面）。
func (r *repo) ListRestorePending(ctx context.Context) ([]pubsub.PubSub, error) {
	return r.query(ctx,
		selectCols+` WHERE deleted_at = '' AND restore_from_backup != '' ORDER BY seq`)
}

// CountByProject 返回 Project 内活跃行数（受理位配额口径）。
func (r *repo) CountByProject(ctx context.Context, projectID string) (int, error) {
	var n int
	err := r.run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pubsubs WHERE project_id = ? AND deleted_at = ''`, projectID).Scan(&n)
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
		UPDATE pubsubs SET generation = generation + 1, spec_fingerprint = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND spec_fingerprint != ?
		RETURNING generation`, fingerprint, now, id, fingerprint).Scan(&gen)
	if err == nil {
		return gen, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		row := r.run.QueryRowContext(ctx,
			`SELECT generation FROM pubsubs WHERE id = ? AND deleted_at = ''`, id)
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
		UPDATE pubsubs SET status = ?, updated_at = ?
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
		UPDATE pubsubs SET settings = ?, cpu_millis = ?, memory_mb = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, column, cpuMillis, memoryMB, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("update pubsub %s: %w", id, state.ErrNotFound)
	}
	return nil
}

// SoftDelete 落 tombstone（幂等口径与 Get 一致：已删 = 不存在 → 再删
// state.ErrNotFound）。
func (r *repo) SoftDelete(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE pubsubs SET deleted_at = ?, updated_at = ?
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

// SetLastBackupAt 落调度锚（成功 Backup 的完成时刻；幂等写）。
func (r *repo) SetLastBackupAt(ctx context.Context, id, at string) error {
	_, err := r.run.ExecContext(ctx,
		`UPDATE pubsubs SET last_backup_at = ? WHERE id = ?`, at, id)
	return err
}

// SetRestorePending 落恢复挂起（备份环执行后清位）。
func (r *repo) SetRestorePending(ctx context.Context, id, backupID string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE pubsubs SET restore_from_backup = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, backupID, now, id)
	return err
}

// SetRestoreError 落恢复失败事实（挂起清位 + error 留痕）。
func (r *repo) SetRestoreError(ctx context.Context, id, errMsg string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE pubsubs SET restore_from_backup = '', restore_error = ?, updated_at = ?
		WHERE id = ?`, errMsg, now, id)
	return err
}

// ClearRestoreSucceeded 清恢复挂起（成功路径；restore_error 不动）。
func (r *repo) ClearRestoreSucceeded(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE pubsubs SET restore_from_backup = '', updated_at = ?
		WHERE id = ?`, now, id)
	return err
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb, credentials_ref,
		generation, spec_fingerprint, backup_interval_secs, backup_retention_secs, status,
		last_backup_at, restore_from_backup, restore_error, created_at, updated_at, deleted_at
	FROM pubsubs`

func (r *repo) query(ctx context.Context, q string, args ...any) ([]pubsub.PubSub, error) {
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []pubsub.PubSub
	for rows.Next() {
		p, err := scanPubSub(rows.Scan)
		if err != nil {
			return nil, persistence.MapScanErr(err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanPubSub(scan func(dest ...any) error) (*pubsub.PubSub, error) {
	var p pubsub.PubSub
	var settings string
	err := scan(&p.ID, &p.Seq, &p.ProjectID, &p.Name, &p.Engine, &p.Version, &settings,
		&p.CPUMillis, &p.MemoryMB, &p.CredentialsRef,
		&p.Generation, &p.SpecFingerprint, &p.BackupIntervalSecs, &p.BackupRetentionSecs, &p.Status,
		&p.LastBackupAt, &p.RestoreFromBackup, &p.RestoreError,
		&p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	p.Settings, err = unmarshalSettings(settings)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// marshalSettings 把旋钮子集序列化为列形 JSON（键序稳定——同值恒同字节；
// database repo 同形）。
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
		return "", fmt.Errorf("marshal pubsub settings: %w", err)
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
