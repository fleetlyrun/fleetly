package topic

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintopic "github.com/fleetlyrun/fleetly/internal/domain/data/topic"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// topics 聚合 repo 冒烟（B8，ADR-0096 决策 10：行面 pubsub 子集形——
// 期望态写/对账状态推进/软删 GC 挂起/硬删收口/级联清理）。
func TestTopicRepoSmoke(t *testing.T) {
	db, _ := statetest.New(t)
	repo := New(db.Clock(), db.Runner())
	ctx := context.Background()

	retention := int64(3600000)
	row := &domaintopic.Topic{
		ProjectID: "prj", PubSubID: "pub-1", Name: "orders", Partitions: 3, RetentionMS: &retention,
	}
	require.NoError(t, repo.Create(ctx, row))
	assert.NotEmpty(t, row.ID)
	assert.Equal(t, domaintopic.StatusPending, row.Status)

	// 同父实例同名冲突；tombstone 后可复用。
	dup := *row
	dup.ID = ""
	require.ErrorIs(t, repo.Create(ctx, &dup), state.ErrAlreadyExists)

	got, err := repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, int32(3), got.Partitions)
	require.NotNil(t, got.RetentionMS)
	assert.Equal(t, int64(3600000), *got.RetentionMS)

	list, err := repo.ListByPubSub(ctx, "pub-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	n, err := repo.CountByPubSub(ctx, "pub-1")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// 期望态写：UpdateDesired 全量替换并回 pending。
	require.NoError(t, repo.UpdateDesired(ctx, row.ID, 5, nil))
	got, err = repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, int32(5), got.Partitions)
	assert.Nil(t, got.RetentionMS)
	assert.Equal(t, domaintopic.StatusPending, got.Status)

	// 对账状态推进：error 留痕 + ready 清位；同值幂等。
	require.NoError(t, repo.SetStatus(ctx, row.ID, domaintopic.StatusError, "boom"))
	got, _ = repo.Get(ctx, row.ID)
	assert.Equal(t, "boom", got.StatusError)
	require.NoError(t, repo.SetStatus(ctx, row.ID, domaintopic.StatusReady, ""))
	got, _ = repo.Get(ctx, row.ID)
	assert.Equal(t, domaintopic.StatusReady, got.Status)

	// 软删（GC 挂起）→ Get 不可见、Count 不计；ListByPubSub 仍可见（GC 面）。
	require.NoError(t, repo.SoftDelete(ctx, row.ID))
	_, err = repo.Get(ctx, row.ID)
	require.ErrorIs(t, err, state.ErrNotFound)
	n, _ = repo.CountByPubSub(ctx, "pub-1")
	assert.Equal(t, 0, n)
	list, err = repo.ListByPubSub(ctx, "pub-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].Deleted())

	// 硬删（GC 收口）幂等。
	require.NoError(t, repo.Delete(ctx, row.ID))
	require.NoError(t, repo.Delete(ctx, row.ID))
	list, _ = repo.ListByPubSub(ctx, "pub-1")
	assert.Empty(t, list)

	// 级联清理：父实例删除一行清空。
	row2 := &domaintopic.Topic{ProjectID: "prj", PubSubID: "pub-2", Name: "a", Partitions: 1}
	require.NoError(t, repo.Create(ctx, row2))
	require.NoError(t, repo.DeleteByPubSub(ctx, "pub-2"))
	list, _ = repo.ListByPubSub(ctx, "pub-2")
	assert.Empty(t, list)
}
