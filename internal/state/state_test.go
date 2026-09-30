package state_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// 四件一拍组合（ADR-0005）：状态 CAS + Outbox 事件 + 审计（本例无
// tombstone——部署记录永不删）在同一个 Tx 内落库；任一件失败全回滚。
func TestFourWriteSameTransaction(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	deployments := deployment.New(clock)
	events := outbox.New(clock)
	audits := audit.New(clock)

	d := &deployment.Deployment{
		ID: "01JD0DEPLOY0000000000000000", AppID: "01JD0APP000000000000000000",
		ToRevision: "01JD0REV000000000000000000", State: deployment.StateQueued,
	}

	var seq int64
	err := db.Tx(ctx, func(tx *sql.Tx) error {
		if err := deployments.Create(ctx, tx, d); err != nil {
			return err
		}
		var err error
		seq, err = events.Append(ctx, tx, "deployment.queued", "deployment", d.ID, []byte(`{}`))
		if err != nil {
			return err
		}
		return audits.Append(ctx, tx, &audit.Entry{
			ID: "01JD0AUDIT00000000000000000", Source: audit.SourceCLI,
			Action: "deployment.create", Resource: "deployment/" + d.ID,
		})
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)

	// 回滚路径：Outbox 未注册事件名使事务失败 → Deployment 行不得残留。
	bad := &deployment.Deployment{
		ID: "01JD0DEPLOY0000000000000001", AppID: d.AppID,
		ToRevision: d.ToRevision, State: deployment.StateQueued,
	}
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		if err := deployments.Create(ctx, tx, bad); err != nil {
			return err
		}
		_, err := events.Append(ctx, tx, "not.registered", "deployment", bad.ID, nil)
		return err
	})
	require.Error(t, err)
	_, err = deployments.Get(ctx, db.Runner(), bad.ID)
	assert.ErrorIs(t, err, state.ErrNotFound, "rolled-back deployment must not persist")
}

// WAL 生效与重复 Open 幂等（goose 迁移只前滚，二次打开不重复执行）。
func TestOpenIdempotentAndWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fleetly.db")
	db, err := state.Open(ctx, path, nil)
	require.NoError(t, err)

	var mode string
	require.NoError(t, db.Runner().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode))
	assert.Equal(t, "wal", mode, "WAL must be enabled")
	require.NoError(t, db.Close())

	db2, err := state.Open(ctx, path, nil)
	require.NoError(t, err)
	require.NoError(t, db2.Close())
}

// FakeClock 推进语义（hermetic 时间戳确定性）。
func TestFakeClockAdvances(t *testing.T) {
	_, clock := statetest.New(t)
	first := clock.Now()
	clock.Advance(61 * time.Second)
	assert.True(t, clock.Now().After(first))
	assert.Equal(t, "2026-01-01T00:01:01Z", state.FormatTime(clock.Now()))
}
