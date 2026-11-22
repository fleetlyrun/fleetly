// Package function 是 Function 聚合 repo 的 SQLite 实现（ADR-0102 任务②）：
// 行类型与 Repo/RevisionRepo 接口住 domain/delivery/function（ADR-0075 Repo
// 接口层），本包只持不导出实现与构造。
package function

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain function.Repo 的 SQLite 实现（聚合根行）。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造聚合根 repo。
func New(clock state.Clock, run state.Runner) function.Repo { return &repo{clock: clock, run: run} }

// Create 落一行（ID 空 = ULID 铸造；Seq 零值 = 计数器铸造）。行面零值
// 归一（exposure 空 → private、容量三列 ≤0 → 1）——行形状归一非业务
// 仲裁：[min,max] 交叉与出界拒留在受理位单点（设计 §8）。
func (r *repo) Create(ctx context.Context, f *function.Function) error {
	if f.Exposure == "" {
		f.Exposure = "private"
	}
	if f.Replicas < 0 {
		f.Replicas = 1
	}
	if f.MinReplicas < 0 {
		f.MinReplicas = 1
	}
	if f.MaxReplicas <= 0 {
		f.MaxReplicas = 1
	}
	now := state.FormatTime(r.clock.Now())
	f.CreatedAt, f.UpdatedAt = now, now
	seq, err := persistence.NextSeq(ctx, r.run, "functions")
	if err != nil {
		return err
	}
	f.Seq = seq
	if f.ID == "" {
		f.ID = entityid.Mint(r.clock.Now())
	}
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO functions
			(id, seq, project_id, name, status, current_revision_id, pending_revision_id, generation,
			 spec_fingerprint, replicas, min_replicas, max_replicas, exposure,
			 public_host, idle_timeout_secs, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		f.ID, f.Seq, f.ProjectID, f.Name, f.Status, f.CurrentRevisionID, f.PendingRevisionID, f.Generation,
		f.SpecFingerprint, f.Replicas, f.MinReplicas, f.MaxReplicas, f.Exposure,
		f.PublicHost, f.IdleTimeoutSecs, f.CreatedAt, f.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: active function with the same name already exists in the project", state.ErrAlreadyExists)
	}
	return err
}

// Get 按 ID 直读（活跃档）。
func (r *repo) Get(ctx context.Context, id string) (*function.Function, error) {
	row := r.run.QueryRowContext(ctx, selectFunctionCols+`
		FROM functions WHERE id = ? AND deleted_at = ''`, id)
	return scanFunction(row.Scan)
}

// GetByName 按项目 + 名字读活跃行（部分唯一索引保证至多一行）。
func (r *repo) GetByName(ctx context.Context, projectID, name string) (*function.Function, error) {
	row := r.run.QueryRowContext(ctx, selectFunctionCols+`
		FROM functions WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanFunction(row.Scan)
}

// ListByProject 按项目列活跃行（升序 + after 游标 + 可选过滤）。
func (r *repo) ListByProject(ctx context.Context, projectID string, afterSeq int64, limit int, exposure, state string) ([]function.Function, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := selectFunctionCols + ` FROM functions WHERE project_id = ? AND deleted_at = ''`
	args := []any{projectID}
	if afterSeq > 0 {
		q += " AND seq > ?"
		args = append(args, afterSeq)
	}
	if exposure != "" {
		q += " AND exposure = ?"
		args = append(args, exposure)
	}
	if state != "" {
		q += " AND status = ?"
		args = append(args, state)
	}
	q += " ORDER BY seq ASC LIMIT ?"
	args = append(args, limit)
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return persistence.Collect(rows, collectFunction)
}

// SetCapacity 落容量期望（行写；受理位已钳制，repo 不二次仲裁）。
func (r *repo) SetCapacity(ctx context.Context, id string, replicas int64) error {
	return r.touch(ctx, id, "UPDATE functions SET replicas = ?, updated_at = ? WHERE id = ? AND deleted_at = ''", replicas, r.now(), id)
}

// SetPending 落/清构建就绪门意图锚（P2-3；"" = 清位）。
func (r *repo) SetPending(ctx context.Context, id, revisionID string) error {
	return r.touch(ctx, id, "UPDATE functions SET pending_revision_id = ?, updated_at = ? WHERE id = ? AND deleted_at = ''", revisionID, r.now(), id)
}

// SetIdleTimeout 落缩零/缩容防抖窗行旋钮（P2-2 决策环；0 = 平台缺省）。
func (r *repo) SetIdleTimeout(ctx context.Context, id string, secs int64) error {
	return r.touch(ctx, id, "UPDATE functions SET idle_timeout_secs = ?, updated_at = ? WHERE id = ? AND deleted_at = ''", secs, r.now(), id)
}

// DecideCapacity 是决策环的条件容量 CAS（P2-2）：(replicas, updated_at)
// 对锚乐观并发——行未被他写变才落目标副本，落败方下拍重算。
func (r *repo) DecideCapacity(ctx context.Context, id string, replicas, sawReplicas int64, sawUpdatedAt string) (bool, error) {
	res, err := r.run.ExecContext(ctx, `
		UPDATE functions SET replicas = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND replicas = ? AND updated_at = ?`,
		replicas, r.now(), id, sawReplicas, sawUpdatedAt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetExposure 落接入两列。
func (r *repo) SetExposure(ctx context.Context, id, exposure, publicHost string) error {
	return r.touch(ctx, id, "UPDATE functions SET exposure = ?, public_host = ?, updated_at = ? WHERE id = ? AND deleted_at = ''", exposure, publicHost, r.now(), id)
}

// SwapCurrent 是原子换轨（指针 + 指纹同拍）。
func (r *repo) SwapCurrent(ctx context.Context, id, revisionID, fingerprint string) error {
	return r.touch(ctx, id, "UPDATE functions SET current_revision_id = ?, spec_fingerprint = ?, updated_at = ? WHERE id = ? AND deleted_at = ''", revisionID, fingerprint, r.now(), id)
}

// AdvanceStatus 推进观测缓存。
func (r *repo) AdvanceStatus(ctx context.Context, id, status string) error {
	return r.touch(ctx, id, "UPDATE functions SET status = ?, updated_at = ? WHERE id = ? AND deleted_at = ''", status, r.now(), id)
}

// EnsureGeneration 是指纹 CAS 的行代数口：未变返现行、变更 +1 同拍落指纹。
func (r *repo) EnsureGeneration(ctx context.Context, id, fingerprint string) (uint64, error) {
	var gen uint64
	err := r.run.QueryRowContext(ctx,
		`UPDATE functions SET generation = generation + 1, spec_fingerprint = ?, updated_at = ?
		 WHERE id = ? AND deleted_at = '' AND spec_fingerprint <> ? RETURNING generation`,
		fingerprint, r.now(), id, fingerprint).Scan(&gen)
	switch {
	case err == nil:
		return gen, nil
	case errors.Is(err, sql.ErrNoRows):
		// 零命中两义：指纹未变（返现行）或行不在活跃档（ErrNotFound）。
		row := r.run.QueryRowContext(ctx,
			`SELECT generation FROM functions WHERE id = ? AND deleted_at = '' AND spec_fingerprint = ?`, id, fingerprint)
		if gerr := row.Scan(&gen); gerr != nil {
			return 0, persistence.MapScanErr(gerr)
		}
		return gen, nil
	default:
		return 0, err
	}
}

// StatsByProject 返回项目活跃函数数与 replicas 之和（配额读面）。
func (r *repo) StatsByProject(ctx context.Context, projectID string) (int, int64, error) {
	var active int
	var sum int64
	err := r.run.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(replicas), 0) FROM functions WHERE project_id = ? AND deleted_at = ''`,
		projectID).Scan(&active, &sum)
	if err != nil {
		return 0, 0, err
	}
	return active, sum, nil
}

// List 列全部活跃行（收敛环扫描面；seq 升序）。
func (r *repo) List(ctx context.Context) ([]function.Function, error) {
	rows, err := r.run.QueryContext(ctx,
		selectFunctionCols+` FROM functions WHERE deleted_at = '' ORDER BY seq ASC`)
	if err != nil {
		return nil, err
	}
	return persistence.Collect(rows, collectFunction)
}

// RequestWake 是唤醒条件容量 CAS（P2-1a；设计 v2 §1.2）：replicas=0 时
// 落 rung=max(1,min_replicas)，CAS 条件同时给幂等与防踩踏。
func (r *repo) RequestWake(ctx context.Context, id string) (bool, error) {
	res, err := r.run.ExecContext(ctx, `
		UPDATE functions SET replicas = MAX(1, min_replicas), updated_at = ?
		WHERE id = ? AND deleted_at = '' AND replicas = 0`, r.now(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SoftDelete 落 tombstone（幂等：已删行零命中零报错）。
func (r *repo) SoftDelete(ctx context.Context, id string) error {
	return r.touch(ctx, id, "UPDATE functions SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at = ''", r.now(), r.now(), id)
}

// ---- 内部帮手 ----

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const selectFunctionCols = `
	SELECT id, seq, project_id, name, status, current_revision_id, pending_revision_id, generation,
	       spec_fingerprint, replicas, min_replicas, max_replicas, exposure,
	       public_host, idle_timeout_secs, created_at, updated_at, deleted_at`

func (r *repo) now() string { return state.FormatTime(r.clock.Now()) }

// touch 是单行条件写的公共骨架：命中零行 = ErrNotFound（活跃档口径——
// 不存在与已 tombstone 同形 404）。
func (r *repo) touch(ctx context.Context, id string, q string, args ...any) error {
	res, err := r.run.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: function %s", state.ErrNotFound, id)
	}
	return nil
}

func scanFunction(scan func(dest ...any) error) (*function.Function, error) {
	var f function.Function
	if err := scan(&f.ID, &f.Seq, &f.ProjectID, &f.Name, &f.Status, &f.CurrentRevisionID,
		&f.PendingRevisionID, &f.Generation, &f.SpecFingerprint, &f.Replicas, &f.MinReplicas, &f.MaxReplicas,
		&f.Exposure, &f.PublicHost, &f.IdleTimeoutSecs, &f.CreatedAt, &f.UpdatedAt, &f.DeletedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &f, nil
}

// collectFunction 是 persistence.Collect 的行函数（scanFunction 解引用形态）。
func collectFunction(rows *sql.Rows) (function.Function, error) {
	f, err := scanFunction(rows.Scan)
	if err != nil {
		return function.Function{}, err
	}
	return *f, nil
}
