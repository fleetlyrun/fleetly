package engine

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/freeze"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
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

// ---- 裁决表 golden（ADR-0082 §1：8 判定行 + 2 非行——表即断言）----

func TestConvergeVerdictGolden(t *testing.T) {
	rows := []struct {
		name string
		in   convergeVerdictInput
	}{
		{"01-spec-mismatch", convergeVerdictInput{SpecMismatch: true}},
		{"02-absent-carrier", convergeVerdictInput{Absent: true}},
		{"03-gen-deviation", convergeVerdictInput{GenDeviated: true}},
		{"04-rollout-stalled-terminal", convergeVerdictInput{RolloutStalled: true}},
		// 行5（spec 相符 + stopped）是信号层非行：稳态看门狗只发事件、不产
		// 收敛信号，判定函数无此分支（ADR-0082 修订版 §1）。
		{"06-active-deployment-incl-failed-rollback", convergeVerdictInput{Driving: true}},
		{"07-frozen", convergeVerdictInput{Frozen: true}},
		{"08-extra-carriers-in-domain", convergeVerdictInput{ExtraCarriers: true}},
		// 行9（域外孤儿只登记）明确未落码：观测面只有域内枚举、无
		// WorkloadOrphaned 事件码（ADR-0082 修订版 §1）。
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
		"converge verdict table drifted from golden (ADR-0082 §1: 8 judged rows + 2 non-rows are pinned)")
}

// ---- 裁决表接线守卫（映射/字段完备性——改真实映射即红）----

// 信号类 → 裁决表字段的映射逐条钉死：某类被误映射到别的行（如 gen 键置
// SpecMismatch）即红——这是"表即断言"在信号侧的执行面。
func TestConvergeVerdictSignalMapping(t *testing.T) {
	cases := []struct {
		name    string
		signals convergeSignals
		want    convergeVerdictInput
		verdict string
	}{
		{"spec", convergeSignals{convergeSignalSpec.key("app-web"): "spec|image"}, convergeVerdictInput{SpecMismatch: true}, verdictConverge},
		{"absent", convergeSignals{convergeSignalAbsent.key(""): "absent|app-web"}, convergeVerdictInput{Absent: true}, verdictConverge},
		{"gen", convergeSignals{convergeSignalGen.key("app-web"): "gen|1|0"}, convergeVerdictInput{GenDeviated: true}, verdictConverge},
		{"rollout", convergeSignals{convergeSignalRollout.key("app-web"): "rollout|paused"}, convergeVerdictInput{RolloutStalled: true}, verdictConverge},
		{"extra", convergeSignals{convergeSignalExtra.key(""): "extra|app-ghost"}, convergeVerdictInput{ExtraCarriers: true}, verdictConverge},
		{"replay", convergeSignals{convergeSignalReplay.key(""): "replay|dep-2"}, convergeVerdictInput{CancelLegacy: true}, verdictReplayBaseline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := signalVerdictInput(tc.signals)
			// 逐字段相等即钉死映射（多置位/错置位都红）。
			assert.Equal(t, tc.want, got, "signal class must map to exactly its own verdict-table row")
			assert.Equal(t, tc.verdict, convergeVerdict(got))
			assert.True(t, convergeActionable(convergeVerdict(got)), "signal rows must be actionable")
		})
	}
}

// 枚举完备性：allConvergeSignalClasses 的每一类都必须能被信号侧映射识别
// （新增枚举而未在 signalVerdictInput 登记 → 输入零值 → 非收敛行 → 红）。
func TestConvergeVerdictSignalClassTotality(t *testing.T) {
	classes := allConvergeSignalClasses()
	require.Len(t, classes, 6, "六类信号：spec/absent/gen/rollout/extra/replay")
	seen := map[string]bool{}
	for _, c := range classes {
		key := c.key("x")
		require.False(t, seen[key], "signal class key %q must be unique", key)
		seen[key] = true
		in := signalVerdictInput(convergeSignals{key: "v"})
		require.NotEqual(t, convergeVerdictInput{}, in,
			"signal class %q has no verdict-table field mapping (register it in signalVerdictInput)", c)
		assert.True(t, convergeActionable(convergeVerdict(in)),
			"signal class %q must resolve to an actionable verdict", c)
	}
	// 未知信号类不伪造输入（不得凭未知键发起收敛）。
	assert.Equal(t, convergeVerdictInput{}, signalVerdictInput(convergeSignals{"bogus|x": "v"}))
}

// 闸门侧映射（冻结/驱动权两行 + 优先序：冻结先于驱动权）。
func TestConvergeVerdictGateMapping(t *testing.T) {
	cases := []struct {
		name            string
		frozen, driving bool
		verdict         string
	}{
		{"frozen", true, false, verdictSuspend},
		{"driving", false, true, verdictSkip},
		{"clear", false, false, verdictSteady},
		{"frozen-before-driving", true, true, verdictSuspend},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.verdict, convergeVerdict(gateVerdictInput(tc.frozen, tc.driving)))
			assert.False(t, convergeActionable(convergeVerdict(gateVerdictInput(tc.frozen, tc.driving))),
				"gate rows never initiate a converge action")
		})
	}
}

// 字段 ⇄ 生产者双向保鲜：反射出的字段名集必须与 verdictFieldProducers 的
// 键集一一对应（加字段不登记即红，表项无字段即红）。
func TestConvergeVerdictFieldProducersComplete(t *testing.T) {
	typ := reflect.TypeOf(convergeVerdictInput{})
	fields := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		fields[typ.Field(i).Name] = true
	}
	require.NotEmpty(t, fields)
	for name := range fields {
		_, ok := verdictFieldProducers[name]
		assert.True(t, ok, "verdict field %q has no registered producer (add it to verdictFieldProducers)", name)
	}
	for name := range verdictFieldProducers {
		assert.True(t, fields[name], "verdictFieldProducers entry %q has no matching input field", name)
	}
	assert.Len(t, verdictFieldProducers, len(fields), "verdict fields and producers must be one-to-one")
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

	// 审计行 source=system、AfterFP=基线 digest、Detail source=fast、不建
	// Deployment 行。
	entries := convergeAuditEntries(t, e)
	require.Len(t, entries, 1)
	assert.Equal(t, audit.SourceSystem, entries[0].Source)
	revRow, err := e.repos.Revisions.Get(context.Background(), rev)
	require.NoError(t, err)
	assert.Equal(t, revRow.Digest, entries[0].AfterFP, "audit AfterFP carries the baseline digest")
	assert.Equal(t, "app/"+tAppID, entries[0].Resource)
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(entries[0].Detail), &detail))
	assert.Equal(t, "fast", detail["source"], "fast-source audit carries source=fast in Detail")

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

// ---- posture 闸门：detect = 零运行时写（四源同受门限，检测照发）----

// tImagePortSpec 是带声明端口的基线 spec：Route 后端解析的期望集必须携带
// 端口（detect 账本重建的路由可解析性钉测面）。
const tImagePortSpec = `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
	`"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27","replicas":1,` +
	`"ports":[{"port":8080,"protocol":"PROTOCOL_HTTP"}]}]}`

// newRestartedEngine 造同库同 runtime 的新引擎实例（重启形态：进程内缓存
// 全清，基线只从行重建）。
func newRestartedEngine(t *testing.T, e *Engine, rt *fakeRuntime, opts Options) *Engine {
	t.Helper()
	e2 := New(Deps{DB: e.db, Stores: testRepos(e.db), Tx: transact.NewTx(e.db, testBinder(e.db)), Runtime: rt, Logger: e.log}, opts)
	t.Cleanup(func() { _ = e2.Stop(context.Background()) })
	return e2
}

func detectOptions() Options {
	return Options{Converge: ConvergeOptions{Posture: ConvergePostureDetect}}
}

// detect 零运行时写（D3 / ADR-0082 §5 第 1-2 条）：漂移注入一拍 → fast 与
// slow 两通道均不 Ensure；启动面只重建账本、不调 Ensure；检测事件照发。
func TestPostureDetectWritesNothing(t *testing.T) {
	e, rt, clock := newTestEngineOpts(t, detectOptions())
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()
	before := len(rt.calls())
	e.driftScan(context.Background())
	assert.Equal(t, 0, ensureDelta(t, rt, before), "detect posture: neither fast nor slow channel may write")
	assert.Len(t, eventNamesOf(t, e, "workload.drift_detected"), 1, "detection face keeps firing")
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"))

	// 启动面（D3）：detect 下重放只重建期望账本，不下发载体。
	e2 := newRestartedEngine(t, e, rt, detectOptions())
	e2.rebuildBaselines(context.Background())
	assert.Equal(t, 0, ensureDelta(t, rt, before), "detect posture: startup replay must not Ensure")
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"), "startup rebuild emits no converge event")
}

// 启动账本重建（D3 / ADR-0082 §5 第 2 条 + §6）：ensuredSpec 在场且为基线
// 投影、ensuredBy = 基线行（D4 采用规则放行）、gen 锚缺席（无 Ensure 回执）。
func TestPostureDetectRebuildsExpectationsWithoutEnsure(t *testing.T) {
	e, rt, clock := newTestEngineOpts(t, detectOptions())
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	before := len(rt.calls())
	e2 := newRestartedEngine(t, e, rt, detectOptions())
	e2.rebuildBaselines(context.Background())

	assert.Equal(t, 0, ensureDelta(t, rt, before), "detect startup rebuild must not Ensure")
	w, ok := e2.obs.ensuredSpecOf(tAppID + "-web")
	require.True(t, ok, "detect rebuild writes ensuredSpec (route/isolation input face)")
	assert.Equal(t, "nginx:1.27", w.Image, "ledger spec is the baseline projection")
	_, hasGen := e2.obs.ensuredGenOf(tAppID + "-web")
	assert.False(t, hasGen, "detect rebuild must not fabricate a gen anchor (Ensure receipt is its only source)")
	by, ok := e2.obs.ensuredByOf(tAppID + "-web")
	require.True(t, ok, "ledger entries carry their writer")
	assert.Equal(t, latestSucceededID(t, e2), by, "writer is the baseline row so the §6 adoption rule accepts it")
}

// 路由仍可解析（F1 硬约束 / ADR-0082 §5 第 2 条）：detect 账本重建后
// publishRoutes 经 routeExpectations 取期望集、Addresses 返回端点、
// Proxy 收到该 Route——期望集为空则端点空、Route 被跳过（失真形态）。
func TestPostureDetectPublishesRoutesFromExpectationCache(t *testing.T) {
	e, rt, clock := newTestEngineOpts(t, detectOptions())
	rev := freezeSpec(t, e, 1, tImagePortSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	ctx := context.Background()
	require.NoError(t, e.repos.Routes.Create(ctx, &route.Route{
		ID: "01JD0ROUTE00000000000000001", ProjectID: tProjectID,
		Host: "shop.127.0.0.1.sslip.io", AppID: tAppID, Process: "web", Port: 8080,
		Protocol: capability.ProtocolHTTP, TLSMode: "auto",
	}))

	rt.mu.Lock()
	rt.addrFromExpected = true // 端点的唯一输入 = 期望集（detect 无 Ensure 存活集）
	rt.mu.Unlock()
	proxy := &fakeProxy{}
	e2 := New(Deps{DB: e.db, Stores: testRepos(e.db), Tx: transact.NewTx(e.db, testBinder(e.db)), Runtime: rt, Proxy: proxy, Logger: e.log}, detectOptions())
	t.Cleanup(func() { _ = e2.Stop(ctx) })

	before := len(rt.calls())
	e2.rebuildBaselines(ctx)
	assert.Equal(t, 0, ensureDelta(t, rt, before), "detect startup rebuild must not Ensure")
	require.Len(t, e2.obs.routeExpectations(tAppID, 0), 1, "rebuilt ledger feeds route backend resolution")

	e2.publishRoutes(ctx)
	calls := rt.addrCallsSnapshot()
	require.Len(t, calls, 1, "route backend resolution must reach Runtime.Addresses")
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, App: tAppID}, calls[0])
	require.Len(t, proxy.published, 1, "the route resolves from the rebuilt expectation ledger (no Ensure)")
	assert.Equal(t, "vip-"+tAppID+"-web:8080", proxy.published[0].BackendAddr)
	assert.Equal(t, "shop.127.0.0.1.sslip.io", proxy.published[0].Host)
}

// spec 漂移检测照常（ADR-0082 §5 第 3 条：spec 面无降级）：账本重建后
// tamper 镜像 → 扫描面出 1 条 workload.drift_detected，仍不 Ensure。
func TestPostureDetectSpecDriftStillDetected(t *testing.T) {
	e, rt, clock := newTestEngineOpts(t, detectOptions())
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	e2 := newRestartedEngine(t, e, rt, detectOptions())
	e2.rebuildBaselines(context.Background())
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.27-alpine"}}
	rt.mu.Unlock()

	before := len(rt.calls())
	e2.driftScan(context.Background())
	assert.Len(t, eventNamesOf(t, e2, "workload.drift_detected"), 1, "detect keeps the spec detection face")
	assert.Equal(t, 0, ensureDelta(t, rt, before), "detect still writes nothing")
}

// gen 失明钉成显式期望（ADR-0082 §5 第 3 条）：detect 账本无 gen 锚 →
// gen tamper 不产 gen| 信号、不调 Ensure；切回 all 后一拍重放修复锚。
func TestPostureDetectGenDriftBlindDocumented(t *testing.T) {
	e, rt, clock := newTestEngineOpts(t, detectOptions())
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	e2 := newRestartedEngine(t, e, rt, detectOptions())
	e2.rebuildBaselines(context.Background())
	ctx := context.Background()
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {gen: 0, genSet: true}}
	rt.mu.Unlock()

	// 扫描面直接钉信号集：无 gen| 键（原「回落 App 级 expected」会把 0
	// 当锚、产假 gen 偏离——本测是删除该分支的反向守卫）。
	d := getDeployment(t, e2, latestSucceededID(t, e2))
	signals := e2.compareSpecs(ctx, tAppID, inspectApp(t, e2), d)
	for key := range signals {
		assert.False(t, strings.HasPrefix(key, "gen|"), "detect ledger without a gen anchor must not raise gen drift (got %q)", key)
	}
	before := len(rt.calls())
	e2.driftScan(ctx)
	assert.Equal(t, 0, ensureDelta(t, rt, before), "detect writes nothing on the blind gen face")

	// 反向：切回 all 后一拍重放把 gen 锚写回（Ensure 回执 = 唯一锚源）。
	e2.opts.Converge.Posture = ConvergePostureAll
	e2.driftScan(ctx)
	require.Equal(t, 1, ensureDelta(t, rt, before), "lifting the posture restores the gen anchor via one baseline replay")
	last := rt.calls()[len(rt.calls())-1]
	assert.Equal(t, capability.Generation(1), last.Gen)
	assert.Equal(t, "nginx:1.27", last.Spec["web"].Image)

	// 锚恢复 + tamper 清除后：无新信号、无新 Ensure（gen 面重新工作）。
	rt.mu.Lock()
	rt.tamper = nil
	rt.mu.Unlock()
	before = len(rt.calls())
	e2.driftScan(ctx)
	assert.Equal(t, 0, ensureDelta(t, rt, before), "recovered anchor and clean observation short-circuit")
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

// 慢通道兜底形态保留：即使没有 replay 标记，60s 例行重放也会把遗留载体
// 卷回基线（本测不依赖标记，仅钉回卷结果与事实面；快信号的钉测见
// TestCancelReplaySignalDrivesBaselineReplay）。
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

	// 取消的部署从未成为期望态：下一拍重放回卷基线。
	e.driftScan(context.Background())
	last := rt.calls()[len(rt.calls())-1]
	assert.Equal(t, "nginx:1.27", last.Spec["web"].Image, "cancel legacy rolls back to the succeeded baseline")
	assert.Equal(t, capability.Generation(1), last.Gen)

	// 回卷事实面：审计 1 条（SourceSystem）+ 事件 1 条（replay 走 fast 源）。
	require.Len(t, eventNamesOf(t, e, "workload.converged"), 1)
	entries := convergeAuditEntries(t, e)
	require.Len(t, entries, 1)
	assert.Equal(t, audit.SourceSystem, entries[0].Source)
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

// ---- 行 8：域内多余载体（extra 快信号）----

// convergeAuditEntries 返回 workload.converge 审计行（收敛事实取证面）。
func convergeAuditEntries(t *testing.T, e *Engine) []audit.Entry {
	t.Helper()
	entries, err := e.repos.Audits.ListFiltered(context.Background(),
		audit.Filter{ActionPrefix: "workload.converge", Limit: 50})
	require.NoError(t, err)
	return entries
}

// inspectApp 取测试 App 的域内观测快照（与 driftScan 同 ns 同选择器）。
func inspectApp(t *testing.T, e *Engine) []capability.WorkloadObservation {
	t.Helper()
	inspector := capability.FacesOf(e.runtime).Inspector
	require.NotNil(t, inspector, "test runtime must expose the inspector subface")
	obs, err := inspector.InspectWorkloads(context.Background(),
		capability.NamespaceRef{Team: "default", Project: tProjectID, App: tAppID})
	require.NoError(t, err)
	return obs
}

func TestExtraCarriersSignalConvergesDomainRemoval(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	// 域内多余载体（如环外 create 出的平台管辖服务）：观测集多一个期望外 ID。
	ghost := capability.Workload{ID: "01JD0GHOST0000000000000000", Process: "ghost", Image: "nginx:1.27", Replicas: 1}
	rt.injectObservedCarrier(ghost)

	// 信号面：extra 可见且裁决输入点亮行 8 的字段。
	d := getDeployment(t, e, latestSucceededID(t, e))
	signals := e.compareSpecs(ctx, tAppID, inspectApp(t, e), d)
	require.Contains(t, signals, convergeSignalExtra.key(""), "an extra in-domain carrier must raise the extra signal")
	assert.True(t, signalVerdictInput(signals).ExtraCarriers, "the extra signal must light the row-8 verdict input")

	// 一拍即收敛（Ensure +1）：下发的是基线投影（不含幽灵）——域内移除由
	// Provider 的 Ensure 语义承载。
	before := len(rt.calls())
	e.driftScan(ctx)
	calls := rt.calls()
	require.Len(t, calls, before+1, "extra carrier converges in one beat")
	assert.NotContains(t, calls[before].ByID, ghost.ID, "convergence re-issues the baseline projection (provider removes the extra)")
}

// 行 6 让位：部署窗内域内"多余"（在途新代载体）不发起收敛，但信号可见。
func TestExtraSignalYieldsToDrivingGate(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	// 在途部署（queued）持有驱动权。
	rev2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.28"))
	_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)

	ghost := capability.Workload{ID: "01JD0GHOST0000000000000001", Process: "ghost", Image: "nginx:1.27", Replicas: 1}
	rt.injectObservedCarrier(ghost)
	d := getDeployment(t, e, latestSucceededID(t, e))
	signals := e.compareSpecs(ctx, tAppID, inspectApp(t, e), d)
	require.Contains(t, signals, convergeSignalExtra.key(""), "the extra signal must stay visible during the deployment window")

	before := len(rt.calls())
	e.driftScan(ctx)
	assert.Equal(t, 0, ensureDelta(t, rt, before), "row 6 (driving) yields over row 8 inside the deployment window")
}

// ---- 行 10：Cancel/失败终态的重放标记（replay 快信号）----

func TestCancelReplaySignalDrivesBaselineReplay(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev1)

	rev2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx) // releasing：gen2 已下发（部分物化 = 遗留形态）
	_, err = e.Cancel(ctx, d2.ID)
	require.NoError(t, err)

	markID, marked := e.converge.replayMarked(tAppID)
	require.True(t, marked, "cancelling a materialized deployment leaves a replay mark")
	assert.Equal(t, d2.ID, markID, "the mark carries the offending deployment ID (a second cancel is a new signature)")

	// 脏账本（写入者 = 已取消的 d2）不得产生假期望：归属采用规则跳过其
	// spec/gen 对照（R16-M3 根因），同拍唯一信号是 replay 标记。
	d1 := getDeployment(t, e, latestSucceededID(t, e))
	assert.Equal(t,
		convergeSignals{convergeSignalReplay.key(""): "replay|" + d2.ID},
		e.compareSpecs(ctx, tAppID, inspectApp(t, e), d1),
		"the cancelled deployment's ledger entries must not produce spec/gen signals")

	// 下一拍即以基线重放回卷（replay 走 fast 通道，不等慢通道 60s）。
	before := len(rt.calls())
	e.driftScan(ctx)
	calls := rt.calls()
	require.Len(t, calls, before+1, "the replay fast signal converges in the next beat")
	assert.Equal(t, "nginx:1.27", calls[before].Spec["web"].Image, "cancel legacy rolls back to the succeeded baseline")
	assert.Equal(t, capability.Generation(1), calls[before].Gen)
	_, marked = e.converge.replayMarked(tAppID)
	assert.False(t, marked, "a successful replay clears the mark")

	// 回卷事实双真：事件 + 审计（replay 是信号驱动 = fast 源）。
	require.Len(t, eventNamesOf(t, e, "workload.converged"), 1, "replay drives the event stream (fast source)")
	entries := convergeAuditEntries(t, e)
	require.Len(t, entries, 1, "the audit row lands with the event")
	assert.Equal(t, audit.SourceSystem, entries[0].Source)
}

// 标记收窄：queued 取消（零下发）不落标记；进入物化相位的取消才落。
func TestReplayMarkOnlyAfterMaterialization(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev1)
	settleSlowChannel(t, e)

	// queued 取消：无标记、无回卷 Ensure、无审计。
	rev2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.28"))
	dQueued, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	_, err = e.Cancel(ctx, dQueued.ID)
	require.NoError(t, err)
	_, marked := e.converge.replayMarked(tAppID)
	assert.False(t, marked, "cancelling a queued deployment must not leave a replay mark (nothing materialized)")

	before := len(rt.calls())
	e.driftScan(ctx)
	assert.Len(t, rt.calls(), before, "no replay signal → no replay ensure")
	assert.Empty(t, convergeAuditEntries(t, e), "no material change and no fast signal → zero fact")

	// releasing 取消：已进入物化相位 → 标记在位。
	rev3 := freezeSpec(t, e, 3, imageSpecFor("nginx:1.29"))
	d3, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev3})
	require.NoError(t, err)
	e.step(ctx)
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d3.ID).State)
	_, err = e.Cancel(ctx, d3.ID)
	require.NoError(t, err)
	_, marked = e.converge.replayMarked(tAppID)
	assert.True(t, marked, "cancelling a releasing deployment must leave a replay mark")
}

// 成功终态撤销标记（失败 → 自动回滚 succeeded），下一拍无多余重放。
func TestReplayMarkClearedOnSucceededDeployment(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
	driftDeployToSucceeded(t, e, rt, clock, r1)

	// R2 首次下发失败 → failed（已物化相位）→ 落标记 → 自动回滚。
	rt.failNext = true
	r2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.27"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: r2})
	require.NoError(t, err)
	e.step(ctx)
	require.Equal(t, deployment.StateRollingBack, getDeployment(t, e, d2.ID).State)
	_, marked := e.converge.replayMarked(tAppID)
	require.True(t, marked, "a failed deployment that entered the materialize phase leaves a replay mark")

	// 回滚就绪 → observing → succeeded：成功终态撤销标记。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 3))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d2.ID).State)
	_, marked = e.converge.replayMarked(tAppID)
	assert.False(t, marked, "a succeeded deployment (rollback) clears the replay mark")

	// 下一拍无多余重放（慢通道备忘先落定——一次例行重放，非回卷信号）。
	settleSlowChannel(t, e)
	before := len(rt.calls())
	e.driftScan(ctx)
	assert.Len(t, rt.calls(), before, "no replay mark → no extra replay")
}

// ---- 账本归属（D4）：写入者 == 当前 LatestSucceeded 才采用 ----

func TestExpectationLedgerSkipsForeignWriter(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev1)

	// d2 在途：materialize 写下账本条目（写入者 = d2，尚未成为期望态）。
	rev2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx)
	require.Equal(t, deployment.StateReleasing, getDeployment(t, e, d2.ID).State)

	// 人工改载体（观测与 d2 写的账本失配）：若账本被采用必出 spec 信号。
	rt.mu.Lock()
	rt.tamper = map[string]tamperEntry{tAppID + "-web": {image: "nginx:1.99"}}
	rt.mu.Unlock()
	d1 := getDeployment(t, e, latestSucceededID(t, e))
	obs := inspectApp(t, e)
	signals := e.compareSpecs(ctx, tAppID, obs, d1)
	for key := range signals {
		class, _ := convergeSignalClassOf(key)
		assert.NotEqual(t, convergeSignalSpec, class, "a foreign-writer ledger entry must not produce a spec signal")
		assert.NotEqual(t, convergeSignalGen, class, "a foreign-writer ledger entry must not produce a gen signal")
	}

	// 同一观测下把写入者改回基线行 → 信号出现（证明是归属在裁决）。
	e.obs.mu.Lock()
	e.obs.ensuredBy[tAppID+"-web"] = d1.ID
	e.obs.mu.Unlock()
	signals = e.compareSpecs(ctx, tAppID, inspectApp(t, e), d1)
	require.Contains(t, signals, convergeSignalSpec.key(tAppID+"-web"),
		"the same observation must produce the spec signal once the writer is the baseline")
}

// 删除收口：账本归属与重放标记一并清（无期望态可回卷）。
func TestExpectationLedgerForgottenOnTeardown(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev1 := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev1)

	// 造账本条目（d2 的 materialize）与重放标记（releasing 取消）。
	rev2 := freezeSpec(t, e, 2, imageSpecFor("nginx:1.28"))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx)
	_, err = e.Cancel(ctx, d2.ID)
	require.NoError(t, err)
	_, marked := e.converge.replayMarked(tAppID)
	require.True(t, marked)
	e.obs.mu.RLock()
	_, ledgered := e.obs.ensuredBy[tAppID+"-web"]
	e.obs.mu.RUnlock()
	require.True(t, ledgered, "the cancelled deployment's materialize wrote the ledger")

	require.NoError(t, e.TeardownApp(ctx, tAppID))
	e.obs.mu.RLock()
	_, ledgered = e.obs.ensuredBy[tAppID+"-web"]
	e.obs.mu.RUnlock()
	assert.False(t, ledgered, "teardown must forget the ledger ownership")
	_, marked = e.converge.replayMarked(tAppID)
	assert.False(t, marked, "teardown must clear the replay mark")
}

// ---- 事实面分流（D2）：审计按真实变更发、事件维持 fast-only ----

func TestConvergeSlowChannelMaterialChangeWritesAudit(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e)

	// 慢通道到期 + 回执含真实变更（外部改动被纠正的形态）。
	rt.mu.Lock()
	rt.reportChanged = []string{tAppID + "-web"}
	rt.mu.Unlock()
	clock.Advance(61 * time.Second)
	e.driftScan(ctx)

	entries := convergeAuditEntries(t, e)
	require.Len(t, entries, 1, "a material change on the slow channel writes exactly one audit row")
	assert.Equal(t, audit.SourceSystem, entries[0].Source)
	revRow, err := e.repos.Revisions.Get(ctx, rev)
	require.NoError(t, err)
	assert.Equal(t, revRow.Digest, entries[0].AfterFP, "audit AfterFP carries the baseline digest")
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(entries[0].Detail), &detail))
	assert.Equal(t, "slow", detail["source"])
	assert.Equal(t, []any{tAppID + "-web"}, detail["changed"])
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"), "the event stream stays signal-driven (slow replay emits none)")
}

func TestConvergeSlowChannelNoChangeWritesNoFact(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)
	settleSlowChannel(t, e) // 首拍例行重放（回执空 = 无变更）

	clock.Advance(61 * time.Second)
	e.driftScan(ctx) // 又一次例行重放，无变更

	assert.Empty(t, convergeAuditEntries(t, e), "routine replay without material change writes no audit")
	assert.Empty(t, eventNamesOf(t, e, "workload.converged"), "routine replay emits no event")
}

func TestConvergeBaselineReplayMaterialChangeWritesAudit(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()
	rev := freezeSpec(t, e, 1, tImageSpec)
	driftDeployToSucceeded(t, e, rt, clock, rev)

	rt.mu.Lock()
	rt.reportChanged = []string{tAppID + "-web"}
	rt.mu.Unlock()

	// "重启"：新引擎实例，启动基线重放的回执含真实变更。
	e2 := New(Deps{DB: e.db, Stores: testRepos(e.db), Tx: transact.NewTx(e.db, testBinder(e.db)), Runtime: rt, Logger: discardLogger()}, Options{})
	e2.rebuildBaselines(ctx)

	entries := convergeAuditEntries(t, e2)
	require.Len(t, entries, 1, "startup baseline replay with material change writes one audit row")
	assert.Equal(t, audit.SourceSystem, entries[0].Source)
	assert.Empty(t, eventNamesOf(t, e2, "workload.converged"), "baseline replay never emits the event (fast-only)")
}

// B10：审计 Detail 的 JSON 契约（source/trigger/changed/removed；空集归一为
// []——不出现 null）。只断言 map 内容，不引入 key 顺序脆弱性。
func TestConvergeAuditDetailCarriesChangedSet(t *testing.T) {
	rep := capability.EnsureReport{
		Changed: []string{"wid-b", "wid-a"},
		Removed: []string{"service/old"},
	}
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(convergeAuditDetailJSON(convergeSourceSlow, "absent|x", rep)), &got))
	assert.Equal(t, "slow", got["source"])
	assert.Equal(t, "absent|x", got["trigger"])
	assert.Equal(t, []any{"wid-b", "wid-a"}, got["changed"])
	assert.Equal(t, []any{"service/old"}, got["removed"])

	var empty map[string]any
	require.NoError(t, json.Unmarshal([]byte(convergeAuditDetailJSON(convergeSourceFast, "", capability.EnsureReport{})), &empty))
	assert.Equal(t, []any{}, empty["changed"], "an empty change set must normalize to [] (never null)")
	assert.Equal(t, []any{}, empty["removed"])
}
