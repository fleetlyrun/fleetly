// Package converge 承载收敛策略的纯裁决面（P3a 批第 4 单元，ADR-0087 决策
// 1/3/7：converge 姿态/冷却/失败阈值（ADR-0082 策略）自 application/engine/
// converge.go 下沉）。落点定界：收敛语义强绑 deployment 交付面（期望集 =
// LatestSucceeded 基线投影、驱动权复查、冻结闸门），不依附单一聚合状态机
// （非迁移表），亦非多规则注册表族——按聚合域包形态落 domain/delivery 邻域
// 小策略包，与漂移判定核（domain/delivery/drift）邻接对称（检出漂移 → 收敛
// 裁决是同一产品线）。留驻 engine 的执行面（converge 是机制原语 L1#8，本单元
// 只下沉策略谓词、机制零触碰）：converges 记账与锁（memo/resolved/fails/
// failSig/posture/cooldown/eventSeen/projMemo/replay/teardown/alertFiring）、
// convergeStep 双通道驱动、attemptConverge 闸门序与 materialize、事实面发射
// （事件/审计）、markReplay/clearReplay 上行钩子、Ensure 执行——engine 侧
// 形态 = 装配（锁内取记账）→ 域裁决 → 执行（记账更新/Ensure/emit）。纯度
// 铁律（ADR-0087 决策 2 + domain 三红线）：零 I/O、时间作参数传入（禁
// time.Now——domainpurity 纪律项）、不写 receiver、不 import genproto。
package converge

import (
	"sort"
	"strings"
)

// ---- 裁决表（ADR-0082 §1：8 判定行 + 2 非行；golden 钉死 = 表即断言）----

// Input 是裁决表一行的输入面（每行一个判定分支 + golden 快照钉死；行号对齐
// ADR-0082 §1 表序）。行 5（spec 相符 + stopped）是**信号层非行**——稳态
// 看门狗只发事件、不产收敛信号，本结构无此字段；行 9（域外孤儿）**明确未
// 落码**——观测面只有域内枚举、无 WorkloadOrphaned 事件码，本结构同样无此
// 字段（ADR-0082 修订版 §1）。
type Input struct {
	Frozen         bool // 行7：冻结中
	Driving        bool // 行6：活跃部署中（含 failed 待自动回滚窗）
	SpecMismatch   bool // 行1：spec 失配（image/command/replicas）
	Absent         bool // 行2：载体缺席（期望集 − 观测集 ≠ ∅）
	GenDeviated    bool // 行3：稳态 gen 偏离
	RolloutStalled bool // 行4：终态后滚动停摆（spec+gen 均一致）
	ExtraCarriers  bool // 行8：域内多余载体
	CancelLegacy   bool // 行10：Cancel 遗留载体（终态行、部分下发）
}

// verdictFieldProducers 是裁决表字段的生产者登记表（字段名 → 唯一生产
// 构造点）：每个字段必须有唯一生产构造者（"不可伪造"原则）。加字段不
// 登记、或表项无对应字段即红（白盒 TestVerdictFieldProducersComplete
// 双向保鲜）；信号侧六类与映射的完备性由 TestVerdictSignalClassTotality
// 执法。生产者均在 engine 装配面（闸门事实查询与信号圈定是 I/O 编排）。
var verdictFieldProducers = map[string]string{
	"Frozen":         "GateInput ← engine convergeFrozen",
	"Driving":        "GateInput ← Deployments.DrivingByApp",
	"SpecMismatch":   "SignalInput ← SignalSpec",
	"Absent":         "SignalInput ← SignalAbsent",
	"GenDeviated":    "SignalInput ← SignalGen",
	"RolloutStalled": "SignalInput ← SignalRollout",
	"ExtraCarriers":  "SignalInput ← SignalExtra",
	"CancelLegacy":   "SignalInput ← SignalReplay",
}

// 裁决值（golden 字面）。
const (
	VerdictSteady         = "steady"          // 无漂移：稳态（慢通道 memo 节拍照常，非裁决行）
	VerdictConverge       = "converge"        // 收敛动作（§2 统一原语）
	VerdictSkip           = "skip"            // 跳过本拍（驱动权在部署链）
	VerdictSuspend        = "suspend"         // 挂起（不消费签名，解冻重扫）
	VerdictReplayBaseline = "replay-baseline" // 回卷 LatestSucceeded 基线
)

// Adjudicate 是收敛裁决表的判定函数（优先序：闸门行 > 信号行；Cancel
// 遗留在信号行之后——同拍多信号时以显式信号裁决优先）。两个生产构造点：
// SignalInput（信号侧）与 GateInput（闸门侧）；engine 的 convergeStep
// 与 attemptConverge 都真实调用它。posture=detect 不是行裁决：它是全局
// 闸门（收敛动作前置检查，零运行时写——fast/slow/启动基线重放/peer 撤销
// 隔离四源同受门限，ADR-0082 §5），检测与事件面照发，见 policy.go 的
// Detect。纯函数：不写 receiver，执行由调用方承担。
func Adjudicate(in Input) string {
	switch {
	case in.Frozen:
		return VerdictSuspend // 行7：不消费签名、不发收敛；解冻后下一拍重扫即收敛
	case in.Driving:
		return VerdictSkip // 行6：复查口径与驱动权同源（ListDriving 语义含 failed 待回滚窗）
	case in.SpecMismatch:
		return VerdictConverge // 行1：driftScan 判定 → 收敛动作
	case in.Absent:
		return VerdictConverge // 行2：期望集 = LatestSucceeded 重投影（决策输入是 Revision 真源）
	case in.GenDeviated:
		return VerdictConverge // 行3：检测下沉 driftScan，独立签名键 + 收敛信号
	case in.RolloutStalled:
		return VerdictConverge // 行4：重放途经 resumeStalledRollout；部署窗内归行6 L1 语义
	case in.CancelLegacy:
		return VerdictReplayBaseline // 行10：取消的部署从未成为期望态（翻案 admission N0 本场景）
	case in.ExtraCarriers:
		return VerdictConverge // 行8：Ensure 域内移除既有语义（顺带）
	default:
		return VerdictSteady
	}
}

// SignalInput 把一次扫描的信号集映射为裁决表输入——信号键→行的
// 唯一映射点（ADR-0082 §1 的"表即断言"在信号侧的执行面）。未知信号键
// 不置位任何字段（不得为未知键伪造裁决输入）。
func SignalInput(signals Signals) Input {
	var in Input
	for key := range signals {
		class, ok := SignalClassOf(key)
		if !ok {
			continue // 未知信号类：无行可映射（不伪造输入）
		}
		switch class {
		case SignalSpec:
			in.SpecMismatch = true
		case SignalAbsent:
			in.Absent = true
		case SignalGen:
			in.GenDeviated = true
		case SignalRollout:
			in.RolloutStalled = true
		case SignalExtra:
			in.ExtraCarriers = true
		case SignalReplay:
			in.CancelLegacy = true
		}
	}
	return in
}

// GateInput 把闸门事实映射为裁决表输入——冻结/驱动权两行（ADR-0082
// §1 行6/行7）的唯一映射点。
func GateInput(frozen, driving bool) Input {
	return Input{Frozen: frozen, Driving: driving}
}

// Actionable 报告裁决值是否应发起收敛动作（converge | replay-baseline；
// 两者动作同为「基线物化」，差别只在触发来源与事实面标签）。
func Actionable(verdict string) bool {
	return verdict == VerdictConverge || verdict == VerdictReplayBaseline
}

// ---- 信号类（键构造与裁决映射的单一枚举）----

// SignalClass 是收敛信号类的单一枚举（键构造与裁决映射的唯一构造点）：
// 每类一个前缀 + 一个裁决表字段，新增类不在两处同时登记即红
// （TestVerdictSignalClassTotality）。
type SignalClass uint8

const (
	SignalSpec    SignalClass = iota // 行1：spec 失配
	SignalAbsent                     // 行2：载体缺席
	SignalGen                        // 行3：稳态 gen 偏离
	SignalRollout                    // 行4：终态后滚动停摆
	SignalExtra                      // 行8：域内多余载体
	SignalReplay                     // 行10：Cancel/失败终态遗留
)

// AllSignalClasses 返回全部信号类（完备性守卫的遍历面）。
func AllSignalClasses() []SignalClass {
	return []SignalClass{
		SignalSpec, SignalAbsent, SignalGen,
		SignalRollout, SignalExtra, SignalReplay,
	}
}

// Key 构造该类在 Signals 里的键（suffix 为空 = 无逐载体维度）。
func (c SignalClass) Key(suffix string) string {
	if suffix == "" {
		return c.String()
	}
	return c.String() + "|" + suffix
}

// String 返回信号类字面（测试/日志；键前缀即本字面）。
func (c SignalClass) String() string {
	switch c {
	case SignalSpec:
		return "spec"
	case SignalAbsent:
		return "absent"
	case SignalGen:
		return "gen"
	case SignalRollout:
		return "rollout"
	case SignalExtra:
		return "extra"
	case SignalReplay:
		return "replay"
	}
	return "unknown"
}

// SignalClassOf 解析信号键的信号类（键前缀即类名）。未知键 = 无类：
// 调用方按"非收敛行"处理，不得为未知键伪造裁决输入。
func SignalClassOf(key string) (SignalClass, bool) {
	prefix, _, _ := strings.Cut(key, "|")
	for _, c := range AllSignalClasses() {
		if c.String() == prefix {
			return c, true
		}
	}
	return 0, false
}

// ---- 信号集与签名词汇（engine 扫描面的传递形态单源）----

// Signals 是一次扫描对某 App 圈定的收敛信号集：键 = 信号类锚
// （六类独立签名键，ADR-0082 §4 与 §1 行 8/10——spec 失配/gen 偏离/缺席/
// 停摆/多余/重放各自去抖互不覆盖），值 = 该信号的签名字面。engine 装配
// （driftScan 圈定）与裁决（SignalInput 映射）共用本形态。
type Signals map[string]string

// Aggregate 把信号集聚合为 App 级签名字面（键排序拼接——快通道「签名变化
// 重触发」与「已收敛短路」的判定输入；键序稳定使聚合结果与遍历序无关）。
func (s Signals) Aggregate() string {
	if len(s) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+s[k])
	}
	return strings.Join(parts, "\x1f")
}

// 信号值字面构造器（行2/行8/行10 的信号值单源——值字面进聚合签名、事件
// payload 与审计 Detail，与 SignalClass.Key 的键字面同居域内单源；调用方
// 守非空集，空集不构造信号）。

// AbsentSignal 构造缺席信号值（行2：缺席载体 ID 逗号序）。
func AbsentSignal(missing []string) string {
	return "absent|" + strings.Join(missing, ",")
}

// ExtraSignal 构造多余信号值（行8：域内多余载体 ID 逗号序）。
func ExtraSignal(extra []string) string {
	return "extra|" + strings.Join(extra, ",")
}

// ReplaySignal 构造重放信号值（行10：值携带部署 ID → 第二次取消 = 新签名，
// 可再次触发）。
func ReplaySignal(deploymentID string) string {
	return "replay|" + deploymentID
}
