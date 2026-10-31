package engine

// 终态计数纯派生（B2a W1b facts 地基，zeabur 借鉴设计 §2.4 第二轮定稿）：
// per-workload 终态计数 = 事件账本派生，**无独立计数器状态**——不建持久
// 计数表、不加进程内计数 map；崩溃循环计数完全由事件切片计算（重启安全、
// 受 Event 保留窗约束——事实持久化，派生不留状态，与 pending 窗落行同
// 哲学）。本文件只提供派生函数与事件账本行 → 事实投影的转换；诊断规则
// 面（domain/services/diagnosis，W1a 领地）不在本批射程。

import (
	"encoding/json"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// EventFact 是事件账本行的最小事实投影（计数派生的输入面，纯值——
// Name/WorkloadID/OccurredAt 恰是终态计数所需，无载荷放大）。
type EventFact struct {
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
func ContainerExitedFacts(events []outbox.Event) []EventFact {
	var facts []EventFact
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
		facts = append(facts, EventFact{Name: ev.Name, WorkloadID: p.WorkloadID, OccurredAt: at})
	}
	return facts
}

// DeriveTerminalCounts 从事件切片派生近窗 per-workload 终态计数：只数
// workload.container_exited 事实，窗内（cutoff = now-window ≤ OccurredAt ≤
// now——未来行视为时钟异常不计入）逐 workload 累计。崩溃循环判定 = 本
// 函数输出 + 阈值；函数是纯函数（同切片必同结果，无任何隐藏状态）。
func DeriveTerminalCounts(events []EventFact, window time.Duration, now time.Time) map[string]int {
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
