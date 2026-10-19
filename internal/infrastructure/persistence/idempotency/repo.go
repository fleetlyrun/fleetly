// Package idempotency 是幂等记录聚合 repo 的 SQLite 实现：行类型与 Repo
// 接口住 domain/automation/idempotency（ADR-0075 Repo 接口层），本包只持
// 不导出实现与构造。
package idempotency

import (
	"context"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/idempotency"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain idempotency.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain idempotency.Repo）。
func New(clock state.Clock, run state.Runner) idempotency.Repo { return &repo{clock: clock, run: run} }

// Get 在作用域内按键直读（含过期行——过期判定与惰性清理在调用方）。
// 作用域外同键不可见（H11 / R14-H4）。
func (r *repo) Get(ctx context.Context, scope, key string) (*idempotency.Record, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT scope, idem_key, method, fingerprint, state, response_type, response_body, created_at, expires_at
		FROM idempotency_records WHERE scope = ? AND idem_key = ?`, scope, key)
	var rec idempotency.Record
	var createdAt, expiresAt string
	if err := row.Scan(&rec.Scope, &rec.Key, &rec.Method, &rec.Fingerprint, &rec.State,
		&rec.ResponseType, &rec.ResponseBody, &createdAt, &expiresAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	var err error
	if rec.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return nil, err
	}
	if rec.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt); err != nil {
		return nil, err
	}
	return &rec, nil
}

// Claim 在作用域内落一条 inflight 认领（(scope, key) 不存在时）。同一
// (scope, key) 已被认领/完成返回 state.ErrAlreadyExists——调用方据此进入
// 重放/冲突分诊，不覆盖任何在册行；异作用域同键各自独立认领。
func (r *repo) Claim(ctx context.Context, scope, key, method, fingerprint string, claimTTL time.Duration) error {
	now := r.clock.Now()
	_, err := r.run.ExecContext(ctx, `
		INSERT INTO idempotency_records (scope, idem_key, method, fingerprint, state, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		scope, key, method, fingerprint, idempotency.StateInflight,
		state.FormatTime(now), state.FormatTime(now.Add(claimTTL)))
	if persistence.IsUniqueViolation(err) {
		return state.ErrAlreadyExists
	}
	return err
}

// Complete 把作用域内的 inflight 行落为完成态：携带响应引用（proto 全名 +
// 确定性序列化体）与 24h 保留窗。行不存在（已被清理/释放）返回
// ErrNotFound——幂等承诺随行消失，调用方按无记录处理（不复活）。
func (r *repo) Complete(ctx context.Context, scope, key, responseType string, body []byte, retention time.Duration) error {
	res, err := r.run.ExecContext(ctx, `
		UPDATE idempotency_records
		SET state = ?, response_type = ?, response_body = ?, expires_at = ?
		WHERE scope = ? AND idem_key = ?`,
		idempotency.StateCompleted, responseType, body,
		state.FormatTime(r.clock.Now().Add(retention)), scope, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return state.ErrNotFound
	}
	return nil
}

// Release 删除作用域内的认领行（handler 失败路径：允许同键干净重试——失败
// 请求无响应可重放）。作用域外同键不受影响。
func (r *repo) Release(ctx context.Context, scope, key string) error {
	_, err := r.run.ExecContext(ctx,
		`DELETE FROM idempotency_records WHERE scope = ? AND idem_key = ?`, scope, key)
	return err
}

// Sweep 清理保留窗外的一切行（janitor 周期执行；返回清理行数供观测）。
func (r *repo) Sweep(ctx context.Context) (int64, error) {
	res, err := r.run.ExecContext(ctx,
		`DELETE FROM idempotency_records WHERE expires_at < ?`,
		state.FormatTime(r.clock.Now()))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
