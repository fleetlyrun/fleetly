package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

func fixture(t *testing.T) (*Repo, state.Runner) {
	t.Helper()
	db, _ := statertest.New(t)
	return New(db.Clock()), db.Runner()
}

func newRow(id, name string) *Database {
	return &Database{
		ID: id, ProjectID: "01JD0PROJ0000000000000001",
		Name: name, Engine: "postgres", CredentialsRef: "database:" + name,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}
}

func TestCreateGetAndNameConflict(t *testing.T) {
	repo, run := fixture(t)
	ctx := context.Background()
	row := newRow("01JD0DB000000000000000001", "shop")
	require.NoError(t, repo.Create(ctx, run, row))
	assert.Equal(t, StatusPending, row.Status)
	assert.Equal(t, uint64(0), row.Generation)
	assert.Equal(t, "", row.SpecFingerprint)

	got, err := repo.Get(ctx, run, row.ID)
	require.NoError(t, err)
	assert.Equal(t, "postgres", got.Engine)
	assert.Equal(t, "database:shop", got.CredentialsRef)

	byName, err := repo.GetByName(ctx, run, row.ProjectID, "shop")
	require.NoError(t, err)
	assert.Equal(t, row.ID, byName.ID)

	dup := newRow("01JD0DB000000000000000002", "shop")
	assert.ErrorIs(t, repo.Create(ctx, run, dup), state.ErrAlreadyExists)
}

func TestListByProjectPagination(t *testing.T) {
	repo, run := fixture(t)
	ctx := context.Background()
	// 逆序落三行（ULID 时间序与落序相反，验证 ORDER BY id DESC）。
	ids := []string{
		"01JD0DB00000000000000000A",
		"01JD0DB00000000000000000B",
		"01JD0DB00000000000000000C",
	}
	for i, id := range ids {
		require.NoError(t, repo.Create(ctx, run, newRow(id, "db"+string(rune('a'+i)))))
	}
	page1, err := repo.ListByProject(ctx, run, "01JD0PROJ0000000000000001", "", 2)
	require.NoError(t, err)
	assert.Len(t, page1, 2)
	assert.Equal(t, "01JD0DB00000000000000000C", page1[0].ID) // 新→旧
	page2, err := repo.ListByProject(ctx, run, "01JD0PROJ0000000000000001", page1[len(page1)-1].ID, 2)
	require.NoError(t, err)
	assert.Len(t, page2, 1)

	all, err := repo.List(ctx, run)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	n, err := repo.CountByProject(ctx, run, "01JD0PROJ0000000000000001")
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

// EnsureGeneration 的重启安全语义（ADR-0029 决策 3）：指纹未变 → 同号
// 幂等重放（不触发载体滚动）；变化 → gen+1 与指纹同事务落行。
func TestEnsureGenerationRestartSafe(t *testing.T) {
	repo, run := fixture(t)
	ctx := context.Background()
	row := newRow("01JD0DB000000000000000001", "shop")
	require.NoError(t, repo.Create(ctx, run, row))

	gen1, err := repo.EnsureGeneration(ctx, run, row.ID, "fp-a")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), gen1)
	// 同指纹重放：同号。
	genAgain, err := repo.EnsureGeneration(ctx, run, row.ID, "fp-a")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), genAgain)
	// 指纹变化：推进。
	gen2, err := repo.EnsureGeneration(ctx, run, row.ID, "fp-b")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), gen2)

	got, err := repo.Get(ctx, run, row.ID)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), got.Generation)
	assert.Equal(t, "fp-b", got.SpecFingerprint)
}

func TestStatusAdvance(t *testing.T) {
	repo, run := fixture(t)
	ctx := context.Background()
	row := newRow("01JD0DB000000000000000001", "shop")
	require.NoError(t, repo.Create(ctx, run, row))

	require.NoError(t, repo.SetStatus(ctx, run, row.ID, StatusRunning))
	got, err := repo.Get(ctx, run, row.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, got.Status)
	// 幂等写入（同值 WHERE 守卫不迁 updated_at）不报错。
	require.NoError(t, repo.SetStatus(ctx, run, row.ID, StatusRunning))
}

func TestSoftDeleteHidesRows(t *testing.T) {
	repo, run := fixture(t)
	ctx := context.Background()
	row := newRow("01JD0DB000000000000000001", "shop")
	require.NoError(t, repo.Create(ctx, run, row))
	require.NoError(t, repo.SoftDelete(ctx, run, row.ID))

	_, err := repo.Get(ctx, run, row.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
	// 再删 = 不存在（幂等口径与 Get 一致）。
	assert.ErrorIs(t, repo.SoftDelete(ctx, run, row.ID), state.ErrNotFound)
	// tombstone 后同 Project 同名可新建（ID 永不复用，ADR-0029 决策 8）。
	fresh := newRow("01JD0DB000000000000000002", "shop")
	require.NoError(t, repo.Create(ctx, run, fresh))
	// 已删行的 gen 面不参与收敛（EnsureGeneration 命中 0 行 → NotFound）。
	_, err = repo.EnsureGeneration(ctx, run, row.ID, "fp")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
