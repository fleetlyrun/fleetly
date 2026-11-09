package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func fixture(t *testing.T) database.Repo {
	t.Helper()
	db, _ := statetest.New(t)
	return New(db.Clock(), db.Runner())
}

func newRow(id, name string) *database.Database {
	return &database.Database{
		ID: id, ProjectID: "01JD0PROJ0000000000000001",
		Name: name, Engine: "postgres", CredentialsRef: "database:" + name,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}
}

func TestCreateGetAndNameConflict(t *testing.T) {
	repo := fixture(t)
	ctx := context.Background()
	row := newRow("01JD0DB000000000000000001", "shop")
	require.NoError(t, repo.Create(ctx, row))
	assert.Equal(t, database.StatusPending, row.Status)
	assert.Equal(t, uint64(0), row.Generation)
	assert.Equal(t, "", row.SpecFingerprint)

	got, err := repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, "postgres", got.Engine)
	assert.Equal(t, "database:shop", got.CredentialsRef)

	byName, err := repo.GetByName(ctx, row.ProjectID, "shop")
	require.NoError(t, err)
	assert.Equal(t, row.ID, byName.ID)

	dup := newRow("01JD0DB000000000000000002", "shop")
	assert.ErrorIs(t, repo.Create(ctx, dup), state.ErrAlreadyExists)
}

func TestListByProjectPagination(t *testing.T) {
	repo := fixture(t)
	ctx := context.Background()
	// 逆序落三行（ULID 时间序与落序相反，验证 ORDER BY id DESC）。
	ids := []string{
		"01JD0DB00000000000000000A",
		"01JD0DB00000000000000000B",
		"01JD0DB00000000000000000C",
	}
	for i, id := range ids {
		require.NoError(t, repo.Create(ctx, newRow(id, "db"+string(rune('a'+i)))))
	}
	page1, err := repo.ListByProject(ctx, "01JD0PROJ0000000000000001", 0, 2)
	require.NoError(t, err)
	assert.Len(t, page1, 2)
	assert.Equal(t, "01JD0DB00000000000000000C", page1[0].ID) // 新→旧
	page2, err := repo.ListByProject(ctx, "01JD0PROJ0000000000000001", page1[len(page1)-1].Seq, 2)
	require.NoError(t, err)
	assert.Len(t, page2, 1)

	all, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	n, err := repo.CountByProject(ctx, "01JD0PROJ0000000000000001")
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

// EnsureGeneration 的重启安全语义（ADR-0029 决策 3）：指纹未变 → 同号
// 幂等重放（不触发载体滚动）；变化 → gen+1 与指纹同事务落行。
func TestEnsureGenerationRestartSafe(t *testing.T) {
	repo := fixture(t)
	ctx := context.Background()
	row := newRow("01JD0DB000000000000000001", "shop")
	require.NoError(t, repo.Create(ctx, row))

	gen1, err := repo.EnsureGeneration(ctx, row.ID, "fp-a")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), gen1)
	// 同指纹重放：同号。
	genAgain, err := repo.EnsureGeneration(ctx, row.ID, "fp-a")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), genAgain)
	// 指纹变化：推进。
	gen2, err := repo.EnsureGeneration(ctx, row.ID, "fp-b")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), gen2)

	got, err := repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), got.Generation)
	assert.Equal(t, "fp-b", got.SpecFingerprint)
}

func TestStatusAdvance(t *testing.T) {
	repo := fixture(t)
	ctx := context.Background()
	row := newRow("01JD0DB000000000000000001", "shop")
	require.NoError(t, repo.Create(ctx, row))

	require.NoError(t, repo.SetStatus(ctx, row.ID, database.StatusRunning))
	got, err := repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, database.StatusRunning, got.Status)
	// 幂等写入（同值 WHERE 守卫不迁 updated_at）不报错。
	require.NoError(t, repo.SetStatus(ctx, row.ID, database.StatusRunning))
}

// 恢复显式状态机（migration 00045）：pending/running 在途、succeeded/
// failed 终态；源历史 restore_from_backup 成功/失败都不清位；启动清扫把
// 打断的 running 回退 pending。
func TestRestoreStateMachine(t *testing.T) {
	repo := fixture(t)
	ctx := context.Background()
	row := newRow("01JD0DB000000000000000001", "shop")
	require.NoError(t, repo.Create(ctx, row))

	got, err := repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, database.RestoreStateNone, got.RestoreState, "fresh rows carry no restore state")

	require.NoError(t, repo.SetRestorePending(ctx, row.ID, "bk-1"))
	pending, err := repo.ListRestorePending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, database.RestoreStatePending, pending[0].RestoreState)

	require.NoError(t, repo.SetRestoreRunning(ctx, row.ID))
	pending, err = repo.ListRestorePending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "running restores stay in the active set")
	assert.Equal(t, database.RestoreStateRunning, pending[0].RestoreState)

	// 启动清扫：running → pending（重试一次是安全上界）。
	n, err := repo.SweepInterruptedRestores(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	got, err = repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, database.RestoreStatePending, got.RestoreState)

	// 失败终态：离开活态集、error 留痕、源历史保留。
	require.NoError(t, repo.SetRestoreFailed(ctx, row.ID, "boom"))
	pending, err = repo.ListRestorePending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "a failed restore is terminal and leaves the active set")
	got, err = repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, database.RestoreStateFailed, got.RestoreState)
	assert.Equal(t, "boom", got.RestoreError)
	assert.Equal(t, "bk-1", got.RestoreFromBackup, "the source history survives failure")

	// 成功终态：源历史保留（不再清位）。
	require.NoError(t, repo.SetRestorePending(ctx, row.ID, "bk-2"))
	require.NoError(t, repo.SetRestoreSucceeded(ctx, row.ID))
	got, err = repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, database.RestoreStateSucceeded, got.RestoreState)
	assert.Equal(t, "bk-2", got.RestoreFromBackup, "the source history survives success")
	pending, err = repo.ListRestorePending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "succeeded restores leave the active set")
}

func TestSoftDeleteHidesRows(t *testing.T) {
	repo := fixture(t)
	ctx := context.Background()
	row := newRow("01JD0DB000000000000000001", "shop")
	require.NoError(t, repo.Create(ctx, row))
	require.NoError(t, repo.SoftDelete(ctx, row.ID))

	_, err := repo.Get(ctx, row.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
	// 再删 = 不存在（幂等口径与 Get 一致）。
	assert.ErrorIs(t, repo.SoftDelete(ctx, row.ID), state.ErrNotFound)
	// tombstone 后同 Project 同名可新建（ID 永不复用，ADR-0029 决策 8）。
	fresh := newRow("01JD0DB000000000000000002", "shop")
	require.NoError(t, repo.Create(ctx, fresh))
	// 已删行的 gen 面不参与收敛（EnsureGeneration 命中 0 行 → NotFound）。
	_, err = repo.EnsureGeneration(ctx, row.ID, "fp")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
