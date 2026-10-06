package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
)

// C2 性质测试：同一 Revision 经 release / rollback / 基线重放三路物化，
// 下发到 Runtime 的（NamespaceRef、Workload 集、Materials）逐字段一致——
// 三路共用 materialize 单序列，任何一路私有加步/漏步（pinVolumes 曾只在
// release）在此红。Generation 各路语义不同（release=原代；rollback=新代
// 重发；重放=行上代），单独断言。spec 带卷：钉住与 Placement 合并全程
// 参与对比（历史分叉点正是漏 pinVolumes）。
func TestMaterializeThreePathsIdentical(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()

	require.NoError(t, e.volumes.Create(ctx, e.db.Runner(), &volume.Volume{
		ID: "01JD0VOL00000000000000000", ProjectID: tProjectID, Name: "data",
	}))
	volSpec := `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":1,"volumes":[{"volume_id":"data"}]}]}`
	rev1 := freezeSpec(t, e, 1, volSpec)
	rev2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.28"))

	// 路径 1：release（v1 首次部署成功）。
	deployToSucceeded(t, e, rev1)
	release := lastEnsure(rt)

	// 路径 2：rollback（v2 部署注入 Ensure 失败 → 自动回滚重放 v1）。
	rt.mu.Lock()
	rt.failNext = true
	rt.mu.Unlock()
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx) // releasing Ensure 失败 → failed → rolling-back → 重放 v1（新 gen）
	rb := getDeployment(t, e, d2.ID)
	require.Equal(t, deployment.StateRollingBack, rb.State)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", rb.Generation))
	e.step(ctx)
	advanceClock(t, e, 61*time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d2.ID).State)
	rbEnsure := lastEnsure(rt)

	// 路径 3：基线重放（重启语义：无活跃部署时按行上 Generation 重下发）。
	e.rebuildBaselines(ctx)
	replay := lastEnsure(rt)

	assert.Equal(t, release.NS.String(), rbEnsure.NS.String(), "rollback namespace must match release")
	assert.Equal(t, release.NS.String(), replay.NS.String(), "baseline replay namespace must match release")
	// 逐载体 Generation 锚随各路 gen 语义走（ADR-0048：stampDeployment
	// Generations 落 gen 于载体）——spec 内容一致性比对先归一该字段。
	stripped := func(c ensureCall) map[string]capability.Workload {
		out := map[string]capability.Workload{}
		for k, w := range c.Spec {
			w.Generation, w.GenerationScoped = 0, false
			out[k] = w
		}
		return out
	}
	assert.Equal(t, stripped(release), stripped(rbEnsure), "rollback must dispatch identical workloads")
	assert.Equal(t, stripped(release), stripped(replay), "baseline replay must dispatch identical workloads")
	assert.Equal(t, release.Spec["web"].ID, rbEnsure.Spec["web"].ID, "rolling workloads keep one identity across paths")
	assert.Equal(t, release.Materials, rbEnsure.Materials, "rollback must dispatch identical materials")
	assert.Equal(t, release.Materials, replay.Materials, "baseline replay must dispatch identical materials")

	// 卷钉住全程在场：Placement 合并的节点约束三路一致（历史分叉锚）。
	require.Len(t, release.Spec["web"].Placement.NodeIDs, 1, "release must carry the merged volume pin")
	assert.Equal(t, release.Spec["web"].Placement.NodeIDs, rbEnsure.Spec["web"].Placement.NodeIDs)
	assert.Equal(t, release.Spec["web"].Placement.NodeIDs, replay.Spec["web"].Placement.NodeIDs)

	// Generation 各路语义。
	assert.Equal(t, capability.Generation(1), release.Gen)
	assert.Equal(t, capability.Generation(3), rbEnsure.Gen, "rollback replays on a fresh generation (v1=1, v2=2)")
	assert.Equal(t, capability.Generation(3), replay.Gen, "baseline replay re-dispatches the row generation")
}

// lastEnsure 返回最近一次 Ensure 调用快照。
func lastEnsure(rt *fakeRuntime) ensureCall {
	calls := rt.calls()
	if len(calls) == 0 {
		return ensureCall{}
	}
	return calls[len(calls)-1]
}
