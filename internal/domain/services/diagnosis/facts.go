// facts.go 是诊断事实的值形状与纯消费（W1a）。装配（观测导出面、事件
// 账本派生、告警行、组件健康快照 → Facts）是 W1b/B2b 的领地，本包只定义
// 值形状与三态语义（docs/design/2026-10-30-zeabur-oss-borrowing.md §2.4）。
package diagnosis

import (
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Option 是三态事实面的载体：None=该面读不了（≠ 空集=可读且为空）。
// 「读不了 ≠ 不存在」是诚实边界：规则对 None 沉默（不猜），面名经
// Facts.UnreadableFaces 透出供 unreadable 聚合展示。
type Option[T any] struct {
	hasValue bool
	value    T
}

// Some 包装一个可读事实集（空集也是 Some——可读且确无）。
func Some[T any](v T) Option[T] { return Option[T]{hasValue: true, value: v} }

// None 表示该面读不了。
func None[T any]() Option[T] { return Option[T]{} }

// IsNone 报告是否读不了。
func (o Option[T]) IsNone() bool { return !o.hasValue }

// IsSome 报告是否可读（含空集）。
func (o Option[T]) IsSome() bool { return o.hasValue }

// Value 取事实集；ok=false 即 None。
func (o Option[T]) Value() (T, bool) { return o.value, o.hasValue }

// 事实面名（UnreadableFaces 的值域，unreadable 聚合按名展示）。
const (
	FaceObservations = "observations"
	FaceEvents       = "events"
	FaceAlerts       = "alerts"
	FaceComponents   = "components"
)

// WorkloadObservationFact 是单个 workload 的最新观测快照（观测缓存导出面
// 的诊断投影；每 workload 一条）。RestartCount 是**派生值**：近窗终态次数，
// 由事件账本纯派生（W1b 单源化——全仓无 restart 计数器状态，重启安全、
// 受 Event 保留窗约束）。Reason 按编排器原文透传：容器退出原因、健康探针
// 失败文案、OOMKilled 标记、镜像拉取失败原文都在此（规则按子串识别，契约
// 见各规则注释）。
type WorkloadObservationFact struct {
	// WorkloadID 是平台 Workload ID（归属判定锚）。
	WorkloadID string `yaml:"workload_id"`
	// Process 是进程模板名（web/worker；Fix 预填与「需要你」页定位用）。
	Process string `yaml:"process"`
	// State 是观测状态（capability.WorkloadState 值域单源）。
	State capability.WorkloadState `yaml:"state"`
	// ExitCode 是最近终态退出码（nil = 未观测/非终态；ADR-0025 决策 2）。
	ExitCode *int `yaml:"exit_code"`
	// Reason 是编排器侧终态补充原文（用户可见文本英文）。
	Reason string `yaml:"reason"`
	// RestartCount 是近窗终态次数（事件账本纯派生值，无独立计数器状态）。
	RestartCount int `yaml:"restart_count"`
	// LastExitAt 是最近一次终态时间（零值 = 窗内无终态）。
	LastExitAt time.Time `yaml:"last_exit_at"`
}

// EventFact 是近窗事件账本行（(team, aggregate, aggregate_id) 索引查询的
// 诊断投影；App 域内近窗，Seq 单调）。Code 是 eventcode 注册表名。
type EventFact struct {
	// Seq 是事件账本序列号（证据回指锚）。
	Seq int64 `yaml:"seq"`
	// Code 是事件名（eventcode 注册表）。
	Code string `yaml:"code"`
	// At 是发生时间。
	At time.Time `yaml:"at"`
	// Detail 是事件详情原文（与事件面同口径，不转写放大）。
	Detail string `yaml:"detail"`
}

// AlertFact 是告警规则评估态行（alertrule 值域：ok|firing；pending 随 B2a
// 持久化入列——诊断吃 pending 态）。v1 首批规则不消费告警态（稳态面），
// 形状先行。
type AlertFact struct {
	// Rule 是告警规则 ID。
	Rule string `yaml:"rule"`
	// State 是评估态（ok|firing|pending）。
	State string `yaml:"state"`
	// Metric 是指标名（alertrule 冻结值域：cpu_percent 等）。
	Metric string `yaml:"metric"`
}

// ComponentHealthFact 是受管组件健康快照行（B2b 共享导出面投影；v1 首批
// 规则不消费，Platform Incident 侧使用，形状先行）。
type ComponentHealthFact struct {
	// Name 是组件名（evidence 闭式清单的组件名维度）。
	Name string `yaml:"name"`
	// Healthy 是探活聚合态。
	Healthy bool `yaml:"healthy"`
}

// Facts 是一次诊断的观测快照（纯值；装配在 W1b/B2b）。四事实源全部
// Option 化三态；Now 是诊断时刻（窗口计算的锚，装配面注入）。
type Facts struct {
	// Observations 是观测快照面（None = 观测面读不了）。
	Observations Option[[]WorkloadObservationFact]
	// Events 是近窗事件面（None = 事件面读不了）。
	Events Option[[]EventFact]
	// Alerts 是告警态面（None = 告警面读不了）。
	Alerts Option[[]AlertFact]
	// Components 是组件健康面（None = 组件健康面读不了）。
	Components Option[[]ComponentHealthFact]
	// Now 是诊断时刻。
	Now time.Time
}

// UnreadableFaces 返回读不了的事实面名（供 unreadable 聚合诚实展示：
// 「这些面读不了，诊断未覆盖」）。
func (f Facts) UnreadableFaces() []string {
	var faces []string
	if f.Observations.IsNone() {
		faces = append(faces, FaceObservations)
	}
	if f.Events.IsNone() {
		faces = append(faces, FaceEvents)
	}
	if f.Alerts.IsNone() {
		faces = append(faces, FaceAlerts)
	}
	if f.Components.IsNone() {
		faces = append(faces, FaceComponents)
	}
	return faces
}

// observations 是规则共用的取数口：None 沉默（返回 false），Some 时给出
// 切片（空集即空切片——可读且确无，规则正常走「无可诊断」分支）。
func (f Facts) observations() ([]WorkloadObservationFact, bool) {
	return f.Observations.Value()
}

// isFailureState 报告观测态是否属「失败态」全集（unexplained 兜底的输入
// 宇宙）：failed（一次性失败终态）、degraded（副本部分失联/重启循环）、
// orphaned（对不上账——平台侧异常但未被任何 v1 规则解释，兜底收留）。
func isFailureState(s capability.WorkloadState) bool {
	return s == capability.WorkloadFailed || s == capability.WorkloadDegraded || s == capability.WorkloadOrphaned
}

// stillTerminated 报告「最近终止态仍在」：最新观测仍是失败/失联态（非
// running/pending/completed/stopped）——crash-loop/OOM 类规则的门槛之一，
// 防止把「已恢复的循环」误报（负向用例主体）。
func stillTerminated(s capability.WorkloadState) bool {
	return s == capability.WorkloadFailed || s == capability.WorkloadDegraded
}

// ExitCode 语义（POSIX/编排器惯例，128+n = SIGn）：仅对任务点名的四档给
// 专门语义，其余返回空串（不猜）。
const (
	// ExitNotExecutable：命令存在但不可执行（126）。
	ExitNotExecutable = 126
	// ExitCommandNotFound：命令不存在（127）。
	ExitCommandNotFound = 127
	// ExitSIGKILL：128+9，被 SIGKILL 杀（OOM killer 的常见面）。
	ExitSIGKILL = 137
	// ExitSIGTERM：128+15，被 SIGTERM 终止（平台停摆/操作者停止的常见面）。
	ExitSIGTERM = 143
)

// ExitCodeMeaning 返回退出码的英文语义短语（"" = 无专门语义）。用户可见
// 文本英文约定。
func ExitCodeMeaning(code int) string {
	switch code {
	case ExitNotExecutable:
		return "command is not executable"
	case ExitCommandNotFound:
		return "command not found"
	case ExitSIGKILL:
		return "killed by SIGKILL (137; often the OOM killer)"
	case ExitSIGTERM:
		return "terminated by SIGTERM (143; a graceful stop signal)"
	default:
		return ""
	}
}
