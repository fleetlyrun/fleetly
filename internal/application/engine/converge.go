package engine

// ADR-0082 缺省收敛（Converge by Default）：App 域收敛升格——检出漂移即
// 收敛（裁决表 8 判定行 + 2 非行），检测与事件面照发（ADR-0022 保留），
// Ensure 仍是唯一写动词（收敛动作的执行机构就是 Ensure/materialize→
// Ensure，升格的是调用者）。本文件承载：缺席/多余检测（LatestSucceeded
// 拍内重投影）、双通道触发（快通道六类信号 + 慢通道 ReconcileReplay-
// Interval 强制重放）、签名生命周期（绑定收敛结果）、冷却记账（N=3 →
// 10min + converge_failed 一次）、冻结闸门（fail-closed）、收敛动作
// （lockApp 双复查 + materialize isolate + 审计/事件按真实变更分流）、
// 裁决表判定函数与两个生产构造点（signalVerdictInput / gateVerdictInput）、
// 非成功终态的重放标记（replay 快信号）。
//
// 防自激三件套（缺一即回到「20s 49 次版本推进」或「永不收敛」两个失败
// 形态）：provider 侧 no-op 断路器 + 内容等价（materialize 同代重放下发
// 近零成本）+ 签名生命周期绑定收敛结果（成功才落、失败计冷却、挂起不落）。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
)

// 事件名锚定（eventcode 注册表同源；usage 反扫的字面量命中点）。
const (
	eventWorkloadConverged      = "workload.converged"
	eventWorkloadConvergeFailed = "workload.converge_failed"
	// systemConvergeRuleID / systemConvergeMetric 是收敛内置告警规则的面
	// （platform-offsite-backup 先例对齐：不落行、不可删、doctor 可见）。
	systemConvergeRuleID = "converge-failed"
	systemConvergeMetric = "converge_failed"
)

// convergeSource 标注一次收敛尝试的触发源（冷却门三源共用同一记账——
// 启动基线重放不受冷却豁免盲打；posture 门三源同受，无任何豁免：detect
// = 零运行时写，ADR-0082 §5）。
type convergeSource uint8

const (
	// convergeSourceFast 快通道：driftScan 四类信号触发（冷却记账的主
	// 消费面；签名生命周期绑定收敛结果）。
	convergeSourceFast convergeSource = iota
	// convergeSourceSlow 慢通道：ReconcileReplayInterval 强制重放（与
	// ring 域同律的兜底——无信号也重放，provider 侧 no-op 断路器 +
	// 内容等价使其近零成本；Cancel 遗留回卷/域内多余载体移除经此兑现）。
	convergeSourceSlow
	// convergeSourceBaseline 启动基线重放：rebuildBaselines 的单 App 面。
	// 与快/慢通道共用闸门序与冷却记账；posture=detect 下**不进入本源**
	// （启动面只重建期望账本，见 rebuildBaselineExpectations），其余场景
	// 与另两源同受 posture 门限（ADR-0082 §5）。
	convergeSourceBaseline
)

// String 返回触发源字面（审计/日志/Detail 的 "source" 面）。
func (src convergeSource) String() string {
	switch src {
	case convergeSourceFast:
		return "fast"
	case convergeSourceSlow:
		return "slow"
	case convergeSourceBaseline:
		return "baseline"
	}
	return "unknown"
}

// convergeSignals 是一次扫描对某 App 圈定的收敛信号集：键 = 信号类锚
// （六类独立签名键，ADR-0082 §4 与 §1 行 8/10——spec 失配/gen 偏离/缺席/
// 停摆/多余/重放各自去抖互不覆盖），值 = 该信号的签名字面。
type convergeSignals map[string]string

// aggregate 把信号集聚合为 App 级签名字面（键排序拼接——快通道「签名变化
// 重触发」与「已收敛短路」的判定输入；键序稳定使聚合结果与遍历序无关）。
func (s convergeSignals) aggregate() string {
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

// convergeTrigger 是一次收敛尝试的触发面（R02-M1）：有签名 = 快/慢通道
// 圈定了未收敛信号（sig = 聚合签名）；无签名 = 启动基线重放 / 隔离重放等
// 例行重放。冷却解除径②（"漂移签名变化"）只在有签名触发上比对——无签名
// 调用既不匹配也不得解除冷却。
type convergeTrigger struct {
	sig string
}

// signaledConvergeTrigger 构造携带签名的触发（快/慢通道的信号驱动面）。
func signaledConvergeTrigger(sig string) convergeTrigger { return convergeTrigger{sig: sig} }

// isSignaled 报告触发是否携带签名（空聚合 = 无未收敛信号 = 无签名）。
func (t convergeTrigger) isSignaled() bool { return t.sig != "" }

// convergeFailureSignature 是失败签名的三态记账（R02-M1）：零值 = 最近一次
// 失败无签名（或账本缺席）；present = 最近一次**有签名**失败的聚合签名在册。
// 比较只在 changedBy 一处做（解除径②的单一判定点）。
type convergeFailureSignature struct {
	sig     string
	present bool
}

// changedBy 报告一次触发是否构成「漂移签名变化」解除径：无签名触发恒
// false（不得解除冷却）；有签名触发在账本无签名或签名不同时为 true。
func (f convergeFailureSignature) changedBy(t convergeTrigger) bool {
	if !t.isSignaled() {
		return false
	}
	return !f.present || f.sig != t.sig
}

// convergeSignalClass 是收敛信号类的单一枚举（键构造与裁决映射的唯一
// 构造点）：每类一个前缀 + 一个裁决表字段，新增类不在两处同时登记即红
// （TestConvergeVerdictSignalClassTotality）。
type convergeSignalClass uint8

const (
	convergeSignalSpec    convergeSignalClass = iota // 行1：spec 失配
	convergeSignalAbsent                             // 行2：载体缺席
	convergeSignalGen                                // 行3：稳态 gen 偏离
	convergeSignalRollout                            // 行4：终态后滚动停摆
	convergeSignalExtra                              // 行8：域内多余载体
	convergeSignalReplay                             // 行10：Cancel/失败终态遗留
)

// allConvergeSignalClasses 返回全部信号类（完备性守卫的遍历面）。
func allConvergeSignalClasses() []convergeSignalClass {
	return []convergeSignalClass{
		convergeSignalSpec, convergeSignalAbsent, convergeSignalGen,
		convergeSignalRollout, convergeSignalExtra, convergeSignalReplay,
	}
}

// key 构造该类在 convergeSignals 里的键（suffix 为空 = 无逐载体维度）。
func (c convergeSignalClass) key(suffix string) string {
	if suffix == "" {
		return c.String()
	}
	return c.String() + "|" + suffix
}

// String 返回信号类字面（测试/日志；键前缀即本字面）。
func (c convergeSignalClass) String() string {
	switch c {
	case convergeSignalSpec:
		return "spec"
	case convergeSignalAbsent:
		return "absent"
	case convergeSignalGen:
		return "gen"
	case convergeSignalRollout:
		return "rollout"
	case convergeSignalExtra:
		return "extra"
	case convergeSignalReplay:
		return "replay"
	}
	return "unknown"
}

// convergeSignalClassOf 解析信号键的信号类（键前缀即类名）。未知键 =
// 无类：调用方按"非收敛行"处理，不得为未知键伪造裁决输入。
func convergeSignalClassOf(key string) (convergeSignalClass, bool) {
	prefix, _, _ := strings.Cut(key, "|")
	for _, c := range allConvergeSignalClasses() {
		if c.String() == prefix {
			return c, true
		}
	}
	return 0, false
}

// converges 是收敛域实例态（内存态——崩溃循环由 boundedStep + 慢通道节拍
// 等效兜底，ADR-0082 §4；多 Engine 实例互不污染，C5 同款取向）。
type converges struct {
	mu sync.Mutex
	// memo 是每 App「上次成功物化」备忘（慢通道签名 = 基线 Revision + 行
	// Generation；ensureMemo 协议复用——签名未变且未到 ReconcileReplay-
	// Interval 即跳过本拍重放）。
	memo map[string]ensureMemo
	// resolved 是每 App 已收敛的聚合信号签名（快通道签名生命周期：收敛
	// 成功才落；失败/挂起不落——「同失配一次收敛」不得消耗唯一触发机会，
	// 否则冷却永远到不了 N，ADR-0082 §4 死锁形态）。
	resolved map[string]string
	// fails 是连续收敛失败计数（冷却阈值 N 的记账面）。
	fails map[string]int
	// failSig 是最近一次**携带签名**的失败触发快照（三态：无签名/有签名同/
	// 有签名异——R02-M1）。无签名失败不写本账（见 convergeFail）。
	failSig map[string]convergeFailureSignature
	// posture 是进入冷却时的 posture 快照（切换清零判定）。
	posture map[string]string
	// cooldown 是冷却窗起点（键缺席 = 未冷却；窗满自动解除）。
	cooldown map[string]time.Time
	// eventSeen 钉「本轮冷却已发 workload.converge_failed」（一次语义）。
	eventSeen map[string]bool
	// projMemo 是基线投影的结果缓存（digest 签名短路：Revision 冻结体
	// 不可变，(Revision, Generation) 即内容锚——缺席/多余检测的每拍重投影
	// 在未变基线上近零成本，ADR-0082 §1）。投影结果即内容，ID 是派生面。
	projMemo map[string][]capability.Workload
	// replay 是非成功终态的待重放标记（appID → 触发重放的 Deployment ID）：
	// 取消/失败/被抢占的部署从未成为期望态，下一拍以 replay 快信号回卷
	// 基线（ADR-0082 §1 行 10 / §6）；值携带部署 ID → 第二次取消 = 新签名，
	// 可再次触发。写点 = transition.transit 单前门，清点 = 成功收敛/
	// 删除收口/成功终态迁移。
	replay map[string]string
	// teardown 是删除收口完成标记（teardown 竞态闭合的机制载体——expect
	// 缓存无法区分「重启冷窗」与「删除收口后」，标记在 appMu 内与
	// forgetOwner 同临界区写入，重建权交还部署驱动器：teardown-abort 的
	// 唯一重建通道，ADR-0023）。
	teardown map[string]bool
	// 内置告警状态对（platform-offsite-backup 同款：扫描环写 / gRPC handler
	// 读跨 goroutine，读写都进 mu 临界区）。
	alertFiring bool
	alertSince  time.Time
}

// init 建域内缓存（Engine.New 装配期一次）。
func (c *converges) init() {
	c.memo = make(map[string]ensureMemo)
	c.resolved = make(map[string]string)
	c.fails = make(map[string]int)
	c.failSig = make(map[string]convergeFailureSignature)
	c.posture = make(map[string]string)
	c.cooldown = make(map[string]time.Time)
	c.eventSeen = make(map[string]bool)
	c.projMemo = make(map[string][]capability.Workload)
	c.replay = make(map[string]string)
	c.teardown = make(map[string]bool)
}

// convergeMemoSig 是慢通道的每 App 物化签名：基线 Revision（冻结体不可变
// → 行锚即内容锚，releaseEnsureSignature 同款纪律）+ 行 Generation。
func convergeMemoSig(d *deployment.Deployment) string {
	if d == nil {
		return ""
	}
	return d.ToRevision + "\x00" + fmt.Sprint(d.Generation)
}

// ---- 裁决表（ADR-0082 §1：8 判定行 + 2 非行；golden 钉死 = 表即断言）----

// convergeVerdictInput 是裁决表一行的输入面（每行一个判定分支 + golden
// 快照钉死；行号对齐 ADR-0082 §1 表序）。行 5（spec 相符 + stopped）是
// **信号层非行**——稳态看门狗只发事件、不产收敛信号，本结构无此字段；
// 行 9（域外孤儿）**明确未落码**——观测面只有域内枚举、无 WorkloadOrphaned
// 事件码，本结构同样无此字段（ADR-0082 修订版 §1）。
type convergeVerdictInput struct {
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
// 登记、或表项无对应字段即红（TestConvergeVerdictFieldProducersComplete
// 双向保鲜）；信号侧六类与映射的完备性由 TestConvergeVerdictSignalClass
// Totality 执法。
var verdictFieldProducers = map[string]string{
	"Frozen":         "gateVerdictInput ← convergeFrozen",
	"Driving":        "gateVerdictInput ← Deployments.DrivingByApp",
	"SpecMismatch":   "signalVerdictInput ← convergeSignalSpec",
	"Absent":         "signalVerdictInput ← convergeSignalAbsent",
	"GenDeviated":    "signalVerdictInput ← convergeSignalGen",
	"RolloutStalled": "signalVerdictInput ← convergeSignalRollout",
	"ExtraCarriers":  "signalVerdictInput ← convergeSignalExtra",
	"CancelLegacy":   "signalVerdictInput ← convergeSignalReplay",
}

// 裁决值（golden 字面）。
const (
	verdictSteady         = "steady"          // 无漂移：稳态（慢通道 memo 节拍照常，非裁决行）
	verdictConverge       = "converge"        // 收敛动作（§2 统一原语）
	verdictSkip           = "skip"            // 跳过本拍（驱动权在部署链）
	verdictSuspend        = "suspend"         // 挂起（不消费签名，解冻重扫）
	verdictReplayBaseline = "replay-baseline" // 回卷 LatestSucceeded 基线
)

// convergeVerdict 是收敛裁决表的判定函数（优先序：闸门行 > 信号行；Cancel
// 遗留在信号行之后——同拍多信号时以显式信号裁决优先）。两个生产构造点：
// signalVerdictInput（信号侧）与 gateVerdictInput（闸门侧）；convergeStep
// 与 attemptConverge 都真实调用它。posture=detect 不是行裁决：它是全局
// 闸门（收敛动作前置检查，零运行时写——fast/slow/启动基线重放/peer 撤销
// 隔离四源同受门限，ADR-0082 §5），检测与事件面照发。
func convergeVerdict(in convergeVerdictInput) string {
	switch {
	case in.Frozen:
		return verdictSuspend // 行7：不消费签名、不发收敛；解冻后下一拍重扫即收敛
	case in.Driving:
		return verdictSkip // 行6：复查口径与驱动权同源（ListDriving 语义含 failed 待回滚窗）
	case in.SpecMismatch:
		return verdictConverge // 行1：driftScan 判定 → 收敛动作
	case in.Absent:
		return verdictConverge // 行2：期望集 = LatestSucceeded 重投影（决策输入是 Revision 真源）
	case in.GenDeviated:
		return verdictConverge // 行3：检测下沉 driftScan，独立签名键 + 收敛信号
	case in.RolloutStalled:
		return verdictConverge // 行4：重放途经 resumeStalledRollout；部署窗内归行6 L1 语义
	case in.CancelLegacy:
		return verdictReplayBaseline // 行10：取消的部署从未成为期望态（翻案 admission N0 本场景）
	case in.ExtraCarriers:
		return verdictConverge // 行8：Ensure 域内移除既有语义（顺带）
	default:
		return verdictSteady
	}
}

// signalVerdictInput 把一次扫描的信号集映射为裁决表输入——信号键→行的
// 唯一映射点（ADR-0082 §1 的"表即断言"在信号侧的执行面）。未知信号键
// 不置位任何字段（不得为未知键伪造裁决输入）。
func signalVerdictInput(signals convergeSignals) convergeVerdictInput {
	var in convergeVerdictInput
	for key := range signals {
		class, ok := convergeSignalClassOf(key)
		if !ok {
			continue // 未知信号类：无行可映射（不伪造输入）
		}
		switch class {
		case convergeSignalSpec:
			in.SpecMismatch = true
		case convergeSignalAbsent:
			in.Absent = true
		case convergeSignalGen:
			in.GenDeviated = true
		case convergeSignalRollout:
			in.RolloutStalled = true
		case convergeSignalExtra:
			in.ExtraCarriers = true
		case convergeSignalReplay:
			in.CancelLegacy = true
		}
	}
	return in
}

// gateVerdictInput 把闸门事实映射为裁决表输入——冻结/驱动权两行（ADR-0082
// §1 行6/行7）的唯一映射点。
func gateVerdictInput(frozen, driving bool) convergeVerdictInput {
	return convergeVerdictInput{Frozen: frozen, Driving: driving}
}

// convergeActionable 报告裁决值是否应发起收敛动作（converge |
// replay-baseline；两者动作同为「基线物化」，差别只在触发来源与事实面标签）。
func convergeActionable(verdict string) bool {
	return verdict == verdictConverge || verdict == verdictReplayBaseline
}

// ---- 缺席/多余检测（期望集 = LatestSucceeded 拍内重投影）----

// expectedWorkloads 返回基线 Revision 的投影载体集（含代戳；缺席/多余/
// detect 账本三面共用同一投影与同一 memo）。与部署 RenderWorkloads/
// materialize 同源走 projectRevision + 代标记 stamp——决策输入是 Revision
// 真源而非期望缓存，ADR-0082 §1；缓存幽灵/BG 旧代/进程缩减三类假阳性
// 根源性消除。(Revision, Generation) 签名短路：投影结果按内容锚缓存，
// 未变基线近零成本。
func (e *Engine) expectedWorkloads(ctx context.Context, d *deployment.Deployment) ([]capability.Workload, error) {
	key := convergeMemoSig(d)
	e.converge.mu.Lock()
	if ws, ok := e.converge.projMemo[key]; ok {
		e.converge.mu.Unlock()
		return ws, nil
	}
	e.converge.mu.Unlock()

	spec, ws, _, err := e.projectRevision(ctx, d.AppID, d.ToRevision, true)
	if err != nil {
		return nil, err
	}
	// 代标记 stamp（与 materialize 同源单源）：blue-green 进程的代次化 ID
	// 在此与部署物化逐字节一致——缺席/多余对照的身份面不可能分叉。
	stampDeploymentGenerations(ws, spec, d.Generation)
	sort.Slice(ws, func(i, j int) bool { return ws[i].ID < ws[j].ID })
	e.converge.mu.Lock()
	e.converge.projMemo[key] = ws
	e.converge.mu.Unlock()
	return ws, nil
}

// expectedCarrierIDs 保留为投影的派生面（缺席检测的对外形态；ID 由
// expectedWorkloads 排序后派生）。
func (e *Engine) expectedCarrierIDs(ctx context.Context, d *deployment.Deployment) ([]string, error) {
	ws, err := e.expectedWorkloads(ctx, d)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ws))
	for _, w := range ws {
		ids = append(ids, w.ID)
	}
	return ids, nil
}

// missingIDs 计算缺席面：期望集 − 观测集（字典序；空 = 无缺席）。与
// extraIDs 是同一对照的两个方向（行2/行8 同源同序，便于对照测试）。
func missingIDs(ws []capability.Workload, obs []capability.WorkloadObservation) []string {
	seen := make(map[string]bool, len(obs))
	for _, o := range obs {
		seen[o.WorkloadID] = true
	}
	var missing []string
	for _, w := range ws {
		if !seen[w.ID] {
			missing = append(missing, w.ID)
		}
	}
	return missing
}

// extraIDs 计算多余面：观测集 − 期望集（字典序；空 = 无多余）。数据源是
// InspectWorkloads 的**域内**枚举（swarm/k3s 两侧都按 fleetly.managed 标签
// + 域选择器过滤）——因此它是「域内多余载体」（行 8）而非「域外孤儿」
// （行 9，明确未落码）。部署窗内在途新代载体由裁决行 6 Driving 让位。
func extraIDs(ws []capability.Workload, obs []capability.WorkloadObservation) []string {
	expected := make(map[string]bool, len(ws))
	for _, w := range ws {
		expected[w.ID] = true
	}
	var extra []string
	for _, o := range obs {
		if !expected[o.WorkloadID] {
			extra = append(extra, o.WorkloadID)
		}
	}
	sort.Strings(extra)
	return extra
}

// ---- 双通道触发与收敛动作 ----

// convergeOutcome 是一次收敛尝试的结局（闸门/动作面的单一回报形态）：
// 让调用方能区分"没做"的原因（冷却/姿态/冻结/驱动权/收口/基线已变/失败），
// 而不是靠 Ensure 调用计数猜。
type convergeOutcome uint8

const (
	convergeOutcomeConverged convergeOutcome = iota
	convergeOutcomeCooling
	convergeOutcomePostureDetect
	convergeOutcomeSuspended // 冻结
	convergeOutcomeDriving   // 驱动权在部署链
	convergeOutcomeTeardown  // 删除收口标记在位
	convergeOutcomeMoved     // 基线已更替（本拍放弃）
	convergeOutcomeFailed
)

// convergeStep 是单 App 的双通道判定（driftScan 每拍消费）：快通道 = 有未
// 收敛信号；慢通道 = 物化备忘到期（ReconcileReplayInterval 强制重放——无
// 信号也重放，环外变更的兜底，ring 域同律）。两通道经同一 attemptConverge
// 过闸；快通道先经裁决表——信号不构成收敛行即不发起动作（表覆盖不全的
// 组合走此分支，不得凭信号存在就收敛）。
func (e *Engine) convergeStep(ctx context.Context, appID string, d *deployment.Deployment, signals convergeSignals) {
	agg := signals.aggregate()
	// 空信号集 = 无未收敛信号：清「已收敛签名」（R02-L1）——同一形态复发
	// 必须以快通道重触发，不得靠慢通道 ≤90s 有界兜底。
	if len(signals) == 0 {
		e.converge.mu.Lock()
		delete(e.converge.resolved, appID)
		e.converge.mu.Unlock()
	}
	if d == nil {
		return // 无成功基线：无可收敛锚（spec 对照照旧，缺席面跳过）
	}
	e.converge.mu.Lock()
	unchanged := e.converge.resolved[appID] == agg
	e.converge.mu.Unlock()
	fast := len(signals) > 0 && !unchanged
	_, fresh := e.ensureFresh(e.converge.memo, appID, convergeMemoSig(d), e.clock.Now())
	if fast {
		if !convergeActionable(convergeVerdict(signalVerdictInput(signals))) {
			return // 信号不构成收敛行（当前六类皆可行动；未知信号类不得发起动作）
		}
		e.attemptConverge(ctx, appID, d, signaledConvergeTrigger(agg), convergeSourceFast)
		return
	}
	if fresh {
		return // 稳态：无未收敛信号 + 物化备忘在期
	}
	// 慢通道也可能携带信号（"已收敛签名"未变的残留信号：fast 让位后由慢
	// 通道重放）；agg 为空即无签名触发（例行重放）。
	e.attemptConverge(ctx, appID, d, convergeTrigger{sig: agg}, convergeSourceSlow)
}

// attemptConverge 是收敛动作的统一原语（ADR-0082 §2）：闸门序（冷却 →
// posture → 冻结 fail-closed）→ lockApp 双复查（①驱动权语义 per-App 查询；
// ②teardown 标记——expect 缓存成员资格复查的机制载体）→ 一次裁决
// （gateVerdictInput：冻结/驱动权两行同一映射点）→ materialize
// （isolate=true，boundedStep 带界）→ 记账（成功落签名/备忘，失败计冷却）。
// 不建 Deployment 行——收敛高频，部署行会成行垃圾（替代案已驳回）；审计
// 走 SourceSystem 行而非部署 lineage。
func (e *Engine) attemptConverge(ctx context.Context, appID string, d *deployment.Deployment, trigger convergeTrigger, src convergeSource) convergeOutcome {
	// 冷却门先于 posture 门：posture 切换（all→detect→all 往返）在冷却
	// 判定点可见即清零记账（解除三径之一；跨重启的切换由记账内存态自然
	// 清零——两径对齐 ADR-0082 §4）。窗内尝试跳过。
	if !e.convergeCooldownOpen(ctx, appID, trigger) {
		return convergeOutcomeCooling
	}
	// posture 门（ADR-0082 §5）：detect = 零运行时写——门限对 fast / slow /
	// 启动基线重放 / peer 撤销隔离一视同仁（原「baseline 豁免」条款废止：
	// 它让观察期在启动与撤销 peer 两个场景静默改载体；启动面的期望账本
	// 重建不走本原语，见 rebuildBaselineExpectations）。检测与事件面照发。
	if e.opts.Converge.Posture == ConvergePostureDetect {
		return convergeOutcomePostureDetect
	}
	// 冻结闸门（ADR-0082 §3 fail-closed）：挂起 = 不消费签名、不记账——
	// 解冻后下一拍重扫即自然收敛。取事实在锁外（现行序），裁决与驱动权
	// 事实合并到锁内一次（gateVerdictInput）。
	frozen, err := e.convergeFrozen(ctx, appID)
	if err != nil {
		// fail-closed：Team 解析失败/存储故障跳过本拍下拍重试——不走
		// transition.go ownerTeam 的降级 '' 契约（那对只读事件面合理，
		// 搬进执法面即 fail-open）。
		e.log.Error("converge: freeze gate", "app", appID, "err", err)
		return convergeOutcomeFailed
	}

	// lockApp 内双复查（共享互斥即闭合「重建刚拆载体」竞态——teardown 的
	// forgetOwner 与 Submit 的受理同在 appMu 内）。
	appMu := e.lockApp(appID)
	appMu.Lock()
	defer appMu.Unlock()

	// 复查②（expect 缓存成员资格的机制载体）：删除收口已完成 = 重建权归
	// 部署驱动器（teardown-abort 的唯一重建通道，ADR-0023）；expect 缓存
	// 本身无法区分「重启冷窗」与「删除收口后」，标记在 forgetOwner 同锁
	// 内写入才是闭合面。
	if e.converge.teardownMarked(appID) {
		return convergeOutcomeTeardown
	}
	// 复查①（驱动权语义 per-App 查询，ListDriving 同口径——活跃态含
	// failed 待回滚窗；ActiveByApp 不含 failed，单独使用会开「收敛×回滚
	// 交错」竞态窗 → vacuously ready → gen 错位 → BG 旧代锚漂移）。
	driving, err := e.repos.Deployments.DrivingByApp(ctx, appID)
	if err != nil {
		e.log.Error("converge: recheck driving", "app", appID, "err", err)
		return convergeOutcomeFailed
	}
	// 一次性裁决（冻结/驱动权两行的唯一映射点）：两行都只是 return，
	// 不消费签名、不记账。
	switch convergeVerdict(gateVerdictInput(frozen, len(driving) > 0)) {
	case verdictSuspend:
		return convergeOutcomeSuspended
	case verdictSkip:
		return convergeOutcomeDriving
	}
	// 基线刷新：扫描到锁之间 succeeded 行可能已更替——锚已变则本拍放弃
	//（下拍以新签名重判；拿着旧基线物化会把新代翻回去）。
	fresh, err := e.repos.Deployments.LatestSucceeded(ctx, appID)
	if err != nil {
		e.log.Error("converge: recheck baseline", "app", appID, "err", err)
		return convergeOutcomeFailed
	}
	if fresh.ToRevision != d.ToRevision || fresh.Generation != d.Generation {
		return convergeOutcomeMoved
	}
	report, err := e.materialize(ctx, fresh, fresh.ToRevision, fresh.Generation, true)
	if err != nil {
		e.log.Error("converge: materialize", "app", appID, "generation", fresh.Generation, "err", err)
		e.convergeFail(ctx, appID, trigger, err)
		return convergeOutcomeFailed
	}
	e.convergeSucceed(ctx, appID, convergeMemoSig(fresh), trigger, fresh, src, report)
	return convergeOutcomeConverged
}

// convergeCooldownOpen 报告该 App 是否可发起收敛尝试（锁外判定；窗内返回
// false）。解除三径（ADR-0082 §4）在窗内逐拍判定：超窗自动 / 漂移签名
// 变化重触发（仅限携带签名的触发，R02-M1）/ posture 切换清零。返回 true
// 时若发生了释放且内置告警在 firing，就地落 resolved 沿（事件 + 通道派发，
// 锁外 I/O）。
func (e *Engine) convergeCooldownOpen(ctx context.Context, appID string, trigger convergeTrigger) bool {
	now := e.clock.Now()
	e.converge.mu.Lock()
	until, cooling := e.converge.cooldown[appID]
	if !cooling {
		e.converge.mu.Unlock()
		return true
	}
	release := now.Sub(until) >= e.opts.Converge.Cooldown ||
		e.converge.failSig[appID].changedBy(trigger) ||
		e.converge.posture[appID] != e.opts.Converge.Posture
	if !release {
		e.converge.mu.Unlock()
		return false
	}
	e.convergeReleaseLocked(appID)
	last := len(e.converge.cooldown) == 0 && e.converge.alertFiring
	if last {
		e.converge.alertFiring = false
	}
	e.converge.mu.Unlock()
	if last {
		e.convergeAlertEdge(ctx, alertrule.StateOK, now)
	}
	return true
}

// convergeReleaseLocked 清单 App 的冷却/失败记账（调用方持 mu）。
func (e *Engine) convergeReleaseLocked(appID string) {
	delete(e.converge.cooldown, appID)
	delete(e.converge.fails, appID)
	delete(e.converge.failSig, appID)
	delete(e.converge.posture, appID)
	delete(e.converge.eventSeen, appID)
}

// convergeSucceed 落收敛成功的全套记账：物化备忘（慢通道签名）+ 已收敛
// 聚合签名（快通道生命周期）+ 冷却清零 + 撤销重放标记 + 事实面。
//
// 事实面按真实变更分流（ADR-0082 §2）：
//   - 审计行（workload.converge，SourceSystem，AfterFP = 基线 digest，
//     Detail 携带 {source, trigger, changed, removed}）：只要本次收敛真的
//     写穿了内容等价判定（EnsureReport.Changed/Removed 非空）**或是信号
//     驱动（fast 源）**就发——"系统改了你的运行时必留痕"；
//   - 事件（workload.converged）：维持 fast 源才发——慢通道/启动重放是
//     例行节拍，进事件流即行垃圾；它们的真实变更由审计行承载。
func (e *Engine) convergeSucceed(ctx context.Context, appID, sig string, trigger convergeTrigger, d *deployment.Deployment, src convergeSource, rep capability.EnsureReport) {
	now := e.clock.Now()
	e.ensureRemember(e.converge.memo, appID, ensureMemo{sig: sig, gen: d.Generation, at: now})
	e.converge.mu.Lock()
	if trigger.isSignaled() {
		e.converge.resolved[appID] = trigger.sig
	} else {
		// 无签名收敛（基线/慢通道例行重放）：无未收敛信号 = 已收敛签名的
		// 生命周期终点（R02-L1——同形态复发必须在快通道重触发）。
		delete(e.converge.resolved, appID)
	}
	e.convergeReleaseLocked(appID)
	resolved := len(e.converge.cooldown) == 0 && e.converge.alertFiring
	if resolved {
		e.converge.alertFiring = false
	}
	e.converge.mu.Unlock()
	// 成功收敛 = 该行已重锚账本（materialize 写入的条目归属它）：撤销非
	// 成功终态的重放标记（ADR-0082 §6）。
	e.converge.clearReplay(appID)
	if resolved {
		e.convergeAlertEdge(ctx, alertrule.StateOK, now)
	}
	if src != convergeSourceFast && !rep.HasMaterialChange() {
		return // 无真实变更的例行重放：零事实（审计不发，事件本就不发）
	}
	// 事实面双真（kernel 默认）：workload.converged 流面 + 审计行
	// source=system（AfterFP = 基线 digest——「换成了什么」的取证锚；
	// Detail 携带变更集——「改了什么」的取证锚）。收敛是系统自治动作，
	// 有审计动词可查证（ADR-0066 写序列 kernel）。
	rev, err := e.repos.Revisions.Get(ctx, d.ToRevision)
	digest := ""
	if err == nil {
		digest = rev.Digest
	}
	team := e.eventTeamOfApp(ctx, e.repos, appID)
	fact := writes.FactRow{
		Name: eventWorkloadConverged, Aggregate: "app", ID: appID,
		Payload: convergeEventPayloadJSON(appID, d.ToRevision, d.Generation, trigger.sig, ""),
		Action:  "workload.converge", Resource: "app/" + appID,
		AfterFP: digest, Source: audit.SourceSystem,
		TeamID: team,
		Detail: convergeAuditDetailJSON(src, trigger.sig, rep),
	}
	if src != convergeSourceFast {
		// LedgerOnly 偏离理由（ADR-0082 §2 第 2 条）：慢通道/启动重放的
		// **真实变更**只落取证面——事件面是操作者时间线，例行节拍（60s
		// 强制重放）进流即行垃圾；R02-M5 的 Cancel 可发现性由行 10 的
		// replay 快信号承担（事件 + 审计同时在场）。
		fact.LedgerOnly = true
		fact.Name, fact.Payload = "", nil
	}
	if err := writes.Emit(ctx, writes.Repos{Outbox: e.repos.Outbox, Audits: e.repos.Audits}, fact); err != nil {
		e.log.Error("converge: emit fact", "app", appID, "err", err)
	}
}

// convergeAuditDetail 是收敛审计的 Detail 面（ADR-0049）：{source, trigger,
// changed, removed}——"改了什么"的取证锚（AfterFP 只给基线 digest，不给
// 变更集）。字段面受控：只放四个短字段，不放开指针/时间。
type convergeAuditDetail struct {
	Source  string   `json:"source"`
	Trigger string   `json:"trigger,omitempty"`
	Changed []string `json:"changed"`
	Removed []string `json:"removed"`
}

// convergeAuditDetailJSON 构造收敛审计的 Detail（空集归一为 []——审计查询
// 面不出现 null）。
func convergeAuditDetailJSON(src convergeSource, agg string, rep capability.EnsureReport) string {
	return auditDetailJSON(convergeAuditDetail{
		Source:  src.String(),
		Trigger: agg,
		Changed: append([]string{}, rep.Changed...),
		Removed: append([]string{}, rep.Removed...),
	})
}

// convergeFail 记一次收敛失败：不落签名（resolved 不动——下拍重扫即自然
// 重试），计冷却计数；连续失败达阈值 N 进入冷却窗 + workload.converge_failed
// 一次 + 内置告警 firing 沿（ADR-0082 §4）。
//
// 签名账三态（R02-M1）：只有携带签名的失败才写 failSig；无签名失败（基线/
// 隔离重放）只计数、不抹签名账——否则一次无签名重放失败会把 failSig 清空，
// 让下一次同签名失败被误判成「签名变化」而解除冷却。
func (e *Engine) convergeFail(ctx context.Context, appID string, trigger convergeTrigger, cause error) {
	now := e.clock.Now()
	e.converge.mu.Lock()
	// 签名变化重触发：与上次**有签名**失败不同的聚合签名 = 新触发（计数重开）。
	if trigger.isSignaled() {
		if prev := e.converge.failSig[appID]; prev.present && prev.sig != trigger.sig {
			e.converge.fails[appID] = 0
		}
		e.converge.failSig[appID] = convergeFailureSignature{sig: trigger.sig, present: true}
	}
	e.converge.fails[appID]++
	n := e.converge.fails[appID]
	enter := n >= e.opts.Converge.FailureThreshold
	_, alreadyCooling := e.converge.cooldown[appID]
	emit := false
	if enter && !alreadyCooling {
		e.converge.cooldown[appID] = now
		e.converge.posture[appID] = e.opts.Converge.Posture
		emit = !e.converge.eventSeen[appID]
		e.converge.eventSeen[appID] = true
		if !e.converge.alertFiring {
			e.converge.alertFiring = true
			e.converge.alertSince = now
		}
	}
	e.converge.mu.Unlock()
	if !emit {
		return
	}
	team := e.eventTeamOfApp(ctx, e.repos, appID)
	if err := e.emitObservation(ctx, team, eventWorkloadConvergeFailed, "app", appID,
		convergeEventPayloadJSON(appID, "", 0, trigger.sig, cause.Error())); err != nil {
		e.log.Error("converge: emit failure event", "app", appID, "err", err)
	}
	e.convergeAlertEdge(ctx, alertrule.StateFiring, now)
}

// convergeAlertEdge 落内置告警迁移沿（alert.fired/alert.resolved + 通道
// 派发；platform-offsite-backup 先例对齐——沿才通知，不逐拍轰炸）。
func (e *Engine) convergeAlertEdge(ctx context.Context, to string, now time.Time) {
	payload, _ := json.Marshal(alertEventPayload{
		RuleID: systemConvergeRuleID, Metric: systemConvergeMetric, State: to,
	})
	name := eventAlertFired
	if to == alertrule.StateOK {
		name = eventAlertResolved
	}
	if err := e.emitObservation(ctx, "", name, "platform", systemConvergeRuleID, payload); err != nil {
		e.log.Error("converge: emit system alert", "err", err)
	}
	e.dispatchNotification(ctx, alertNotification{
		Type: "alert", RuleID: systemConvergeRuleID, Metric: systemConvergeMetric,
		State: to, At: state.FormatTime(now),
	})
}

// convergeFrozen 是冻结闸门判定（ADR-0082 §3）：owner 的 Team 解析 +
// ActiveForTeam（” 全局行随谓词命中——平台域走全局 ” 判定）。判定
// fail-closed：解析失败/存储故障上抛，调用方跳过本拍下拍重试。
func (e *Engine) convergeFrozen(ctx context.Context, appID string) (bool, error) {
	team, _, err := e.appTeam(ctx, appID)
	if err != nil {
		return false, err
	}
	rows, err := e.repos.Freezes.ActiveForTeam(ctx, team)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// ---- teardown 竞态闭合面 ----

// markTeardown 记删除收口完成（TeardownApp 在 appMu 内、forgetOwner 之后
// 调用——与收敛复查同锁即闭合「重建刚拆载体」竞态）。
func (c *converges) markTeardown(appID string) {
	c.mu.Lock()
	c.teardown[appID] = true
	c.mu.Unlock()
}

// clearTeardown 交还重建权（Submit 受理成功 = 操作者显式让部署驱动器重建
// ——teardown-abort 语义的唯一重建通道，ADR-0023；appMu 内调用）。
func (c *converges) clearTeardown(appID string) {
	c.mu.Lock()
	delete(c.teardown, appID)
	c.mu.Unlock()
}

// teardownMarked 报告删除收口标记在位（attemptConverge 锁内复查②）。
func (c *converges) teardownMarked(appID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.teardown[appID]
}

// ---- 非成功终态重放标记（ADR-0082 §1 行 10 / §6）----

// stateMayHaveMaterialized 报告该部署行在终态前是否已进入物化相位
// （releasing/observing/rolling-back = materialize 的三个调用位点）：非
// 成功终态只有在这三个相位才可能把"未成为期望态"的内容写进账本，故重放
// 标记以此判据收窄（queued/preparing/building 取消 = 零下发，无脏账本可
// 回卷）。后续新增物化相位须同改本判据（否则漏标 → 回到慢通道兜底）。
func stateMayHaveMaterialized(s deployment.State) bool {
	switch s {
	case deployment.StateReleasing, deployment.StateObserving, deployment.StateRollingBack:
		return true
	}
	return false
}

// markReplay 落重放标记（非成功终态且该行已可能物化）：值携带部署 ID——
// 第二次取消 = 新签名，可再次触发（不造空签名源，R02-M1 同族的边界面）。
func (c *converges) markReplay(appID, deploymentID string) {
	c.mu.Lock()
	c.replay[appID] = deploymentID
	c.mu.Unlock()
}

// replayMarked 返回待重放的部署 ID（drift 扫描的 replay 信号源）。
func (c *converges) replayMarked(appID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.replay[appID]
	return id, ok
}

// clearReplay 撤销重放标记（成功收敛/删除收口/成功终态迁移）。
func (c *converges) clearReplay(appID string) {
	c.mu.Lock()
	delete(c.replay, appID)
	c.mu.Unlock()
}

// ---- 可发现性面（doctor / ListAlertStates）----

// SystemConvergeAlertState 是收敛内置规则的现行状态（ListAlertStates 消费
// ——任一 App 处于收敛冷却窗即 firing；platform-offsite-backup 同款形态）。
func (e *Engine) SystemConvergeAlertState() SystemAlertState {
	e.converge.mu.Lock()
	defer e.converge.mu.Unlock()
	st, since := alertrule.StateOK, time.Time{}
	if e.converge.alertFiring {
		st, since = alertrule.StateFiring, e.converge.alertSince
	}
	return SystemAlertState{RuleID: systemConvergeRuleID, State: st, StateSince: since, System: true}
}

// convergeEventPayloadJSON 构造 workload.converged / workload.converge_failed
// 载荷（converged 携带基线锚与触发签名；converge_failed 携带错误尾迹）。
func convergeEventPayloadJSON(appID, revision string, gen uint64, trigger, errMsg string) []byte {
	b, _ := json.Marshal(convergeEventPayload{
		AppID: appID, Revision: revision, Generation: gen, Trigger: trigger, Error: errMsg,
	})
	return b
}

// convergeEventPayload 是收敛事件载荷（字段只增）。
type convergeEventPayload struct {
	AppID      string `json:"app_id"`
	Revision   string `json:"revision,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	// Trigger 是触发收敛的聚合信号签名（慢通道重放不发本事件，此字段
	// 恒在场于信号驱动收敛）。
	Trigger string `json:"trigger,omitempty"`
	Error   string `json:"error,omitempty"`
}
