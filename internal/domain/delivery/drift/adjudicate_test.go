package drift

// adjudicate_test.go 是漂移判定面的白盒测试（ADR-0087 P3a 批第 3 单元：
// 裁决表全行 + 去抖签名边界 + 签名稳定性——同输入同输出；零 fake/编排
// 依赖 import，纯函数直测）。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// scanBase 是扫描面表测的基准输入（gen 锚在场、spec 相符的稳态载体）。
func scanBase() ScanInput {
	return ScanInput{
		Obs: capability.WorkloadObservation{
			WorkloadID: "wl-1", Generation: capability.Generation(3),
			Image: "nginx:1.27", Command: []string{"sleep", "3600"}, Replicas: 1,
		},
		Managed: true,
		Want: capability.Workload{
			ID: "wl-1", Process: "web", Image: "nginx:1.27",
			Command: []string{"sleep", "3600"}, Replicas: 1,
		},
		LedgerWriter: "dep-1", WriterKnown: true, BaselineID: "dep-1",
		OwnerGen: 3, EnsuredGen: 3, HasEnsuredGen: true,
	}
}

// TestAdjudicateScanTable 钉扫描面裁决表全行：账本采用规则（Skip 三态）、
// 停摆/停摆恢复、gen 偏离让位 spec 对照、无锚跳过 gen 面、spec 三面失配
// 与相符清签。语义源 = ADR-0082 §1/§4/§6 + ADR-0048 决策 2 + ADR-0083
// （restart_seq 不对照）。
func TestAdjudicateScanTable(t *testing.T) {
	cases := []struct {
		name string
		in   ScanInput
		want ScanVerdict
	}{
		{
			name: "unmanaged carrier (no ledger entry) skips with zero actions",
			in:   func() ScanInput { in := scanBase(); in.Managed = false; return in }(),
			want: ScanVerdict{Skip: true},
		},
		{
			name: "no succeeded baseline skips the spec and gen faces",
			in:   func() ScanInput { in := scanBase(); in.BaselineID = ""; return in }(),
			want: ScanVerdict{Skip: true},
		},
		{
			name: "ledger entry written by a non-baseline deployment is not adopted",
			in:   func() ScanInput { in := scanBase(); in.LedgerWriter = "dep-cancelled"; return in }(),
			want: ScanVerdict{Skip: true},
		},
		{
			name: "ledger entry without a writer record is not adopted",
			in:   func() ScanInput { in := scanBase(); in.WriterKnown = false; return in }(),
			want: ScanVerdict{Skip: true},
		},
		{
			name: "steady matching carrier clears both debounce signatures",
			in:   scanBase(),
			want: ScanVerdict{RolloutClear: true, SpecClear: true, Anchor: 3},
		},
		{
			name: "rollout stall raises the stall signature without spec actions",
			in: func() ScanInput {
				in := scanBase()
				in.Obs.RolloutStalled = true
				in.Obs.RolloutDetail = "update paused due to failure"
				return in
			}(),
			want: ScanVerdict{RolloutSig: "rollout|update paused due to failure", SpecClear: true, Anchor: 3},
		},
		{
			name: "stall recovery clears the stall signature",
			in:   scanBase(), // RolloutStalled=false is the recovery face
			want: ScanVerdict{RolloutClear: true, SpecClear: true, Anchor: 3},
		},
		{
			name: "gen deviation signals and defers the spec face (stall still judged)",
			in: func() ScanInput {
				in := scanBase()
				in.Obs.Generation = capability.Generation(2)
				in.Obs.RolloutStalled = true
				in.Obs.RolloutDetail = "paused"
				in.Obs.Image = "tampered:latest"
				return in
			}(),
			want: ScanVerdict{RolloutSig: "rollout|paused", GenSig: "gen|3|2", Anchor: 3},
		},
		{
			name: "missing gen anchor skips the gen face and anchors on the owner expectation",
			in: func() ScanInput {
				in := scanBase()
				in.HasEnsuredGen = false
				in.Obs.Generation = capability.Generation(2)
				return in
			}(),
			want: ScanVerdict{RolloutClear: true, SpecClear: true, Anchor: 3},
		},
		{
			name: "image mismatch wins over command and replicas",
			in: func() ScanInput {
				in := scanBase()
				in.Obs.Image = "nginx:1.28"
				in.Obs.Command = []string{"sleep", "9999"}
				in.Obs.Replicas = 4
				return in
			}(),
			want: ScanVerdict{RolloutClear: true,
				SpecMismatch: `image "nginx:1.28" != expected "nginx:1.27"`,
				SpecSig:      `spec|image "nginx:1.28" != expected "nginx:1.27"`, Anchor: 3},
		},
		{
			name: "command mismatch is reported with both sides",
			in: func() ScanInput {
				in := scanBase()
				in.Obs.Command = []string{"sleep", "9999"}
				return in
			}(),
			want: ScanVerdict{RolloutClear: true,
				SpecMismatch: `command ["sleep" "9999"] != expected ["sleep" "3600"]`,
				SpecSig:      `spec|command ["sleep" "9999"] != expected ["sleep" "3600"]`, Anchor: 3},
		},
		{
			name: "replicas mismatch is the last face",
			in: func() ScanInput {
				in := scanBase()
				in.Obs.Replicas = 3
				return in
			}(),
			want: ScanVerdict{RolloutClear: true,
				SpecMismatch: "replicas 3 != expected 1",
				SpecSig:      "spec|replicas 3 != expected 1", Anchor: 3},
		},
		{
			name: "nil and empty command slices are equivalent (no false drift)",
			in: func() ScanInput {
				in := scanBase()
				in.Obs.Command = nil
				in.Want.Command = []string{}
				return in
			}(),
			want: ScanVerdict{RolloutClear: true, SpecClear: true, Anchor: 3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AdjudicateScan(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestAdjudicateScanPurity 钉判定纯度与签名稳定性：同输入两次裁决同输出、
// 不写输入（无 receiver 可写——ScanInput 按值传入，此锚钉「同输入同
// 输出」的签名稳定性）。
func TestAdjudicateScanPurity(t *testing.T) {
	in := scanBase()
	in.Obs.Image = "tampered:latest"
	first, second := AdjudicateScan(in), AdjudicateScan(in)
	assert.Equal(t, first, second, "the same input must yield the same verdict (signature stability)")
	assert.NotEmpty(t, first.SpecSig)
}

// TestAdjudicateWatchTable 钉 Watch 流漂移裁决表全行（架构 §5 Drift 判定：
// gen 偏离与载体自报漂移双路径；无锚非漂移静默不猜）。
func TestAdjudicateWatchTable(t *testing.T) {
	cases := []struct {
		name string
		in   WatchInput
		want WatchVerdict
	}{
		{
			name: "matching generation without a self-report clears the signature",
			in:   WatchInput{Expected: 3, Observed: 3, Drifted: false},
			want: WatchVerdict{Clear: true},
		},
		{
			name: "no anchor and no self-report stays silent",
			in:   WatchInput{Expected: 0, Observed: 1, Drifted: false},
			want: WatchVerdict{Silent: true},
		},
		{
			name: "generation deviation carries the drift signature",
			in:   WatchInput{Expected: 3, Observed: 0, Drifted: false},
			want: WatchVerdict{Sig: "3|0|false"},
		},
		{
			name: "self-reported drift with a matching generation still signals",
			in:   WatchInput{Expected: 3, Observed: 3, Drifted: true},
			want: WatchVerdict{Sig: "3|3|true"},
		},
		{
			name: "self-reported drift without an anchor signals",
			in:   WatchInput{Expected: 0, Observed: 0, Drifted: true},
			want: WatchVerdict{Sig: "0|0|true"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AdjudicateWatch(tc.in)
			assert.Equal(t, tc.want, got)
			// 三态互斥不变量。
			flags := 0
			for _, b := range []bool{got.Clear, got.Silent, got.Sig != ""} {
				if b {
					flags++
				}
			}
			require.Equal(t, 1, flags, "clear/silent/sig are mutually exclusive states")
		})
	}
}

// TestSteadyStopCandidateTable 钉稳态看门狗候选表全行（ADR-0082 §1 行 5
// 非收敛行；在途让位；零值观测〔未见〕恒非候选）。
func TestSteadyStopCandidateTable(t *testing.T) {
	stopped := func(gen capability.Generation) capability.WorkloadEvent {
		return capability.WorkloadEvent{WorkloadID: "wl-1", Generation: gen, State: capability.WorkloadStopped}
	}
	cases := []struct {
		name      string
		expected  uint64
		appInFly  bool
		ev        capability.WorkloadEvent
		candidate bool
	}{
		{name: "stopped at the expected generation is a candidate", expected: 3, ev: stopped(3), candidate: true},
		{name: "missing expectation (zero anchor) is never a candidate", expected: 0, ev: stopped(0), candidate: false},
		{name: "in-flight app deployments defer to the deployment chain", expected: 3, appInFly: true, ev: stopped(3), candidate: false},
		{name: "stopped at a stale generation is not steady-state", expected: 3, ev: stopped(2), candidate: false},
		{name: "running observations are not stop candidates", expected: 3,
			ev:        capability.WorkloadEvent{WorkloadID: "wl-1", Generation: 3, State: capability.WorkloadRunning},
			candidate: false},
		{name: "an unseen carrier (zero event) is never a candidate", expected: 3, ev: capability.WorkloadEvent{}, candidate: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.candidate, SteadyStopCandidate(tc.expected, tc.appInFly, tc.ev))
		})
	}
}

// TestStoppedSigLiteral 钉稳态 stopped 去抖签名字面（gen 作用域：同 gen
// 同签名、异 gen 异签名——去抖键的格式契约）。
func TestStoppedSigLiteral(t *testing.T) {
	assert.Equal(t, "stopped|7", StoppedSig(7))
	assert.Equal(t, StoppedSig(7), StoppedSig(7))
	assert.NotEqual(t, StoppedSig(7), StoppedSig(8))
}

// TestMissingExtraIDs 钉缺席/多余两面（同一对照的两个方向：期望 − 观测 /
// 观测 − 期望；多余面字典序，缺席面随喂入期望集的序）。
func TestMissingExtraIDs(t *testing.T) {
	ws := []capability.Workload{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}
	obs := []capability.WorkloadObservation{
		{WorkloadID: "b"}, {WorkloadID: "d"}, {WorkloadID: "e"},
	}
	assert.Equal(t, []string{"a", "c"}, MissingIDs(ws, obs))
	assert.Equal(t, []string{"d", "e"}, ExtraIDs(ws, obs))
	// 全交集：两面皆空（nil——调用方按 len==0 判空）。
	obsAll := []capability.WorkloadObservation{{WorkloadID: "a"}, {WorkloadID: "b"}, {WorkloadID: "c"}}
	assert.Empty(t, MissingIDs(ws, obsAll))
	assert.Empty(t, ExtraIDs(ws, obsAll))
}
