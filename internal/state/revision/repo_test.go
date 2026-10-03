package revision_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func TestRevisionImmutableSequence(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	revisions := revision.New(clock)

	const appID = "01JD0APP000000000000000000"
	for i, spec := range []string{`{"a":1}`, `{"a":2}`} {
		seq, err := revisions.NextSeq(ctx, db.Runner(), appID)
		require.NoError(t, err)
		rev := &revision.Revision{
			ID:    "01JD0REV00000000000000000" + string(rune('0'+i)),
			AppID: appID, Seq: seq, Spec: []byte(spec),
		}
		require.NoError(t, revisions.Create(ctx, db.Runner(), rev))
	}

	// 同 App 同内容重复冻结 → 唯一约束命中（ErrAlreadyExists；内容寻址
	// 复用走 FindByDigest）。
	dup := &revision.Revision{
		ID: "01JD0REV000000000000000009", AppID: appID,
		Seq: 3, Spec: []byte(`{"a":1}`),
	}
	err := revisions.Create(ctx, db.Runner(), dup)
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	// digest 派生自 spec 内容；FindByDigest 复用既有冻结体。
	digest := revision.Digest([]byte(`{"a":1}`))
	found, err := revisions.FindByDigest(ctx, db.Runner(), appID, digest)
	require.NoError(t, err)
	assert.Equal(t, int64(1), found.Seq, "identical spec must reuse the existing revision")

	latest, err := revisions.Latest(ctx, db.Runner(), appID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), latest.Seq)

	all, err := revisions.ListByApp(ctx, db.Runner(), appID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	_, err = revisions.Latest(ctx, db.Runner(), "01JD0APP000000000000000099")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// ADR-0026 List 分页（after_* + limit）：R1..Rn 升序（既有响应序）、
// after_seq 游标跳过、limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）。
func TestRevisionListByAppPagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	revisions := revision.New(clock)

	const appID = "01JD0APP000000000000000000"
	for i := 0; i < 3; i++ {
		seq, err := revisions.NextSeq(ctx, db.Runner(), appID)
		require.NoError(t, err)
		require.NoError(t, revisions.Create(ctx, db.Runner(), &revision.Revision{
			ID:    fmt.Sprintf("01JD0REV0000000000000000%02d", i),
			AppID: appID, Seq: seq, Spec: []byte(fmt.Sprintf(`{"n":%d}`, i)),
		}))
	}

	// 升序首页截断（seq 轴，与 events 面同款升序形态）。
	page1, err := revisions.ListByApp(ctx, db.Runner(), appID, 0, 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, int64(1), page1[0].Seq)
	assert.Equal(t, int64(2), page1[1].Seq)

	// 游标跳过首页。
	page2, err := revisions.ListByApp(ctx, db.Runner(), appID, page1[len(page1)-1].Seq, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, int64(3), page2[0].Seq)

	// 游标越过末条 → 空页。
	page3, err := revisions.ListByApp(ctx, db.Runner(), appID, 3, 2)
	require.NoError(t, err)
	assert.Empty(t, page3)

	// limit 钳制：<=0 回落缺省 50（三行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -7, 201} {
		got, err := revisions.ListByApp(ctx, db.Runner(), appID, 0, limit)
		require.NoError(t, err)
		assert.Len(t, got, 3, "limit %d clamps into range and returns all rows", limit)
	}
}
