package engine

// ADR-0082 缺省收敛（Converge by Default）：App 域收敛升格——检出漂移即
// 收敛（裁决表十行），检测与事件面照发（ADR-0022 保留），Ensure 仍是唯一
// 写动词（收敛动作的执行机构就是 Ensure/materialize→Ensure，升格的是调用
// 者）。本文件承载：缺席检测（LatestSucceeded 拍内重投影）、双通道触发
// （快通道四类信号 + 慢通道 ReconcileReplayInterval 强制重放）、签名生命
// 周期（绑定收敛结果）、冷却记账（N=3 → 10min + converge_failed 一次）、
// 冻结闸门（fail-closed）、收敛动作（lockApp 双复查 + materialize isolate
// + 审计行 source=system + workload.converged）与裁决表判定函数（golden
// 钉死）。
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

// convergeSource 标注一次收敛尝试的触发源（闸门差异的载体：posture 门只
// 豁免基线源；冷却门三源共用同一记账——启动基线重放不受冷却豁免盲打）。
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
	// 与快/慢通道共用闸门序与冷却记账；posture 门不生效（ADR-0005 旧行为
	// 保留——期望缓存重建是检测面的前提，detect 也要能看见漂移）。
	convergeSourceBaseline
)

// convergeSignals 是一次扫描对某 App 圈定的收敛信号集：键 = 信号类锚
// （四类独立签名键，ADR-0082 §4——spec 失配/gen 偏离/缺席/停摆各自去抖
// 互不覆盖），值 = 该信号的签名字面。
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
	// failSig 是最近一次失败时的聚合签名（签名变化重触发判定）。
	failSig map[string]string
	// posture 是进入冷却时的 posture 快照（切换清零判定）。
	posture map[string]string
	// cooldown 是冷却窗起点（键缺席 = 未冷却；窗满自动解除）。
	cooldown map[string]time.Time
	// eventSeen 钉「本轮冷却已发 workload.converge_failed」（一次语义）。
	eventSeen map[string]bool
	// projMemo 是基线重投影的 ID 集缓存（digest 签名短路：Revision 冻结体
	// 不可变，(Revision, Generation) 即内容锚——缺席检测的每拍重投影在
	// 未变基线上近零成本，ADR-0082 §1）。
	projMemo map[string][]string
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
	c.failSig = make(map[string]string)
	c.posture = make(map[string]string)
	c.cooldown = make(map[string]time.Time)
	c.eventSeen = make(map[string]bool)
	c.projMemo = make(map[string][]string)
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

// ---- 裁决表（ADR-0082 §1 十行落码；golden 钉死 = 表即断言）----

// convergeVerdictInput 是裁决表一行的输入面（每行一个判定分支 + golden
// 快照钉死；行号对齐 ADR-0082 §1 表序）。
type convergeVerdictInput struct {
	Frozen         bool // 行7：冻结中
	Driving        bool // 行6：活跃部署中（含 failed 待自动回滚窗）
	SpecMismatch   bool // 行1：spec 失配（image/command/replicas）
	Absent         bool // 行2：载体缺席（期望集 − 观测集 ≠ ∅）
	GenDeviated    bool // 行3：稳态 gen 偏离
	RolloutStalled bool // 行4：终态后滚动停摆（spec+gen 均一致）
	ExtraCarriers  bool // 行8：域内多余载体
	SteadyStopped  bool // 行5：spec 相符 + stopped
	OrphanObserved bool // 行9：域外/无主孤儿观测
	CancelLegacy   bool // 行10：Cancel 遗留载体（终态行、部分下发）
}

// 裁决值（golden 字面）。
const (
	verdictSteady         = "steady"          // 无漂移：稳态（慢通道 memo 节拍照常，非裁决行）
	verdictConverge       = "converge"        // 收敛动作（§2 统一原语）
	verdictAlertOnly      = "alert-only"      // 只告警不收敛（ADR-0022 decision 3 实质保留）
	verdictSkip           = "skip"            // 跳过本拍（驱动权在部署链）
	verdictSuspend        = "suspend"         // 挂起（不消费签名，解冻重扫）
	verdictRecordOnly     = "record-only"     // 只登记永不自动删（人裁保留）
	verdictReplayBaseline = "replay-baseline" // 回卷 LatestSucceeded 基线
)

// convergeVerdict 是收敛裁决表的判定函数（优先序：闸门行 > 信号行 > 观测
// 行；Cancel 遗留在信号行之后——同拍多信号时以显式信号裁决优先，遗留面
// 由慢通道重放兜底兑现回卷）。posture=detect 不是行裁决：它是全局闸门
// （收敛动作前置检查），检测与事件面照发。
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
	case in.SteadyStopped:
		return verdictAlertOnly // 行5：Ensure 内容等价 skip = 无效动作；swarm 自负重启
	case in.OrphanObserved:
		return verdictRecordOnly // 行9：WorkloadOrphaned 只登记原则不动
	default:
		return verdictSteady
	}
}

// ---- 缺席检测（期望集 = LatestSucceeded 拍内重投影）----

// expectedCarrierIDs 重投影基线 Revision 的期望载体 ID 集（与部署
// RenderWorkloads/materialize 同源走 projectRevision + 代标记 stamp——
// 决策输入是 Revision 真源而非期望缓存，ADR-0082 §1；缓存幽灵/BG 旧代/
// 进程缩减三类假阳性根源性消除）。(Revision, Generation) 签名短路：投影
// 结果按内容锚缓存，未变基线近零成本。
func (e *Engine) expectedCarrierIDs(ctx context.Context, d *deployment.Deployment) ([]string, error) {
	key := convergeMemoSig(d)
	e.converge.mu.Lock()
	if ids, ok := e.converge.projMemo[key]; ok {
		e.converge.mu.Unlock()
		return ids, nil
	}
	e.converge.mu.Unlock()

	spec, ws, _, err := e.projectRevision(ctx, d.AppID, d.ToRevision, true)
	if err != nil {
		return nil, err
	}
	// 代标记 stamp（与 materialize 同源单源）：blue-green 进程的代次化 ID
	// 在此与部署物化逐字节一致——缺席对照的身份面不可能分叉。
	stampDeploymentGenerations(ws, spec, d.Generation)
	ids := make([]string, 0, len(ws))
	for _, w := range ws {
		ids = append(ids, w.ID)
	}
	sort.Strings(ids)
	e.converge.mu.Lock()
	e.converge.projMemo[key] = ids
	e.converge.mu.Unlock()
	return ids, nil
}

// missingExpectedCarriers 计算缺席面：期望集 − 观测集（字典序；空 = 无缺席）。
func (e *Engine) missingExpectedCarriers(ctx context.Context, d *deployment.Deployment, obs []capability.WorkloadObservation) ([]string, error) {
	ids, err := e.expectedCarrierIDs(ctx, d)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(obs))
	for _, o := range obs {
		seen[o.WorkloadID] = true
	}
	var missing []string
	for _, id := range ids {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// ---- 双通道触发与收敛动作 ----

// convergeStep 是单 App 的双通道判定（driftScan 每拍消费）：快通道 = 有未
// 收敛信号；慢通道 = 物化备忘到期（ReconcileReplayInterval 强制重放——无
// 信号也重放，环外变更/Cancel 遗留的兜底，ring 域同律）。两通道经同一
// attemptConverge 过闸。
func (e *Engine) convergeStep(ctx context.Context, appID string, d *deployment.Deployment, signals convergeSignals) {
	if d == nil {
		return // 无成功基线：无可收敛锚（spec 对照照旧，缺席面跳过）
	}
	agg := signals.aggregate()
	fast := len(signals) > 0
	e.converge.mu.Lock()
	unchanged := e.converge.resolved[appID] == agg
	e.converge.mu.Unlock()
	fast = fast && !unchanged
	_, fresh := e.ensureFresh(e.converge.memo, appID, convergeMemoSig(d), e.clock.Now())
	if !fast && fresh {
		return // 稳态：无未收敛信号 + 物化备忘在期
	}
	src := convergeSourceSlow
	if fast {
		src = convergeSourceFast
	}
	e.attemptConverge(ctx, appID, d, agg, src)
}

// attemptConverge 是收敛动作的统一原语（ADR-0082 §2）：闸门序（posture →
// 冷却 → 冻结 fail-closed）→ lockApp 双复查（①驱动权语义 per-App 查询；
// ②teardown 标记——expect 缓存成员资格复查的机制载体）→ materialize
// （isolate=true，boundedStep 带界）→ 记账（成功落签名/备忘，失败计冷却）。
// 不建 Deployment 行——收敛高频，部署行会成行垃圾（替代案已驳回）；审计
// 走 SourceSystem 行而非部署 lineage。
func (e *Engine) attemptConverge(ctx context.Context, appID string, d *deployment.Deployment, agg string, src convergeSource) {
	// 冷却门先于 posture 门：posture 切换（all→detect→all 往返）在冷却
	// 判定点可见即清零记账（解除三径之一；跨重启的切换由记账内存态自然
	// 清零——两径对齐 ADR-0082 §4）。窗内尝试跳过。
	if !e.convergeCooldownOpen(ctx, appID, agg) {
		return
	}
	// posture 门（ADR-0082 §5）：detect = 检测照发、收敛全跳（观察期逃生
	// 门）；启动基线重放不受门限——缓存重建是检测面的前提（ADR-0005 旧
	// 行为保留）。
	if src != convergeSourceBaseline && e.opts.Converge.Posture == ConvergePostureDetect {
		return
	}
	// 冻结闸门（ADR-0082 §3 fail-closed）：挂起 = 不消费签名、不记账——
	// 解冻后下一拍重扫即自然收敛。
	frozen, err := e.convergeFrozen(ctx, appID)
	if err != nil {
		// fail-closed：Team 解析失败/存储故障跳过本拍下拍重试——不走
		// transition.go ownerTeam 的降级 '' 契约（那对只读事件面合理，
		// 搬进执法面即 fail-open）。
		e.log.Error("converge: freeze gate", "app", appID, "err", err)
		return
	}
	if frozen {
		return
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
		return
	}
	// 复查①（驱动权语义 per-App 查询，ListDriving 同口径——活跃态含
	// failed 待回滚窗；ActiveByApp 不含 failed，单独使用会开「收敛×回滚
	// 交错」竞态窗 → vacuously ready → gen 错位 → BG 旧代锚漂移）。
	driving, err := e.repos.Deployments.DrivingByApp(ctx, appID)
	if err != nil {
		e.log.Error("converge: recheck driving", "app", appID, "err", err)
		return
	}
	if len(driving) > 0 {
		return // 驱动权在部署链：本拍跳过（不记账不落签名）
	}
	// 基线刷新：扫描到锁之间 succeeded 行可能已更替——锚已变则本拍放弃
	//（下拍以新签名重判；拿着旧基线物化会把新代翻回去）。
	fresh, err := e.repos.Deployments.LatestSucceeded(ctx, appID)
	if err != nil {
		e.log.Error("converge: recheck baseline", "app", appID, "err", err)
		return
	}
	if fresh.ToRevision != d.ToRevision || fresh.Generation != d.Generation {
		return
	}
	if err := e.materialize(ctx, fresh, fresh.ToRevision, fresh.Generation, true); err != nil {
		e.log.Error("converge: materialize", "app", appID, "generation", fresh.Generation, "err", err)
		e.convergeFail(ctx, appID, agg, err)
		return
	}
	e.convergeSucceed(ctx, appID, convergeMemoSig(fresh), agg, fresh, src)
}

// convergeCooldownOpen 报告该 App 是否可发起收敛尝试（锁外判定；窗内返回
// false）。解除三径（ADR-0082 §4）在窗内逐拍判定：超窗自动 / 漂移签名
// 变化重触发 / posture 切换清零。返回 true 时若发生了释放且内置告警在
// firing，就地落 resolved 沿（事件 + 通道派发，锁外 I/O）。
func (e *Engine) convergeCooldownOpen(ctx context.Context, appID, agg string) bool {
	now := e.clock.Now()
	e.converge.mu.Lock()
	until, cooling := e.converge.cooldown[appID]
	if !cooling {
		e.converge.mu.Unlock()
		return true
	}
	release := now.Sub(until) >= e.opts.Converge.Cooldown ||
		e.converge.failSig[appID] != agg ||
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
// 聚合签名（快通道生命周期）+ 冷却清零 + 事实面（快通道才发 workload.
// converged——慢通道重放与启动基线重放是无差别的幂等重放，逐拍发事件即
// 行垃圾；信号驱动的真收敛才有事件价值）。
func (e *Engine) convergeSucceed(ctx context.Context, appID, sig, agg string, d *deployment.Deployment, src convergeSource) {
	now := e.clock.Now()
	e.ensureRemember(e.converge.memo, appID, ensureMemo{sig: sig, gen: d.Generation, at: now})
	e.converge.mu.Lock()
	e.converge.resolved[appID] = agg
	e.convergeReleaseLocked(appID)
	resolved := len(e.converge.cooldown) == 0 && e.converge.alertFiring
	if resolved {
		e.converge.alertFiring = false
	}
	e.converge.mu.Unlock()
	if resolved {
		e.convergeAlertEdge(ctx, alertrule.StateOK, now)
	}
	if src != convergeSourceFast {
		return
	}
	// 事实面双真（kernel 默认）：workload.converged 流面 + 审计行
	// source=system（AfterFP = 基线 digest——「换成了什么」的取证锚）。
	// 收敛是系统自治动作，有审计动词可查证（ADR-0066 写序列 kernel）。
	rev, err := e.repos.Revisions.Get(ctx, d.ToRevision)
	digest := ""
	if err == nil {
		digest = rev.Digest
	}
	team := e.eventTeamOfApp(ctx, e.repos, appID)
	if err := writes.Emit(ctx, writes.Repos{Outbox: e.repos.Outbox, Audits: e.repos.Audits}, writes.FactRow{
		Name: eventWorkloadConverged, Aggregate: "app", ID: appID,
		Payload: convergeEventPayloadJSON(appID, d.ToRevision, d.Generation, agg, ""),
		Action:  "workload.converge", Resource: "app/" + appID,
		AfterFP: digest, Source: audit.SourceSystem,
		TeamID: team,
	}); err != nil {
		e.log.Error("converge: emit event", "app", appID, "err", err)
	}
}

// convergeFail 记一次收敛失败：不落签名（resolved 不动——下拍重扫即自然
// 重试），计冷却计数；连续失败达阈值 N 进入冷却窗 + workload.converge_failed
// 一次 + 内置告警 firing 沿（ADR-0082 §4）。
func (e *Engine) convergeFail(ctx context.Context, appID, agg string, cause error) {
	now := e.clock.Now()
	e.converge.mu.Lock()
	// 签名变化重触发：与上次失败不同的聚合签名 = 新触发（计数重开）。
	if prev, ok := e.converge.failSig[appID]; ok && prev != agg {
		e.converge.fails[appID] = 0
	}
	e.converge.failSig[appID] = agg
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
		convergeEventPayloadJSON(appID, "", 0, agg, cause.Error())); err != nil {
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
