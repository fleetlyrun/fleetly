package engine

// blue-green 编排变体的引擎面测试（ADR-0048 决策 1/2 验收锚的 hermetic
// 半边；真机零 5xx/载体存活锚在 e2e dind-bluegreen.sh）：
//   - 双代窗物化：联合期望集单次 Ensure（旧代成员基线 gen/材料 + 新代
//     成员本部署 gen/代次化 ID）；
//   - 切换与收口：releasing→observing 迁移即服务代翻转（servingGenerations
//     推导 + routeExpectations 过滤的 Route 后端断言）；观察窗满 → 仅新代
//     收口 Ensure → succeeded；
//   - 新代 L1 失败：仅基线 Ensure + failed 终态 + rollback_attempted（无
//     Replay），旧代载体零扰动；
//   - 观察窗内切回（watchdog 形态 = 手动切回的同一机制）：rolling-back
//     在基线 gen 重放（无新号），旧代未重建。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/route"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// bgSpecFor 构造 blue-green 进程 spec（web 策略声明 + 端口供 Route 断言）。
func bgSpecFor(ref string) string {
	return `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"` + ref + `"}},` +
		`"processes":[{"name":"web","image":"` + ref + `","replicas":1,` +
		`"ports":[{"port":8080,"protocol":"PROTOCOL_HTTP"}],"strategy":"DEPLOY_STRATEGY_BLUE_GREEN"}]}`
}

// rollingSpecWithPort 构造带端口的 rolling spec（基线腿需要端口供 Route）。
func rollingSpecWithPort(ref string) string {
	return `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"` + ref + `"}},` +
		`"processes":[{"name":"web","image":"` + ref + `","replicas":1,` +
		`"ports":[{"port":8080,"protocol":"PROTOCOL_HTTP"}]}]}`
}

// TestBlueGreenWindowMaterializesUnion：双代窗物化——单次 Ensure 联合集
// （rolling 基线 → BG 升级的混合形态：旧代成员 rolling 名 + 基线 gen，
// 新代成员代次化 ID + 本部署 gen；调用 gen = 基线 gen——旧代标签不翻新）。
func TestBlueGreenWindowMaterializesUnion(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, rollingSpecWithPort("nginx:1.27"))
	deployToSucceeded(t, e, rev1)

	rev2 := freezeSpec(t, e, 2, bgSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	require.Equal(t, uint64(1), d2.FromGeneration, "admission anchors the baseline generation")
	e.step(ctx) // preparing → releasing（窗口物化 + L1 deadline）
	d2 = getDeployment(t, e, d2.ID)
	require.Equal(t, deployment.StateReleasing, d2.State)

	calls := rt.calls()
	win := calls[len(calls)-1]
	oldID := tAppID + "-web"
	newID := genScopedWorkloadID(oldID, 2)
	require.Len(t, win.ByID, 2, "the window is one Ensure with both generations")
	assert.Equal(t, capability.Generation(1), win.Gen, "the window Ensure calls at the baseline generation")

	oldW, ok := win.ByID[oldID]
	require.True(t, ok, "baseline carrier joins the union")
	assert.Equal(t, uint64(1), oldW.Generation, "baseline member carries its baseline gen anchor")
	assert.False(t, oldW.GenerationScoped, "rolling-era baseline carrier keeps its plain name")
	assert.Equal(t, "nginx:1.27", oldW.Image)
	assert.NotNil(t, oldW.Materials, "baseline member carries its baseline materials (zero-touch anchor)")

	newW, ok := win.ByID[newID]
	require.True(t, ok, "new-generation carrier is generation-scoped")
	assert.Equal(t, uint64(2), newW.Generation)
	assert.True(t, newW.GenerationScoped)
	assert.Equal(t, "nginx:1.28", newW.Image)
	assert.Nil(t, newW.Materials, "new-generation member inherits the call-level materials")
	assert.Contains(t, newW.Addressing, capability.Address{Name: "web.g2"},
		"scoped addressing carries the generation name ({proc}.g{gen}, ADR-0048 decision 1.4)")
}

// TestBlueGreenSwitchAndCollection：切换与收口全链——L1 过 → observing
// （服务代翻转到本代：Route 后端从旧代 VIP 切到新代 VIP）→ 观察窗满 →
// 仅新代收口 Ensure（旧代移除）→ succeeded。
func TestBlueGreenSwitchAndCollection(t *testing.T) {
	e, rt, proxy, _ := newBGProxyEngine(t)
	ctx := context.Background()
	require.NoError(t, e.routes.Create(ctx, e.db.Runner(), &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "none",
	}))

	rev1 := freezeSpec(t, e, 1, rollingSpecWithPort("nginx:1.27"))
	deployToSucceeded(t, e, rev1)
	oldID := tAppID + "-web"
	newID := genScopedWorkloadID(oldID, 2)

	rev2 := freezeSpec(t, e, 2, bgSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx)
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d2.ID).State)

	// 切换前：服务代 = 基线代——Route 后端解析到旧代期望集。
	e.PublishRoutesNow()
	e.managedStep(ctx)
	require.NotEmpty(t, proxy.published)
	assert.Equal(t, "vip-"+oldID+":8080", proxy.published[len(proxy.published)-1].BackendAddr,
		"pre-switch backend must resolve to the baseline generation")

	// L1：新代 running@2 → observing（切换）。
	e.handleObservation(ctx, workloadEventRunning(newID, 2))
	e.step(ctx)
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, d2.ID).State)

	e.PublishRoutesNow()
	e.managedStep(ctx)
	assert.Equal(t, "vip-"+newID+":8080", proxy.published[len(proxy.published)-1].BackendAddr,
		"the switch (releasing→observing) flips the serving generation")

	// 收口：观察窗满 → 仅新代 Ensure → succeeded。
	advanceClock(t, e, 61*time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d2.ID).State)
	calls := rt.calls()
	last := calls[len(calls)-1]
	require.Len(t, last.ByID, 1, "collection ensures the new generation only")
	_, ok := last.ByID[newID]
	assert.True(t, ok, "the surviving carrier is the new generation")
	assert.Equal(t, capability.Generation(2), last.Gen)
}

// TestBlueGreenL1FailureZeroTouch：新代 L1 失败——Ensure 仅基线 + failed
// 终态 + rollback_attempted（无 Replay：failed 分支不再铸回放），旧代载体
// 零扰动（期望集仍含旧代、基线 gen 原样）。
func TestBlueGreenL1FailureZeroTouch(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, rollingSpecWithPort("nginx:1.27"))
	deployToSucceeded(t, e, rev1)
	oldID := tAppID + "-web"

	rev2 := freezeSpec(t, e, 2, bgSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx) // 窗口物化（无观测 → L1 等待）
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d2.ID).State)

	// 新代永不出观测 → L1 超时 → 仅基线 Ensure → failed 终态。
	advanceClock(t, e, 121*time.Second)
	e.step(ctx)
	fresh := getDeployment(t, e, d2.ID)
	require.Equal(t, deployment.StateFailed, fresh.State)
	assert.True(t, fresh.RollbackAttempted, "baseline in service means rollback is already effectuated; no Replay")
	assert.Contains(t, fresh.Error, "new generation")

	calls := rt.calls()
	last := calls[len(calls)-1]
	require.Len(t, last.ByID, 1, "L1 failure collapses the expectation set to the baseline")
	w, ok := last.ByID[oldID]
	require.True(t, ok)
	assert.Equal(t, uint64(1), w.Generation, "the baseline carrier is re-anchored at its baseline gen, untouched")
	assert.Equal(t, capability.Generation(1), last.Gen)

	// failed 分支不再推进（无自动 Replay）。
	e.step(ctx)
	assert.Equal(t, deployment.StateFailed, getDeployment(t, e, d2.ID).State)
}

// TestBlueGreenObservingSwitchBack：观察窗内切回（watchdog 咬合与手动
// Replay 共用同一机制）——failed 的服务代推导切回基线，rolling-back 在
// 基线 gen 重放（无新号；旧代载体未重建），succeeded 收口后新代载体已被
// 期望集移除。
func TestBlueGreenObservingSwitchBack(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, rollingSpecWithPort("nginx:1.27"))
	deployToSucceeded(t, e, rev1)
	oldID := tAppID + "-web"
	newID := genScopedWorkloadID(oldID, 2)

	rev2 := freezeSpec(t, e, 2, bgSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx)
	e.handleObservation(ctx, workloadEventRunning(newID, 2))
	e.step(ctx)
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, d2.ID).State)

	// 观察窗内新代劣化 → 切回。drive 链一次推进：observing → failed（服务
	// 代推导切回基线）→ rolling-back（基线 gen 重放，无新号——旧代在服
	// 即就绪）→ 回放自身的 observing。
	e.handleObservation(ctx, capability.WorkloadEvent{
		WorkloadID: newID, Generation: capability.Generation(2),
		State: capability.WorkloadStopped, Message: "crash loop",
	})
	e.step(ctx)
	rb := getDeployment(t, e, d2.ID)
	require.Equal(t, deployment.StateObserving, rb.State, "the switch-back replays straight to its own observing window (baseline already serving)")
	assert.Equal(t, rev1, rb.ToRevision, "terminal fact correction: the running revision is the baseline")
	require.Equal(t, uint64(1), rb.Generation, "the switch-back replays at the baseline generation (no fresh number)")
	assert.Contains(t, rb.Error, "rolled back to previous revision")

	// 回放观察窗满 → succeeded；最后一笔 Ensure = 仅基线（新代已移除）。
	advanceClock(t, e, 61*time.Second)
	e.step(ctx)
	final := getDeployment(t, e, d2.ID)
	require.Equal(t, deployment.StateSucceeded, final.State)
	assert.Equal(t, rev1, final.ToRevision)
	assert.Equal(t, uint64(1), final.Generation, "the succeeded row keeps the in-service carrier gen (baseline anchor invariant)")

	calls := rt.calls()
	last := calls[len(calls)-1]
	require.Len(t, last.ByID, 1, "the replay expectation set drops the new generation")
	_, ok := last.ByID[oldID]
	assert.True(t, ok, "the baseline carrier was never rebuilt (same identity, same gen)")
	assert.Equal(t, capability.Generation(1), last.Gen)
}

// TestBlueGreenSupersedeCollectsWindow：双代窗内被抢占——抢占者首个期望集
// 移除被抢占部署的新代载体（期望集不含即移除；旧代 = 抢占者的 from 基线
// 照常接管），零残留。
func TestBlueGreenSupersedeCollectsWindow(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, rollingSpecWithPort("nginx:1.27"))
	deployToSucceeded(t, e, rev1)
	oldID := tAppID + "-web"

	rev2 := freezeSpec(t, e, 2, bgSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx) // d2 双代窗在途（等新代 L1）
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d2.ID).State)

	rev3 := freezeSpec(t, e, 3, bgSpecFor("nginx:1.29"))
	d3, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev3, Supersede: true})
	require.NoError(t, err)
	e.step(ctx)
	assert.Equal(t, deployment.StateSuperseded, getDeployment(t, e, d2.ID).State)

	calls := rt.calls()
	last := calls[len(calls)-1]
	g2 := genScopedWorkloadID(oldID, 2)
	g3 := genScopedWorkloadID(oldID, 3)
	_, linger := last.ByID[g2]
	assert.False(t, linger, "the superseded deployment's new generation is collected by the pre-emptor's first expectation set")
	_, base := last.ByID[oldID]
	assert.True(t, base, "the baseline (pre-emptor's from) is carried by its window")
	_, next := last.ByID[g3]
	assert.True(t, next, "the pre-emptor's own new generation joins the window")

	// 推进到终态：d3 新代就绪 → observing → 收口 succeeded（单代 g3）。
	e.handleObservation(ctx, workloadEventRunning(g3, 3))
	e.step(ctx)
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, d3.ID).State)
	advanceClock(t, e, 61*time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d3.ID).State)
}

// TestServingGenerationsDerivation：服务代推导纯函数面——observing = 本代，
// 其余在途 = 基线代；无在途不进表（稳态不过滤）。
func TestServingGenerationsDerivation(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, rollingSpecWithPort("nginx:1.27"))
	deployToSucceeded(t, e, rev1)

	rev2 := freezeSpec(t, e, 2, bgSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)

	// queued：服务代 = 基线（FromGeneration）。
	assert.Equal(t, map[string]uint64{tAppID: 1}, e.servingGenerations(ctx))
	e.step(ctx) // releasing 窗口
	assert.Equal(t, map[string]uint64{tAppID: 1}, e.servingGenerations(ctx), "pre-switch serving stays on the baseline")

	e.handleObservation(ctx, workloadEventRunning(genScopedWorkloadID(tAppID+"-web", 2), 2))
	e.step(ctx) // → observing（切换）
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, d2.ID).State)
	assert.Equal(t, map[string]uint64{tAppID: 2}, e.servingGenerations(ctx), "the observing deployment serves its own generation")

	advanceClock(t, e, 61*time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d2.ID).State)
	assert.Empty(t, e.servingGenerations(ctx), "no in-flight deployment: stable single generation, no filtering")
}

// newBGProxyEngine 建带 Proxy 的 hermetic 引擎（Route 切换断言面——
// newTestEngine 不装 Proxy）。
func newBGProxyEngine(t *testing.T) (*Engine, *fakeRuntime, *fakeProxy, *statetest.FakeClock) {
	t.Helper()
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	e := New(Deps{DB: db, Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	require.NoError(t, app.New(clock).Create(ctx, db.Runner(), &app.App{
		ID: tAppID, ProjectID: tProjectID, Name: "web",
	}))
	return e, rt, proxy, clock
}
