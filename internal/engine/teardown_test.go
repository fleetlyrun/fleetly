package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	e.expectMu.Lock()
	_, hasExpected := e.expected[tAppID]
	e.expectMu.Unlock()
	assert.False(t, hasExpected, "expected-generation cache must be cleared")
	e.obsMu.RLock()
	_, hasOwner := e.workloadApp[tAppID+"-web"]
	e.obsMu.RUnlock()
	assert.False(t, hasOwner, "ownership cache must be cleared")

	// 幂等：再拆一次不报错、不重复计数。
	require.NoError(t, e.TeardownApp(context.Background(), tAppID))
	assert.Len(t, rt.removedSnapshot(), 2, "Remove is idempotent-reachable (carrier count per call is provider-side)")
}
