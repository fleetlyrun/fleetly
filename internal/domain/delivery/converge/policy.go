package converge

// 节流策略面（ADR-0082 §4 双通道触发与冷却、§5 posture 旗标的纯判定）：
// 何时允许发起收敛尝试（冷却窗三径解除）、一次失败如何记账（连续失败
// 阈值 → 熔断进窗 + converge_failed 一次沿 + 内置告警起爆沿）、posture
// 闸门（detect = 零运行时写）。自 application/engine/converge.go 下沉
// （ADR-0087 决策 7：converge 姿态/冷却/失败阈值）。留驻 engine 的执行
// 面：converges 的 fails/failSig/posture/cooldown/eventSeen/alertFiring
// 记账与 mu 锁、convergeCooldownOpen/convergeFail 的 I/O（事件发射与
// 告警沿派发）——形态 = 装配（锁内取记账）→ 域裁决 → 执行（记账更新/
// emit）。纯度铁律：零 I/O、时间作参数（禁 time.Now）、不写 receiver。

import (
	"time"
)

// ---- posture 闸门（ADR-0082 §5）----

// 收敛姿态值（配置键承载；engine.ParseConvergePosture 归一，缺省 all）。
const (
	// PostureAll：缺省收敛——检出漂移即收敛（staging 上站裁决：不设
	// detect 观察期）。
	PostureAll = "all"
	// PostureDetect：**零运行时写**——只报事件，不收敛、不物化、不隔离、
	// 不重建载体（收敛的观察期逃生门而非缺省）。门限对 fast / slow / 启动
	// 基线重放 / peer 撤销隔离四源一视同仁；启动面只重建期望账本
	// （ensuredSpec + ensuredBy，不写 gen 锚）。成文降级：gen/stopped 面在
	// 进程重启后失明、撤销 peer 的剥离延后到恢复 all 的下一拍——ADR-0082
	// §5 第 3/4 条。
	PostureDetect = "detect"
)

// Detect 报告 posture 是否处于 detect（零运行时写）姿态：收敛动作被拦
// （检测与事件面照发，ADR-0082 §5）；启动基线面同样以此分流（只重建期望
// 账本，不下发载体）。只认 detect 字面——其余值（含空）按 all 处置
// （配置值域执法在 engine.ParseConvergePosture fail-fast）。
func Detect(posture string) bool { return posture == PostureDetect }

// ---- 触发面与失败签名账（R02-M1）----

// Trigger 是一次收敛尝试的触发面（R02-M1）：有签名 = 快/慢通道圈定了
// 未收敛信号（Sig = 聚合签名）；无签名 = 启动基线重放 / 隔离重放等例行
// 重放。冷却解除径②（"漂移签名变化"）只在有签名触发上比对——无签名
// 调用既不匹配也不得解除冷却。
type Trigger struct {
	Sig string
}

// Signaled 报告触发是否携带签名（空聚合 = 无未收敛信号 = 无签名）。
func (t Trigger) Signaled() bool { return t.Sig != "" }

// FailureSignature 是失败签名的三态记账（R02-M1）：零值 = 最近一次失败
// 无签名（或账本缺席）；Present = 最近一次**有签名**失败的聚合签名在册。
// 比较只在 ChangedBy 一处做（解除径②的单一判定点）。
type FailureSignature struct {
	Sig     string
	Present bool
}

// ChangedBy 报告一次触发是否构成「漂移签名变化」解除径：无签名触发恒
// false（不得解除冷却）；有签名触发在账本无签名或签名不同时为 true。
func (f FailureSignature) ChangedBy(t Trigger) bool {
	if !t.Signaled() {
		return false
	}
	return !f.Present || f.Sig != t.Sig
}

// ---- 冷却窗判定（ADR-0082 §4 解除三径）----

// CooldownInput 是冷却窗判定的输入（engine 装配：锁内取记账快照 + 配置 +
// 时钟——时间作参数，禁 time.Now）。
type CooldownInput struct {
	// Cooling 报告记账面该 App 是否处于冷却窗（cooldown 键在场）。
	Cooling bool
	// Until 是冷却窗起点（Cooling 为真时有效；进窗时刻 = 第 N 次失败时刻）。
	Until time.Time
	// Window 是冷却窗时长（配置 Converge.Cooldown，缺省 10m）。
	Window time.Duration
	// Now 是判定时刻（engine 持时钟）。
	Now time.Time
	// FailSig 是最近一次**有签名**失败的快照（解除径②的比对账）。
	FailSig FailureSignature
	// PostureAtEnter 是进窗时的 posture 快照（解除径③的比对账）。
	PostureAtEnter string
	// PostureNow 是当前 posture（配置现值）。
	PostureNow string
	// Trigger 是本次收敛尝试的触发（径②仅携带签名的触发可解除）。
	Trigger Trigger
}

// CooldownOpen 裁决该 App 是否可发起收敛尝试（ADR-0082 §4 解除三径在窗内
// 逐拍判定）：
//
//	未记账冷却                      → 放行
//	超窗（Now − Until ≥ Window）    → 放行（径①：窗满自动）
//	漂移签名变化（有签名触发 × 账本无签名或签名异）→ 放行（径②：重触发）
//	posture 已切换（快照 ≠ 现值）   → 放行（径③：切换清零）
//	其余                            → 拦截（窗内尝试跳过）
//
// 纯函数：不写 receiver；释放的记账清账与告警 resolved 沿由 engine 执行
// （convergeReleaseLocked + alertFiring 临界区 + 事件派发）。
func CooldownOpen(in CooldownInput) bool {
	if !in.Cooling {
		return true
	}
	return in.Now.Sub(in.Until) >= in.Window ||
		in.FailSig.ChangedBy(in.Trigger) ||
		in.PostureAtEnter != in.PostureNow
}

// ---- 失败阈值裁决（ADR-0082 §4 冷却钉死）----

// FailInput 是一次收敛失败的裁决输入（engine 装配：锁内取记账快照 + 配置）。
type FailInput struct {
	// Fails 是既有连续失败计数（冷却阈值 N 的记账面）。
	Fails int
	// FailSig 是既有失败签名账（最近一次有签名失败的快照）。
	FailSig FailureSignature
	// Threshold 是冷却阈值 N（配置 Converge.FailureThreshold，缺省 3）。
	Threshold int
	// AlreadyCooling 报告记账面该 App 已处于冷却窗（一次语义的第二道
	// 防线：窗内再失败不重进窗、不重发事件）。
	AlreadyCooling bool
	// EventSeen 钉「本轮冷却已发 workload.converge_failed」（一次语义）。
	EventSeen bool
	// AlertFiring 报告内置告警现行 firing（converge-failed 系统行；
	// 沿才通知，不逐拍轰炸——platform-offsite-backup 先例对齐）。
	AlertFiring bool
	// Trigger 是本次失败的触发（签名账三态判定输入，R02-M1）。
	Trigger Trigger
}

// FailVerdict 是失败裁决的输出（新记账值 + 进窗/沿动作；engine 据此执行
// 记账更新与 I/O）。
type FailVerdict struct {
	// Fails 是新连续失败计数（签名变化重开为 1；否则 +1）。
	Fails int
	// FailSig 是新失败签名账（无签名失败保留旧账——一次无签名重放失败
	// 不得把签名账清成「无签名」，否则下一次同签名失败被误判成「签名
	// 变化」而解除冷却）。
	FailSig FailureSignature
	// EnterCooldown = 连续失败达阈值 N 且尚未在窗：engine 落冷却窗起点
	//（now）+ 进窗 posture 快照。
	EnterCooldown bool
	// EmitFailureEvent = 进窗沿且本轮未发过：engine 落
	// workload.converge_failed 事件 + eventSeen 记账（一次语义）。
	EmitFailureEvent bool
	// AlertEdge = 进窗沿且内置告警未 firing：engine 落 alertFiring +
	// alertSince（起爆沿）。
	AlertEdge bool
}

// AdjudicateFail 裁决一次收敛失败的记账与沿（ADR-0082 §4 冷却钉死：连续
// 失败 N=3 → 冷却窗 10min + workload.converge_failed 一次 + 内置告警
// firing 沿）：
//
//	失败携带签名 × 账本有签名且签名异 → 计数重开后 +1（签名变化重触发，
//	  新形态的失败是新失败序列的第一笔）
//	失败携带签名                      → 签名账更新为本次签名
//	失败无签名                        → 签名账原样（只计数、不抹账）
//	新计数 ≥ 阈值 N × 未在窗          → 进窗 + 事件沿（未发过）+ 告警沿
//	  （未 firing）
//
// 纯函数：不写 receiver；记账落账、事件发射与告警沿派发由 engine 执行。
func AdjudicateFail(in FailInput) FailVerdict {
	v := FailVerdict{Fails: in.Fails, FailSig: in.FailSig}
	// 签名变化重触发：与上次**有签名**失败不同的聚合签名 = 新触发（计数
	// 重开）。
	if in.Trigger.Signaled() {
		if prev := in.FailSig; prev.Present && prev.Sig != in.Trigger.Sig {
			v.Fails = 0
		}
		v.FailSig = FailureSignature{Sig: in.Trigger.Sig, Present: true}
	}
	v.Fails++
	enter := v.Fails >= in.Threshold
	if enter && !in.AlreadyCooling {
		v.EnterCooldown = true
		v.EmitFailureEvent = !in.EventSeen
		v.AlertEdge = !in.AlertFiring
	}
	return v
}
