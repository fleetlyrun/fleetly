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

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
)

// ADR-0023 回归（N0 修复批 C2）：TeardownApp 拆载体 + 缓存收口。
func TestTeardownAppRemovesCarriersAndClearsCaches(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	require.NoError(t, e.TeardownApp(context.Background(), tAppID, nil))

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
	require.NoError(t, e.TeardownApp(context.Background(), tAppID, nil))
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

	err := e.TeardownApp(ctx, tAppID, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrActiveDeployment), "the failed rollback window is driving; teardown must refuse, got %v", err)
	assert.Len(t, rt.removedSnapshot(), before, "the refusal happens before any carrier side effect")

	// 回滚窗关闭（已尝试回滚 = 终态失败）：收口放行。
	require.NoError(t, e.repos.Deployments.Transit(ctx, planted.ID,
		[]deployment.State{deployment.StateFailed}, deployment.StateFailed,
		func(m *deployment.Deployment) { m.RollbackAttempted = true }))
	require.NoError(t, e.TeardownApp(ctx, tAppID, nil))
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
	go func() { done <- e.TeardownApp(context.Background(), tAppID, nil) }()
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

	require.NoError(t, e.TeardownApp(ctx, tAppID, nil))

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

// mintTeardownTicket 按铸票同参铸拆除票（修复批 F2 测试夹具）：base 三锚
// （TeardownAnchors）+ RequestDigest（TeardownCascade/TeardownCascadeDigest
// 铸验共享单源——与 PreviewTeardown 铸票面同一对函数），返回票与铸时锚
// （漂移注入前的世界快照）。
func mintTeardownTicket(t *testing.T, e *Engine, tickets *fakeConfirmTickets, appID string) (string, governance.ConfirmBase) {
	t.Helper()
	ctx := context.Background()
	base, err := e.TeardownAnchors(ctx, appID)
	require.NoError(t, err)
	cascade, err := e.TeardownCascade(ctx, appID)
	require.NoError(t, err)
	ticket := tickets.mint(governance.ConfirmTicketPayload{
		Verb:          "/fleetly.structure.v1.AppsService/DeleteApp",
		TargetApp:     appID,
		RequestDigest: TeardownCascadeDigest(cascade),
		Base:          base,
		PreviewDigest: "preview-digest-test",
	})
	return ticket, base
}

// 修复批 F2 #1：铸时缺席锚的 from-empty 腿——预览时无 hook 行/无 route
// 行，确认前新增（空→非空）必须 STALE。修复前 `!= ""` 卫把铸时缺席的锚
// 整体跳过：预览后新增的 hook/route 行被清单从未展示地删除。拒绝零载体
// 副作用、票不烧。
func TestTeardownConfirmFromEmptyAnchors(t *testing.T) {
	ctx := context.Background()

	newEnv := func(t *testing.T) (*Engine, *fakeRuntime, *fakeConfirmTickets, string) {
		t.Helper()
		e, rt, _ := newTestEngine(t)
		tickets := newFakeConfirmTickets()
		e.BindConfirmTickets(tickets)
		ticket, _ := mintTeardownTicket(t, e, tickets, tAppID)
		return e, rt, tickets, ticket
	}
	assertRefusedZeroSideEffects := func(t *testing.T, e *Engine, rt *fakeRuntime, tickets *fakeConfirmTickets, ticket string, err error) {
		t.Helper()
		require.ErrorIs(t, err, ErrConfirmStale)
		assert.Empty(t, rt.removedSnapshot(), "the refusal happens before any carrier side effect")
		_, status := tickets.PeekConfirm(ticket)
		assert.Equal(t, governance.ConfirmPeekHit, status, "a refused teardown must not burn the ticket")
	}

	t.Run("hook row appears after the preview", func(t *testing.T) {
		e, rt, tickets, ticket := newEnv(t)
		require.NoError(t, e.repos.Hooks.Create(ctx, &hook.Hook{
			AppID: tAppID, Repo: "https://github.com/acme/web.git", Branch: "main",
			UpdatedAt: "v2", SecretCiphertext: []byte("envelope"),
		}))
		assertRefusedZeroSideEffects(t, e, rt, tickets, ticket,
			e.TeardownApp(ctx, tAppID, &ConfirmInput{Ticket: ticket}))
	})

	t.Run("first route appears after the preview", func(t *testing.T) {
		e, rt, tickets, ticket := newEnv(t)
		require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
			ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
			Host: "late.teardown.test", AppID: tAppID, Process: "web", Port: 8000,
		}))
		assertRefusedZeroSideEffects(t, e, rt, tickets, ticket,
			e.TeardownApp(ctx, tAppID, &ConfirmInput{Ticket: ticket}))
	})

	t.Run("non-empty route anchor plus another route stays stale", func(t *testing.T) {
		// 既有「非空锚再加一条」腿（apitest TestPreviewTeardownGoldenAndTOCTOU
		// 的 engine 侧同形）：锚非空时新增行走 RouteSetDigest 漂移，保持绿。
		e, rt, tickets, _ := newEnv(t)
		require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
			ID: "01JD0ROUTE00000000000000002", ProjectID: tProjectID,
			Host: "first.teardown.test", AppID: tAppID, Process: "web", Port: 8000,
		}))
		ticket, _ := mintTeardownTicket(t, e, tickets, tAppID)
		require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
			ID: "01JD0ROUTE00000000000000003", ProjectID: tProjectID,
			Host: "second.teardown.test", AppID: tAppID, Process: "web", Port: 8000,
		}))
		assertRefusedZeroSideEffects(t, e, rt, tickets, ticket,
			e.TeardownApp(ctx, tAppID, &ConfirmInput{Ticket: ticket}))
	})
}

// 修复批 F2 #2：载体集漂移腿——预览（级联 = W1）→ 一次部署成功改变载体
// 集（W2）：app 行版本不动、无活跃部署行（DrivingByApp 只拦活跃行），
// 三锚全过而清单已换 → ④清单 digest 重算兜住（修复前只铸不验，直接放行
// 拆除 W2——清单从未展示）。同世界确认腿照常烧票拆除（铸验一致性）。
func TestTeardownConfirmCarrierSetDrift(t *testing.T) {
	ctx := context.Background()
	workerSpec := `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"busybox:1.37"}},"processes":[{"name":"worker","image":"busybox:1.37","replicas":1}]}`

	t.Run("a succeeded deploy changes the carrier set under unchanged anchors", func(t *testing.T) {
		e, rt, clock := newTestEngine(t)
		tickets := newFakeConfirmTickets()
		e.BindConfirmTickets(tickets)
		// W1：web 进程基线到 succeeded → 铸票（级联 = [workload app-web]）。
		rev1 := freezeSpec(t, e, 1, tImageSpec)
		driftDeployToSucceeded(t, e, rt, clock, rev1)
		ticket, minted := mintTeardownTicket(t, e, tickets, tAppID)

		// W2：worker 进程部署到 succeeded（载体集换、无活跃行）。
		rev2 := freezeSpec(t, e, 2, workerSpec)
		d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
		require.NoError(t, err)
		e.step(ctx)
		e.handleObservation(ctx, workloadEventRunning(tAppID+"-worker", d.Generation))
		e.step(ctx)
		clock.Advance(61 * time.Second)
		e.step(ctx)
		require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d.ID).State)

		// 洞形态断言：三锚全过（app 行版本/hook/route 未动），只有④兜得住。
		nowBase, aerr := e.TeardownAnchors(ctx, tAppID)
		require.NoError(t, aerr)
		assert.Equal(t, minted, nowBase, "fixture: the base anchors must be unchanged (the digest leg is the only catch)")

		err = e.TeardownApp(ctx, tAppID, &ConfirmInput{Ticket: ticket})
		require.ErrorIs(t, err, ErrConfirmStale)
		assert.Empty(t, rt.removedSnapshot(), "the refusal happens before any carrier side effect")
		_, status := tickets.PeekConfirm(ticket)
		assert.Equal(t, governance.ConfirmPeekHit, status, "a refused teardown must not burn the ticket")
	})

	t.Run("unchanged world redeems and tears down", func(t *testing.T) {
		e, rt, clock := newTestEngine(t)
		tickets := newFakeConfirmTickets()
		e.BindConfirmTickets(tickets)
		rev := freezeSpec(t, e, 1, tImageSpec)
		driftDeployToSucceeded(t, e, rt, clock, rev)
		ticket, _ := mintTeardownTicket(t, e, tickets, tAppID)

		require.NoError(t, e.TeardownApp(ctx, tAppID, &ConfirmInput{Ticket: ticket}))
		require.Len(t, rt.removedSnapshot(), 1, "the confirmed teardown removes the carriers")
		_, status := tickets.PeekConfirm(ticket)
		assert.Equal(t, governance.ConfirmPeekConsumed, status, "a successful teardown burns the ticket")
	})
}

// 修复批 F2 #2：清单导出共享函数（TeardownCascade/TeardownCascadeDigest）
// 的铸验一致性单测——铸造侧（PreviewTeardown）与消费面（peekTeardownConfirm）
// 同函数产出，本测试钉死纯函数面（行形/序敏感/稳定性）与采集面（同世界
// 同 digest、逐腿注入漂移必变）。
func TestTeardownCascadeDigestMintVerifyConsistency(t *testing.T) {
	// 纯函数面：条目序敏感（同序拼接语义）、同清单稳定、清单内容必进指纹。
	w1 := []TeardownCascadeEntry{
		{Kind: deliveryv1.TeardownItemKind_TEARDOWN_ITEM_KIND_WORKLOAD, Name: "app-web"},
		{Kind: deliveryv1.TeardownItemKind_TEARDOWN_ITEM_KIND_HOOK_ROW, Name: "https://github.com/acme/web.git"},
	}
	assert.Equal(t, TeardownCascadeDigest(w1), TeardownCascadeDigest(append([]TeardownCascadeEntry(nil), w1...)),
		"the same manifest re-derives the same digest (mint/verify single source)")
	assert.NotEqual(t, TeardownCascadeDigest(w1), TeardownCascadeDigest([]TeardownCascadeEntry{
		{Kind: deliveryv1.TeardownItemKind_TEARDOWN_ITEM_KIND_HOOK_ROW, Name: "https://github.com/acme/web.git"},
		{Kind: deliveryv1.TeardownItemKind_TEARDOWN_ITEM_KIND_WORKLOAD, Name: "app-web"},
	}), "the digest is order-sensitive (same-order concatenation)")
	assert.NotEqual(t, TeardownCascadeDigest(nil), TeardownCascadeDigest(w1), "manifest content must move the digest")

	// 采集面：同世界两次采集同 digest；hook 腿注入漂移必变（route/载体腿
	// 由 TestTeardownConfirmFromEmptyAnchors/TestTeardownConfirmCarrierSetDrift
	// 经完整 peek 覆盖）。
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	c1, err := e.TeardownCascade(ctx, tAppID)
	require.NoError(t, err)
	c2, err := e.TeardownCascade(ctx, tAppID)
	require.NoError(t, err)
	assert.Equal(t, c1, c2, "the same world derives the same manifest")
	require.Len(t, c1, 1, "fixture: the carrier leg is the succeeded projection")
	assert.Equal(t, deliveryv1.TeardownItemKind_TEARDOWN_ITEM_KIND_WORKLOAD, c1[0].Kind)
	assert.Equal(t, tAppID+"-web", c1[0].Name)
	before := TeardownCascadeDigest(c1)

	require.NoError(t, e.repos.Hooks.Create(ctx, &hook.Hook{
		AppID: tAppID, Repo: "https://github.com/acme/web.git", Branch: "main",
		UpdatedAt: "v2", SecretCiphertext: []byte("envelope"),
	}))
	after, err := e.TeardownCascade(ctx, tAppID)
	require.NoError(t, err)
	require.Len(t, after, 2, "the hook row joins the cascade after it appears")
	assert.NotEqual(t, before, TeardownCascadeDigest(after), "a new hook row must move the manifest digest")
}
