// Package logcursor 是采集游标的行仓储（F2.4，ADR-0040 决策 2）：每活跃
// 隔离域一行，last_ts 只在 Ingest 成功后推进（断流自愈锚——Since=游标
// 从 docker json-file 缓冲重放补窗）。
package logcursor

import (
	"context"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Repo 是 log_cursors 表的仓储。
type Repo struct {
	clock state.Clock
}

// New 构造仓储。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Get 返回该隔离域的采集游标（无行 = 零值：首采走有界回看窗——
// engine.logFirstCollectLookback，不整灌 docker 既有历史；增量从启用
// 时刻起算，ADR-0040）。
func (r *Repo) Get(ctx context.Context, run state.Runner, namespace string) (time.Time, error) {
	var ts string
	err := run.QueryRowContext(ctx, `SELECT last_ts FROM log_cursors WHERE namespace = ?`, namespace).Scan(&ts)
	if err != nil {
		if state.MapScanErr(err) == state.ErrNotFound {
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
func (r *Repo) Save(ctx context.Context, run state.Runner, namespace string, ts time.Time) error {
	_, err := run.ExecContext(ctx, `
		INSERT INTO log_cursors (namespace, last_ts, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(namespace) DO UPDATE SET last_ts = excluded.last_ts, updated_at = excluded.updated_at`,
		namespace, ts.UTC().Format(time.RFC3339Nano), r.clock.Now().UTC().Format(time.RFC3339Nano))
	return err
}
