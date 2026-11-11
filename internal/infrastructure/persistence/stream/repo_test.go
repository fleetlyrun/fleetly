package stream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainstream "github.com/fleetlyrun/fleetly/internal/domain/data/stream"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// streams 聚合 repo 冒烟（B8，ADR-0096 决策 10：topic repo_test 同形范本
// ——subjects 序列化/不可变轴承载/期望态写/GC 面）。
func TestStreamRepoSmoke(t *testing.T) {
	db, _ := statetest.New(t)
	repo := New(db.Clock(), db.Runner())
	ctx := context.Background()
	statetest.SeedProject(t, db, "prj", "default")
	statetest.SeedPubsub(t, db, "pub-1", "prj")
	statetest.SeedPubsub(t, db, "pub-2", "prj")

	max := int64(1048576)
	row := &domainstream.Stream{
		ProjectID: "prj", PubSubID: "pub-1", Name: "ORDERS",
		Subjects: []string{"orders.>", "bills.>"}, Retention: domainstream.RetentionLimits,
		Storage: domainstream.StorageFile, MaxBytes: &max,
	}
	require.NoError(t, repo.Create(ctx, row))
	assert.NotEmpty(t, row.ID)
	assert.Equal(t, domainstream.StatusPending, row.Status)

	dup := *row
	dup.ID = ""
	require.ErrorIs(t, repo.Create(ctx, &dup), state.ErrAlreadyExists)

	got, err := repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"bills.>", "orders.>"}, got.Subjects, "subjects persist in the canonical ascending order")
	assert.Equal(t, domainstream.RetentionLimits, got.Retention)
	assert.Equal(t, domainstream.StorageFile, got.Storage)
	require.NotNil(t, got.MaxBytes)

	list, err := repo.ListByPubSub(ctx, "pub-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	n, err := repo.CountByPubSub(ctx, "pub-1")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// 期望态写：可变面 subjects/max_bytes 全量替换。
	require.NoError(t, repo.UpdateDesired(ctx, row.ID, []string{"orders.>"}, nil))
	got, err = repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"orders.>"}, got.Subjects)
	assert.Nil(t, got.MaxBytes)
	assert.Equal(t, domainstream.StatusPending, got.Status)

	// 对账状态推进 + 软删/硬删/级联（topic 范本同律）。
	require.NoError(t, repo.SetStatus(ctx, row.ID, domainstream.StatusError, "boom"))
	got, _ = repo.Get(ctx, row.ID)
	assert.Equal(t, "boom", got.StatusErr)
	require.NoError(t, repo.SoftDelete(ctx, row.ID))
	_, err = repo.Get(ctx, row.ID)
	require.ErrorIs(t, err, state.ErrNotFound)
	require.NoError(t, repo.Delete(ctx, row.ID))
	list, _ = repo.ListByPubSub(ctx, "pub-1")
	assert.Empty(t, list)

	row2 := &domainstream.Stream{ProjectID: "prj", PubSubID: "pub-2", Name: "B"}
	require.NoError(t, repo.Create(ctx, row2))
	require.NoError(t, repo.DeleteByPubSub(ctx, "pub-2"))
	list, _ = repo.ListByPubSub(ctx, "pub-2")
	assert.Empty(t, list)
}
