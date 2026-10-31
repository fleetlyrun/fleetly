package schedule

// adjudicate_test.go 是重叠策略与到期判定的白盒测试（ADR-0087 P3a 批第 6
// 单元：档位全行 × 重叠事实矩阵 + 到期窗口归属边界——相等即到期、坏时刻
// 不误拍、手动面不问档位、档位字面钉值；零 fake/编排依赖 import，纯函数
// 直测）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dueBase 是到期判定的基准时刻（固定时钟——时间作参数传入，禁 time.Now
// 的域纪律在测试同形；state.FormatTime 同口径 UTC RFC3339）。
var dueBase = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// TestScheduleDue 钉到期窗口归属：next_fire_at ≤ now 即到期；空时刻
// （不触发）与时间面异常（创建面校验后的不可达防御）判未到期——坏行诚实
// 跳过，不误拍。语义源 = ADR-0018 §A（表自 engine schedule.go 的墙钟比较
// 下沉，逐字对表）。
func TestScheduleDue(t *testing.T) {
	cases := []struct {
		name   string
		fireAt string
		now    time.Time
		want   bool
	}{
		{name: "empty next_fire_at never fires", fireAt: "", now: dueBase, want: false},
		{name: "corrupt next_fire_at is not due (honest skip)", fireAt: "not-a-timestamp", now: dueBase, want: false},
		{name: "future occurrence is not due", fireAt: "2026-11-02T00:00:00Z", now: dueBase, want: false},
		{name: "one nanosecond early is not due", fireAt: "2026-11-01T00:00:00Z", now: dueBase.Add(-time.Nanosecond), want: false},
		{name: "exact boundary is due (now == next_fire_at)", fireAt: "2026-11-01T00:00:00Z", now: dueBase, want: true},
		{name: "past occurrence is due (missed window replays once)", fireAt: "2026-10-31T23:59:00Z", now: dueBase, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Schedule{NextFireAt: tc.fireAt}
			assert.Equal(t, tc.want, s.Due(tc.now))
		})
	}
}

// TestScheduleDueDoesNotMutateRow 钉决策纯度：Due 不写 receiver（拍/跳/
// next_fire_at 推进执行留 engine——ADR-0087 决策 3 状态机搬家禁令）。
func TestScheduleDueDoesNotMutateRow(t *testing.T) {
	s := &Schedule{ID: "01JD0SCHD00000000000000000", NextFireAt: "2026-11-01T00:00:00Z"}
	before := *s
	_ = s.Due(dueBase)
	_ = s.Due(dueBase.Add(time.Hour))
	assert.Equal(t, before, *s, "the due verdict must leave the row untouched")
}

// TestOverlapAdjudicateTable 钉重叠裁决表逐格（语义源 = ADR-0018 A.3 修订 /
// ADR-0017 附录 A.4，表自 engine schedule.go 下沉，逐字对表）：重叠事实 ×
// 档位 → 到期拍跳/照常拍；手动面（Overlapping，恒拒语义）不问档位。
func TestOverlapAdjudicateTable(t *testing.T) {
	cases := []struct {
		name string
		fact Overlap
		// 手动面：重叠事实（档位无关）。
		wantOverlapping bool
		// 到期拍面：档位全行 → 跳或拍。
		wantSkip map[string]bool
	}{
		{
			name:            "never fired (no last task) is never overlapping",
			fact:            Overlap{},
			wantOverlapping: false,
			wantSkip:        map[string]bool{OverlapSkip: false, OverlapFire: false},
		},
		{
			name:            "driving runs without a last task id do not overlap (defensive guard)",
			fact:            Overlap{DrivingRuns: 1},
			wantOverlapping: false,
			wantSkip:        map[string]bool{OverlapSkip: false, OverlapFire: false},
		},
		{
			name:            "last task with all runs terminal is not overlapping",
			fact:            Overlap{LastTaskID: "01JD0TASK0000000000000000A", DrivingRuns: 0},
			wantOverlapping: false,
			wantSkip:        map[string]bool{OverlapSkip: false, OverlapFire: false},
		},
		{
			name:            "one driving run overlaps (run state is the source of truth)",
			fact:            Overlap{LastTaskID: "01JD0TASK0000000000000000A", DrivingRuns: 1},
			wantOverlapping: true,
			wantSkip:        map[string]bool{OverlapSkip: true, OverlapFire: false},
		},
		{
			name:            "several driving runs overlap",
			fact:            Overlap{LastTaskID: "01JD0TASK0000000000000000A", DrivingRuns: 3},
			wantOverlapping: true,
			wantSkip:        map[string]bool{OverlapSkip: true, OverlapFire: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantOverlapping, tc.fact.Overlapping())
			for policy, want := range tc.wantSkip {
				assert.Equal(t, want, tc.fact.SkipDueFire(policy), "policy %q", policy)
			}
		})
	}
}

// TestOverlapPolicyLiterals 钉档位字面值（engine 配置词汇面与
// ParseScheduleOverlap 消费同一字面——值漂移即红）与未登记值的处置：空/
// 未登记档按 skip 档处置（值域执法在 engine 配置面 fail-fast，域侧忠实
// 承载原「非 fire 即跳」口径）。
func TestOverlapPolicyLiterals(t *testing.T) {
	assert.Equal(t, "skip", OverlapSkip)
	assert.Equal(t, "fire", OverlapFire)
	require.NotEqual(t, OverlapSkip, OverlapFire, "the two policy tiers must stay distinct literals")

	overlapping := Overlap{LastTaskID: "01JD0TASK0000000000000000A", DrivingRuns: 1}
	for _, policy := range []string{"", "queue", "SKIP", "unknown"} {
		assert.True(t, overlapping.SkipDueFire(policy),
			"policy %q must behave as skip for an overlapping due fire", policy)
	}
}
