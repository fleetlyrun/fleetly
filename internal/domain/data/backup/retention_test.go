package backup

// retention_test.go 是保留策略判定的白盒测试（P3a 批第 5 单元随批立法，
// ADR-0087 决策 2 白盒纪律项：零 fake/编排依赖 import，纯函数直测——
// 保留表全行：窗内/窗边界/窗外/混合策略（行上快照逐行不同）/时间面异常/
// 空集/全留/全删/保序；平台轨 keep-within duration 铸造全值域）。时间全
// 作参数（固定基准时刻——禁 time.Now 的域纪律在测试同形）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// retainBase 是保留表测试的基准时刻（固定时钟）。
var retainBase = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// retainRow 构造一行台账（CreatedAt 按 state.FormatTime 同口径铸 UTC
// RFC3339 文本）。
func retainRow(id string, created time.Time, retentionSecs int64) Backup {
	return Backup{ID: id, RetentionSecs: retentionSecs, CreatedAt: created.UTC().Format(time.RFC3339)}
}

// TestPruneDueWindowRows 钉单行窗判定全行（窗 = created_at + 行上
// retention 快照；边界 = 严格窗外，elapsed == retention 仍留）。
func TestPruneDueWindowRows(t *testing.T) {
	const week = 7 * 24 * 60 * 60 // 604800s：缺省保留窗
	cases := []struct {
		name string
		row  Backup
		want bool
	}{
		{
			name: "within window stays",
			row:  retainRow("b1", retainBase.Add(-6*24*time.Hour), week),
			want: false,
		},
		{
			name: "window boundary stays (strictly past due only)",
			row:  retainRow("b2", retainBase.Add(-7*24*time.Hour), week),
			want: false,
		},
		{
			name: "one second past the window is due",
			row:  retainRow("b3", retainBase.Add(-7*24*time.Hour-time.Second), week),
			want: true,
		},
		{
			name: "per-row snapshot window drives the verdict (short window, young row)",
			row:  retainRow("b4", retainBase.Add(-2*time.Hour), 3600),
			want: true,
		},
		{
			name: "zero retention is due after any elapsed time",
			row:  retainRow("b5", retainBase.Add(-time.Second), 0),
			want: true,
		},
		{
			name: "zero elapsed with zero retention stays (strictly greater)",
			row:  retainRow("b6", retainBase, 0),
			want: false,
		},
		{
			name: "clock setback keeps the row (negative elapsed never due)",
			row:  retainRow("b7", retainBase.Add(time.Hour), week),
			want: false,
		},
		{
			name: "empty created-at is never deletable (honest skip)",
			row:  Backup{ID: "b8", RetentionSecs: week, CreatedAt: ""},
			want: false,
		},
		{
			name: "corrupt created-at is never deletable (honest skip)",
			row:  Backup{ID: "b9", RetentionSecs: week, CreatedAt: "not-a-timestamp"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.row.PruneDue(retainBase))
		})
	}
}

// TestPruneSelection 钉集面选删：混合策略（同行集行上快照逐行不同）、
// 空集/全留/全删、保序（输入序传导输出）、不写输入（纯函数无副作用）。
func TestPruneSelection(t *testing.T) {
	old := retainRow("old", retainBase.Add(-10*24*time.Hour), 7*24*60*60)       // 窗外：删
	boundary := retainRow("boundary", retainBase.Add(-24*time.Hour), 24*60*60)  // 边界：留
	fresh := retainRow("fresh", retainBase.Add(-time.Hour), 7*24*60*60)         // 窗内：留
	shortWindow := retainRow("short", retainBase.Add(-2*time.Hour), 3600)       // 混合策略：短窗已过：删
	corrupt := Backup{ID: "corrupt", RetentionSecs: 3600, CreatedAt: "garbage"} // 时间面异常：留

	t.Run("mixed policies select only strictly-past rows in input order", func(t *testing.T) {
		in := []Backup{old, boundary, fresh, shortWindow, corrupt}
		got := PruneSelection(in, retainBase)
		assert.Equal(t, []Backup{old, shortWindow}, got)
	})

	t.Run("empty set selects nothing", func(t *testing.T) {
		assert.Empty(t, PruneSelection(nil, retainBase))
		assert.Empty(t, PruneSelection([]Backup{}, retainBase))
	})

	t.Run("all-within set selects nothing", func(t *testing.T) {
		assert.Empty(t, PruneSelection([]Backup{boundary, fresh}, retainBase))
	})

	t.Run("all-due set selects everything", func(t *testing.T) {
		in := []Backup{old, shortWindow}
		assert.Equal(t, in, PruneSelection(in, retainBase))
	})

	t.Run("selection does not mutate the ledger rows", func(t *testing.T) {
		in := []Backup{old, boundary, shortWindow}
		snapshot := append([]Backup(nil), in...)
		_ = PruneSelection(in, retainBase)
		assert.Equal(t, snapshot, in, "PruneSelection must stay a pure read")
	})
}

// TestResticKeepWithin 钉平台轨快照保留窗的 restic 表达：值域单位 y/m/w/
// d/h（不收 Go duration 串的 m/s 尾巴）、小时向上取整、最小 1h 钳位。
func TestResticKeepWithin(t *testing.T) {
	cases := []struct {
		name      string
		retention time.Duration
		want      string
	}{
		{name: "seven days as the default window", retention: 7 * 24 * time.Hour, want: "168h"},
		{name: "whole hours pass through", retention: 24 * time.Hour, want: "24h"},
		{name: "one hour minimum unit", retention: time.Hour, want: "1h"},
		{name: "sub-hour windows clamp up to 1h", retention: 30 * time.Minute, want: "1h"},
		{name: "fractional hours round up (keep more, not less)", retention: 90 * time.Minute, want: "2h"},
		{name: "zero retention clamps to 1h", retention: 0, want: "1h"},
		{name: "negative retention clamps to 1h", retention: -time.Hour, want: "1h"},
		// dind 实证锚：Go duration 串 "168h0m0s" 被 restic 拒——铸造面恒出纯小时字面。
		{name: "go duration tails never leak into the literal", retention: 168*time.Hour + 30*time.Minute, want: "169h"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ResticKeepWithin(tc.retention))
		})
	}
}
