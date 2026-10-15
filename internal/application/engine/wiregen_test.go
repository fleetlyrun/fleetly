package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// tTwoProcSpec：双进程夹具（web + sidecar）——归因回路的未变/已变载体
// 对照面。V2 只换 web 镜像、sidecar 逐字节不变。
const tTwoProcSpec = `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
	`"source":{"image":{"ref":"nginx:1.27"}},` +
	`"processes":[{"name":"web","image":"nginx:1.27","replicas":1},` +
	`{"name":"sidecar","image":"envoy:1.31","replicas":1}]}`

const tTwoProcSpecV2 = `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
	`"source":{"image":{"ref":"nginx:1.27"}},` +
	`"processes":[{"name":"web","image":"nginx:1.29","replicas":1},` +
	`{"name":"sidecar","image":"envoy:1.31","replicas":1}]}`

// ADR-0064 决策 4 回归锚：跨代部署里未变载体按回执 wire-gen 记账——
// swarm 侧跳过/scale 分支不换代戳，观测事件（任务级 gen 标签）按旧代
// 上报。若引擎仍全员按部署 gen 记账，L1 门会等一个永远不会到来的
// running@部署gen 而假超时（staging 2026-10-11 事故的引擎侧形态）。
func TestWireGenAttributionUnchangedWorkloadsDontBlockL1(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()

	// gen 1：双进程首部署，两载体都滚（回执空 = 全员调用 gen）。
	rev1 := freezeSpec(t, e, 1, tTwoProcSpec)
	d1, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev1})
	require.NoError(t, err)
	e.step(ctx) // → releasing
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-sidecar", 1))
	e.step(ctx)
	clock.Advance(e.opts.ObserveWindow + time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d1.ID).State)

	// gen 2：新 revision（web 内容变化驱动新代）。回执模拟 swarm 三分：
	// web 滚动（缺席 = 调用 gen 2）；sidecar 内容未变被跳过（wire-gen 1，
	// 服务保留旧代戳）。
	rt.wireGens = map[string]capability.Generation{tAppID + "-sidecar": 1}
	rev2 := freezeSpec(t, e, 2, tTwoProcSpecV2) // web 镜像变化驱动新代；sidecar 逐字节不变
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx) // → releasing（materialize 消费回执）
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d2.ID).State)

	// 归因锚断言：web @2、sidecar @1（未变载体不被部署 gen 覆写）。
	webGen, ok := e.obs.ensuredGenOf(tAppID + "-web")
	require.True(t, ok)
	assert.Equal(t, uint64(2), webGen, "rolled workload anchors at the deployment gen")
	sideGen, ok := e.obs.ensuredGenOf(tAppID + "-sidecar")
	require.True(t, ok)
	assert.Equal(t, uint64(1), sideGen, "unchanged workload keeps its wire gen")

	// L1：只等已变载体（web running@2 到达即放行）——sidecar 的旧代
	// 观测不阻塞、也不再需要新观测。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 2))
	e.step(ctx)
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, d2.ID).State,
		"L1 must pass on the changed set only — unchanged workloads must not block on a running@deploy-gen that never arrives")

	// L3 收口。
	clock.Advance(e.opts.ObserveWindow + time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d2.ID).State)
}

// ADR-0064 纯 scale 部署（staging 2026-10-11 用户场景的本体）：全部载体
// wire-gen 停旧值（跳过/scale 不换代）→ L1 门集合为空 = vacuously
// ready——不得等一个永远不会到来的 running@部署gen（空集判假会把纯
// scale 部署卡成 L1 超时 + 回滚也超时的双门事故）。
func TestPureScaleDeploymentPassesL1(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()

	rev1 := freezeSpec(t, e, 1, tTwoProcSpec)
	d1, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev1})
	require.NoError(t, err)
	e.step(ctx)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-sidecar", 1))
	e.step(ctx)
	clock.Advance(e.opts.ObserveWindow + time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d1.ID).State)

	// gen 2：web 副本 1→2（纯 scale）。回执全员 wire-gen=1（swarm scale-
	// only 分支保模板；sidecar 跳过）。
	rt.wireGens = map[string]capability.Generation{
		tAppID + "-web":     1,
		tAppID + "-sidecar": 1,
	}
	scaled := `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"nginx:1.27"}},` +
		`"processes":[{"name":"web","image":"nginx:1.27","replicas":2},` +
		`{"name":"sidecar","image":"envoy:1.31","replicas":1}]}`
	rev2 := freezeSpec(t, e, 2, scaled)
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx) // materialize + 空集 L1 → 一拍直落 observing
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, d2.ID).State,
		"pure-scale deployments must pass L1 vacuously (no workload rolled at the deployment gen)")

	clock.Advance(e.opts.ObserveWindow + time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d2.ID).State)
}
