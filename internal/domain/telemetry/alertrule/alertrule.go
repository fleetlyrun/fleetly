// Package alertrule 是阈值告警规则聚合的领域面（F2.5，ADR-0041 决策 3，
// ADR-0075 Repo 接口层）：per-App 规则 + 评估状态机落行（ok|firing）。
// pending 持续窗直接落 pending_since 列（B2a W1b 第二轮定稿：无双写、
// 重启续窗——engine 起动/规则装载时从行恢复起算点，重启不重置；采样
// 连续性仍按采样序近似）。评估写面（Observe/BeginPending/ClearPending/
// Transition）与配置写面（Create/Delete）分立。行类型与 Repo 接口在此
// 单源，SQLite 实现住 infrastructure/persistence/alertrule。
package alertrule

// 指标值域（冻结词汇，ADR-0041）。
const (
	MetricCPUPercent       = "cpu_percent"
	MetricMemoryWorkingSet = "memory_working_set_bytes"
)

// 状态值域。
const (
	StateOK     = "ok"
	StateFiring = "firing"
)

// Rule 是一行阈值规则（含评估状态面）。
type Rule struct {
	ID             string
	Seq            int64
	AppID          string
	Metric         string
	Threshold      float64
	ForSecs        int64
	Enabled        bool
	State          string
	StateSince     string
	LastValue      float64
	LastObservedAt string
	// PendingSince 是越限持续窗的起算时刻（B2a W1b 直接改列形态）：
	// '' = 无 pending 窗；非空 = UTC RFC3339（state.FormatTime 同口径）。
	// 重启续窗锚：engine 装载规则时从本列恢复起算点，重启不重置。
	PendingSince string
	CreatedAt    string
	UpdatedAt    string
}
