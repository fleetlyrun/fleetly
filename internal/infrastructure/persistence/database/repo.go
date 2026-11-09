// Package database 是 Database 聚合 repo 的 SQLite 实现：行类型与 Repo
// 接口住 domain/data/database（ADR-0075 Repo 接口层），本包只持不导出
// 实现与构造。
package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain database.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain database.Repo）。
func New(clock state.Clock, run state.Runner) database.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同 Project 内同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, d *database.Database) error {
	seq, err := persistence.NextSeq(ctx, r.run, "databases")
	if err != nil {
		return err
	}
	d.Seq = seq
	if d.ID == "" {
		d.ID = entityid.Mint(r.clock.Now())
	}
	settings, err := marshalSettings(d.Settings)
	if err != nil {
		return err
	}
	now := state.FormatTime(r.clock.Now())
	d.CreatedAt, d.UpdatedAt, d.Status = now, now, database.StatusPending
	d.Generation, d.SpecFingerprint = 0, ""
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO databases (id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb, credentials_ref,
			generation, spec_fingerprint, backup_interval_secs, backup_retention_secs, status, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?, ?, ?, ?, '')`,
		d.ID, d.Seq, d.ProjectID, d.Name, d.Engine, d.Version, settings, d.CPUMillis, d.MemoryMB, d.CredentialsRef,
		d.BackupIntervalSecs, d.BackupRetentionSecs, d.Status, d.CreatedAt, d.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: database name %q already exists in project", state.ErrAlreadyExists, d.Name)
	}
	return err
}

// Get 按 ID 读活跃行（ADR-0023 统一口径：tombstone 后一律不存在）。
func (r *repo) Get(ctx context.Context, id string) (*database.Database, error) {
	row := r.run.QueryRowContext(ctx, selectCols+` WHERE id = ? AND deleted_at = ''`, id)
	return scanDatabase(row.Scan)
}

// GetByName 在 Project 内按名读活跃行。
func (r *repo) GetByName(ctx context.Context, projectID, name string) (*database.Database, error) {
	row := r.run.QueryRowContext(ctx, selectCols+`
		WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanDatabase(row.Scan)
}

// GetByNameAnyState 在 Project 内按名读最新行（含 tombstone；同名重建后
// 多行同名取最新 seq——B7 volumes purge 预览的关联库披露面）。
func (r *repo) GetByNameAnyState(ctx context.Context, projectID, name string) (*database.Database, error) {
	row := r.run.QueryRowContext(ctx, selectCols+`
		WHERE project_id = ? AND name = ?
		ORDER BY seq DESC LIMIT 1`, projectID, name)
	return scanDatabase(row.Scan)
}

// ListByProject 新→旧分页（ADR-0026 after_* + limit；游标 = seq 创建序）。
func (r *repo) ListByProject(ctx context.Context, projectID string, afterSeq int64, limit int) ([]database.Database, error) {
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

// List 返回全部活跃行（收敛环的枚举面；按 id 升序稳定遍历）。
func (r *repo) List(ctx context.Context) ([]database.Database, error) {
	return r.query(ctx, selectCols+` WHERE deleted_at = '' ORDER BY seq`)
}

// ListRestorePending 返回恢复在途的活跃行（restore_state ∈ pending/
// running；ADR-0039 决策 6 修订——restore_from_backup 是源历史，不再充当
// 在途判据）。
func (r *repo) ListRestorePending(ctx context.Context) ([]database.Database, error) {
	return r.query(ctx,
		selectCols+` WHERE deleted_at = '' AND restore_state IN ('pending', 'running') ORDER BY seq`)
}

// CountByProject 返回 Project 内活跃行数（受理位配额口径）。
func (r *repo) CountByProject(ctx context.Context, projectID string) (int, error) {
	var n int
	err := r.run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM databases WHERE project_id = ? AND deleted_at = ''`, projectID).Scan(&n)
	if err != nil {
		return 0, persistence.MapScanErr(err)
	}
	return n, nil
}

// EnsureGeneration 是收敛环的下发编号口（ADR-0029 决策 3）：投影指纹与
// 行上指纹一致 → 返回现行 gen（幂等重放同号，不触发载体滚动）；变化 →
// gen+1 与新指纹同事务落行并返回新号（Ensure 失败不回退——下一拍指纹
// 已一致，同号干净重试）。已删/不存在返回 ErrNotFound（收敛环跳过）。
func (r *repo) EnsureGeneration(ctx context.Context, id, fingerprint string) (uint64, error) {
	now := state.FormatTime(r.clock.Now())
	var gen uint64
	err := r.run.QueryRowContext(ctx, `
		UPDATE databases SET generation = generation + 1, spec_fingerprint = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND spec_fingerprint != ?
		RETURNING generation`, fingerprint, now, id, fingerprint).Scan(&gen)
	if err == nil {
		return gen, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		// 指纹一致（或行不存在）：一致 → 回现行 gen（幂等重放同号）；
		// 不存在 → ErrNotFound（收敛环跳过）。
		row := r.run.QueryRowContext(ctx,
			`SELECT generation FROM databases WHERE id = ? AND deleted_at = ''`, id)
		if serr := row.Scan(&gen); serr != nil {
			return 0, persistence.MapScanErr(serr)
		}
		return gen, nil
	}
	return 0, persistence.MapScanErr(err)
}

// SetStatus 落观测状态（收敛环从观测缓存推进；WHERE 带 status != ? 守卫
// ——同值写入不迁 updated_at）。命中 0 行静默返回（观测迟到于删除）。
func (r *repo) SetStatus(ctx context.Context, id, status string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE databases SET status = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND status != ?`, status, now, id, status)
	return err
}

// UpdateDesired 落期望态面（B2d，ADR-0095 决策 5/7）：settings 全量替换
// （列形 JSON 键序稳定）+ 资源面全量替换（NULL = 无限制）。值域校验在
// 受理位；本口只承载行写。命中 0 行 = 已删/不存在 → state.ErrNotFound
// （Update 调用方映射 API 404）。
func (r *repo) UpdateDesired(ctx context.Context, id string, settings map[string]string, cpuMillis, memoryMB *int32) error {
	column, err := marshalSettings(settings)
	if err != nil {
		return err
	}
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE databases SET settings = ?, cpu_millis = ?, memory_mb = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, column, cpuMillis, memoryMB, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("update database %s: %w", id, state.ErrNotFound)
	}
	return nil
}

// SoftDelete 落 tombstone（幂等口径与 Get 一致：已删 = 不存在 → 再删
// ErrNotFound）。
func (r *repo) SoftDelete(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE databases SET deleted_at = ?, updated_at = ?
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
		`UPDATE databases SET last_backup_at = ? WHERE id = ?`, at, id)
	return err
}

// SetRestorePending 落恢复挂起（API 创建面消费：restore_from_backup 指向
// 一颗成功备份行 + restore_state = pending；备份环执行后推进终态）。
func (r *repo) SetRestorePending(ctx context.Context, id, backupID string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE databases SET restore_from_backup = ?, restore_state = 'pending', updated_at = ?
		WHERE id = ? AND deleted_at = ''`, backupID, now, id)
	return err
}

// SetRestoreRunning 落恢复执行中（工具容器调用前落位；只从活态推进，
// 终态不被迟到回执覆写）。
func (r *repo) SetRestoreRunning(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE databases SET restore_state = 'running', updated_at = ?
		WHERE id = ? AND deleted_at = '' AND restore_state IN ('pending', 'running')`, now, id)
	return err
}

// SetRestoreSucceeded 落恢复成功终态（restore_from_backup 保留为源历史，
// 不清位；restore_error 同样不动——历史事实；终态不可被迟到回执覆写）。
func (r *repo) SetRestoreSucceeded(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE databases SET restore_state = 'succeeded', updated_at = ?
		WHERE id = ? AND deleted_at = '' AND restore_state IN ('pending', 'running')`, now, id)
	return err
}

// SetRestoreFailed 落恢复失败终态（restore_error 留痕；restore_from_backup
// 保留——permanent 失败不再重试，半恢复态不静默收敛；终态不可覆写）。
func (r *repo) SetRestoreFailed(ctx context.Context, id, errMsg string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE databases SET restore_state = 'failed', restore_error = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND restore_state IN ('pending', 'running')`, errMsg, now, id)
	return err
}

// SweepInterruptedRestores 把重启打断的 running 恢复回退 pending（启动
// 清扫；重试一次是安全上界——再失败即落 failed 终态）。
func (r *repo) SweepInterruptedRestores(ctx context.Context) (int64, error) {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE databases SET restore_state = 'pending', updated_at = ?
		WHERE restore_state = 'running' AND deleted_at = ''`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectCols = `
	SELECT id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb, credentials_ref,
		generation, spec_fingerprint, backup_interval_secs, backup_retention_secs, status,
		last_backup_at, restore_from_backup, restore_state, restore_error, created_at, updated_at, deleted_at
	FROM databases`

func (r *repo) query(ctx context.Context, q string, args ...any) ([]database.Database, error) {
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []database.Database
	for rows.Next() {
		d, err := scanDatabase(rows.Scan)
		if err != nil {
			return nil, persistence.MapScanErr(err)
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func scanDatabase(scan func(dest ...any) error) (*database.Database, error) {
	var d database.Database
	var settings string
	err := scan(&d.ID, &d.Seq, &d.ProjectID, &d.Name, &d.Engine, &d.Version, &settings,
		&d.CPUMillis, &d.MemoryMB, &d.CredentialsRef,
		&d.Generation, &d.SpecFingerprint, &d.BackupIntervalSecs, &d.BackupRetentionSecs, &d.Status,
		&d.LastBackupAt, &d.RestoreFromBackup, &d.RestoreState, &d.RestoreError,
		&d.CreatedAt, &d.UpdatedAt, &d.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	d.Settings, err = unmarshalSettings(settings)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// marshalSettings 把旋钮子集序列化为列形 JSON（键序稳定——同值恒同字节，
// 指纹/ golden 不随 map 遍历序漂移；空集落 '{}' 与列缺省一致）。
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
		return "", fmt.Errorf("marshal database settings: %w", err)
	}
	return string(b), nil
}

// unmarshalSettings 反序列化旋钮列（宽容读：空/畸形态落 nil——旋钮面缺席
// 不应炸毁整行读；受理位已保证落库值合法）。
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
