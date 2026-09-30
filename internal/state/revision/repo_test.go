package revision_test

import (
	"context"
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

	// 同 App 同内容重复冻结 → 冲突（内容寻址复用走 FindByDigest）。
	dup := &revision.Revision{
		ID: "01JD0REV000000000000000009", AppID: appID,
		Seq: 3, Spec: []byte(`{"a":1}`),
	}
	err := revisions.Create(ctx, db.Runner(), dup)
	assert.ErrorIs(t, err, state.ErrConflict)

	// digest 派生自 spec 内容；FindByDigest 复用既有冻结体。
	digest := revision.Digest([]byte(`{"a":1}`))
	found, err := revisions.FindByDigest(ctx, db.Runner(), appID, digest)
	require.NoError(t, err)
	assert.Equal(t, int64(1), found.Seq, "identical spec must reuse the existing revision")

	latest, err := revisions.Latest(ctx, db.Runner(), appID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), latest.Seq)

	all, err := revisions.ListByApp(ctx, db.Runner(), appID)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	_, err = revisions.Latest(ctx, db.Runner(), "01JD0APP000000000000000099")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
