package engine

import "fmt"

// 发布状态机（release-semantics §2.3）：queued → preparing → building →
// releasing → observing → succeeded，任何非常态失败/取消进入 failed /
// cancelled。building 在全部服务镜像可得（镜像模式或 build 层已有 digest）
// 时直通（preparing → releasing）。releasing 的 blocked_waiting 是子状态
// （deployments.phase 列），不进主状态词表——节点 DOWN 看门狗暂停计时，
// 状态仍是 releasing。
//
// 转移纪律：
//   - 终态（succeeded/failed/cancelled）不可逆（表内无出边）；
//   - observing 不可取消（曾健康不可 cancel，409 语义）——表内无
//     observing → cancelled 边，cancel 准入由引擎按 first_healthy_at 预检
//     后走 releasing → cancelled；
//   - 引擎外的兜底失败（控制面重启无法判定）允许非终态直达 failed。

// Phase 是状态机主状态（与 state.DeploymentStatus 同词表；本包自有常量
// 避免引擎→state 的枚举反向依赖——行写入时按字符串落库）。
type Phase string

const (
	PhaseQueued    Phase = "queued"
	PhasePreparing Phase = "preparing"
	PhaseBuilding  Phase = "building"
	PhaseReleasing Phase = "releasing"
	PhaseObserving Phase = "observing"
	PhaseSucceeded Phase = "succeeded"
	PhaseFailed    Phase = "failed"
	PhaseCancelled Phase = "cancelled"
	// PhaseNone 是子状态空值（无子状态）。
	PhaseNone string = ""
)

// SubPhase 是 releasing 内的子状态（blocked_waiting，场景 15）。
const (
	SubBlockedWaiting = "blocked_waiting"
)

// Terminal 报告主状态是否终态。
func (p Phase) Terminal() bool {
	switch p {
	case PhaseSucceeded, PhaseFailed, PhaseCancelled:
		return true
	}
	return false
}

// Valid 报告主状态是否在词表内。
func (p Phase) Valid() bool {
	switch p {
	case PhaseQueued, PhasePreparing, PhaseBuilding, PhaseReleasing,
		PhaseObserving, PhaseSucceeded, PhaseFailed, PhaseCancelled:
		return true
	}
	return false
}

// transitions 是合法转移表（穷举定义；表外全部非法）。行 = from，列集合
// = 允许的 to。
var transitions = map[Phase][]Phase{
	PhaseQueued:    {PhasePreparing, PhaseFailed, PhaseCancelled},
	PhasePreparing: {PhaseBuilding, PhaseReleasing, PhaseFailed, PhaseCancelled},
	PhaseBuilding:  {PhaseReleasing, PhaseFailed, PhaseCancelled},
	PhaseReleasing: {PhaseObserving, PhaseFailed, PhaseCancelled},
	PhaseObserving: {PhaseSucceeded, PhaseFailed},
	// 终态无出边。
}

// CanTransition 报告 from → to 是否合法转移。
func CanTransition(from, to Phase) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if from.Terminal() {
		return false
	}
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// ErrIllegalTransition 表示非法状态转移（含终态出边）。
type ErrIllegalTransition struct {
	From Phase
	To   Phase
}

func (e *ErrIllegalTransition) Error() string {
	return fmt.Sprintf("engine: illegal deployment transition %s -> %s", e.From, e.To)
}

// Transition 校验 from → to 合法，非法返回 *ErrIllegalTransition。
func Transition(from, to Phase) error {
	if CanTransition(from, to) {
		return nil
	}
	return &ErrIllegalTransition{From: from, To: to}
}

// LegalTransitions 返回 from 的合法目标集合（穷举测试断言用；from 为终态
// 或未知状态返回空）。
func LegalTransitions(from Phase) []Phase {
	out := make([]Phase, 0, len(transitions[from]))
	if !from.Valid() || from.Terminal() {
		return out
	}
	return append(out, transitions[from]...)
}

// AllPhases 返回主状态全词表（穷举测试遍历用）。
func AllPhases() []Phase {
	return []Phase{PhaseQueued, PhasePreparing, PhaseBuilding, PhaseReleasing,
		PhaseObserving, PhaseSucceeded, PhaseFailed, PhaseCancelled}
}
