package bucketresource

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainbucket "github.com/fleetlyrun/fleetly/internal/domain/data/bucketresource"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// bucket_resources 聚合 repo 冒烟（B8，ADR-0096 决策 10：topic repo_test
// 同形范本——quota 期望态写/对账状态/GC 面/级联清理）。
func TestBucketResourceRepoSmoke(t *testing.T) {
	db, _ := statetest.New(t)
	repo := New(db.Clock(), db.Runner())
	ctx := context.Background()
	statetest.SeedProject(t, db, "prj", "default")
	statetest.SeedBucketStore(t, db, "bkt-1", "prj")
	statetest.SeedBucketStore(t, db, "bkt-2", "prj")

	quota := int64(1073741824)
	row := &domainbucket.Bucket{
		ProjectID: "prj", BucketStoreID: "bkt-1", Name: "media", QuotaBytes: &quota,
	}
	require.NoError(t, repo.Create(ctx, row))
	assert.NotEmpty(t, row.ID)
	assert.Equal(t, domainbucket.StatusPending, row.Status)

	dup := *row
	dup.ID = ""
	require.ErrorIs(t, repo.Create(ctx, &dup), state.ErrAlreadyExists)

	got, err := repo.Get(ctx, row.ID)
	require.NoError(t, err)
	require.NotNil(t, got.QuotaBytes)
	assert.Equal(t, int64(1073741824), *got.QuotaBytes)

	list, err := repo.ListByBucketStore(ctx, "bkt-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	n, err := repo.CountByBucketStore(ctx, "bkt-1")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// 期望态写：quota 全量替换（nil = 撤配额）。
	require.NoError(t, repo.UpdateDesired(ctx, row.ID, nil))
	got, err = repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Nil(t, got.QuotaBytes)

	// 对账状态推进 + 软删/硬删/级联。
	require.NoError(t, repo.SetStatus(ctx, row.ID, domainbucket.StatusError, "boom"))
	got, _ = repo.Get(ctx, row.ID)
	assert.Equal(t, "boom", got.StatusErr)
	require.NoError(t, repo.SetStatus(ctx, row.ID, domainbucket.StatusReady, ""))
	require.NoError(t, repo.SoftDelete(ctx, row.ID))
	_, err = repo.Get(ctx, row.ID)
	require.ErrorIs(t, err, state.ErrNotFound)
	require.NoError(t, repo.Delete(ctx, row.ID))
	require.NoError(t, repo.Delete(ctx, row.ID))
	list, _ = repo.ListByBucketStore(ctx, "bkt-1")
	assert.Empty(t, list)

	row2 := &domainbucket.Bucket{ProjectID: "prj", BucketStoreID: "bkt-2", Name: "x"}
	require.NoError(t, repo.Create(ctx, row2))
	require.NoError(t, repo.DeleteByBucketStore(ctx, "bkt-2"))
	list, _ = repo.ListByBucketStore(ctx, "bkt-2")
	assert.Empty(t, list)
}
