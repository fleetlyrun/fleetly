package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
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

// R16-L2 + R02-L7（S15b-c7）：TeardownApp 末尾清该 App 的全部收敛域记账
// （memo/resolved/fails/failSig/posture/cooldown/eventSeen/projMemo/replay），
// 并重钉 teardown 标记；内置 converge-failed 告警随之解除（不残留 firing）。
func TestTeardownAppClearsConvergeLedger(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e) // 慢通道 memo + projMemo 落账

	// 一次成功的信号驱动收敛：resolved 生命周期面在册。
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()
	e.driftScan(ctx)
	e.converge.mu.Lock()
	_, hasResolved := e.converge.resolved[tAppID]
	e.converge.mu.Unlock()
	require.True(t, hasResolved, "a fast-source converge records the resolved signature")

	// 不同形态的三连失败 → 冷却窗 + 内置告警 firing（fails/failSig/posture/
	// cooldown/eventSeen 全部在册）。
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {replicas: 3}}
	rt.mu.Unlock()
	convergeFailOnce(t, e, rt)
	convergeFailOnce(t, e, rt)
	convergeFailOnce(t, e, rt)
	e.converge.markReplay(tAppID, "01JD0DEPLOYREPLAY0000000")

	e.converge.mu.Lock()
	_, hasMemo := e.converge.memo[tAppID]
	_, hasFails := e.converge.fails[tAppID]
	_, hasFailSig := e.converge.failSig[tAppID]
	_, hasPosture := e.converge.posture[tAppID]
	_, hasCooldown := e.converge.cooldown[tAppID]
	_, hasEventSeen := e.converge.eventSeen[tAppID]
	_, hasReplay := e.converge.replay[tAppID]
	projKeys := len(e.converge.projMemo)
	e.converge.mu.Unlock()
	require.True(t, hasMemo && hasFails && hasFailSig && hasPosture && hasCooldown && hasEventSeen && hasReplay,
		"fixture: every converge ledger entry must be present before teardown")
	require.Positive(t, projKeys, "fixture: the projection memo holds the app's baseline key")
	assert.Equal(t, "firing", e.SystemConvergeAlertState().State, "fixture: the builtin converge alert is firing")

	require.NoError(t, e.TeardownApp(ctx, tAppID))

	e.converge.mu.Lock()
	_, memoAfter := e.converge.memo[tAppID]
	_, resolvedAfter := e.converge.resolved[tAppID]
	_, failsAfter := e.converge.fails[tAppID]
	_, failSigAfter := e.converge.failSig[tAppID]
	_, postureAfter := e.converge.posture[tAppID]
	_, cooldownAfter := e.converge.cooldown[tAppID]
	_, eventSeenAfter := e.converge.eventSeen[tAppID]
	_, replayAfter := e.converge.replay[tAppID]
	for key := range e.converge.projMemo {
		assert.False(t, strings.HasPrefix(key, tAppID+"\x00"), "projMemo key %q of the deleted app must be dropped", key)
	}
	e.converge.mu.Unlock()
	assert.False(t, memoAfter, "memo")
	assert.False(t, resolvedAfter, "resolved")
	assert.False(t, failsAfter, "fails")
	assert.False(t, failSigAfter, "failSig")
	assert.False(t, postureAfter, "posture")
	assert.False(t, cooldownAfter, "cooldown")
	assert.False(t, eventSeenAfter, "eventSeen")
	assert.False(t, replayAfter, "replay")
	assert.True(t, e.converge.teardownMarked(tAppID), "the teardown mark is re-pinned after the purge (race closure)")
	assert.Equal(t, "ok", e.SystemConvergeAlertState().State, "deleting the last cooling app clears the builtin alert")
	assert.Len(t, eventNamesOf(t, e, "alert.resolved"), 1, "the resolved edge is emitted (same沿 handling as a cooldown release)")
}

// c7 第二半：projMemo 容量上限 + LRU 淘汰（长跑无界增长的回归锚；淘汰后
// 投影仍正确重算）。
func TestConvergeProjMemoBoundedLRU(t *testing.T) {
	e, _, _ := newTestEngine(t)
	c := &e.converge
	keyFor := func(i int) string { return fmt.Sprintf("app%04d\x00rev\x00%d", i, i) }

	c.mu.Lock()
	for i := 0; i < convergeProjMemoLimit; i++ {
		c.projMemoPut(keyFor(i), []capability.Workload{{ID: keyFor(i)}})
	}
	// 触达最早键让 LRU 序生效：再插一条应淘汰第二旧的键而非被触达的键。
	c.projMemoGet(keyFor(0))
	c.projMemoPut("newest\x00rev\x001", []capability.Workload{{ID: "newest"}})
	size := len(c.projMemo)
	_, oldestKept := c.projMemo[keyFor(0)]
	_, secondEvicted := c.projMemo[keyFor(1)]
	c.mu.Unlock()

	require.Equal(t, convergeProjMemoLimit, size, "the projection memo is capacity-bounded")
	assert.True(t, oldestKept, "a recently used key survives eviction (LRU)")
	assert.False(t, secondEvicted, "the least recently used key is evicted first")

	// 淘汰后读径仍正确：真实基线投影重算并命中缓存。
	rev := freezeSpec(t, e, 1, tImageSpec)
	deployToSucceeded(t, e, rev)
	d, err := e.repos.Deployments.LatestSucceeded(context.Background(), tAppID)
	require.NoError(t, err)
	ws, err := e.expectedWorkloads(context.Background(), d)
	require.NoError(t, err)
	require.NotEmpty(t, ws, "the projection is recomputed after eviction")
	cached, err := e.expectedWorkloads(context.Background(), d)
	require.NoError(t, err)
	assert.Equal(t, ws, cached, "the recomputed projection is cached and served identically")
}
