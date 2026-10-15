package backup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/backup"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func newBackup(id string) *backup.Backup {
	return &backup.Backup{
		ID: id, ProjectID: "01JD0PROJ0000000000000000A",
		DatabaseID: "01JD0DB0000000000000000000", Engine: "postgres", RetentionSecs: 3600,
	}
}

// TestBackupTerminalStateIsFinal：终态是事实——succeeded/failed 行拒绝
// 一切迟到回执（启动清扫 SweepInterrupted 与迟到 Finish* 的竞态不得覆写
// 终态），CAS 前置不符按 ErrConflict 上抛；pending/running 行的合法回执
// 照常受理（FinishFailed 在 MarkRunning 前后都合法）。
func TestBackupTerminalStateIsFinal(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repo := backup.New(clock)

	// running → succeeded 后：迟到失败回执被拒，终态与回执三元组不变。
	b := newBackup("01JD0BKPC00000000000000001")
	require.NoError(t, repo.Create(ctx, db.Runner(), b))
	require.NoError(t, repo.MarkRunning(ctx, db.Runner(), b.ID))
	require.NoError(t, repo.FinishSucceeded(ctx, db.Runner(), b.ID, "backups/p/d/k1", "digest-1", 42))
	clock.Advance(1e9)
	err := repo.FinishFailed(ctx, db.Runner(), b.ID, "late failure after sweep")
	assert.ErrorIs(t, err, state.ErrConflict, "late failure receipt must not overwrite the terminal state")
	got, err := repo.Get(ctx, db.Runner(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, backup.StatusSucceeded, got.Status)
	assert.Equal(t, "digest-1", got.Digest)
	assert.Equal(t, int64(42), got.SizeBytes)
	assert.Equal(t, "backups/p/d/k1", got.ObjectKey)

	// succeeded 行上的重复成功回执同样被拒（幂等面在行现状，不在重放）。
	clock.Advance(1e9)
	err = repo.FinishSucceeded(ctx, db.Runner(), b.ID, "backups/p/d/k2", "digest-2", 43)
	assert.ErrorIs(t, err, state.ErrConflict)
	got, err = repo.Get(ctx, db.Runner(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, "digest-1", got.Digest, "terminal receipt triple must stay frozen")

	// pending 直落失败（MarkRunning 前的失败回执）合法。
	p := newBackup("01JD0BKPC00000000000000002")
	require.NoError(t, repo.Create(ctx, db.Runner(), p))
	require.NoError(t, repo.FinishFailed(ctx, db.Runner(), p.ID, "database row unavailable"))

	// running → failed（清扫路径）后：迟到成功回执被拒。
	f := newBackup("01JD0BKPC00000000000000003")
	require.NoError(t, repo.Create(ctx, db.Runner(), f))
	require.NoError(t, repo.MarkRunning(ctx, db.Runner(), f.ID))
	require.NoError(t, repo.FinishFailed(ctx, db.Runner(), f.ID, "interrupted by platform restart"))
	clock.Advance(1e9)
	err = repo.FinishSucceeded(ctx, db.Runner(), f.ID, "backups/p/d/k3", "digest-3", 44)
	assert.ErrorIs(t, err, state.ErrConflict, "late success receipt must not overwrite the terminal state")
	got, err = repo.Get(ctx, db.Runner(), f.ID)
	require.NoError(t, err)
	assert.Equal(t, backup.StatusFailed, got.Status)
	assert.Equal(t, "interrupted by platform restart", got.Error)

	// pending 行的成功回执不合法（成功只属于本进程标记过的 running 行）。
	q := newBackup("01JD0BKPC00000000000000004")
	require.NoError(t, repo.Create(ctx, db.Runner(), q))
	err = repo.FinishSucceeded(ctx, db.Runner(), q.ID, "backups/p/d/k4", "digest-4", 45)
	assert.ErrorIs(t, err, state.ErrConflict)

	// 不存在行：ErrConflict（RowsAffected=0 同口径，MarkRunning 先例）。
	err = repo.FinishFailed(ctx, db.Runner(), "missing", "boom")
	assert.True(t, errors.Is(err, state.ErrConflict))
}
