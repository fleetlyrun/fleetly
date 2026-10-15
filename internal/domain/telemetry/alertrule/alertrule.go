// Package alertrule 是阈值告警规则聚合的领域面（F2.5，ADR-0041 决策 3，
// ADR-0075 Repo 接口层）：per-App 规则 + 评估状态机落行（ok|firing；
// pending 持续窗在 engine 内存，重启重置——采样近似语义的一部分）。
// 评估写面（Observe/Transition）与配置写面（Create/Delete）分立。行类型
// 与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/alertrule。
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
	CreatedAt      string
	UpdatedAt      string
}
