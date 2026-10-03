package engine

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// ADR-0023 回归（N0 修复批 C2）：TeardownApp 拆载体 + 缓存收口。
func TestTeardownAppRemovesCarriersAndClearsCaches(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	require.NoError(t, e.TeardownApp(context.Background(), tAppID))

	removed := rt.removedSnapshot()
	require.Len(t, removed, 1, "teardown must call Runtime.Remove exactly once")
	assert.Equal(t, "default/"+tProjectID+"/"+tAppID, removed[0].String())

	// 缓存收口：归属/期望清空（drift/steady-state 不再咬已删 App）。
	e.expect.mu.Lock()
	_, hasExpected := e.expect.expected[tAppID]
	e.expect.mu.Unlock()
	assert.False(t, hasExpected, "expected-generation cache must be cleared")
	e.obs.mu.RLock()
	_, hasOwner := e.obs.workloadApp[tAppID+"-web"]
	e.obs.mu.RUnlock()
	assert.False(t, hasOwner, "ownership cache must be cleared")

	// 幂等：再拆一次不报错、不重复计数。
	require.NoError(t, e.TeardownApp(context.Background(), tAppID))
	assert.Len(t, rt.removedSnapshot(), 2, "Remove is idempotent-reachable (carrier count per call is provider-side)")
}

// ADR-0023 统一口径（N0.1 P1-1）：tombstone 后 Submit 在事务内拒绝——
// 删后 deploy 不得重建载体（API/webhook 的预读只是快速失败面，权威判定
// 在 Submit 事务内）。
func TestSubmitRejectsDeletedApp(t *testing.T) {
	e, _, _ := newTestEngine(t)
	require.NoError(t, e.db.Tx(context.Background(), func(tx *sql.Tx) error {
		return e.apps.SoftDelete(context.Background(), tx, tAppID)
	}))
	rev := freezeSpec(t, e, 1, tImageSpec)

	_, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.ErrorIs(t, err, state.ErrNotFound, "submitting to a tombstoned app must be rejected in-transaction")
}

// 统一口径（N0.1 P1-1）：repo 读面对已删行同构隐藏——Get 与 List 同形
// （DeleteApp 再删 404 的根），SoftDelete 命中 0 行返回 ErrNotFound。
func TestAppRepoHidesTombstonedRows(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	a, err := e.apps.Get(ctx, e.db.Runner(), tAppID)
	require.NoError(t, err)
	require.False(t, a.Deleted())

	require.NoError(t, e.db.Tx(ctx, func(tx *sql.Tx) error {
		return e.apps.SoftDelete(ctx, tx, tAppID)
	}))

	_, err = e.apps.Get(ctx, e.db.Runner(), tAppID)
	require.ErrorIs(t, err, state.ErrNotFound)
	listed, err := e.apps.List(ctx, e.db.Runner())
	require.NoError(t, err)
	assert.Empty(t, listed, "tombstoned app must not be listed")

	err = e.db.Tx(ctx, func(tx *sql.Tx) error {
		return e.apps.SoftDelete(ctx, tx, tAppID)
	})
	require.ErrorIs(t, err, state.ErrNotFound, "re-delete surfaces as not-found (unified semantics)")
}
