package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
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
	_, hasExpected := e.expect.expected[appOwner(tAppID)]
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

// R02-M4：收口预检用驱动权谓词（DrivingByApp 含 failed 待自动回滚窗，与
// converger 同口径）——failed 待回滚行在位时拒绝收口且零副作用；回滚窗
// 关闭（rollback_attempted=1）后方可拆除。
func TestTeardownAppRefusesFailedPendingRollbackWindow(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	// failed 待自动回滚行（from_revision 非空 + rollback_attempted=0；
	// 形态与 drive.go 的 startRollback 拾取面一致）。
	planted := &deployment.Deployment{
		ID: "01JD0DEPLOYFAILED00000000", AppID: tAppID,
		FromRevision: rev, ToRevision: rev,
		State: deployment.StateFailed, Generation: 2, FromGeneration: 1,
	}
	require.NoError(t, e.repos.Deployments.Create(ctx, planted))
	before := len(rt.removedSnapshot())

	err := e.TeardownApp(ctx, tAppID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrActiveDeployment), "the failed rollback window is driving; teardown must refuse, got %v", err)
	assert.Len(t, rt.removedSnapshot(), before, "the refusal happens before any carrier side effect")

	// 回滚窗关闭（已尝试回滚 = 终态失败）：收口放行。
	require.NoError(t, e.repos.Deployments.Transit(ctx, planted.ID,
		[]deployment.State{deployment.StateFailed}, deployment.StateFailed,
		func(m *deployment.Deployment) { m.RollbackAttempted = true }))
	require.NoError(t, e.TeardownApp(ctx, tAppID))
	assert.Len(t, rt.removedSnapshot(), before+1, "after the rollback window closes the teardown proceeds")
}

// R13-L1：TeardownApp 是载体写动词——持 maintenanceMu 读半边，与网络重建
// 写锁串行化（重建窗内不得插入拆载体）。用写锁占位 + 超时断言防死锁。
func TestTeardownAppWaitsForMaintenanceWriter(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	before := len(rt.removedSnapshot())

	e.maintenanceMu.Lock() // 模拟 RebuildNetwork 写半边
	done := make(chan error, 1)
	go func() { done <- e.TeardownApp(context.Background(), tAppID) }()
	select {
	case err := <-done:
		e.maintenanceMu.Unlock()
		t.Fatalf("teardown bypassed the maintenance lock: err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}
	e.maintenanceMu.Unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
		assert.Len(t, rt.removedSnapshot(), before+1)
	case <-time.After(5 * time.Second):
		t.Fatal("teardown did not resume after the maintenance writer released the lock (deadlock)")
	}
}

// ADR-0023 统一口径（N0.1 P1-1）：tombstone 后 Submit 在事务内拒绝——
// 删后 deploy 不得重建载体（API/webhook 的预读只是快速失败面，权威判定
// 在 Submit 事务内）。
func TestSubmitRejectsDeletedApp(t *testing.T) {
	e, _, _ := newTestEngine(t)
	require.NoError(t, e.tx.Do(context.Background(), func(s transact.Stores) error {
		return s.Apps.SoftDelete(context.Background(), tAppID)
	}))
	rev := freezeSpec(t, e, 1, tImageSpec)

	_, _, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.ErrorIs(t, err, state.ErrNotFound, "submitting to a tombstoned app must be rejected in-transaction")
}

// 统一口径（N0.1 P1-1）：repo 读面对已删行同构隐藏——Get 与 List 同形
// （DeleteApp 再删 404 的根），SoftDelete 命中 0 行返回 ErrNotFound。
func TestAppRepoHidesTombstonedRows(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	a, err := e.repos.Apps.Get(ctx, tAppID)
	require.NoError(t, err)
	require.False(t, a.Deleted())

	require.NoError(t, e.tx.Do(ctx, func(s transact.Stores) error {
		return s.Apps.SoftDelete(ctx, tAppID)
	}))

	_, err = e.repos.Apps.Get(ctx, tAppID)
	require.ErrorIs(t, err, state.ErrNotFound)
	listed, err := e.repos.Apps.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, listed, "tombstoned app must not be listed")

	err = e.tx.Do(ctx, func(s transact.Stores) error {
		return s.Apps.SoftDelete(ctx, tAppID)
	})
	require.ErrorIs(t, err, state.ErrNotFound, "re-delete surfaces as not-found (unified semantics)")
}
