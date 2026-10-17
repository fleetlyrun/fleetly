package catalog_test

// Catalog 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：单行快照
// （id 恒 1）Put/Get 两方法——缺席即 ErrNoSnapshot 哨兵（回退内嵌目录的
// 正常态）、Put 从假时钟钉 fetched_at、二次 Put 整体覆盖（digest/body/
// fetched_at 三列同换，单行表不增行）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/catalog"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	catalogrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/catalog"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// TestCatalogSnapshotPutGetOverwrite：缺席哨兵 → 首播落行 → 覆盖写。
func TestCatalogSnapshotPutGetOverwrite(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	snapshots := catalogrepo.New(clock, db.Runner())

	// 缺席：ErrNoSnapshot（非 state.ErrNotFound——回退内嵌目录是正常态）。
	_, err := snapshots.Get(ctx)
	assert.ErrorIs(t, err, catalog.ErrNoSnapshot)
	assert.NotErrorIs(t, err, state.ErrNotFound,
		"absent snapshot must surface the domain sentinel, not the generic not-found")

	// 首播落行：fetched_at 取假时钟，digest/body 原文读回。
	first := &catalog.Snapshot{Digest: "sha256:aaa", Body: `[{"name":"web"}]`}
	require.NoError(t, snapshots.Put(ctx, first))
	assert.Equal(t, state.FormatTime(clock.Now()), first.FetchedAt, "Put stamps fetched_at from the clock")
	got, err := snapshots.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "sha256:aaa", got.Digest)
	assert.Equal(t, `[{"name":"web"}]`, got.Body)
	assert.Equal(t, first.FetchedAt, got.FetchedAt)

	// 二次 Put：整体替换三列，单行表不增行（id=1 覆盖写语义）。
	clock.Advance(30e9)
	second := &catalog.Snapshot{Digest: "sha256:bbb", Body: `[{"name":"web"},{"name":"api"}]`}
	require.NoError(t, snapshots.Put(ctx, second))
	got, err = snapshots.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "sha256:bbb", got.Digest, "second Put replaces the digest")
	assert.Equal(t, `[{"name":"web"},{"name":"api"}]`, got.Body, "second Put replaces the body")
	assert.Equal(t, state.FormatTime(clock.Now()), got.FetchedAt,
		"second Put refreshes fetched_at to the newer instant")
	assert.NotEqual(t, first.FetchedAt, got.FetchedAt)
}
