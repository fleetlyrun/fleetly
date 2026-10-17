// Package logcursor 是采集游标 repo 的 SQLite 实现：Repo 接口住
// domain/telemetry/logcursor（ADR-0075 Repo 接口层），本包只持不导出实现
// 与构造（行序列化 RFC3339Nano——亚秒精度面，注释随契约在 domain 侧）。
package logcursor

import (
	"context"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/logcursor"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain logcursor.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain logcursor.Repo）。
func New(clock state.Clock, run state.Runner) logcursor.Repo { return &repo{clock: clock, run: run} }

// Get 返回该隔离域的采集游标（无行 = 零值：首采走有界回看窗——
// engine.logFirstCollectLookback，不整灌 docker 既有历史；增量从启用
// 时刻起算，ADR-0040）。
func (r *repo) Get(ctx context.Context, namespace string) (time.Time, error) {
	var ts string
	err := r.run.QueryRowContext(ctx, `SELECT last_ts FROM log_cursors WHERE namespace = ?`, namespace).Scan(&ts)
	if err != nil {
		if persistence.MapScanErr(err) == state.ErrNotFound {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if ts == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, nil // 坏行按无游标处理（首采语义）；不 fail loud——游标是优化面非权威面
	}
	return parsed, nil
}

// Save 推进游标（upsert；调用方保证只在 Ingest 成功后调用）。
func (r *repo) Save(ctx context.Context, namespace string, ts time.Time) error {
	_, err := r.run.ExecContext(ctx, `
		INSERT INTO log_cursors (namespace, last_ts, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(namespace) DO UPDATE SET last_ts = excluded.last_ts, updated_at = excluded.updated_at`,
		namespace, ts.UTC().Format(time.RFC3339Nano), r.clock.Now().UTC().Format(time.RFC3339Nano))
	return err
}
