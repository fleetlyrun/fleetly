package revision_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	revisionrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/revision"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func TestRevisionImmutableSequence(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	revisions := revisionrepo.New(clock, db.Runner())
	statetest.SeedApp(t, db, "01JD0APP000000000000000000", "project-revision")

	const appID = "01JD0APP000000000000000000"
	for i, spec := range []string{`{"a":1}`, `{"a":2}`} {
		seq, err := revisions.NextSeq(ctx, appID)
		require.NoError(t, err)
		rev := &revision.Revision{
			ID:    "01JD0REV00000000000000000" + string(rune('0'+i)),
			AppID: appID, Seq: seq, Spec: []byte(spec),
		}
		require.NoError(t, revisions.Create(ctx, rev))
	}

	// 同 App 同内容重复冻结 → 唯一约束命中（ErrAlreadyExists；内容寻址
	// 复用走 FindByDigest）。
	dup := &revision.Revision{
		ID: "01JD0REV000000000000000009", AppID: appID,
		Seq: 3, Spec: []byte(`{"a":1}`),
	}
	err := revisions.Create(ctx, dup)
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	// digest 派生自 spec 内容；FindByDigest 复用既有冻结体。
	digest := revision.Digest([]byte(`{"a":1}`))
	found, err := revisions.FindByDigest(ctx, appID, digest)
	require.NoError(t, err)
	assert.Equal(t, int64(1), found.Seq, "identical spec must reuse the existing revision")

	latest, err := revisions.Latest(ctx, appID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), latest.Seq)

	all, err := revisions.ListByApp(ctx, appID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	_, err = revisions.Latest(ctx, "01JD0APP000000000000000099")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// ADR-0026 List 分页（after_* + limit）：R1..Rn 升序（既有响应序）、
// after_seq 游标跳过、limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）。
func TestRevisionListByAppPagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	revisions := revisionrepo.New(clock, db.Runner())
	statetest.SeedApp(t, db, "01JD0APP000000000000000000", "project-revision")

	const appID = "01JD0APP000000000000000000"
	for i := 0; i < 3; i++ {
		seq, err := revisions.NextSeq(ctx, appID)
		require.NoError(t, err)
		require.NoError(t, revisions.Create(ctx, &revision.Revision{
			ID:    fmt.Sprintf("01JD0REV0000000000000000%02d", i),
			AppID: appID, Seq: seq, Spec: []byte(fmt.Sprintf(`{"n":%d}`, i)),
		}))
	}

	// 升序首页截断（seq 轴，与 events 面同款升序形态）。
	page1, err := revisions.ListByApp(ctx, appID, 0, 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, int64(1), page1[0].Seq)
	assert.Equal(t, int64(2), page1[1].Seq)

	// 游标跳过首页。
	page2, err := revisions.ListByApp(ctx, appID, page1[len(page1)-1].Seq, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, int64(3), page2[0].Seq)

	// 游标越过末条 → 空页。
	page3, err := revisions.ListByApp(ctx, appID, 3, 2)
	require.NoError(t, err)
	assert.Empty(t, page3)

	// limit 钳制：<=0 回落缺省 50（三行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -7, 201} {
		got, err := revisions.ListByApp(ctx, appID, 0, limit)
		require.NoError(t, err)
		assert.Len(t, got, 3, "limit %d clamps into range and returns all rows", limit)
	}
}

// Prune 保留窗三件（ADR-0101）：恰清最旧非活跃至窗、protect 名单窗外存活
// （软上限）、无超限幂等零删。
func TestRevisionPruneRetentionWindow(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	revisions := revisionrepo.New(clock, db.Runner())
	statetest.SeedApp(t, db, "01JD0APP000000000000000000", "project-revision")
	const appID = "01JD0APP000000000000000000"

	seed := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			seq, err := revisions.NextSeq(ctx, appID)
			require.NoError(t, err)
			require.NoError(t, revisions.Create(ctx, &revision.Revision{
				AppID: appID, Seq: seq, Spec: []byte(fmt.Sprintf(`{"v":%d}`, seq)),
			}))
		}
	}

	// 105 行 → 恰清最旧 5，窗内全留。
	seed(105)
	n, err := revisions.Prune(ctx, appID, nil, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 5, n)
	rows, err := revisions.ListByApp(ctx, appID, 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 100)
	assert.Equal(t, int64(6), rows[0].Seq, "oldest surviving row is seq 6")
	assert.Equal(t, int64(105), rows[len(rows)-1].Seq)

	// protect 窗外存活：再冻 6 行（seq 106..111，总数 106），protect seq 6
	// → 窗外非保护行恰 5 行（seq 7..11）被清，总数 = 窗 100 + 保护行 1。
	oldest := rows[0].ID
	seed(6)
	n, err = revisions.Prune(ctx, appID, []string{oldest}, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 5, n)
	rows, err = revisions.ListByApp(ctx, appID, 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 101, "window plus protected row beyond it")
	assert.Equal(t, int64(6), rows[0].Seq, "protected row survives beyond the window")

	// 无超限：幂等零删（唯一窗外行即保护行）。
	n, err = revisions.Prune(ctx, appID, []string{oldest}, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 0, n)
}

// DeleteAllByApp 聚合终局全清（ADR-0101 决策 4）：目标 App 行全清、他 App
// 行不动。
func TestRevisionDeleteAllByApp(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	revisions := revisionrepo.New(clock, db.Runner())
	const (
		appA  = "01JD0APP000000000000000000"
		appB  = "01JD0APP000000000000000001"
		projA = "project-revision-a"
		projB = "project-revision-b"
	)
	statetest.SeedApp(t, db, appA, projA)
	statetest.SeedApp(t, db, appB, projB)

	for _, appID := range []string{appA, appB} {
		seq, err := revisions.NextSeq(ctx, appID)
		require.NoError(t, err)
		require.NoError(t, revisions.Create(ctx, &revision.Revision{
			AppID: appID, Seq: seq, Spec: []byte(`{"a":1}`),
		}))
	}

	require.NoError(t, revisions.DeleteAllByApp(ctx, appA))
	empty, err := revisions.ListByApp(ctx, appA, 0, 50)
	require.NoError(t, err)
	assert.Empty(t, empty, "terminal app revisions are fully cleared")
	kept, err := revisions.ListByApp(ctx, appB, 0, 50)
	require.NoError(t, err)
	assert.Len(t, kept, 1, "other apps are untouched")
}
