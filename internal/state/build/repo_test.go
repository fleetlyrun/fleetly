package build_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// Build Transit 的 CAS 面（Q-5）：前置不符与并发迁移（RowsAffected 纵深
// 防御）都归一 ErrConflict，不静默当成功。
func TestBuildTransitConflictPaths(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	builds := build.New(clock)

	b := &build.Build{
		ID: "01JD0BUILD00000000000000000", AppID: "01JD0APP000000000000000000",
		RevisionID: "01JD0REV000000000000000000", State: build.StateQueued,
	}
	require.NoError(t, builds.Create(ctx, db.Runner(), b))

	// 并发迁移形态：前置 Get 通过后、UPDATE 落库前行状态被他人改走
	//（mut 作同步点，单连接池下确定交错）→ RowsAffected=0 → ErrConflict。
	inMut := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- builds.Transit(ctx, db.Runner(), b.ID,
			[]build.State{build.StateQueued}, build.StateBuilding,
			func(m *build.Build) {
				close(inMut) // 前置 Get 已过，停在 UPDATE 前
				<-release
			})
	}()
	<-inMut
	_, err := db.Runner().ExecContext(ctx,
		`UPDATE builds SET state = ? WHERE id = ?`, string(build.StateFailed), b.ID)
	require.NoError(t, err)
	close(release)
	assert.ErrorIs(t, <-done, state.ErrConflict)

	// 前置不符：终态不可再迁移。
	err = builds.Transit(ctx, db.Runner(), b.ID,
		[]build.State{build.StateQueued}, build.StateSucceeded, nil)
	assert.ErrorIs(t, err, state.ErrConflict)

	got, err := builds.Get(ctx, db.Runner(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, build.StateFailed, got.State, "the concurrent writer's state stands")
}

// ADR-0026 List 分页（after_* + limit）：新→旧序、游标跳过、limit 截断与
// 钳制（<=0 或 >200 回落/钳缺省 50）。
func TestBuildListByAppPagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	builds := build.New(clock)

	const appID = "01JD0APP000000000000000000"
	// 逆序落三行（ULID 时间序与落序相反，验证 ORDER BY id DESC）。
	ids := []string{"01JD0BUILD0000000000000000A", "01JD0BUILD0000000000000000B", "01JD0BUILD0000000000000000C"}
	for _, id := range ids {
		require.NoError(t, builds.Create(ctx, db.Runner(), &build.Build{
			ID: id, AppID: appID, RevisionID: "01JD0REV000000000000000000", State: build.StateQueued,
		}))
	}

	page1, err := builds.ListByApp(ctx, db.Runner(), appID, "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, ids[2], page1[0].ID, "newest first")
	assert.Equal(t, ids[1], page1[1].ID)

	page2, err := builds.ListByApp(ctx, db.Runner(), appID, page1[len(page1)-1].ID, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, ids[0], page2[0].ID, "cursor skips the first page")

	// limit 钳制：<=0 回落缺省 50（三行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -1, 500} {
		got, err := builds.ListByApp(ctx, db.Runner(), appID, "", limit)
		require.NoError(t, err)
		assert.Len(t, got, 3, "limit %d clamps into range and returns all rows", limit)
	}
}
