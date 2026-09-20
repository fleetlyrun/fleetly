package engine

// app 派生状态优先级测试（state-model §2.10：down > blocked > degraded >
// running；场景矩阵 14/7/8/9/10 的派生来源）。

import (
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestDeriveAppStatePriorityTable 表驱动覆盖派生裁决的全触发组合与优先级。
func TestDeriveAppStatePriorityTable(t *testing.T) {
	now := time.Now().UTC()

	latest := func(mutate func(*state.DeployRecord)) state.DeployRecord {
		r := state.DeployRecord{ID: "d1", Status: state.DeployFailed}
		if mutate != nil {
			mutate(&r)
		}
		return r
	}
	succeeded := func(mutate func(*state.DeployRecord)) state.DeployRecord {
		r := state.DeployRecord{ID: "s1", Status: state.DeploySucceeded}
		if mutate != nil {
			mutate(&r)
		}
		return r
	}

	cases := []struct {
		name string
		f    AppFacts
		want string
	}{
		{
			name: "no deployment records → down",
			f:    AppFacts{},
			want: DerivedDown,
		},
		{
			name: "first deploy failed with scale=0 → down (scenario 14)",
			f: AppFacts{
				Latest: latest(func(r *state.DeployRecord) { r.SubstrateHalted = true }),
			},
			want: DerivedDown,
		},
		{
			name: "first deploy failed before switch (no revision to replay) → down",
			f: AppFacts{
				Latest: latest(nil),
			},
			want: DerivedDown,
		},
		{
			name: "first deploy switched, observe window failed (unstable) → degraded (new version still serving)",
			f: AppFacts{
				Latest: latest(func(r *state.DeployRecord) { r.FirstHealthyAt = now; r.Verdict = state.VerdictUnstable }),
			},
			want: DerivedDegraded,
		},
		{
			name: "valid revision + placement blocked → blocked (takes precedence over degraded)",
			f: AppFacts{
				PlacementState:  string(state.PlacementBlocked),
				Latest:          latest(func(r *state.DeployRecord) { r.Verdict = state.VerdictUnstable }),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedBlocked,
		},
		{
			name: "valid revision + placement unresolved → blocked",
			f: AppFacts{
				PlacementState:  string(state.PlacementUnresolved),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedBlocked,
		},
		{
			name: "valid revision + observe window failed unstable → degraded (scenario 7/8)",
			f: AppFacts{
				Latest:          latest(func(r *state.DeployRecord) { r.FirstHealthyAt = now; r.Verdict = state.VerdictUnstable }),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedDegraded,
		},
		{
			name: "post-window instability (succeeded deploy with post-window alert flag) → degraded (scenario 10)",
			f: AppFacts{
				Latest:          succeeded(func(r *state.DeployRecord) {}),
				LatestSucceeded: succeeded(func(r *state.DeployRecord) { r.Flags = state.DeployFlagPostWindowAlerted }),
			},
			want: DerivedDegraded,
		},
		{
			name: "warning pass (W_DEPLOY_INSTABILITY flag) → degraded (scenario 9, §2.10)",
			f: AppFacts{
				Latest:          succeeded(func(r *state.DeployRecord) { r.Flags = state.DeployFlagInstabilityWarning }),
				LatestSucceeded: succeeded(func(r *state.DeployRecord) { r.Flags = state.DeployFlagInstabilityWarning }),
			},
			want: DerivedDegraded,
		},
		{
			name: "failed after successful replay recovery (replay, no verdict) → running (old version serving)",
			f: AppFacts{
				Latest:          latest(func(r *state.DeployRecord) { r.Recovery = state.RecoveryReplay }),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedRunning,
		},
		{
			name: "succeeded deploy, no abnormal flags → running",
			f: AppFacts{
				Latest:          succeeded(nil),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedRunning,
		},
		{
			name: "down and blocked coexist → down wins",
			f: AppFacts{
				PlacementState: string(state.PlacementBlocked),
				Latest:         latest(func(r *state.DeployRecord) { r.SubstrateHalted = true }),
			},
			want: DerivedDown,
		},
	}
	for _, tc := range cases {
		if got := DeriveAppState(tc.f); got != tc.want {
			t.Fatalf("%s: DeriveAppState = %s, want %s", tc.name, got, tc.want)
		}
	}
}
