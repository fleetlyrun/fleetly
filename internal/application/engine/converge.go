package engine

// ADR-0082 缺省收敛（Converge by Default）：App 域收敛升格——检出漂移即
// 收敛（裁决表 8 判定行 + 2 非行），检测与事件面照发（ADR-0022 保留），
// Ensure 仍是唯一写动词（收敛动作的执行机构就是 Ensure/materialize→
// Ensure，升格的是调用者）。本文件承载：缺席/多余检测（LatestSucceeded
// 拍内重投影）、双通道触发（快通道六类信号 + 慢通道 ReconcileReplay-
// Interval 强制重放）、签名生命周期（绑定收敛结果）、冷却记账（N=3 →
// 10min + converge_failed 一次）、冻结闸门（fail-closed）、收敛动作
// （lockApp 双复查 + materialize isolate + 审计/事件按真实变更分流）、
// 非成功终态的重放标记（replay 快信号）。
//
// P3a（ADR-0087 决策 1/3/7）：收敛策略判定单源在域——domain/delivery/
// converge（裁决表 Adjudicate + 信号类/信号集 + 冷却窗 CooldownOpen +
// 失败阈值 AdjudicateFail + posture 闸门 Detect）；本文件持记账状态与锁、
// 闸门事实装配（冻结/驱动权/teardown 查询）、materialize 执行与事实面
// 发射（converge 是机制原语 L1#8——本批只下沉策略谓词，机制零触碰）。
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

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/converge"
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
	// convergeSourceFast 快通道：driftScan 六类信号触发（S12 修订扩
	// extra/replay 后与 ADR-0082 §4 同步；冷却记账的主消费面，签名生命
	// 周期绑定收敛结果）。
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

// 收敛信号集（converge.Signals）、触发面（converge.Trigger）与失败签名账
// （converge.FailureSignature）的类型与签名词汇单源在 domain/delivery/
// converge（P3a 第 4 单元下沉）；信号类键构造（converge.SignalSpec.Key 等）
// 与信号值字面（converge.AbsentSignal 等）同彼。

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
	failSig map[string]converge.FailureSignature
	// posture 是进入冷却时的 posture 快照（切换清零判定）。
	posture map[string]string
	// cooldown 是冷却窗起点（键缺席 = 未冷却；窗满自动解除）。
	cooldown map[string]time.Time
	// eventSeen 钉「本轮冷却已发 workload.converge_failed」（一次语义）。
	eventSeen map[string]bool
	// projMemo 是基线投影的结果缓存（digest 签名短路：Revision 冻结体
	// 不可变，(App, Revision, Generation) 即内容锚——缺席/多余检测的每拍
	// 重投影在未变基线上近零成本，ADR-0082 §1）。投影结果即内容，ID 是
	// 派生面。键带 App 前缀（删除收口的差集清账可按前缀精确回收）+ 容量
	// 上限（R16-L2：长跑进程的每部署条目不得无界增长）；LRU 记账在
	// projUsed（访问序）与 projTick（单调时钟）两字段。
	projMemo map[string][]capability.Workload
	projUsed map[string]uint64 // projMemo 键 → 最近访问序
	projTick uint64            // 访问序发号器（projMemo 的 LRU 单一记账面）
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
	c.failSig = make(map[string]converge.FailureSignature)
	c.posture = make(map[string]string)
	c.cooldown = make(map[string]time.Time)
	c.eventSeen = make(map[string]bool)
	c.projMemo = make(map[string][]capability.Workload)
	c.projUsed = make(map[string]uint64)
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

// convergeProjMemoLimit 是基线投影缓存的容量上限（LRU 淘汰；512 条 ≈ 数百
// 个 (App, Revision, Generation) 组合，覆盖 N0 小团队全部现役基线的同时
// 给长跑进程一个硬上界——R16-L2）。
const convergeProjMemoLimit = 512

// convergeProjMemoKey 构造投影缓存键：App 前缀让删除收口按前缀精确差集
// （Revision 在 App 内唯一，前缀不引入歧义），(Revision, Generation) 是
// 内容锚。
func convergeProjMemoKey(appID string, d *deployment.Deployment) string {
	return appID + "\x00" + convergeMemoSig(d)
}

// managedFingerprint 返回受管 Workload 集的稳定指纹（json.Marshal 对 map
// 键排序，切片序取 ManagedWorkloads 稳定返回序；序列化失败退化为逐次
// 唯一值——保守推进 gen，宁替换不漏变更）。签名构造族（备忘协议 L0#4
// 的配套面，ensure.go 家族清单成员）——manageds/databases/browses 三域
// memo 共用，归核单源（P4 wave-3b 星间债务清偿：原驻 managed.go，browses/
// databases 的消费曾是星间硬边豁免表成员）。
func managedFingerprint(ws []capability.Workload) string {
	b, err := json.Marshal(ws)
	if err != nil {
		return fmt.Sprintf("unserializable-%p", &ws)
	}
	return string(b)
}

// materialsFingerprint 返回材料的稳定指纹（json.Marshal 对 map 键排序；
// []byte 走 base64——确定性序列化。序列化失败退化为逐次唯一值——保守
// 推进，宁重下发不漏变更，managedFingerprint 同款取舍）。
func materialsFingerprint(m capability.Materials) string {
	b, err := json.Marshal(struct {
		RegistryAuth map[string]capability.RegistryCredential `json:"registry_auth,omitempty"`
		SecretFiles  map[string][]byte                        `json:"secret_files,omitempty"`
	}{m.RegistryAuth, m.SecretFiles})
	if err != nil {
		return fmt.Sprintf("unserializable-%p", &m)
	}
	return string(b)
}

// projMemoGet 读投影缓存并刷新 LRU 访问序（调用方持 mu）。
func (c *converges) projMemoGet(key string) ([]capability.Workload, bool) {
	ws, ok := c.projMemo[key]
	if ok {
		c.projTick++
		c.projUsed[key] = c.projTick
	}
	return ws, ok
}

// projMemoPut 写投影缓存并按容量上限淘汰最久未用的条目（调用方持 mu）。
func (c *converges) projMemoPut(key string, ws []capability.Workload) {
	c.projTick++
	c.projMemo[key] = ws
	c.projUsed[key] = c.projTick
	for len(c.projMemo) > convergeProjMemoLimit {
		oldestKey, oldest := "", ^uint64(0)
		for k, seq := range c.projUsed {
			if seq < oldest {
				oldestKey, oldest = k, seq
			}
		}
		delete(c.projMemo, oldestKey)
		delete(c.projUsed, oldestKey)
	}
}

// ---- 裁决表（ADR-0082 §1：8 判定行 + 2 非行）----
//
// 判定函数（converge.Adjudicate）与两个生产构造点（信号侧 converge.SignalInput
// / 闸门侧 converge.GateInput）、裁决值字面、字段生产者登记表、信号类枚举
// 与键值字面——单源在 domain/delivery/converge（P3a 第 4 单元下沉，ADR-0087
// 决策 7；golden 随迁彼处 testdata，表即断言）。本文件的消费点：convergeStep
// （信号侧，快通道行动性判定）与 attemptConverge（闸门侧，冻结/驱动权复查）。

// ---- 缺席/多余检测（期望集 = LatestSucceeded 拍内重投影）----

// expectedWorkloads 返回基线 Revision 的投影载体集（含代戳；缺席/多余/
// detect 账本三面共用同一投影与同一 memo）。与部署 RenderWorkloads/
// materialize 同源走 projectRevision + 代标记 stamp——决策输入是 Revision
// 真源而非期望缓存，ADR-0082 §1；缓存幽灵/BG 旧代/进程缩减三类假阳性
// 根源性消除。(Revision, Generation) 签名短路：投影结果按内容锚缓存，
// 未变基线近零成本。
func (e *Engine) expectedWorkloads(ctx context.Context, d *deployment.Deployment) ([]capability.Workload, error) {
	key := convergeProjMemoKey(d.AppID, d)
	e.converge.mu.Lock()
	if ws, ok := e.converge.projMemoGet(key); ok {
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
	e.converge.projMemoPut(key, ws)
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

// 缺席/多余两面（missingIDs/extraIDs：期望集对照观测集的两个方向）已随
// 漂移判定核下沉 domain/delivery/drift（ADR-0087 决策 7，P3a 批第 3 单元
// ——drift.go compareSpecs 消费 MissingIDs/ExtraIDs）。

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
func (e *Engine) convergeStep(ctx context.Context, appID string, d *deployment.Deployment, signals converge.Signals) {
	agg := signals.Aggregate()
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
		if !converge.Actionable(converge.Adjudicate(converge.SignalInput(signals))) {
			return // 信号不构成收敛行（当前六类皆可行动；未知信号类不得发起动作）
		}
		e.attemptConverge(ctx, appID, d, converge.Trigger{Sig: agg}, convergeSourceFast)
		return
	}
	if fresh {
		return // 稳态：无未收敛信号 + 物化备忘在期
	}
	// 慢通道也可能携带信号（"已收敛签名"未变的残留信号：fast 让位后由慢
	// 通道重放）；agg 为空即无签名触发（例行重放）。
	e.attemptConverge(ctx, appID, d, converge.Trigger{Sig: agg}, convergeSourceSlow)
}

// attemptConverge 是收敛动作的统一原语（ADR-0082 §2）：闸门序（冷却 →
// posture → 冻结 fail-closed）→ lockApp 双复查（①驱动权语义 per-App 查询；
// ②teardown 标记——expect 缓存成员资格复查的机制载体）→ 一次裁决
// （converge.GateInput：冻结/驱动权两行同一映射点）→ materialize
// （isolate=true，boundedStep 带界）→ 记账（成功落签名/备忘，失败计冷却）。
// 不建 Deployment 行——收敛高频，部署行会成行垃圾（替代案已驳回）；审计
// 走 SourceSystem 行而非部署 lineage。
func (e *Engine) attemptConverge(ctx context.Context, appID string, d *deployment.Deployment, trigger converge.Trigger, src convergeSource) convergeOutcome {
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
	// 判定谓词在 domain/delivery/converge（Detect）。
	if converge.Detect(e.opts.Converge.Posture) {
		return convergeOutcomePostureDetect
	}
	// 冻结闸门（ADR-0082 §3 fail-closed）：挂起 = 不消费签名、不记账——
	// 解冻后下一拍重扫即自然收敛。取事实在锁外（现行序），裁决与驱动权
	// 事实合并到锁内一次（converge.GateInput）。
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
	// 一次性裁决（冻结/驱动权两行的唯一映射点 = converge.GateInput）：两行
	// 都只是 return，不消费签名、不记账。
	switch converge.Adjudicate(converge.GateInput(frozen, len(driving) > 0)) {
	case converge.VerdictSuspend:
		return convergeOutcomeSuspended
	case converge.VerdictSkip:
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
// false）。解除三径（ADR-0082 §4：超窗自动 / 漂移签名变化重触发〔仅限携带
// 签名的触发，R02-M1〕/ posture 切换清零）的判定核在 domain/delivery/
// converge（CooldownOpen——时间作参数传入）。返回 true 时若发生了释放且
// 内置告警在 firing，就地落 resolved 沿（事件 + 通道派发，锁外 I/O）。
// 形态：装配（锁内取记账快照 + 时钟）→ 域裁决 → 执行（清账 + 告警沿）。
func (e *Engine) convergeCooldownOpen(ctx context.Context, appID string, trigger converge.Trigger) bool {
	now := e.clock.Now()
	e.converge.mu.Lock()
	until, cooling := e.converge.cooldown[appID]
	open := converge.CooldownOpen(converge.CooldownInput{
		Cooling: cooling, Until: until, Window: e.opts.Converge.Cooldown, Now: now,
		FailSig: e.converge.failSig[appID], PostureAtEnter: e.converge.posture[appID],
		PostureNow: e.opts.Converge.Posture, Trigger: trigger,
	})
	if !open {
		e.converge.mu.Unlock()
		return false
	}
	if cooling {
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
	e.converge.mu.Unlock()
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
func (e *Engine) convergeSucceed(ctx context.Context, appID, sig string, trigger converge.Trigger, d *deployment.Deployment, src convergeSource, rep capability.EnsureReport) {
	now := e.clock.Now()
	e.ensureRemember(e.converge.memo, appID, ensureMemo{sig: sig, gen: d.Generation, at: now})
	e.converge.mu.Lock()
	if trigger.Signaled() {
		e.converge.resolved[appID] = trigger.Sig
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
		Payload: convergeEventPayloadJSON(appID, d.ToRevision, d.Generation, trigger.Sig, ""),
		Action:  "workload.converge", Resource: "app/" + appID,
		AfterFP: digest, Source: audit.SourceSystem,
		TeamID: team,
		Detail: convergeAuditDetailJSON(src, trigger.Sig, rep),
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
// 一次 + 内置告警 firing 沿（ADR-0082 §4）。判定核（计数推进/签名账三态/
// 进窗/一次沿）在 domain/delivery/converge（AdjudicateFail——纯函数）；
// 本函数持记账落账与 I/O（事件发射 + 告警沿派发）。形态：装配（锁内取记账
// 快照 + 配置）→ 域裁决 → 执行（记账更新/emit）。
//
// 签名账三态（R02-M1）：只有携带签名的失败才写 failSig；无签名失败（基线/
// 隔离重放）只计数、不抹签名账——否则一次无签名重放失败会把 failSig 清空，
// 让下一次同签名失败被误判成「签名变化」而解除冷却。
func (e *Engine) convergeFail(ctx context.Context, appID string, trigger converge.Trigger, cause error) {
	now := e.clock.Now()
	e.converge.mu.Lock()
	_, alreadyCooling := e.converge.cooldown[appID]
	v := converge.AdjudicateFail(converge.FailInput{
		Fails: e.converge.fails[appID], FailSig: e.converge.failSig[appID],
		Threshold: e.opts.Converge.FailureThreshold, AlreadyCooling: alreadyCooling,
		EventSeen: e.converge.eventSeen[appID], AlertFiring: e.converge.alertFiring,
		Trigger: trigger,
	})
	e.converge.fails[appID] = v.Fails
	e.converge.failSig[appID] = v.FailSig
	if v.EnterCooldown {
		e.converge.cooldown[appID] = now
		e.converge.posture[appID] = e.opts.Converge.Posture
		e.converge.eventSeen[appID] = true
	}
	if v.AlertEdge {
		e.converge.alertFiring = true
		e.converge.alertSince = now
	}
	e.converge.mu.Unlock()
	if !v.EmitFailureEvent {
		return
	}
	team := e.eventTeamOfApp(ctx, e.repos, appID)
	if err := e.emitObservation(ctx, team, eventWorkloadConvergeFailed, "app", appID,
		convergeEventPayloadJSON(appID, "", 0, trigger.Sig, cause.Error())); err != nil {
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

// forgetApp 清该 App 的全部收敛域记账（删除收口，R16-L2 + R02-L7）：memo /
// resolved / fails / failSig / posture / cooldown / eventSeen / projMemo /
// replay / teardown 十面一次清（调用方持 appMu——锁身份先取后删，与并发
// 收敛动作的锁内复查互斥）。返回是否需发内置告警解除沿：该 App 是最后一个
// 冷却中的 App 时 alertFiring 随记账清零（否则已删 App 的 converge-failed
// 会永久 firing，只能重启进程清除）。teardown 标记由调用方随后重钉。
//
// projMemo 按 App 前缀差集清理；并发的漂移对照读到本 App 旧基线时可能在
// 清理后回填一条（对照面不持 appMu）——残留有界（容量 LRU）且键含 App ID
// （ID 不复用），不再服务任何读径。
func (c *converges) forgetApp(appID string) (alertResolved bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.memo, appID)
	delete(c.resolved, appID)
	delete(c.fails, appID)
	delete(c.failSig, appID)
	delete(c.posture, appID)
	delete(c.cooldown, appID)
	delete(c.eventSeen, appID)
	delete(c.replay, appID)
	delete(c.teardown, appID)
	prefix := appID + "\x00"
	for key := range c.projMemo {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		delete(c.projMemo, key)
		delete(c.projUsed, key)
	}
	if len(c.cooldown) == 0 && c.alertFiring {
		c.alertFiring = false
		c.alertSince = time.Time{}
		return true
	}
	return false
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
