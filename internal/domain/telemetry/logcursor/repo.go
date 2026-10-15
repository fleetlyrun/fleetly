package logcursor

import (
	"context"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 log_cursors 表的仓储。方法集机械平移自既有 repo 导出面；Runner
// 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件
// 一拍事务，ADR-0005）。行序列化用 RFC3339Nano（游标是亚秒精度面——
// 对 state.FormatTime 的 RFC3339 秒精度惯例的显式豁免，随行迁移本注）。
type Repo interface {
	// Get 返回该隔离域的采集游标（无行 = 零值：首采走有界回看窗——
	// engine.logFirstCollectLookback，不整灌 docker 既有历史；增量从启用
	// 时刻起算，ADR-0040。坏行按无游标处理（首采语义）；不 fail
	// loud——游标是优化面非权威面）。
	Get(ctx context.Context, run state.Runner, namespace string) (time.Time, error)
	// Save 推进游标（upsert；调用方保证只在 Ingest 成功后调用）。
	Save(ctx context.Context, run state.Runner, namespace string, ts time.Time) error
}
