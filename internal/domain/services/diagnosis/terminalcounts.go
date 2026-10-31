// terminalcounts.go 是终态计数纯派生（P3a 批第 8 单元，ADR-0087 决策
// 1/2/7：per-workload 终态计数 = 事件账本派生，自 application/engine/
// terminalcounts.go 下沉本包——事件账本行 → 事实投影的转换与 Facts 消费
// 面同包〔W1a 领地，B2a 注释自认「诊断规则面在 domain/services/diagnosis
// 不在本批射程」，本单元补上那批射程〕）。**无独立计数器状态**——不建
// 持久计数表、不加进程内计数 map；崩溃循环计数完全由事件切片计算（重启
// 安全、受 Event 保留窗约束——事实持久化，派生不留状态，与 pending 窗落
// 行同哲学；持久面零计数表/计数列由 guards/terminalcounters_test.go 执
// 法）。纯度铁律（ADR-0087 决策 2 + domain 三红线）：零 I/O、时间作参数
// 传入、不写 receiver、不 import genproto。留驻 engine 的是装配面：outbox
// 读与调用点（diagnose.go/crashnotify.go 经本包 API 消费）。

package diagnosis

import (
	"encoding/json"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// eventWorkloadContainerExit 是终态计数事实的事件名锚（eventcode 注册表
// 同源；usage 反扫的字面量命中点——engine/events.go 同字面锚并存）。
const eventWorkloadContainerExit = "workload.container_exited"

// TerminalCountFact 是事件账本行的最小事实投影（计数派生的输入面，纯值
// ——Name/WorkloadID/OccurredAt 恰是终态计数所需，无载荷放大）。
type TerminalCountFact struct {
	// Name 是事件名（eventcode 注册表同源）。
	Name string
	// WorkloadID 是事实的主体载体（payload workload_id 的单源投影）。
	WorkloadID string
	// OccurredAt 是事实落账时刻（outbox created_at）。
	OccurredAt time.Time
}

// ContainerExitedFacts 把事件账本行投影为终态计数事实：只取
// workload.container_exited 行（workload_id 从 payload 单源解析——消费方
// 不持载荷键知识）；非容器终态行与坏载荷行（解析失败/缺 workload_id）
// 不入事实面。
func ContainerExitedFacts(events []outbox.Event) []TerminalCountFact {
	var facts []TerminalCountFact
	for _, ev := range events {
		if ev.Name != eventWorkloadContainerExit {
			continue
		}
		var p struct {
			WorkloadID string `json:"workload_id"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.WorkloadID == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, ev.CreatedAt)
		if err != nil {
			continue
		}
		facts = append(facts, TerminalCountFact{Name: ev.Name, WorkloadID: p.WorkloadID, OccurredAt: at})
	}
	return facts
}

// DeriveTerminalCounts 从事件切片派生近窗 per-workload 终态计数：只数
// workload.container_exited 事实，窗内（cutoff = now-window ≤ OccurredAt ≤
// now——未来行视为时钟异常不计入）逐 workload 累计。崩溃循环判定 = 本
// 函数输出 + 阈值（CrashLoopThreshold）；函数是纯函数（同切片必同结果，
// 无任何隐藏状态）。
func DeriveTerminalCounts(events []TerminalCountFact, window time.Duration, now time.Time) map[string]int {
	cutoff := now.Add(-window)
	counts := map[string]int{}
	for _, f := range events {
		if f.Name != eventWorkloadContainerExit || f.WorkloadID == "" {
			continue
		}
		if f.OccurredAt.Before(cutoff) || f.OccurredAt.After(now) {
			continue
		}
		counts[f.WorkloadID]++
	}
	return counts
}

// LastExitAt 取窗内最近终态时刻（零值 = 窗内无终态；与
// DeriveTerminalCounts 同窗同过滤口径——WorkloadObservationFact.LastExitAt
// 的派生源）。
func LastExitAt(facts []TerminalCountFact, window time.Duration, now time.Time) time.Time {
	cutoff := now.Add(-window)
	var last time.Time
	for _, f := range facts {
		if f.Name != eventWorkloadContainerExit || f.WorkloadID == "" {
			continue
		}
		if f.OccurredAt.Before(cutoff) || f.OccurredAt.After(now) {
			continue
		}
		if f.OccurredAt.After(last) {
			last = f.OccurredAt
		}
	}
	return last
}
