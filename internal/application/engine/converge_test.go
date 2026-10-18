package engine

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/freeze"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// ADR-0082 缺省收敛钉测族：裁决表十行 golden + 快慢通道 + 签名生命周期 +
// 冷却三径 + 冻结 fail-closed + 缺席重建 + 幽灵负向 + teardown 竞态 +
// posture 闸门 + Cancel 遗留回卷 + 账本盲区修复。

var verdictGoldenUpdate = flag.Bool("update", false, "rewrite converge verdict golden")

// ---- 夹具 ----

// settleSlowChannel 跑一拍扫描让慢通道物化备忘落定（部署链不播 converge
// memo——进程内首拍重放一次是有意语义）；后续拍未推进时钟即 memo 在期，
// 任何新增 Ensure 都是信号驱动的快通道动作。
func settleSlowChannel(t *testing.T, e *Engine) {
	t.Helper()
	e.driftScan(context.Background())
}

// ensureDelta 返回扫描前后新增的 Ensure 调用数。
func ensureDelta(t *testing.T, rt *fakeRuntime, before int) int {
	t.Helper()
	return len(rt.calls()) - before
}

// eventNamesOf 按事件名过滤全量事件（converged/converge_failed 是 app 聚合
// 级——与 workload 级 eventNames 的过滤锚不同）。
func eventNamesOf(t *testing.T, e *Engine, name string) []string {
	t.Helper()
	evs, err := e.repos.Outbox.ListAfter(context.Background(), 0, 1000, "")
	require.NoError(t, err)
	var out []string
	for _, ev := range evs {
		if ev.Name == name {
			out = append(out, ev.AggregateID)
		}
	}
	return out
}

// latestSucceededID 返回测试 App 最近 succeeded 行 ID。
func latestSucceededID(t *testing.T, e *Engine) string {
	t.Helper()
	d, err := e.repos.Deployments.LatestSucceeded(context.Background(), tAppID)
	require.NoError(t, err)
	return d.ID
}

// setTeamFreeze 落一条 Team 活跃冻结并返回 ID（lift 复位用）。
func setTeamFreeze(t *testing.T, e *Engine) string {
	t.Helper()
	f := &freeze.Freeze{TeamID: "default", Reason: "converge pin test", CreatedBy: "test"}
	require.NoError(t, e.repos.Freezes.Create(context.Background(), f))
	return f.ID
}

// ---- 裁决表 golden（ADR-0082 §1 十行落码——表即断言）----

func TestConvergeVerdictGolden(t *testing.T) {
	rows := []struct {
		name string
		in   convergeVerdictInput
	}{
		{"01-spec-mismatch", convergeVerdictInput{SpecMismatch: true}},
		{"02-absent-carrier", convergeVerdictInput{Absent: true}},
		{"03-gen-deviation", convergeVerdictInput{GenDeviated: true}},
		{"04-rollout-stalled-terminal", convergeVerdictInput{RolloutStalled: true}},
		{"05-steady-stopped-spec-match", convergeVerdictInput{SteadyStopped: true}},
		{"06-active-deployment-incl-failed-rollback", convergeVerdictInput{Driving: true}},
		{"07-frozen", convergeVerdictInput{Frozen: true}},
		{"08-extra-carriers-in-domain", convergeVerdictInput{ExtraCarriers: true}},
		{"09-orphan-out-of-domain", convergeVerdictInput{OrphanObserved: true}},
		{"10-cancel-legacy-partial-issue", convergeVerdictInput{CancelLegacy: true}},
	}
	var b strings.Builder
	for _, r := range rows {
		got := convergeVerdict(r.in)
		fmt.Fprintf(&b, "%s %s\n", r.name, got)
	}
	path := filepath.Join("testdata", "converge-verdicts.golden")
	if *verdictGoldenUpdate {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750)) //nolint:gosec // 测试产物目录
		require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // 读取本包 testdata 自有夹具
	require.NoError(t, err, "golden missing; run go test ./internal/application/engine -update")
	assert.Equal(t, string(want), b.String(),
		"converge verdict table drifted from golden (ADR-0082 §1 ten rows are pinned)")
}

// ---- spec 失配 → 收敛 + 事件（检测与事件面照发，收敛半边增收）----

func TestConvergeOnSpecMismatchRestoresBaseline(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()
	before := len(rt.calls())
	e.driftScan(context.Background())

	// 收敛 + 事件双面：基线镜像重下发（gen 1）+ workload.converged。
	calls := rt.calls()
	require.Len(t, calls, before+1)
	assert.Equal(t, capability.Generation(1), calls[before].Gen, "converge re-issues the baseline generation")
	assert.Equal(t, "nginx:1.27", calls[before].Spec["web"].Image, "converge restores the baseline image")
	require.Len(t, eventNamesOf(t, e, "workload.converged"), 1)
	require.Len(t, eventNamesOf(t, e, "workload.drift_detected"), 1, "detection face unchanged")

	// 审计行 source=system、AfterFP=基线 digest、不建 Deployment 行。
	entries, err := e.repos.Audits.ListFiltered(context.Background(), audit.Filter{ActionPrefix: "workload.converge", Limit: 10})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, audit.SourceSystem, entries[0].Source)
	revRow, err := e.repos.Revisions.Get(context.Background(), rev)
	require.NoError(t, err)
	assert.Equal(t, revRow.Digest, entries[0].AfterFP, "audit AfterFP carries the baseline digest")
	assert.Equal(t, "app/"+tAppID, entries[0].Resource)

	// 去抖：同一失配（签名已收敛 + memo 在期）不重复收敛不重复发事件。
	e.driftScan(context.Background())
	assert.Len(t, rt.calls(), before+1)
	assert.Len(t, eventNamesOf(t, e, "workload.converged"), 1)
}

// ---- 冻结闸门（fail-closed）：挂起不消费签名，解冻重扫即收敛 ----

func TestFreezeSuspendsConvergeUntilLift(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	freezeID := setTeamFreeze(t, e)
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()
	before := len(rt.calls())
	e.driftScan(context.Background())

	// 冻结期：检测与事件照发、收敛挂起（无 Ensure、无 converged）。
	assert.Equal(t, 0, ensureDelta(t, rt, before), "frozen app must not converge (no ensure)")
	assert.Len(t, eventNamesOf(t, e, "workload.drift_detected"), 1, "detection face keeps firing during freeze")
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"), "converge suspended during freeze")

	// 解冻：下一拍重扫即自然收敛（挂起不落签名——重扫重触发）。
	require.NoError(t, e.repos.Freezes.Lift(context.Background(), freezeID))
	e.driftScan(context.Background())
	assert.Equal(t, 1, ensureDelta(t, rt, before), "post-lift rescan converges")
	assert.Len(t, eventNamesOf(t, e, "workload.converged"), 1)
}

// Team 解析失败 fail-closed（跳过本拍，不走 ownerTeam 降级 '' 契约）：
// App 行消失后收敛闸门如实放弃（ Get 活跃行口径报错 → 本拍跳过）。

func TestFreezeGateFailClosedOnUnresolvableTeam(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	// 直接注入一个基线锚 + 信号，让收敛判定指向已删 App（appTeam 解析失败）。
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()
	require.NoError(t, e.repos.Apps.SoftDelete(context.Background(), tAppID))
	before := len(rt.calls())
	e.driftScan(context.Background())
	assert.Equal(t, 0, ensureDelta(t, rt, before), "unresolvable team must skip the round (fail-closed)")
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"))
}

// ---- 活跃部署跳过（含 failed 待自动回滚窗）----

func TestConvergeSkipsDrivingAppIncludingFailedRollbackWindow(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	// 排队中的部署即驱动权在部署链。
	rev2 := freezeSpec(t, e, 2, `{"schema_version":1,"app":{"id":"`+tAppID+`","project":"`+tProjectID+`"},`+
		`"source":{"image":{"ref":"nginx:1.28"}},"processes":[{"name":"web","image":"nginx:1.28","replicas":1}]}`)
	_, _, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()
	before := len(rt.calls())
	e.driftScan(context.Background())
	assert.Equal(t, 0, ensureDelta(t, rt, before), "queued deployment owns the app; converge must skip")
	assert.Len(t, eventNamesOf(t, e, "workload.drift_detected"), 1, "detection face keeps firing while driving")

	// failed 待自动回滚窗：release Ensure 失败 → failed（基线在，待回滚）
	// → 同一 step 链式进 rolling-back——整窗都在驱动集，ListDriving 同口径
	// 复查在此闭合「收敛×回滚交错」竞态。
	rt.failNext = true
	e.step(context.Background())
	rt.failNext = false
	driving, err := e.repos.Deployments.DrivingByApp(context.Background(), tAppID)
	require.NoError(t, err)
	require.Len(t, driving, 1, "the rollback window stays in the driving set")
	require.NotEqual(t, "succeeded", string(driving[0].State))
	before = len(rt.calls()) // 驱动器自身的回放 Ensure 不计入收敛断言
	e.driftScan(context.Background())
	assert.Equal(t, 0, ensureDelta(t, rt, before), "the rollback window still owns the app; converge must skip")
}

// ---- stopped 不收敛（ADR-0022 decision 3 实质保留）----

func TestSteadyStoppedDoesNotConverge(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	before := len(rt.calls())
	e.handleObservation(context.Background(), capability.WorkloadEvent{
		WorkloadID: tAppID + "-web", Generation: capability.Generation(1),
		State: capability.WorkloadStopped, Message: "container exited",
	})
	e.driftScan(context.Background())
	assert.Equal(t, []string{tAppID + "-web"}, eventNamesOf(t, e, "workload.stopped"), "watchdog still reports")
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"), "steady stop must not converge (swarm restarts on its own)")
	assert.Equal(t, 0, ensureDelta(t, rt, before))
}

// ---- 缺席重建（期望集 = LatestSucceeded 重投影）----

func TestAbsenceRebuildsMissingCarrier(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	// 载体整删（docker service rm 等价）：观测面清零。
	rt.mu.Lock()
	rt.ensures = nil
	rt.mu.Unlock()
	before := len(rt.calls())
	e.driftScan(context.Background())

	calls := rt.calls()
	require.Len(t, calls, before+1, "missing carrier must be rebuilt")
	assert.Equal(t, capability.Generation(1), calls[before].Gen)
	assert.Equal(t, "nginx:1.27", calls[before].Spec["web"].Image)
	require.Len(t, eventNamesOf(t, e, "workload.converged"), 1, "absence convergence emits workload.converged")
}

// ---- 幽灵负向：缓存幽灵不重建（决策输入是 Revision 重投影）----

func TestGhostOldGenerationMemberNotRebuiltAfterBGCollection(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, tImageSpec)
	deployToSucceeded(t, e, rev1)
	oldID := tAppID + "-web"
	newID := genScopedWorkloadID(oldID, 2)

	rev2 := freezeSpec(t, e, 2, bgSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx)
	e.handleObservation(ctx, workloadEventRunning(newID, 2))
	e.step(ctx)
	advanceClock(t, e, 61*time.Second)
	e.step(ctx)
	require.Equal(t, "succeeded", string(getDeployment(t, e, d2.ID).State))

	// 收口后期望账本仍持旧代条目（窗口联合批的缓存幽灵）——缺席对照以
	// 重投影为准：旧代成员不得重建。
	settleSlowChannel(t, e)
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{newID: {image: "nginx:1.28-alpine"}}
	rt.mu.Unlock()
	before := len(rt.calls())
	e.driftScan(context.Background())
	calls := rt.calls()
	require.Len(t, calls, before+1, "new-generation drift converges")
	last := calls[len(calls)-1]
	assert.NotContains(t, last.ByID, oldID, "old-generation member must not be rebuilt (projection is the decision input)")
	_, ok := last.ByID[newID]
	assert.True(t, ok, "new-generation member is the convergence target")
}

func TestGhostRemovedProcessNotRebuiltAfterScaleDown(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	twoProc := `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":1},` +
		`{"name":"worker","image":"nginx:1.27","replicas":1}]}`
	oneProc := `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":1}]}`
	rev1 := freezeSpec(t, e, 1, twoProc)
	d1, _, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev1})
	require.NoError(t, err)
	e.step(context.Background())
	e.handleObservation(context.Background(), workloadEventRunning(tAppID+"-web", d1.Generation))
	e.handleObservation(context.Background(), workloadEventRunning(tAppID+"-worker", d1.Generation))
	e.step(context.Background())
	advanceClock(t, e, 61*time.Second)
	e.step(context.Background())
	require.Equal(t, "succeeded", string(getDeployment(t, e, d1.ID).State))
	rev2 := freezeSpec(t, e, 2, oneProc)
	deployToSucceeded(t, e, rev2)

	// 期望账本仍持 worker 幽灵条目（gen1 Ensure 记录）——重投影期望集
	// 只含 web：worker 不得重建。
	settleSlowChannel(t, e)
	before := len(rt.calls())
	e.driftScan(context.Background())
	assert.Equal(t, 0, ensureDelta(t, rt, before), "steady state with a ledger ghost: nothing to converge")
	for _, c := range rt.calls()[before:] {
		assert.NotContains(t, c.ByID, tAppID+"-worker", "removed process must never be rebuilt")
	}
}

// ---- teardown 竞态不重建（forgetOwner 同锁标记，重建权归部署驱动器）----

func TestTeardownRaceDoesNotRebuild(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	require.NoError(t, e.TeardownApp(context.Background(), tAppID))
	before := len(rt.calls())
	e.driftScan(context.Background())
	assert.Equal(t, 0, ensureDelta(t, rt, before), "post-teardown carriers must not be rebuilt by convergence")
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"))

	// 重建权唯一通道：受理（teardown-abort 语义）清标记，收敛恢复资格。
	rev2 := freezeSpec(t, e, 2, `{"schema_version":1,"app":{"id":"`+tAppID+`","project":"`+tProjectID+`"},`+
		`"source":{"image":{"ref":"nginx:1.28"}},"processes":[{"name":"web","image":"nginx:1.28","replicas":1}]}`)
	_, _, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	assert.False(t, e.converge.teardownMarked(tAppID), "admission hands rebuild ownership back to the deployment driver")
}

// ---- gen 偏离收敛（检测下沉 driftScan：独立签名键 + 收敛信号）----

func TestGenLabelRollbackConverges(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	// docker service rollback 等价：载体回旧代标签（spec 面一致）。
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {gen: 0, genSet: true}}
	rt.mu.Unlock()
	before := len(rt.calls())
	e.driftScan(context.Background())

	assert.Equal(t, 1, ensureDelta(t, rt, before), "gen deviation triggers convergence (no longer cedes to the watch stream)")
	assert.Len(t, eventNamesOf(t, e, "workload.converged"), 1)
	assert.Empty(t, eventNamesOf(t, e, "workload.drift_detected"),
		"scan-face gen deviation emits no duplicate event (watch stream owns that face)")
}

// ---- 终态后滚动停摆恢复（重放途经 resumeStalledRollout）----

func TestTerminalRolloutStallConverges(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	stall := tamperEntry{rollout: "update paused due to failure or early termination of task zzk"}
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": stall}
	rt.mu.Unlock()
	before := len(rt.calls())
	e.driftScan(context.Background())

	assert.Equal(t, 1, ensureDelta(t, rt, before), "terminal stall replays the baseline (provider-side resume)")
	assert.Len(t, eventNamesOf(t, e, "workload.rollout_stalled"), 1, "observation face unchanged")
	assert.Len(t, eventNamesOf(t, e, "workload.converged"), 1)
}

// ---- 签名生命周期 + 冷却（N=3 → 10min + converge_failed 一次；三径解除）----

func convergeFailOnce(t *testing.T, e *Engine, rt *fakeRuntime) {
	t.Helper()
	rt.mu.Lock()
	rt.failNext = true
	rt.mu.Unlock()
	e.driftScan(context.Background())
	rt.mu.Lock()
	rt.failNext = false
	rt.mu.Unlock()
}

func TestConvergeCooldownLifecycle(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()

	// 连续失败 ×3 → 冷却窗 + converge_failed 一次。
	convergeFailOnce(t, e, rt)
	convergeFailOnce(t, e, rt)
	assert.Empty(t, eventNamesOf(t, e, "workload.converge_failed"), "below threshold: no event")
	convergeFailOnce(t, e, rt)
	require.Len(t, eventNamesOf(t, e, "workload.converge_failed"), 1, "cooldown entry emits once")
	e.converge.mu.Lock()
	_, cooling := e.converge.cooldown[tAppID]
	e.converge.mu.Unlock()
	require.True(t, cooling, "third consecutive failure enters the cooldown window")

	// 窗内不尝试（签名生命周期：失败不落签名，重试由冷却 pacing）。
	before := len(rt.calls())
	e.driftScan(context.Background())
	assert.Equal(t, 0, ensureDelta(t, rt, before))
	assert.Len(t, eventNamesOf(t, e, "workload.converge_failed"), 1, "no second event inside the window")

	// 解除径①：超窗自动——重试恢复（失败重新计数，不再立即发事件）。
	clock.Advance(10 * time.Minute)
	convergeFailOnce(t, e, rt)
	e.converge.mu.Lock()
	_, stillCooling := e.converge.cooldown[tAppID]
	nFails := e.converge.fails[tAppID]
	e.converge.mu.Unlock()
	assert.False(t, stillCooling, "window expiry releases the cooldown")
	assert.Equal(t, 1, nFails, "failure counting restarts after release")

	// 再入冷却（×3），解除径②：漂移签名变化重触发（换失配字段 = 新聚合
	// 签名 → 立即可试）。
	convergeFailOnce(t, e, rt)
	convergeFailOnce(t, e, rt)
	convergeFailOnce(t, e, rt)
	e.converge.mu.Lock()
	_, cooling2 := e.converge.cooldown[tAppID]
	e.converge.mu.Unlock()
	require.True(t, cooling2)
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {replicas: 3}}
	rt.mu.Unlock()
	convergeFailOnce(t, e, rt) // 新签名：冷却即释（本次尝试真执行——消耗 failNext）
	e.converge.mu.Lock()
	_, coolingAfterSig := e.converge.cooldown[tAppID]
	sigFails := e.converge.fails[tAppID]
	e.converge.mu.Unlock()
	assert.False(t, coolingAfterSig, "a changed drift signature re-triggers through the cooldown")
	assert.Equal(t, 1, sigFails, "counting restarts for the new signature")

	// 再入冷却，解除径③：posture 切换清零。
	convergeFailOnce(t, e, rt)
	convergeFailOnce(t, e, rt)
	convergeFailOnce(t, e, rt)
	e.converge.mu.Lock()
	_, cooling3 := e.converge.cooldown[tAppID]
	e.converge.mu.Unlock()
	require.True(t, cooling3)
	e.opts.Converge.Posture = ConvergePostureDetect
	e.driftScan(context.Background())
	e.converge.mu.Lock()
	_, coolingAfterSwitch := e.converge.cooldown[tAppID]
	e.converge.mu.Unlock()
	assert.False(t, coolingAfterSwitch, "posture switch clears the ledger (release path 3)")
	assert.True(t, e.SystemConvergeAlertState().State == "ok", "resolved edge cleared the builtin alert")
}

// 冷却记账对启动基线重放一视同仁（不受豁免盲打）。

func TestBaselineReplaySharesCooldownLedger(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	// 冷却窗在位（直接钉记账——聚焦基线路径的闸门消费）。
	now := e.clock.Now()
	e.converge.mu.Lock()
	e.converge.cooldown[tAppID] = now
	e.converge.posture[tAppID] = ConvergePostureAll
	e.converge.failSig[tAppID] = ""
	e.converge.mu.Unlock()

	appRow, err := e.repos.Apps.Get(context.Background(), tAppID)
	require.NoError(t, err)
	before := len(rt.calls())
	e.replayAppBaseline(context.Background(), appRow, getDeployment(t, e, latestSucceededID(t, e)))
	assert.Equal(t, 0, ensureDelta(t, rt, before), "baseline replay is gated by the shared cooldown ledger")
}

// ---- posture 闸门：detect 全跳（检测照发），基线重放不受门限 ----

func TestPostureDetectSkipsConverge(t *testing.T) {
	e, rt, clock := newTestEngineOpts(t, Options{Converge: ConvergeOptions{Posture: ConvergePostureDetect}})
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()
	before := len(rt.calls())
	e.driftScan(context.Background())
	assert.Equal(t, 0, ensureDelta(t, rt, before), "detect posture: no fast-channel converge")
	assert.Equal(t, 0, ensureDelta(t, rt, before), "detect posture: no slow-channel replay")
	assert.Len(t, eventNamesOf(t, e, "workload.drift_detected"), 1, "detection face keeps firing")
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"))

	// 启动基线重放不受 posture 门限（ADR-0005 旧行为保留：缓存重建是
	// 检测面的前提）。
	e2 := New(Deps{DB: e.db, Stores: testRepos(e.db), Tx: transact.NewTx(e.db, testBinder(e.db)), Runtime: rt, Logger: discardLogger()},
		Options{Converge: ConvergeOptions{Posture: ConvergePostureDetect}})
	e2.rebuildBaselines(context.Background())
	assert.Greater(t, len(rt.calls()), before, "baseline replay still runs under detect")
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"), "baseline replay emits no converge event")
}

// posture 缺省 all 钉测（fill 归一）。

func TestConvergePostureDefaultsToAll(t *testing.T) {
	e, _, _ := newTestEngine(t)
	assert.Equal(t, ConvergePostureAll, e.opts.Converge.Posture)
	assert.Equal(t, 3, e.opts.Converge.FailureThreshold)
	assert.Equal(t, 10*time.Minute, e.opts.Converge.Cooldown)
	_, err := ParseConvergePosture("bogus")
	require.Error(t, err, "invalid posture fails fast")
}

// ---- Cancel 遗留载体：回卷 LatestSucceeded 基线（翻案 admission N0 本场景）----

func TestCancelLegacyConvergesBackToBaseline(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev1 := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev1)

	rev2 := freezeSpec(t, e, 2, `{"schema_version":1,"app":{"id":"`+tAppID+`","project":"`+tProjectID+`"},`+
		`"source":{"image":{"ref":"nginx:1.28"}},"processes":[{"name":"web","image":"nginx:1.28","replicas":1}]}`)
	d2, _, err := e.Submit(context.Background(), SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(context.Background()) // releasing：gen2 已下发（部分物化 = 遗留形态）
	_, err = e.Cancel(context.Background(), d2.ID)
	require.NoError(t, err)
	assert.Equal(t, "nginx:1.28", rt.calls()[len(rt.calls())-1].Spec["web"].Image, "legacy carrier is at the cancelled generation")

	// 取消的部署从未成为期望态：慢通道重放回卷基线。
	e.driftScan(context.Background())
	last := rt.calls()[len(rt.calls())-1]
	assert.Equal(t, "nginx:1.27", last.Spec["web"].Image, "cancel legacy rolls back to the succeeded baseline")
	assert.Equal(t, capability.Generation(1), last.Gen)
}

// ---- 账本纪律：「succeeded 无期望缓存 → 收敛触发」（自愈盲修复）----

func TestSucceededWithoutExpectCacheConverges(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	// 「重启」：新实例（期望缓存清空），不跑 rebuildBaselines——扫描面
	// 盲区分支（有 succeeded 基线但无期望缓存的存活 App）接手。
	e2 := New(Deps{DB: e.db, Stores: testRepos(e.db), Tx: transact.NewTx(e.db, testBinder(e.db)), Runtime: rt, Logger: discardLogger()}, Options{})
	e2.driftScan(context.Background())

	assert.Greater(t, len(rt.calls()), 0, "blind-spot face converges")
	e2.expect.mu.Lock()
	gen, rebuilt := e2.expect.expected[appOwner(tAppID)]
	e2.expect.mu.Unlock()
	assert.True(t, rebuilt, "convergence rebuilds the expectation cache")
	assert.Equal(t, uint64(1), gen)
}

// 投影缓存短路：未变基线的重复扫描不再重投影（digest 签名短路——开销
// 控制的钉测，经 Ensure 调用数间接钉投影次数不可行，这里直接钉 memo 命中）。

func TestProjectionMemoShortCircuit(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	d := getDeployment(t, e, latestSucceededID(t, e))

	ids1, err := e.expectedCarrierIDs(context.Background(), d)
	require.NoError(t, err)
	require.Equal(t, []string{tAppID + "-web"}, ids1)
	e.converge.mu.Lock()
	projLen := len(e.converge.projMemo)
	e.converge.mu.Unlock()
	require.Equal(t, 1, projLen)

	ids2, err := e.expectedCarrierIDs(context.Background(), d)
	require.NoError(t, err)
	assert.Equal(t, ids1, ids2)
	e.converge.mu.Lock()
	projLen = len(e.converge.projMemo)
	e.converge.mu.Unlock()
	assert.Equal(t, 1, projLen, "unchanged baseline reuses the memo (no re-projection)")
}
