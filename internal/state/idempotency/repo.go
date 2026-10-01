// Package idempotency 是通用幂等记录聚合 repo（ADR-0024 F1.1）：存储面
// 只管行事实（claim/complete/release/sweep），执法语义（指纹计算、重放、
// 冲突分诊）在 internal/idem 拦截器收口。
package idempotency

import (
	"context"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// 行状态。
const (
	StateInflight  = "inflight"
	StateCompleted = "completed"
)

// Record 是幂等记录行。
type Record struct {
	Key          string
	Method       string
	Fingerprint  string
	State        string
	ResponseType string
	ResponseBody []byte
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// Repo 是幂等记录聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Get 按键直读（含过期行——过期判定与惰性清理在调用方，见 internal/idem）。
func (r *Repo) Get(ctx context.Context, run state.Runner, key string) (*Record, error) {
	row := run.QueryRowContext(ctx, `
		SELECT idem_key, method, fingerprint, state, response_type, response_body, created_at, expires_at
		FROM idempotency_records WHERE idem_key = ?`, key)
	var rec Record
	var createdAt, expiresAt string
	if err := row.Scan(&rec.Key, &rec.Method, &rec.Fingerprint, &rec.State,
		&rec.ResponseType, &rec.ResponseBody, &createdAt, &expiresAt); err != nil {
		return nil, state.MapScanErr(err)
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

// Claim 落一条 inflight 认领（键不存在时）。键已被认领/完成返回
// state.ErrAlreadyExists——调用方据此进入重放/冲突分诊，不覆盖任何在册行。
func (r *Repo) Claim(ctx context.Context, run state.Runner, key, method, fingerprint string, claimTTL time.Duration) error {
	now := r.clock.Now()
	_, err := run.ExecContext(ctx, `
		INSERT INTO idempotency_records (idem_key, method, fingerprint, state, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		key, method, fingerprint, StateInflight,
		state.FormatTime(now), state.FormatTime(now.Add(claimTTL)))
	if state.IsUniqueViolation(err) {
		return state.ErrAlreadyExists
	}
	return err
}

// Complete 把 inflight 行落为完成态：携带响应引用（proto 全名 + 确定性
// 序列化体）与 24h 保留窗。行不存在（已被清理/释放）返回 ErrNotFound——
// 幂等承诺随行消失，调用方按无记录处理（不复活）。
func (r *Repo) Complete(ctx context.Context, run state.Runner, key, responseType string, body []byte, retention time.Duration) error {
	res, err := run.ExecContext(ctx, `
		UPDATE idempotency_records
		SET state = ?, response_type = ?, response_body = ?, expires_at = ?
		WHERE idem_key = ?`,
		StateCompleted, responseType, body,
		state.FormatTime(r.clock.Now().Add(retention)), key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return state.ErrNotFound
	}
	return nil
}

// Release 删除认领行（handler 失败路径：允许同键干净重试——失败请求无
// 响应可重放）。
func (r *Repo) Release(ctx context.Context, run state.Runner, key string) error {
	_, err := run.ExecContext(ctx, `DELETE FROM idempotency_records WHERE idem_key = ?`, key)
	return err
}

// Sweep 清理保留窗外的一切行（janitor 周期执行；返回清理行数供观测）。
func (r *Repo) Sweep(ctx context.Context, run state.Runner) (int64, error) {
	res, err := run.ExecContext(ctx,
		`DELETE FROM idempotency_records WHERE expires_at < ?`,
		state.FormatTime(r.clock.Now()))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
