// terminalcounts_test.go 是终态计数纯派生的白盒测试（P3a 第 8 单元随迁
// 自 engine facts_test.go 的 TestDeriveTerminalCountsPureFromSlice，并按
// 「派生表全行」扩面：投影行（名过滤/坏载荷/缺主体/坏时间戳）与计数行
// （窗内/窗外/未来/名不符/主体缺席 + 窗边界严格性）逐行钉死）。
package diagnosis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// tcNow 是计数窗锚（与 diagnosis_test.go 的 testNow 同刻——同包共享面）。
var tcNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// tcFact 造一条终态计数事实（事件名缺省 = workload.container_exited）。
func tcFact(wid string, at time.Time) TerminalCountFact {
	return TerminalCountFact{Name: eventWorkloadContainerExit, WorkloadID: wid, OccurredAt: at}
}

// TestContainerExitedFactsProjectionRows：投影表全行——只取
// workload.container_exited 行且 workload_id 从 payload 单源解析；非容器
// 终态行、坏 JSON、缺 workload_id、坏时间戳逐行不入事实面。
func TestContainerExitedFactsProjectionRows(t *testing.T) {
	at := tcNow.Add(-time.Minute)
	rows := []outbox.Event{
		{Name: eventWorkloadContainerExit, Payload: []byte(`{"workload_id":"wl-a"}`), CreatedAt: at.Format(time.RFC3339)},
		{Name: "workload.stopped", Payload: []byte(`{"workload_id":"wl-a"}`), CreatedAt: at.Format(time.RFC3339)},     // 名不符：不入
		{Name: eventWorkloadContainerExit, Payload: []byte(`{not-json`), CreatedAt: at.Format(time.RFC3339)},          // 坏载荷：不入
		{Name: eventWorkloadContainerExit, Payload: []byte(`{}`), CreatedAt: at.Format(time.RFC3339)},                 // 缺 workload_id：不入
		{Name: eventWorkloadContainerExit, Payload: []byte(`{"workload_id":""}`), CreatedAt: at.Format(time.RFC3339)}, // 空主体：不入
		{Name: eventWorkloadContainerExit, Payload: []byte(`{"workload_id":"wl-b"}`), CreatedAt: "not-a-time"},        // 坏时间戳：不入
	}
	facts := ContainerExitedFacts(rows)
	assert.Equal(t, []TerminalCountFact{{Name: eventWorkloadContainerExit, WorkloadID: "wl-a", OccurredAt: at}}, facts,
		"only well-formed container-exited rows project into terminal-count facts")

	// 载荷多余字段不放大进事实面（最小投影）。
	rich := ContainerExitedFacts([]outbox.Event{{
		Name:      eventWorkloadContainerExit,
		Payload:   []byte(`{"workload_id":"wl-a","exit_code":137,"reason":"oom"}`),
		CreatedAt: at.Format(time.RFC3339),
	}})
	assert.Equal(t, []TerminalCountFact{tcFact("wl-a", at)}, rich, "payload fields beyond workload_id do not leak into the fact")

	// 空账本 → nil 事实面（可读且确无）。
	assert.Empty(t, ContainerExitedFacts(nil))
}

// TestDeriveTerminalCountsRows：计数派生表全行——窗内累计、窗外（早于
// cutoff）/未来行（晚于 now）不计、非终态事实不计、主体缺席不计；窗边界
// 双端含端点（cutoff = now-window ≤ OccurredAt ≤ now——Before/After 严格，
// 恰在边界计入）。
func TestDeriveTerminalCountsRows(t *testing.T) {
	window := 10 * time.Minute
	events := []TerminalCountFact{
		tcFact("wl-a", tcNow.Add(-1*time.Minute)),
		tcFact("wl-a", tcNow.Add(-2*time.Minute)),
		tcFact("wl-a", tcNow.Add(-3*time.Minute)),
		tcFact("wl-a", tcNow.Add(-11*time.Minute)),                        // 窗外
		tcFact("wl-b", tcNow.Add(-9*time.Minute)),                         // 第二载体
		{Name: "workload.stopped", WorkloadID: "wl-a", OccurredAt: tcNow}, // 非终态事实
		tcFact("", tcNow),                                      // 主体缺席
		tcFact("wl-c", tcNow.Add(time.Minute)),                 // 未来行（时钟异常）
		tcFact("wl-edge", tcNow.Add(-window)),                  // 恰在 cutoff：计入（Before 严格）
		tcFact("wl-edge", tcNow),                               // 恰在 now：计入（After 严格）
		tcFact("wl-edge2", tcNow.Add(-window-time.Nanosecond)), // 恰出窗：不计
	}
	assert.Equal(t, map[string]int{
		"wl-a": 3, "wl-b": 1, "wl-edge": 2,
	}, DeriveTerminalCounts(events, window, tcNow),
		"crash-loop counts derive purely from the event slice (edges inclusive)")

	// 空切片 → 空 map（可读且确无，非 nil 语义由调用面 [wid] 索引承载）。
	assert.Equal(t, map[string]int{}, DeriveTerminalCounts(nil, window, tcNow))
}

// TestDeriveTerminalCountsPurity：纯函数纪律——乱序同结果、重复调用同
// 结果、突变返回 map 不回染（无隐藏状态）。
func TestDeriveTerminalCountsPurity(t *testing.T) {
	window := 10 * time.Minute
	events := []TerminalCountFact{
		tcFact("wl-a", tcNow.Add(-1*time.Minute)),
		tcFact("wl-a", tcNow.Add(-2*time.Minute)),
		tcFact("wl-a", tcNow.Add(-3*time.Minute)),
		tcFact("wl-a", tcNow.Add(-11*time.Minute)),                        // 窗外
		tcFact("wl-b", tcNow.Add(-9*time.Minute)),                         // 第二载体
		{Name: "workload.stopped", WorkloadID: "wl-a", OccurredAt: tcNow}, // 非终态事实
		tcFact("", tcNow),                      // 主体缺席
		tcFact("wl-c", tcNow.Add(time.Minute)), // 未来行（时钟异常）
	}
	want := map[string]int{"wl-a": 3, "wl-b": 1}
	assert.Equal(t, want, DeriveTerminalCounts(events, window, tcNow))

	shuffled := []TerminalCountFact{events[3], events[0], events[7], events[4], events[6], events[2], events[5], events[1]}
	assert.Equal(t, want, DeriveTerminalCounts(shuffled, window, tcNow), "order must not matter (pure derivation)")
	counts := DeriveTerminalCounts(events, window, tcNow)
	counts["wl-a"] = 99 // 突变返回值不回染
	assert.Equal(t, 3, DeriveTerminalCounts(events, window, tcNow)["wl-a"])
}

// TestLastExitAtRows：最近终态时刻派生——窗内取最大 OccurredAt、窗外新
// 行（晚于 now 的未来行）不计、全窗外/空面 → 零值；窗边界与计数同口径
// （双端含端点）。
func TestLastExitAtRows(t *testing.T) {
	window := 10 * time.Minute
	facts := []TerminalCountFact{
		tcFact("wl-a", tcNow.Add(-5*time.Minute)),
		tcFact("wl-a", tcNow.Add(-2*time.Minute)),                         // 窗内最近
		tcFact("wl-a", tcNow.Add(-11*time.Minute)),                        // 窗外旧
		tcFact("wl-a", tcNow.Add(time.Minute)),                            // 未来行不计
		{Name: "workload.stopped", WorkloadID: "wl-a", OccurredAt: tcNow}, // 非终态不计
		tcFact("", tcNow), // 主体缺席不计
	}
	assert.Equal(t, tcNow.Add(-2*time.Minute), LastExitAt(facts, window, tcNow),
		"last exit time is the newest in-window terminal fact")

	// 边界含端点：恰在 now 与恰在 cutoff 都是窗内。
	edges := []TerminalCountFact{tcFact("wl-a", tcNow.Add(-window)), tcFact("wl-a", tcNow)}
	assert.Equal(t, tcNow, LastExitAt(edges, window, tcNow))

	// 全窗外 / 空面 → 零值（窗内无终态）。
	assert.True(t, LastExitAt([]TerminalCountFact{tcFact("wl-a", tcNow.Add(-window-time.Nanosecond))}, window, tcNow).IsZero())
	assert.True(t, LastExitAt(nil, window, tcNow).IsZero())
}
