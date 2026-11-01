package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// deployToSucceeded 辅助：完整部署一条 Revision 到终态成功（返回 deployment）。
func deployToSucceeded(t *testing.T, e *Engine, revID string) *deployment.Deployment {
	t.Helper()
	ctx := context.Background()
	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", d.Generation))
	e.step(ctx)
	advanceClock(t, e, 61*time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d.ID).State)
	return getDeployment(t, e, d.ID)
}

func advanceClock(t *testing.T, e *Engine, d time.Duration) {
	t.Helper()
	if adv, ok := e.clock.(interface{ Advance(time.Duration) }); ok {
		adv.Advance(d)
	}
}

// Rollback 一等动词：缺省目标解析 + 显式抢占在途 + 审计标注回放。
// 在途腿：最新行（在途 releasing，非 succeeded）不触发跳过 → 最近
// succeeded 即目标（本例 r2 = 当前基线，修复性回卷语义）。
func TestRollbackReplayVerb(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
	r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.27"))

	deployToSucceeded(t, e, r1)
	deployToSucceeded(t, e, r2)

	// 在途一条（r2 再部署）后 Rollback：抢占在途、缺省目标 = 最近
	// succeeded r2（最新行非 succeeded，不跳过——今日行为保持）。
	inFlight, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2})
	require.NoError(t, err)
	e.step(ctx) // → releasing（在途）

	rb, _, err := e.Rollback(ctx, tAppID, "", nil)
	require.NoError(t, err)
	assert.Equal(t, r2, rb.ToRevision, "empty target rolls back to the last succeeded baseline")
	assert.Equal(t, deployment.StateSuperseded, getDeployment(t, e, inFlight.ID).State, "rollback preempts in-flight")

	// 显式目标回放（r1）。
	rb2, _, err := e.Rollback(ctx, tAppID, r1, nil)
	require.NoError(t, err)
	assert.Equal(t, r1, rb2.ToRevision)
}

// Rollback 无成功基线 → 明确错误（首次部署无回滚对象）。哨兵锚定
// （Q-13）：API 层经 errors.Is 判定 E_NO_BASELINE，文案改写不得破坏契约。
func TestRollbackWithoutBaseline(t *testing.T) {
	e, _, _ := newTestEngine(t)
	_, _, err := e.Rollback(context.Background(), tAppID, "", nil)
	require.ErrorIs(t, err, ErrNoSuccessfulBaseline)
	assert.ErrorContains(t, err, "no successful baseline")
}

// 反悔回滚腿（staging 2026-11-01 走查定罪场景复刻）：修订链
// [R1✓, R2✓(当前生效)] 且无在途——缺省回滚必须跳过当前生效修订取
// **前**者（旧语义复读 R2 自身：kind=rollback 但载体 image 不回卷、
// R2 缺端口声明时下游路由丢发布）。显式 toRevisionID 不受缺省语义
// 影响（指定当前生效修订仍可显式重放——操作者自担的复读）。
func TestRollbackDefaultSkipsCurrentRevision(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
	r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.27"))

	deployToSucceeded(t, e, r1)
	deployToSucceeded(t, e, r2)

	rb, _, err := e.Rollback(ctx, tAppID, "", nil)
	require.NoError(t, err)
	assert.Equal(t, r1, rb.ToRevision, "regret rollback must replay the earlier baseline, not the current revision")
	assert.Equal(t, r2, rb.FromRevision, "the replay departs from the current effective revision")

	// 显式目标不受缺省语义影响：显式指定当前生效修订仍受理。
	explicit, _, err := e.Rollback(ctx, tAppID, r2, nil)
	require.NoError(t, err)
	assert.Equal(t, r2, explicit.ToRevision, "explicit target is honored even when it is the current revision")
}

// 失败后回滚腿（今日行为保持）：最新行 failed（非 succeeded）不触发
// 跳过，最近 succeeded 即当前基线——重放它是修复性回卷（失败部署可能
// 留下破碎载体，显式回卷重收敛到当前基线）。
func TestRollbackDefaultAfterFailureKeepsBaseline(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
	r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.27"))

	deployToSucceeded(t, e, r1)

	// r2 部署失败：载体永不上报 running → L1 健康门超时。failed 行同拍
	// 即被自动回滚拾取（failed → rolling-back，ADR ListDriving 谓词）——
	// 两态皆非 succeeded，正构成本腿的"最新行非 succeeded"前置。
	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2})
	require.NoError(t, err)
	e.step(ctx)
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State)
	clock.Advance(e.opts.Deploy.ReleaseTimeout + time.Second)
	e.step(ctx)
	require.Contains(t, []deployment.State{deployment.StateFailed, deployment.StateRollingBack},
		getDeployment(t, e, d.ID).State, "the r2 deploy must not have succeeded")

	rb, _, err := e.Rollback(ctx, tAppID, "", nil)
	require.NoError(t, err)
	assert.Equal(t, r1, rb.ToRevision, "failure rollback keeps today's target: the latest succeeded baseline (= current baseline)")
}

// 仅一条成功基线腿：唯一成功基线即当前生效修订 → 诚实报错（同哨兵
// ErrNoSuccessfulBaseline、同 E_NO_BASELINE 映射，零新码——文案写明
// 无可回滚对象，不与"无成功基线"文案混淆）。
func TestRollbackDefaultOnlyBaselineIsCurrent(t *testing.T) {
	e, _, _ := newTestEngine(t)
	r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
	deployToSucceeded(t, e, r1)

	_, _, err := e.Rollback(context.Background(), tAppID, "", nil)
	require.ErrorIs(t, err, ErrNoSuccessfulBaseline)
	assert.ErrorContains(t, err, "the only successful baseline is the current revision")
}

// 场景 1 完整回归：releasing 中途"被杀"（引擎排空 + 新实例）→ 按
// Generation 幂等重放 → 终态成功（领域模型场景 1）。
func TestScenario1KilledMidReleaseReplay(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)

	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx)
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State)
	gens := len(rt.calls())

	// "被杀"：引擎停止（观测/Ensure 缓存清空语义）→ 新实例恢复。
	require.NoError(t, e.Stop(context.Background()))
	e2 := New(Deps{DB: e.db, Stores: testRepos(e.db), Tx: transact.NewTx(e.db, testBinder(e.db)), Runtime: rt, Logger: e.log}, Options{})
	e2.step(ctx) // 幂等重放同 Generation
	e2.handleObservation(ctx, workloadEventRunning(tAppID+"-web", d.Generation))
	e2.step(ctx)
	advanceClock(t, e2, 61*time.Second)
	e2.step(ctx)

	final := getDeployment(t, e2, d.ID)
	require.Equal(t, deployment.StateSucceeded, final.State)
	calls := rt.calls()
	assert.Greater(t, len(calls), gens, "replay must re-ensure after the kill")
	assert.Equal(t, capability.Generation(d.Generation), calls[len(calls)-1].Gen)
}

// 节点对账：快照消失的节点在宽限窗满后 → node.left 事件 + 观测缓存下线
// （一次性；S11b 窗内单拍不触发）。
func TestNodeLeftReconciliation(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	e.managedStep(ctx) // 首轮：node-1 在快照内（upsert）

	// 节点消失（fake cluster 视图切空——空视图也是显式结果）。
	rt.mu.Lock()
	rt.clusterOverride = true
	rt.clusterView = capability.ClusterView{}
	rt.mu.Unlock()
	e.managedStep(ctx) // 宽限窗起算：单拍缺行不清理
	assert.Empty(t, eventNames(t, e, "01JD0NODE00000000000000000"))

	clock.Advance(e.opts.Node.LeaveGrace)
	e.managedStep(ctx)
	assert.Equal(t, []string{"node.left"}, eventNames(t, e, "01JD0NODE00000000000000000"))
	n, err := e.repos.Nodes.Get(ctx, "01JD0NODE00000000000000000")
	require.NoError(t, err)
	assert.False(t, n.Available, "departed node marked unavailable in the cache")

	// 再对账不重复发（去抖）。
	e.managedStep(ctx)
	assert.Len(t, eventNames(t, e, "01JD0NODE00000000000000000"), 1)
}
