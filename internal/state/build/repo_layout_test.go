package build_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

// repo 列往返（ADR-0036 N2 兑现节 2）：创建定型、读回原样；StampRepo 幂等
// 一次性（空值补章、已章行不受扰——前纲切换的存量在途行兼容面）。
func TestBuildRepoColumnRoundTrip(t *testing.T) {
	db, clock := statertest.New(t)
	ctx := context.Background()
	builds := build.New(clock)

	b := &build.Build{
		ID: "01JD0BUILD00000000000000000", AppID: "01JD0APP000000000000000000",
		RevisionID: "01JD0REV000000000000000000", State: build.StateQueued,
		Repo: "01jd0app000000000000000000/web",
	}
	require.NoError(t, builds.Create(ctx, db.Runner(), b))
	got, err := builds.Get(ctx, db.Runner(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, "01jd0app000000000000000000/web", got.Repo)

	legacy := &build.Build{
		ID: "01JD0BUILD00000000000000001", AppID: "01JD0APP000000000000000000",
		RevisionID: "01JD0REV000000000000000001", State: build.StateBuilding,
	}
	require.NoError(t, builds.Create(ctx, db.Runner(), legacy))
	require.NoError(t, builds.StampRepo(ctx, db.Runner(), legacy.ID, "01jd0app000000000000000000/web"))
	require.NoError(t, builds.StampRepo(ctx, db.Runner(), legacy.ID, "other/shape"))
	got, err = builds.Get(ctx, db.Runner(), legacy.ID)
	require.NoError(t, err)
	assert.Equal(t, "01jd0app000000000000000000/web", got.Repo, "StampRepo is one-shot on empty, never rewrites")
}
