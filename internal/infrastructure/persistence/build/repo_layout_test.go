package build_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	buildrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/build"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// repo 列往返（ADR-0036 N2 兑现节 2）：创建定型、读回原样——repo 是产物
// 定位的唯一事实（<project>/<app> 前纲；旧扁平兼容族——空 repo 投影回退
// 与 StampRepo 补章——已删，repo 恒由生产构造面定型写入）。
func TestBuildRepoColumnRoundTrip(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	builds := buildrepo.New(clock, db.Runner())

	b := &build.Build{
		ID: "01JD0BUILD00000000000000000", AppID: "01JD0APP000000000000000000",
		RevisionID: "01JD0REV000000000000000000", State: build.StateQueued,
		Repo: "01jd0app000000000000000000/web",
	}
	require.NoError(t, builds.Create(ctx, b))
	got, err := builds.Get(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, "01jd0app000000000000000000/web", got.Repo)
}
