package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// deployToSucceeded 辅助：完整部署一条 Revision 到终态成功（返回 deployment）。
func deployToSucceeded(t *testing.T, e *Engine, revID string) *deployment.Deployment {
	t.Helper()
	ctx := context.Background()
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
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

// Rollback 一等动词：默认回到上一成功基线；显式抢占在途；审计标注回放。
func TestRollbackReplayVerb(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
	r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.27"))

	deployToSucceeded(t, e, r1)
	deployToSucceeded(t, e, r2)

	// 在途一条（r2 再部署）后 Rollback：抢占在途、目标 = 上一成功基线 r2。
	inFlight, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2})
	require.NoError(t, err)
	e.step(ctx) // → releasing（在途）

	rb, err := e.Rollback(ctx, tAppID, "")
	require.NoError(t, err)
	assert.Equal(t, r2, rb.ToRevision, "empty target rolls back to the last succeeded baseline")
	assert.Equal(t, deployment.StateSuperseded, getDeployment(t, e, inFlight.ID).State, "rollback preempts in-flight")

	// 显式目标回放（r1）。
	rb2, err := e.Rollback(ctx, tAppID, r1)
	require.NoError(t, err)
	assert.Equal(t, r1, rb2.ToRevision)
}

// Rollback 无成功基线 → 明确错误（首次部署无回滚对象）。哨兵锚定
// （Q-13）：API 层经 errors.Is 判定 E_NO_BASELINE，文案改写不得破坏契约。
func TestRollbackWithoutBaseline(t *testing.T) {
	e, _, _ := newTestEngine(t)
	_, err := e.Rollback(context.Background(), tAppID, "")
	require.ErrorIs(t, err, ErrNoSuccessfulBaseline)
	assert.ErrorContains(t, err, "no successful baseline")
}

// 场景 1 完整回归：releasing 中途"被杀"（引擎排空 + 新实例）→ 按
// Generation 幂等重放 → 终态成功（领域模型场景 1）。
func TestScenario1KilledMidReleaseReplay(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx)
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State)
	gens := len(rt.calls())

	// "被杀"：引擎停止（观测/Ensure 缓存清空语义）→ 新实例恢复。
	require.NoError(t, e.Stop(context.Background()))
	e2 := New(Deps{DB: e.db, Runtime: rt, Logger: e.log}, Options{})
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

// 节点对账：快照消失的节点 → node.left 事件 + 观测缓存下线（一次性）。
func TestNodeLeftReconciliation(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	e.managedStep(ctx) // 首轮：node-1 在快照内（upsert）

	// 节点消失（fake cluster 视图切空——空视图也是显式结果）。
	rt.mu.Lock()
	rt.clusterOverride = true
	rt.clusterView = capability.ClusterView{}
	rt.mu.Unlock()
	e.managedStep(ctx)

	assert.Equal(t, []string{"node.left"}, eventNames(t, e, "01JD0NODE00000000000000000"))
	n, err := e.nodes.Get(ctx, e.db.Runner(), "01JD0NODE00000000000000000")
	require.NoError(t, err)
	assert.False(t, n.Available, "departed node marked unavailable in the cache")

	// 再对账不重复发（去抖）。
	e.managedStep(ctx)
	assert.Len(t, eventNames(t, e, "01JD0NODE00000000000000000"), 1)
}
