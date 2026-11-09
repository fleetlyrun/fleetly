package bucketstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainbucket "github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// bucket_stores 聚合 repo 冒烟（B5，ADR-0096 决策 1：行面与 pubsubs 同形——
// 此处钉同形性不重放全矩阵；行为等价由 database/pubsub repo_test 的范式
// 承载；本族无备份/恢复口，冒烟面相应收窄）。
func TestBucketStoreRepoSmoke(t *testing.T) {
	db, _ := statetest.New(t)
	repo := New(db.Clock(), db.Runner())
	ctx := context.Background()

	row := &domainbucket.BucketStore{
		ProjectID: "prj", Name: "assets", Engine: "silo", Version: "2026-10",
		CredentialsRef: "bucketstore:" + "assets",
	}
	require.NoError(t, repo.Create(ctx, row))
	assert.NotEmpty(t, row.ID)
	assert.Equal(t, domainbucket.StatusPending, row.Status)

	// 同项目同名冲突（tombstone 后可复用）。
	dup := *row
	dup.ID = ""
	err := repo.Create(ctx, &dup)
	require.ErrorIs(t, err, state.ErrAlreadyExists)

	got, err := repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, "silo", got.Engine)
	assert.Nil(t, got.Settings, "zero-knob kind rows read back with nil settings")

	byName, err := repo.GetByName(ctx, "prj", "assets")
	require.NoError(t, err)
	assert.Equal(t, row.ID, byName.ID)

	list, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	n, err := repo.CountByProject(ctx, "prj")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// EnsureGeneration：变化推进 +1，指纹一致幂等重放同号。
	gen1, err := repo.EnsureGeneration(ctx, row.ID, "fp-1")
	require.NoError(t, err)
	gen1b, err := repo.EnsureGeneration(ctx, row.ID, "fp-1")
	require.NoError(t, err)
	assert.Equal(t, gen1, gen1b)
	gen2, err := repo.EnsureGeneration(ctx, row.ID, "fp-2")
	require.NoError(t, err)
	assert.Equal(t, gen1+1, gen2)

	// 期望态全量替换（resources 面；settings 恒空）。
	cpu := int32(500)
	require.NoError(t, repo.UpdateDesired(ctx, row.ID, nil, &cpu, nil))
	got, _ = repo.Get(ctx, row.ID)
	require.NotNil(t, got.CPUMillis)
	assert.Equal(t, int32(500), *got.CPUMillis)

	// 状态推进。
	require.NoError(t, repo.SetStatus(ctx, row.ID, domainbucket.StatusRunning))

	// tombstone：幂等口径。
	require.NoError(t, repo.SoftDelete(ctx, row.ID))
	require.ErrorIs(t, repo.SoftDelete(ctx, row.ID), state.ErrNotFound)
	gone, err := repo.Get(ctx, row.ID)
	require.ErrorIs(t, err, state.ErrNotFound)
	assert.Nil(t, gone)

	// tombstone 后同名可新建（partial unique index 同 pubsubs 形）。
	fresh := &domainbucket.BucketStore{
		ProjectID: "prj", Name: "assets", Engine: "silo", Version: "2026-10",
		CredentialsRef: "bucketstore:" + "assets",
	}
	require.NoError(t, repo.Create(ctx, fresh))
	assert.NotEqual(t, row.ID, fresh.ID, "ID 永不复用")
	_ = persistence.MapScanErr(nil)
}
