package run

// adjudicate_test.go 是观测裁决表的白盒测试（ADR-0087 P3a 批第 2 单元：
// 裁决表全行逐格 + 边界——退出码原样记账、stopping 起因保留、
// platform_drained 归类、未列组合零动作；零 fake/编排依赖 import，纯函数
// 直测）。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// exit 是退出码指针构造子（nil 语义用字面 nil 直测）。
func exit(n int) *int { return &n }

// TestAdjudicateTable 钉裁决表逐格：观测态 × 行态 → 迁移目标/起因/退出码
// 携带。语义源 = F1.5/F1.6 观测两轨（表自 engine taskobs.go 下沉，逐字对
// 表）；未列组合一律零动作（零值 Verdict）。
func TestAdjudicateTable(t *testing.T) {
	cases := []struct {
		name string
		row  State
		obs  capability.WorkloadState
		exit *int
		want Verdict
	}{
		// 表行 1：观测 running + 行 pending → running（无终列写）。
		{name: "running observation moves a pending row to running",
			row: StatePending, obs: capability.WorkloadRunning,
			want: Verdict{Transit: true, From: []State{StatePending}, To: StateRunning}},
		{name: "repeated running observation on a running row is a no-op",
			row: StateRunning, obs: capability.WorkloadRunning,
			want: Verdict{}},
		{name: "running observation on a stopping row is a no-op",
			row: StateStopping, obs: capability.WorkloadRunning,
			want: Verdict{}},

		// 表行 2：观测 completed + 行 pending/running → stopped/completed。
		{name: "completed observation closes a pending row as completed",
			row: StatePending, obs: capability.WorkloadCompleted, exit: exit(0),
			want: Verdict{Transit: true, From: ActiveStates, To: StateStopped,
				StopReason: ReasonCompleted, ExitCode: exit(0)}},
		{name: "completed observation closes a running row as completed",
			row: StateRunning, obs: capability.WorkloadCompleted, exit: exit(0),
			want: Verdict{Transit: true, From: ActiveStates, To: StateStopped,
				StopReason: ReasonCompleted, ExitCode: exit(0)}},
		{name: "completed observation on a stopping row is a no-op",
			row: StateStopping, obs: capability.WorkloadCompleted, exit: exit(0),
			want: Verdict{}},

		// 表行 3：观测 failed + 行 pending/running → failed/failed。
		{name: "failed observation fails a pending row",
			row: StatePending, obs: capability.WorkloadFailed, exit: exit(1),
			want: Verdict{Transit: true, From: ActiveStates, To: StateFailed,
				StopReason: ReasonFailed, ExitCode: exit(1)}},
		{name: "failed observation fails a running row",
			row: StateRunning, obs: capability.WorkloadFailed, exit: exit(1),
			want: Verdict{Transit: true, From: ActiveStates, To: StateFailed,
				StopReason: ReasonFailed, ExitCode: exit(1)}},
		{name: "failed observation on a stopping row is a no-op",
			row: StateStopping, obs: capability.WorkloadFailed, exit: exit(1),
			want: Verdict{}},

		// 表行 4：观测 stopped + 行 stopping → stopped/<stopping 起因>
		//（起因保留：Verdict 不带起因表值，Mutation 不触该列）。
		{name: "stopped observation confirms a stopping row as stopped with the stamped reason kept",
			row: StateStopping, obs: capability.WorkloadStopped, exit: exit(137),
			want: Verdict{Transit: true, From: []State{StateStopping}, To: StateStopped,
				ExitCode: exit(137)}},

		// 表行 5：观测 stopped + 行 pending/running → stopped/platform_drained
		//（载体被平台侧移除——诚实归类，不伪装成败）。
		{name: "stopped observation drains a pending row as platform_drained",
			row: StatePending, obs: capability.WorkloadStopped,
			want: Verdict{Transit: true, From: ActiveStates, To: StateStopped,
				StopReason: ReasonPlatformDrained, ExitCode: nil}},
		{name: "stopped observation drains a running row as platform_drained",
			row: StateRunning, obs: capability.WorkloadStopped, exit: exit(137),
			want: Verdict{Transit: true, From: ActiveStates, To: StateStopped,
				StopReason: ReasonPlatformDrained, ExitCode: exit(137)}},

		// 终态行（迟到观测不重开状态机）：全观测态零动作。
		{name: "any observation on a stopped row is a no-op",
			row: StateStopped, obs: capability.WorkloadStopped,
			want: Verdict{}},
		{name: "any observation on a failed row is a no-op",
			row: StateFailed, obs: capability.WorkloadCompleted, exit: exit(0),
			want: Verdict{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Run{State: tc.row}
			ev := capability.WorkloadEvent{State: tc.obs, ExitCode: tc.exit}
			assert.Equal(t, tc.want, r.Adjudicate(ev))
		})
	}
}

// TestAdjudicateOffTableObservationStates 钉表外观测态：degraded/pending/
// orphaned 与未知值对全部行态零动作（Run 观测裁决表只认 running/completed/
// failed/stopped 四观测态——劣化/失联是 App 部署面的观测语义，不进 Run 对账）。
func TestAdjudicateOffTableObservationStates(t *testing.T) {
	rows := []State{StatePending, StateRunning, StateStopping, StateStopped, StateFailed}
	offs := []capability.WorkloadState{
		capability.WorkloadDegraded, capability.WorkloadPending,
		capability.WorkloadOrphaned, capability.WorkloadState("mystery"),
	}
	for _, row := range rows {
		for _, obs := range offs {
			r := &Run{State: row}
			assert.Equal(t, Verdict{}, r.Adjudicate(capability.WorkloadEvent{State: obs}),
				"row=%s obs=%s: off-table observation must stay a no-op", row, obs)
		}
	}
}

// TestAdjudicateExitCodeIsVerbatim 钉退出码原样记账的边界：成败归类由观测
// 状态决定（表注 0/非 0 是 capability 侧归类口径），本表不复核——异常组合
// （completed 带非 0、failed 带非 nil/0、终态观测未带退出码）一律原样落列。
func TestAdjudicateExitCodeIsVerbatim(t *testing.T) {
	cases := []struct {
		name string
		row  State
		obs  capability.WorkloadState
		exit *int
		want *int
	}{
		{name: "completed with a nil exit code carries nil",
			row: StateRunning, obs: capability.WorkloadCompleted, exit: nil, want: nil},
		{name: "completed with a non-zero exit code carries it verbatim",
			row: StateRunning, obs: capability.WorkloadCompleted, exit: exit(3), want: exit(3)},
		{name: "failed with a nil exit code carries nil",
			row: StateRunning, obs: capability.WorkloadFailed, exit: nil, want: nil},
		{name: "failed with a zero exit code carries it verbatim",
			row: StateRunning, obs: capability.WorkloadFailed, exit: exit(0), want: exit(0)},
		{name: "stopped confirmation with a nil exit code carries nil",
			row: StateStopping, obs: capability.WorkloadStopped, exit: nil, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Run{State: tc.row}
			got := r.Adjudicate(capability.WorkloadEvent{State: tc.obs, ExitCode: tc.exit})
			require.True(t, got.Transit)
			if tc.want == nil {
				assert.Nil(t, got.ExitCode)
				return
			}
			require.NotNil(t, got.ExitCode)
			assert.Equal(t, *tc.want, *got.ExitCode)
		})
	}
}

// TestAdjudicateIgnoresIrrelevantEventFields 钉表键窄面：裁决只认观测 State
// 与 ExitCode——Instance/Reason/Generation/Message 等不参与对账（引擎观测
// 缓存的 last-write-wins 全量入、裁决窄键出）。
func TestAdjudicateIgnoresIrrelevantEventFields(t *testing.T) {
	bare := (&Run{State: StateRunning}).
		Adjudicate(capability.WorkloadEvent{State: capability.WorkloadCompleted, ExitCode: exit(0)})
	rich := (&Run{State: StateRunning}).
		Adjudicate(capability.WorkloadEvent{
			WorkloadID: "run-7", Generation: capability.Generation(9), Drift: true,
			Node: "node-1", Message: "Exited", Reason: "Completed",
			Instance: "task-7", RestartCount: 2,
			State: capability.WorkloadCompleted, ExitCode: exit(0),
		})
	assert.Equal(t, bare, rich)
}

// TestVerdictMutation 钉行写回调的列语义：终态裁决写终列（起因非空才写、
// 退出码恒写原样），stopping 起因保留；非终态/零动作裁决返回 nil——
// pending→running 不触终列。
func TestVerdictMutation(t *testing.T) {
	t.Run("non-transit and non-terminal verdicts mutate no columns", func(t *testing.T) {
		assert.Nil(t, Verdict{}.Mutation())
		assert.Nil(t, (Verdict{Transit: true, From: []State{StatePending}, To: StateRunning}).Mutation())
	})
	t.Run("completed verdict writes the reason and the observed exit code", func(t *testing.T) {
		v := (&Run{State: StateRunning}).
			Adjudicate(capability.WorkloadEvent{State: capability.WorkloadCompleted, ExitCode: exit(0)})
		row := &Run{State: StateStopping, StopReason: ReasonTTLExpired, ExitCode: exit(9)}
		v.Mutation()(row)
		assert.Equal(t, ReasonCompleted, row.StopReason)
		require.NotNil(t, row.ExitCode)
		assert.Equal(t, 0, *row.ExitCode)
	})
	t.Run("stopped confirmation keeps the stamped stopping reason and writes the exit code", func(t *testing.T) {
		v := (&Run{State: StateStopping}).
			Adjudicate(capability.WorkloadEvent{State: capability.WorkloadStopped, ExitCode: exit(137)})
		row := &Run{State: StateStopping, StopReason: ReasonTTLExpired}
		v.Mutation()(row)
		assert.Equal(t, ReasonTTLExpired, row.StopReason, "the stopping reason stamped at stop time survives the terminal confirmation")
		require.NotNil(t, row.ExitCode)
		assert.Equal(t, 137, *row.ExitCode)
	})
	t.Run("stopped confirmation overwrites a stale exit code with an unobserved nil", func(t *testing.T) {
		v := (&Run{State: StateStopping}).
			Adjudicate(capability.WorkloadEvent{State: capability.WorkloadStopped})
		row := &Run{State: StateStopping, StopReason: ReasonLeaseExpired, ExitCode: exit(9)}
		v.Mutation()(row)
		assert.Equal(t, ReasonLeaseExpired, row.StopReason)
		assert.Nil(t, row.ExitCode)
	})
	t.Run("platform drain stamps platform_drained", func(t *testing.T) {
		v := (&Run{State: StateRunning}).
			Adjudicate(capability.WorkloadEvent{State: capability.WorkloadStopped, ExitCode: exit(137)})
		row := &Run{State: StateRunning}
		v.Mutation()(row)
		assert.Equal(t, ReasonPlatformDrained, row.StopReason)
		require.NotNil(t, row.ExitCode)
		assert.Equal(t, 137, *row.ExitCode)
	})
}

// TestAdjudicateDoesNotMutateRow 钉决策纯度：Adjudicate 不写 receiver（行写
// 执行留 engine——ADR-0087 决策 3 状态机搬家禁令）。
func TestAdjudicateDoesNotMutateRow(t *testing.T) {
	row := &Run{ID: "r1", State: StateRunning, StopReason: "", ExitCode: nil}
	before := *row
	_ = row.Adjudicate(capability.WorkloadEvent{State: capability.WorkloadFailed, ExitCode: exit(1)})
	_ = row.Adjudicate(capability.WorkloadEvent{State: capability.WorkloadRunning})
	assert.Equal(t, before, *row, "the verdict must leave the row untouched")
}

// TestAdjudicateTotality 钉 Verdict 全表不变量：观测态全集 × 行态全集扫描，
// 凡迁移必是合法目标态、from 集必含当前行态、行写回调只在终态迁移出现
// （执行面据此可安全把 Mutation 直传 Transit）。
func TestAdjudicateTotality(t *testing.T) {
	observations := []capability.WorkloadState{
		capability.WorkloadPending, capability.WorkloadRunning, capability.WorkloadDegraded,
		capability.WorkloadStopped, capability.WorkloadOrphaned,
		capability.WorkloadCompleted, capability.WorkloadFailed, capability.WorkloadState(""),
	}
	rows := []State{StatePending, StateRunning, StateStopping, StateStopped, StateFailed, State("bogus")}
	exits := []*int{nil, exit(0), exit(1)}
	for _, row := range rows {
		for _, obs := range observations {
			for _, exitCode := range exits {
				r := &Run{State: row}
				v := r.Adjudicate(capability.WorkloadEvent{State: obs, ExitCode: exitCode})
				if !v.Transit {
					assert.Equal(t, Verdict{}, v, "row=%s obs=%s: a no-op verdict must be the zero value", row, obs)
					assert.Nil(t, v.Mutation(), "row=%s obs=%s: a no-op verdict must not carry a mutation", row, obs)
					continue
				}
				assert.Contains(t, []State{StateRunning, StateStopped, StateFailed}, v.To,
					"row=%s obs=%s: illegal transition target %s", row, obs, v.To)
				assert.Contains(t, v.From, row,
					"row=%s obs=%s: the CAS from-set must contain the adjudicated row state", row, obs)
				if v.To.Terminal() {
					require.NotNil(t, v.Mutation(), "row=%s obs=%s: a terminal verdict must carry a mutation", row, obs)
				} else {
					assert.Nil(t, v.Mutation(), "row=%s obs=%s: a non-terminal verdict must not carry a mutation", row, obs)
				}
			}
		}
	}
}
