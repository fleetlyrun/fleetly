package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// 测试常量（固定 ID/时间，事件断言确定性）。
const (
	tProjectID = "01JD0PROJ00000000000000000"
	tAppID     = "01JD0APP000000000000000000"
	tApp2ID    = "01JD0APP000000000000000001" // 跨 App 场景的第二 App
	tImageSpec = `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27","replicas":1}]}`
)

// newTestEngine 建 hermetic 引擎（不 Start；step 由测试手动驱动，观测经
// handleObservation 直注——不依赖真实节拍）。
func newTestEngine(t *testing.T) (*Engine, *fakeRuntime, *statetest.FakeClock) {
	t.Helper()
	return newTestEngineOpts(t, Options{})
}

// newTestEngineOpts 同 newTestEngine，但注入 Options（重叠策略旋钮等）。
func newTestEngineOpts(t *testing.T, opts Options) (*Engine, *fakeRuntime, *statetest.FakeClock) {
	t.Helper()
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	e := New(Deps{DB: db, Runtime: rt, Logger: discardLogger()}, opts)
	t.Cleanup(func() { _ = e.Stop(context.Background()) })

	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	require.NoError(t, app.New(clock).Create(ctx, db.Runner(), &app.App{
		ID: tAppID, ProjectID: tProjectID, Name: "web",
	}))
	return e, rt, clock
}

// freezeSpec 冻结一条 Revision 并返回其 ID（n 保证跨调用唯一）。
func freezeSpec(t *testing.T, e *Engine, n int, specJSON string) string {
	t.Helper()
	return freezeSpecForApp(t, e, tAppID, n, specJSON)
}

// freezeSpecForApp 同 freezeSpec，但锚定指定 App（跨 App 场景夹具）。
func freezeSpecForApp(t *testing.T, e *Engine, appID string, n int, specJSON string) string {
	t.Helper()
	rev := &revision.Revision{
		ID: fmt.Sprintf("01JD0REV0000000000000000%d", n), AppID: appID,
		Spec: []byte(specJSON),
	}
	var err error
	rev.Seq, err = e.revisions.NextSeq(context.Background(), e.db.Runner(), appID)
	require.NoError(t, err)
	require.NoError(t, e.revisions.Create(context.Background(), e.db.Runner(), rev))
	return rev.ID
}

func getDeployment(t *testing.T, e *Engine, id string) *deployment.Deployment {
	t.Helper()
	d, err := e.deployments.Get(context.Background(), e.db.Runner(), id)
	require.NoError(t, err)
	return d
}

func eventNames(t *testing.T, e *Engine, aggregateID string) []string {
	t.Helper()
	evs, err := e.outbox.ListAfter(context.Background(), e.db.Runner(), 0, 1000)
	require.NoError(t, err)
	var names []string
	for _, ev := range evs {
		if aggregateID == "" || ev.AggregateID == aggregateID {
			names = append(names, ev.Name)
		}
	}
	return names
}

// 全链：镜像直投 → preparing → releasing（Ensure gen=1）→ L1 running →
// observing → 时钟过 L3 窗 → succeeded；事件序列四件一拍完整。
func TestDeployImageHappyPath(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	revID := freezeSpec(t, e, 1, tImageSpec)

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	assert.Equal(t, deployment.StateQueued, d.State)
	assert.Equal(t, uint64(1), d.Generation)

	e.step(ctx) // queued → preparing → releasing（首次 Ensure + L1 deadline）
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State)

	calls := rt.calls()
	require.NotEmpty(t, calls)
	first := calls[0]
	assert.Equal(t, capability.Generation(1), first.Gen)
	assert.Equal(t, "nginx:1.27", first.Spec["web"].Image)
	assert.Equal(t, tAppID+"-web", first.Spec["web"].ID, "workload ID is stable app+process")
	for _, c := range calls {
		assert.Equal(t, capability.Generation(1), c.Gen, "replays keep the same generation until L1 passes")
	}

	// L1：running 观测（watch 路径）→ observing。
	ws := []capability.Workload{{ID: tAppID + "-web", Process: "web"}}
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateObserving, d.State)
	assert.NotEmpty(t, d.ObserveDeadline)

	// L3：时钟过窗 → succeeded。
	clock.Advance(61 * time.Second)
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateSucceeded, d.State)

	assert.Equal(t, []string{
		"deployment.queued", "deployment.preparing", "deployment.releasing",
		"deployment.observing", "deployment.succeeded",
	}, eventNames(t, e, d.ID))
	_ = ws
}

// 失败自动回滚 = Revision Replay：Ensure 失败 → failed → rolling-back →
// Ensure(from_revision spec, 新 gen) → observing → succeeded（to_revision
// 修正为回放目标；场景 2 的"重建缺失对象"由 Ensure 域内收敛语义承载）。
func TestFailureAutoRollbackReplay(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()

	// 先落一条成功基线（R1 = nginx:1.26）。
	r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
	first, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r1})
	require.NoError(t, err)
	e.step(ctx)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, first.ID).State)

	// 再部署 R2（nginx:1.27），注入 Ensure 失败 → 自动回滚到 R1。
	rt.failNext = true // R2 首次下发即失败
	r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.27"))
	second, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2})
	require.NoError(t, err)
	e.step(ctx) // preparing → releasing → Ensure 失败 → failed → rolling-back（回放 Ensure）
	d := getDeployment(t, e, second.ID)
	require.Equal(t, deployment.StateRollingBack, d.State, "auto rollback should be in flight")

	// 回放就绪 → observing → succeeded。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 3))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	d = getDeployment(t, e, second.ID)
	require.Equal(t, deployment.StateSucceeded, d.State)
	assert.Equal(t, r1, d.ToRevision, "terminal fact points at the replayed revision")
	assert.Contains(t, d.Error, "rolled back to previous revision")

	calls := rt.calls()
	require.GreaterOrEqual(t, len(calls), 3)
	assert.Equal(t, "nginx:1.26", calls[len(calls)-1].Spec["web"].Image, "replay re-ensures the from_revision spec")
	assert.Equal(t, capability.Generation(3), calls[len(calls)-1].Gen, "replay uses a fresh monotonic generation")

	assert.Equal(t, []string{
		"deployment.queued", "deployment.preparing", "deployment.releasing",
		"deployment.failed", "deployment.rolling_back", "deployment.observing",
		"deployment.succeeded",
	}, eventNames(t, e, second.ID))
}

// admission：同幂等键去重、commit 去重、latest-wins 合并、queue_full、
// 显式 supersede、排队与在途取消（ADR-0016 场景 9）。
func TestAdmissionSemantics(t *testing.T) {
	ctx := context.Background()

	t.Run("idempotency key dedup returns existing", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		rev := freezeSpec(t, e, 1, tImageSpec)
		a, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev, IdempotencyKey: "k1"})
		require.NoError(t, err)
		b, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev, IdempotencyKey: "k1"})
		require.NoError(t, err)
		assert.Equal(t, a.ID, b.ID)
	})

	t.Run("commit sha dedup", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		rev := freezeSpec(t, e, 1, tImageSpec)
		a, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev, CommitSHA: "abc123"})
		require.NoError(t, err)
		b, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev, CommitSHA: "abc123"})
		require.NoError(t, err)
		assert.Equal(t, a.ID, b.ID)
	})

	t.Run("latest wins merges queued", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
		r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.27"))
		a, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r1})
		require.NoError(t, err)
		b, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2})
		require.NoError(t, err)
		assert.Equal(t, uint64(2), b.Generation)
		e.step(ctx) // latest-wins 收口 + 驱动最新
		assert.Equal(t, deployment.StateSuperseded, getDeployment(t, e, a.ID).State)
		assert.Equal(t, b.ID, getDeployment(t, e, a.ID).SupersededBy)
		assert.Equal(t, deployment.StateReleasing, getDeployment(t, e, b.ID).State)
	})

	t.Run("queue full is explicit feedback", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		e.opts.QueueCapacity = 1
		rev := freezeSpec(t, e, 1, tImageSpec)
		_, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
		require.NoError(t, err)
		_, err = e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
		assert.ErrorIs(t, err, ErrQueueFull)
	})

	t.Run("explicit supersede preempts in-flight", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
		r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.27"))
		a, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r1})
		require.NoError(t, err)
		e.step(ctx) // a → releasing（在途）
		require.Equal(t, deployment.StateReleasing, getDeployment(t, e, a.ID).State)

		b, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2, Supersede: true})
		require.NoError(t, err)
		assert.Equal(t, deployment.StateSuperseded, getDeployment(t, e, a.ID).State)
		assert.Equal(t, b.ID, getDeployment(t, e, a.ID).SupersededBy)
	})

	t.Run("cancel queued and in-flight", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		rev := freezeSpec(t, e, 1, tImageSpec)
		a, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
		require.NoError(t, err)
		out, err := e.Cancel(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, deployment.StateCancelled, out.State)
		_, err = e.Cancel(ctx, a.ID)
		assert.ErrorIs(t, err, ErrNotCancellable, "terminal deployments are not cancellable")
	})
}

// 场景 1 核心：引擎重启（进程被杀等价）后，在途 releasing 按 Generation
// 幂等重放——新引擎实例对同 Generation 重新 Ensure，不重复推进状态。
func TestEngineRestartReplaysInFlightGeneration(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx) // → releasing（Ensure gen=1 + L1 deadline 落库）
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d.ID).State)

	// "重启"：新引擎实例（观测/Ensure 内存缓存清空），同库同 runtime。
	e2 := New(Deps{DB: e.db, Runtime: rt, Logger: e.log}, Options{})
	e2.step(ctx)

	still := getDeployment(t, e2, d.ID)
	assert.Equal(t, deployment.StateReleasing, still.State, "restart re-ensures without state regression")
	calls := rt.calls()
	require.GreaterOrEqual(t, len(calls), 2, "restart must re-ensure the in-flight generation")
	assert.Equal(t, capability.Generation(1), calls[len(calls)-1].Gen, "replay keeps the same generation")
}

// Drift：观测 Generation 偏离最近 Ensure → workload.drift_detected 一次
// （去抖），回归后再次偏离才发第二条。
func TestDriftDetectionDebounced(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx) // Ensure gen=1
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d.ID).State)

	// 人工干预等价：观测到旧/异 gen → drift 一次。
	e.handleObservation(ctx, capability.WorkloadEvent{WorkloadID: tAppID + "-web", Generation: 0, State: capability.WorkloadStopped, Message: "manually updated"})
	e.handleObservation(ctx, capability.WorkloadEvent{WorkloadID: tAppID + "-web", Generation: 0, State: capability.WorkloadStopped, Message: "manually updated"})
	names := eventNames(t, e, tAppID+"-web")
	assert.Equal(t, []string{"workload.drift_detected"}, names, "same signature must debounce")

	// 回归 expected gen 清签名 → 再次偏离发第二条。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.handleObservation(ctx, capability.WorkloadEvent{WorkloadID: tAppID + "-web", Generation: 2, State: capability.WorkloadRunning})
	names = eventNames(t, e, tAppID+"-web")
	assert.Len(t, names, 2, "second drift after recovery emits again")
}

// 节点锚定落库：Minted 的 node.joined 发事件 + nodes 观测缓存 upsert。
func TestNodeJoinedAnchoring(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	e.handleObservation(ctx, capability.WorkloadEvent{NodeJoined: &capability.NodeJoined{
		NodeID: "01JD0NODE00000000000000000", CarrierID: "swarmabc", Minted: true,
	}})
	// 重复锚定扫描（非 Minted）：缓存刷新、不重复发事件。
	e.handleObservation(ctx, capability.WorkloadEvent{NodeJoined: &capability.NodeJoined{
		NodeID: "01JD0NODE00000000000000000", CarrierID: "swarmabc",
	}})
	assert.Equal(t, []string{"node.joined"}, eventNames(t, e, "01JD0NODE00000000000000000"))
	n, err := e.nodes.Get(ctx, e.db.Runner(), "01JD0NODE00000000000000000")
	require.NoError(t, err)
	assert.Equal(t, "swarmabc", n.CarrierID)
	assert.True(t, n.Available)
}

func workloadEventRunning(wid string, gen uint64) capability.WorkloadEvent {
	return capability.WorkloadEvent{WorkloadID: wid, Generation: capability.Generation(gen), State: capability.WorkloadRunning}
}

// 滚动更新期看门狗不咬旧代事件（L2 假咬合回归，F0.12）：观测槽按
// Workload ID last-write-wins，旧 task 的 stopped@旧代 迟到事件会覆盖
// running@当前代——看门狗必须只认当前代（dind 真机场景 1 曾秒败进
// rolling-back，根因形态见批记录）。
func TestWatchdogIgnoresStaleGenerationStops(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()

	// gen1 部署走完整链到 succeeded（基线）。
	rev := freezeSpec(t, e, 1, tImageSpec)
	d1, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d1.ID).State)

	// gen2 部署（同 spec 重部署 = 滚动更新形态）：running@2 先到，
	// 旧代 stopped@1 迟到覆盖观测槽。
	d2, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev})
	require.NoError(t, err)
	e.step(ctx)
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d2.ID).State)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 2))
	e.step(ctx)
	require.Equal(t, deployment.StateObserving, getDeployment(t, e, d2.ID).State)

	e.handleObservation(ctx, capability.WorkloadEvent{
		WorkloadID: tAppID + "-web", Generation: capability.Generation(1),
		State: capability.WorkloadStopped, Message: "old task shutdown",
	})
	e.step(ctx)
	still := getDeployment(t, e, d2.ID)
	require.Equal(t, deployment.StateObserving, still.State,
		"a stale-generation stop must not bite the current generation's watchdog (error: %s)", still.Error)

	// 窗口走满照常收口 succeeded。
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d2.ID).State)
}

func imageSpecFor(ref string) string {
	return imageSpecForApp(tAppID, ref)
}

// imageSpecForApp 构造锚定指定 App 的 image 直投 spec（跨 App 场景夹具）。
func imageSpecForApp(appID, ref string) string {
	return `{"schema_version":1,"app":{"id":"` + appID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"` + ref + `"}},"processes":[{"name":"web","image":"` + ref + `","replicas":1}]}`
}

// 事件 payload 反序列化守卫（订阅面契约：字段名/类型只增不变）。
func TestEventPayloadSchema(t *testing.T) {
	var p deploymentEventPayload
	require.NoError(t, json.Unmarshal([]byte(`{"deployment_id":"x","app_id":"y","state":"succeeded","generation":3}`), &p))
	assert.Equal(t, "x", p.DeploymentID)

	var d driftEventPayload
	require.NoError(t, json.Unmarshal([]byte(`{"workload_id":"w","app_id":"a","expected_generation":2,"observed_generation":1,"observed_state":"stopped"}`), &d))
	assert.Equal(t, uint64(2), d.ExpectedGeneration)
}
