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
			name: "无部署记录 → down",
			f:    AppFacts{},
			want: DerivedDown,
		},
		{
			name: "首发失败 scale=0 → down（场景 14）",
			f: AppFacts{
				Latest: latest(func(r *state.DeployRecord) { r.SubstrateHalted = true }),
			},
			want: DerivedDown,
		},
		{
			name: "首发未切流失败（无版本归位）→ down",
			f: AppFacts{
				Latest: latest(nil),
			},
			want: DerivedDown,
		},
		{
			name: "首发已切流观察窗失败（unstable）→ degraded（新版本仍服务）",
			f: AppFacts{
				Latest: latest(func(r *state.DeployRecord) { r.FirstHealthyAt = now; r.Verdict = state.VerdictUnstable }),
			},
			want: DerivedDegraded,
		},
		{
			name: "有有效版本 + 绑定 blocked → blocked（优先于 degraded）",
			f: AppFacts{
				PlacementState:  string(state.PlacementBlocked),
				Latest:          latest(func(r *state.DeployRecord) { r.Verdict = state.VerdictUnstable }),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedBlocked,
		},
		{
			name: "有有效版本 + 绑定 unresolved → blocked",
			f: AppFacts{
				PlacementState:  string(state.PlacementUnresolved),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedBlocked,
		},
		{
			name: "有有效版本 + 观察窗失败 unstable → degraded（场景 7/8）",
			f: AppFacts{
				Latest:          latest(func(r *state.DeployRecord) { r.FirstHealthyAt = now; r.Verdict = state.VerdictUnstable }),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedDegraded,
		},
		{
			name: "窗后不稳定（成功部署 post-window 告警位）→ degraded（场景 10）",
			f: AppFacts{
				Latest:          succeeded(func(r *state.DeployRecord) {}),
				LatestSucceeded: succeeded(func(r *state.DeployRecord) { r.Flags = state.DeployFlagPostWindowAlerted }),
			},
			want: DerivedDegraded,
		},
		{
			name: "警告通过（W_DEPLOY_INSTABILITY 位）→ degraded（场景 9，§2.10）",
			f: AppFacts{
				Latest:          succeeded(func(r *state.DeployRecord) { r.Flags = state.DeployFlagInstabilityWarning }),
				LatestSucceeded: succeeded(func(r *state.DeployRecord) { r.Flags = state.DeployFlagInstabilityWarning }),
			},
			want: DerivedDegraded,
		},
		{
			name: "归位成功的失败（replay，无 verdict）→ running（旧版本服务中）",
			f: AppFacts{
				Latest:          latest(func(r *state.DeployRecord) { r.Recovery = state.RecoveryReplay }),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedRunning,
		},
		{
			name: "成功部署、无异常位 → running",
			f: AppFacts{
				Latest:          succeeded(nil),
				LatestSucceeded: succeeded(nil),
			},
			want: DerivedRunning,
		},
		{
			name: "down 与 blocked 并存 → down 优先",
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
