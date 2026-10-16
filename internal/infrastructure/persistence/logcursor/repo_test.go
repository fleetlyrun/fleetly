package logcursor_test

// LogCursor 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：单游标
// Get/Save 两方法——无行回落零值（首采走有界回看窗，ADR-0040）、Save 落
// 游标 RFC3339Nano 亚秒精度读回（对秒精度惯例的显式豁免）、Save upsert
// 推进（同 namespace 覆写）、空串/坏行按无游标处理（游标是优化面非权威
// 面，不 fail loud——repo 注释口径）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	logcursorrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/logcursor"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// TestLogCursorZeroFallbackAndAdvance：零值回落 → 落游标 → 推进 → 坏行豁免。
func TestLogCursorZeroFallbackAndAdvance(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	cursors := logcursorrepo.New(clock)

	const ns = "task:web:app-logs"

	// 无行 → 零值 + 无错（首采语义：调用方走有界回看窗）。
	got, err := cursors.Get(ctx, db.Runner(), ns)
	require.NoError(t, err)
	assert.True(t, got.IsZero(), "a missing cursor row falls back to the zero time")

	// Save 落游标：亚秒精度 RFC3339Nano 读回（采样时刻不丢纳秒）。
	ts := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	require.NoError(t, cursors.Save(ctx, db.Runner(), ns, ts))
	got, err = cursors.Get(ctx, db.Runner(), ns)
	require.NoError(t, err)
	assert.True(t, got.Equal(ts), "saved cursor must round-trip with sub-second precision")

	// Save 再落同 namespace：upsert 推进（Ingest 成功后才前移的调用方
	// 契约在此只验存储面覆盖写）。
	later := ts.Add(90 * time.Second)
	require.NoError(t, cursors.Save(ctx, db.Runner(), ns, later))
	got, err = cursors.Get(ctx, db.Runner(), ns)
	require.NoError(t, err)
	assert.True(t, got.Equal(later), "a later Save advances the cursor in place")

	// 双 namespace 互不串台。
	other, err := cursors.Get(ctx, db.Runner(), "db:pg-main")
	require.NoError(t, err)
	assert.True(t, other.IsZero(), "cursors are per-namespace facts")

	// 空串 last_ts 行（迁移缺省形态）→ 按无游标处理。
	_, err = db.Runner().ExecContext(ctx,
		`INSERT INTO log_cursors (namespace, last_ts, updated_at) VALUES ('task:empty', '', '2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	got, err = cursors.Get(ctx, db.Runner(), "task:empty")
	require.NoError(t, err)
	assert.True(t, got.IsZero(), "an empty last_ts reads back as no cursor")

	// 坏行（不可解析时刻）→ 按无游标处理、不 fail loud（优化面诚实豁免）。
	_, err = db.Runner().ExecContext(ctx,
		`INSERT INTO log_cursors (namespace, last_ts, updated_at) VALUES ('task:corrupt', 'not-a-timestamp', '2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	got, err = cursors.Get(ctx, db.Runner(), "task:corrupt")
	require.NoError(t, err, "a corrupt cursor row must not fail loud")
	assert.True(t, got.IsZero())
}
