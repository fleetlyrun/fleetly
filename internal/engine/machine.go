package engine

import (
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// 发布状态机（release-semantics §2.3）：queued → preparing → building →
// releasing → observing → succeeded，任何非常态失败/取消进入 failed /
// cancelled。building 在全部服务镜像可得（镜像模式或 build 层已有 digest）
// 时直通（preparing → releasing）。releasing 的 blocked_waiting 是子状态
//（deployments.phase 列），不进主状态词表——节点 DOWN 看门狗暂停计时，
// 状态仍是 releasing。
//
// S16-C5：转移表真源已下沉 internal/state/machine.go（state 是部署行的
// 权威状态层）——UpdateDeployment 的 CAS 分支在写路径按表校验 from→to，
// 非法转移拒写（state.ErrIllegalTransition）。本文件保留引擎侧 Phase 词表
// 与兼容导出（转移判定委托 state 权威实现，表不两份；穷举测试
// machine_test.go 钉死两侧一致性）。

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
	return state.DeploymentStatus(p).Terminal()
}

// Valid 报告主状态是否在词表内。
func (p Phase) Valid() bool {
	return state.DeploymentStatus(p).Valid()
}

// CanTransition 报告 from → to 是否合法转移（委托 state 权威转移表）。
func CanTransition(from, to Phase) bool {
	return state.CanTransitionDeployment(state.DeploymentStatus(from), state.DeploymentStatus(to))
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
	out := make([]Phase, 0, 4)
	for _, s := range state.LegalDeploymentTransitions(state.DeploymentStatus(from)) {
		out = append(out, Phase(s))
	}
	return out
}

// AllPhases 返回主状态全词表（穷举测试遍历用）。
func AllPhases() []Phase {
	all := state.AllDeploymentStatuses()
	out := make([]Phase, 0, len(all))
	for _, s := range all {
		out = append(out, Phase(s))
	}
	return out
}
