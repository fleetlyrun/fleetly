# 收敛语义深水区设计（S12）：判定表单源化 · 静默面事实化 · posture/账本语义收口

| 项 | 值 |
|---|---|
| 状态 | 批 1/2 已实施（commit `2740a09d` / `402132fe`）；**D3 已实施（detect = 零运行时写）**；D5/D6 已实施（S12b2） |
| 日期 | 2026-10-19 |
| 基线 | `main` @ `e99d2470`（工作区干净；S1-S11 已 push；Go 1.26.6；本机无 Docker → e2e 不可跑）；实施后 HEAD `2740a09d`（S12b1 已 push） |
| 实施注记 | S12b1 落码把设计稿中的 `revert` 信号命名为 `replay`（键 `replay\|<deployment-id>`、`convergeSignalReplay`；测试名 `TestCancelReplaySignalDrivesBaselineReplay` / `TestReplayMark*`），语义不变——ADR-0082 文本按落码命名。detect 的 `rebuildBaselineExpectations` / `recordProjectedSpecs` 已随 D3 落码（§3.3），实施细节与产物见 §3.4/§3.7。 |
| 评审锚 | `docs/reviews/2026-10-19-architecture-quality-acceptance.md`（§3 遗留项、§7 M-9/M-14/M-15）；`R02-engine-deploy.md` §R02-M2/M5（`:97-115`、`:159-178`）；`R16-declarative-model.md` §R16-M2/M3/M5/M6（`:134-159`、`:161-186`、`:212-232`、`:234-248`）、§4A 十行落点表（`:354-385`）、§4B C5（`:437`） |
| 关联 ADR | **修订**：ADR-0082（§1 裁决表 / §2 收敛动作 / §5 posture / §6 账本 / 验收锚）、ADR-0081（§1 DECLARATION 判据 / §5 边界案例 / 附录 A.6+A.14 / 统计与验收锚）。**追记**：ADR-0083（§3 双面契约 + 落码注记 + 验收锚）。词条同步：CONTEXT.md `Posture`（`:286-287`）、`Declaration`（`:263-264`）、`Converge`（`:282-284`） |
| 结论摘要 | ① 裁决表**真正接线**：8 行由生产路径构造（含为行 8/10 补两个真实信号），行 5/9 降为「信号层非行」并在 ADR 标注行 9 未落码；② `EnsureReport` 扩 `Changed`/`Removed`，`materialize` 回传回执，**审计按真实变更发、事件维持 fast-only**；③ `posture=detect` 改为**零运行时写**，启动面只重建期望账本（含 `ensuredSpec`——路由解析依赖它）；④ 期望账本条目加**写入部署归属**，非基线写入不采用；非成功终态落**重放标记**取代「靠慢通道兜底」；⑤ ADR-0081 修判据（两个子类 + 域级收敛面表）；⑥ ADR-0083 追记 restart 源=生效基线 |
| 关键新发现 | `posture=detect` 若简单去掉 baseline 豁免将**打断路由发布**（`routepublish.go:136-152` 依赖 `ensuredSpec`）——这是 D3 推荐案必须先补账本重建的原因；ADR-0081 统计 153 已落后于 proto 实数 **154**（S10a 增 `QueryScopedMetrics`） |

> 纪律：本文件是 S12b 实现的唯一设计依据；每个决策点都裁决完毕（无「待定」）。所有「本轮做/后续做」判定见 §8。凡标 **待复核** 处均给出验证命令或测试名。

---

## 0. 决策总表

| # | 发现 | 推荐案（一句话） | 本轮 | 规模 | 主验收锚 |
|---|---|---|---|---|---|
| D1 | R02-M2 判定表是装饰函数 | 裁决表接进生产：信号侧 + 闸门侧两处构造 input；补 `extra`/`revert` 两个真实信号；行 5/9 降为非行（行 9 标未落码）；删死字段与两个死 verdict（alert-only/record-only） | 代码（S12b-1/2） | M | 8 行 golden + 信号类/字段生产者双保鮮 + 行为测 |
| D2 | R16-M6/R02-M5 慢/基线收敛静默 | `EnsureReport` + `Changed`/`Removed`；`materialize` 回传回执；审计按 `fast源 ∨ 真实变更` 发（`SourceSystem` + `AfterFP` digest + Detail 变更集），事件维持 fast-only | 代码（S12b-1） | M | 双 provider 回执测 + 3 条审计/事件分流测 |
| D3 | R16-M2 detect 语义双向错位 | `detect` = **零运行时写**（删 baseline 豁免）；启动面改「只重建期望账本」（spec 面精确、gen 面成文失明）；隔离重放被拦即 Warn | **已实施**（D3 批：四源同受门限 + 启动只重建账本 + 隔离 Warn） | M | 零写钉测 + 账本重建 + 路由可解析 + 降级清单 |
| D4 | R16-M3 账本被未成功部署写脏 | 账本条目带**写入部署 ID**，drift 期望侧只在写入者==当前 `LatestSucceeded` 时采用；非成功终态（已物化的行）落**重放标记**→下一拍真实 `revert` 快信号 | 代码（S12b-2） | M | 归属采用/跳过测 + 标记触发测 + 全链 Cancel 测加强 |
| D5 | R16-M5 ADR-0081 判据自相矛盾 | 修判据：DECLARATION 分「可重放 no-op」与「状态即事实」两子类 + 新增**域级收敛面表**；`SetUserPassword` 归后者并写明 bcrypt 反例；顺带修 153→154 | **本批完成**（S12b2 文档 + 守卫） | S | 判据文字 + 附录统计对账 + 互斥/域面断言 |
| D6 | H15 已修但 ADR-0083 无记录 | 追记：声明面重启源 = **生效基线**（非最新冻结 Revision），附 S4 落码注记、验收锚与 `--runtime` 校验残余 | **本批完成**（S12b2 文档） | S | 追记段落 + 新验收锚条目 |

**一句话**：D1+D2 让「十行表」从文档走进执行面并让系统改载体留痕；D3+D4 把「谁有资格当期望」和「detect 到底写不写」两件事钉死；D5+D6 只动法源文字，不冒险改行为。

> 术语：表中的「**本轮**」= **S12b 实施波次**（本设计稿批准后的代码落地轮，见 §8），**不是**本设计会话——本会话零代码改动。「只做文档」= 该次实施只改 ADR/词条/注释，无行为变更。

> **实施现状（2026-10-19）**：批次 1（D1+D2）与批次 2（D1 剩余+D4）已合并落在 S12b1 `2740a09d`（含 `extra`/`replay` 信号与 `ensuredBy` 归属）；批次 3 的**法源半边**（D5+D6 文档、守卫补强、CONTEXT 词条）由 S12b2 完成；**D3（detect 零写）已实施**——Lead 在 S12b2 后复核裁决执行（不再延后），代码清单 §3.3 与 C1-C6 钉测同批落地。表中 D1/D2/D4 的「本轮」列保留为原计划批次标签，实际归属以本注记为准。

---

## 0.1 Lead 初读代码事实复核（逐条）

| # | Lead 初读 | 复核结论 | 证据/修正 |
|---|---|---|---|
| 1 | `converge.go:144-201` 十行判定，生产零引用 | **确认** | 全仓 `convergeVerdict` 仅 `converge.go:146,148,172,176`（定义/注释）+ `converge_test.go:77-108`（唯一调用点）；golden `testdata/converge-verdicts.golden` 10 行 |
| 2 | 真实闸门在 `attemptConverge:289-356` | **确认** | 序：冷却 `:293` → posture `:299` → 冻结 `:304-314` → `lockApp` `:318-320` → teardown `:326` → 驱动权 `:332-339` → 基线复查 `:342-349` → `materialize` `:350` → `convergeSucceed` `:355` |
| 3 | 真实信号在 `drift.go:compareSpecs` | **确认** | 四个键：`absent`（`:234-241`）、`rollout\|<wid>`（`:272-280`）、`gen\|<wid>`（`:289-299`）、`spec\|<wid>`（`:300-318`）；双通道 `convergeStep:262-281` |
| 4 | `convergeSucceed:398-437` 只有 fast 发事件+审计 | **确认** | `:416-418` 提前 return；`:428-436` 是唯一 `writes.Emit` |
| 5 | `attemptConverge:296-301` posture 只拦非 baseline | **确认** | `:299` `if src != convergeSourceBaseline && Posture == detect` |
| 6 | 启动/peer 隔离重放都走 baseline 源 | **确认** | `drift.go:86`（启动循环）→`:94-96`；`peers.go:166`、`:213-215` |
| 7 | `materialize.go:75-85` releasing 阶段即 `recordEnsured` | **确认** | `:75` Ensure → `:82-84` WireGen → `:85 recordEnsured`；`recordEnsured` 是 `expect/ensuredSpec/ensuredGen` 唯一写点（`observ.go:110-125`） |
| 8 | `admission.go` Cancel 不清账 | **确认** | `:304` 只 `ensureForget(e.delivery.release, id)`；注释 `:279-283` 仍持 N0 口径 |
| 9 | drift 期望侧取内存账本 | **确认** | `drift.go:243-245`（App 级锚）、`:259-263`（`ensuredSpecOf` 缺席即跳过）、`:289-299`（`ensuredGenOf`） |
| 10 | `EnsureReport` 只有 `Gens` | **确认** | `capability/runtime.go:282-301`；5 个实现：swarm `:40`、k3s `:41`、engine 假件 `fakeruntime_test.go:126`、`render_test.go:113`、apitest `harness.go:562-570`（恒空回执，R12-M7 已报） |
| 11 | `SetUserPassword` bcrypt 随机盐 → 未变重放非 no-op | **确认** | `identity.go:181` `bcrypt.GenerateFromPassword`（每次新盐）；ADR-0081 `:20` vs `:155` 判据冲突 |
| 12 | ADR-0081:20/155 判据矛盾 | **确认** | 原文见 §5.1 引用块 |
| 13 | `apps restart` 已在 S4 修为生效基线 | **确认** | `c925ba3b`：`verbs_structure.go:460` 改调 `latestSucceededRevision`；新增纯函数 `selectRestartBaseline`；golden `apps-restart-json.golden` to_revision `r3→r2`；ADR-0083 无任何记载 |

**本轮新增事实（Lead 初读未含，均影响决策）**：

| # | 新事实 | 证据 | 影响 |
|---|---|---|---|
| F1 | **路由发布依赖 `ensuredSpec` 缓存**，而该缓存只由 Ensure 写 | `routepublish.go:136-152`（`resolveBackend` → `routeExpectations`）；`observdomain.go:184-222`（读 `ensuredSpec`/`ensuredGen`）；`routepublish.go:39-41` 的就绪门在 `lifecycle.go:93-95` 由基线重放收口打开 | **D3 若让 detect 完全不物化，启动后路由会解析不到后端 → 用户路由消失**。故 detect 必须补「期望账本重建（含 spec/ports）」 |
| F2 | ADR-0081 统计 **153** 已落后于 proto 实数 **154** | `proto/**` rpc 实数 154（本会话复核命令见附录 A）；`writeclassguard_test.go:201` 已断言 154；ADR-0081 `:11,85,277` 仍写 153/73 | D5 修订必须带上计数与 `QueryScopedMetrics` 行（否则法源与守卫对不上） |
| F3 | 行 8/10 在**文档层**另有孤立事实：行 9 的 `WorkloadOrphaned` 事件码根本不存在 | `eventcode/events.go` 无该名；`observ.go:251-255` 对非管辖载体直接 return | D1 只能把行 9 标「未落码」，不能假装接线 |
| F4 | `verdictAlertOnly`/`verdictRecordOnly`/`verdictReplayBaseline` 三个常量生产零消费 | `converge.go:162-170` + 全仓 grep | D1 删前两个；保留 replay-baseline 作为行 10 的裁决字面（动作同 converge） |
| F5 | `factdual` 守卫只执法 `LedgerOnly: true` **字面量**（表达式赋值不在扫面） | `guards/factdual_test.go:130-171`（仅 `*ast.Ident == true`） | D2 的「按源分流」用表达式赋值不会踩守卫，但**必须**按守卫意图写理由注释（本设计已给注释文案） |
| F6 | `converge_test.go:493-511` 的冷却钉测手工把 `failSig` 置空——真实生产形态（空 `agg` 的基线重放）未被覆盖 | 同文件 `:498-504` | 与 R02-M1 同族；本设计**不修**（超出 S12 射程），但在 D1 的信号映射里保持 `revert` 携带部署 ID 以免再制造空签名源（见 §1.7） |
| F7 | `--runtime` 进程存在性校验仍取最新冻结 Revision | `processrestart.go:31`（`Revisions.Latest`） | D6 追记必须登记为残余（与 H15 同族但方向相反），并可作 S12b-3 的可选小修 |

---

## 1. D1 —— R02-M2：裁决表单源化（把表接进生产路径）

### 1.1 问题复述

`convergeVerdict`（`converge.go:176-201`）是纯函数，**生产代码零调用**；真实决策分散在三处：

- 信号产生：`drift.go:234-318`（四类信号键，按 if 分支各自成键）；
- 快/慢通道与签名生命周期：`converge.go:262-281`；
- 闸门序：`converge.go:293-349`（冷却/posture/冻结/锁/teardown/驱动权/基线复查各自 return，无表）。

golden `testdata/converge-verdicts.golden` 钉的是这个死函数——改真实分支不会让它变红（R02-M2 判据成立）。同时行 8（域内多余载体）/行 9（域外孤儿）/行 10（Cancel 遗留）在生产里**没有任何检测落点**：行 8 靠 Ensure 副作用，行 10 靠慢通道，行 9 连事件码都不存在（F3）。

### 1.2 候选方案

| 案 | 内容 | sound | 成本 | 回归风险 |
|---|---|---|---|---|
| **A（推荐变体 A′）** | 表接进生产：`signalVerdictInput`（drift 信号 → 行）+ `gateVerdictInput`（frozen/driving → 行）+ `convergeVerdict` 两处真实调用；**同时给行 8/10 补真实信号**（`extra`/`revert`），行 5 降为非行、行 9 标未落码 | 高：表成为唯一判定点，golden + 映射测 + 行为测三层同时执法 | M（~250 行含测试） | 中：新增 `extra` 信号会改变稳态与部署窗的收敛触发面（需按 §1.7 逐条翻向） |
| B（最小接线） | 只做两处接线，`extra`/`revert` 不补；行 8/10 在 ADR 标注「未接线、由慢通道兑现」 | 中：表仍拿不到行 8/10 的生产输入，等于保留一半装饰 | S（~120 行） | 低 |
| C（只改文档） | 不动代码，把 ADR 验收锚①改为「文档锚」并删死分支 | 低：R02-M2 的核心指控（改动真实决策不会红）原样保留 | S | 极低 |

**否决 B**：B 之后行 8/10 仍然只能靠「没有信号的行不得伪造输入」的注释活着——下一位实现者会再次面对同一问题；而 `extra`（`InspectWorkloads` 域内枚举 − 投影期望集）与 `revert`（Cancel/失败终态标记）都是**零新外部依赖、数据源就在手边**的真实信号，成本各约 40 行。
**否决 C**：与 R02-M2 的 a 案、验收报告 §7 M-14 的裁决直接冲突。

**推荐 A′**，并把 A′ 的「不可伪造」原则写成硬规则：`convergeVerdictInput` 的每个字段必须有唯一生产构造者（守卫见 §1.5）。

### 1.3 精确改动清单

#### 1.3.1 信号类单一构造点（`internal/application/engine/converge.go`）

```go
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
	convergeSignalRevert                             // 行10：Cancel/失败终态遗留
)

// allConvergeSignalClasses 返回全部信号类（完备性守卫的遍历面）。
func allConvergeSignalClasses() []convergeSignalClass

// key 构造该类在 convergeSignals 里的键（suffix 为空 = 无逐载体维度）。
func (c convergeSignalClass) key(suffix string) string
// String 返回审计/日志字面（"spec"/"absent"/"gen"/"rollout"/"extra"/"revert"）。
func (c convergeSignalClass) String() string
```

`drift.go:compareSpecs` 的四处键字面量改为 `convergeSignalSpec.key(o.WorkloadID)` 等；`converge.go:239` 的 `signals["absent"]` 改为 `convergeSignalAbsent.key("")`。

#### 1.3.2 裁决表裁剪（`converge.go`）

- `convergeVerdictInput` **删除** `SteadyStopped`（行5）、`OrphanObserved`（行9）两个字段；其余 8 字段保留（`Frozen`/`Driving`/`SpecMismatch`/`Absent`/`GenDeviated`/`RolloutStalled`/`ExtraCarriers`/`CancelLegacy`）。
- 常量**删除** `verdictAlertOnly`、`verdictRecordOnly`；保留 `verdictSteady`、`verdictConverge`、`verdictSkip`、`verdictSuspend`、`verdictReplayBaseline`。
- `convergeVerdict` 删掉对应两个 `case`（函数变 8 行）。
- 新增两个映射函数（同文件）：

```go
// signalVerdictInput 把一次扫描的信号集映射为裁决表输入——信号键→行的
// 唯一映射点（ADR-0082 §1 的"表即断言"在信号侧的执行面）。
func signalVerdictInput(signals convergeSignals) convergeVerdictInput

// gateVerdictInput 把闸门事实映射为裁决表输入——冻结/驱动权两行（ADR-0082
// §1 行6/行7）的唯一映射点。
func gateVerdictInput(frozen, driving bool) convergeVerdictInput

// convergeActionable 报告裁决值是否应发起收敛动作（converge |
// replay-baseline；两者动作同为「基线物化」，差别只在触发来源与事实面标签）。
func convergeActionable(verdict string) bool
```

#### 1.3.3 生产接线（`converge.go:262-356`）

先补一个结果枚举（批次 1 引入，批次 3 消费其 detect/隔离分支）：

```go
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
```

`convergeStep` 改为：

```go
func (e *Engine) convergeStep(ctx context.Context, appID string, d *deployment.Deployment, signals convergeSignals) {
	if d == nil {
		return
	}
	agg := signals.aggregate()
	e.converge.mu.Lock()
	unchanged := e.converge.resolved[appID] == agg
	e.converge.mu.Unlock()
	fast := len(signals) > 0 && !unchanged
	_, fresh := e.ensureFresh(e.converge.memo, appID, convergeMemoSig(d), e.clock.Now())
	if fast {
		if !convergeActionable(convergeVerdict(signalVerdictInput(signals))) {
			return // 信号不构成收敛行（当前仅 revert|extra 之外的组合；表覆盖不全即此分支）
		}
		e.attemptConverge(ctx, appID, d, agg, convergeSourceFast)
		return
	}
	if fresh {
		return // 稳态：无未收敛信号 + 物化备忘在期
	}
	e.attemptConverge(ctx, appID, d, agg, convergeSourceSlow)
}
```

`attemptConverge` 的闸门段（`:296-314` 与 `:332-339`）改为**先取事实、再一次性裁决**：

```go
	// posture 门（ADR-0082 §5）：detect = 零运行时写（D3 后无 baseline 豁免）。
	if e.opts.Converge.Posture == ConvergePostureDetect {
		return convergeOutcomePostureDetect
	}
	frozen, err := e.convergeFrozen(ctx, appID)
	if err != nil { /* fail-closed 原样 */ return convergeOutcomeFailed }
	// ... lockApp / teardown / 驱动权复查（driving 查询结果保留）...
	switch v := convergeVerdict(gateVerdictInput(frozen, len(driving) > 0)); v {
	case verdictSuspend:
		return convergeOutcomeSuspended // 不消费签名、不记账
	case verdictSkip:
		return convergeOutcomeDriving // 驱动权在部署链
	}
```

> 说明：冻结判定必须在驱动权复查**之前**（现行序：冻结在锁外、驱动权在锁内）；为让 `gateVerdictInput` 一次拿全两事实，改动为「先冻结（锁外）→ 锁内查 driving → 一次裁决」。语义不变：两行都只是 return，不消费签名。

签名变更：`attemptConverge(...) convergeOutcome`、`replayAppBaseline(...) convergeOutcome`（`drift.go:94-96` 与 `peers.go:166,214` 的调用点用返回值做 detect 告警，见 §3）。

#### 1.3.4 行 8：`extra` 真实信号（`drift.go`）

- `converges.projMemo`（`converge.go:110`）类型 `map[string][]string` → `map[string][]capability.Workload`（投影结果即内容，ID 是派生面）；新增

```go
// expectedWorkloads 返回基线 Revision 的投影载体集（含代戳；缺席/多余/
// detect 账本三面共用同一投影与同一 memo）。
func (e *Engine) expectedWorkloads(ctx context.Context, d *deployment.Deployment) ([]capability.Workload, error)
// expectedCarrierIDs 保留为派生面（缺席检测的对外形态）。
func (e *Engine) expectedCarrierIDs(ctx context.Context, d *deployment.Deployment) ([]string, error)
// missingIDs / extraIDs 是集合差的两个纯函数（字典序；期望集 vs 观测集），
// 缺席（行2）与多余（行8）同源同序，便于对照测试。
func missingIDs(ws []capability.Workload, obs []capability.WorkloadObservation) []string
func extraIDs(ws []capability.Workload, obs []capability.WorkloadObservation) []string
```

- `compareSpecs:234-241` 改为一次投影派生两面：

```go
	if d != nil {
		ws, err := e.expectedWorkloads(ctx, d)
		if err != nil {
			e.log.Error("drift scan: project expected set", "app", appID, "err", err)
		} else {
			if missing := missingIDs(ws, obs); len(missing) > 0 {
				signals[convergeSignalAbsent.key("")] = "absent|" + strings.Join(missing, ",")
			}
			if extra := extraIDs(ws, obs); len(extra) > 0 {
				signals[convergeSignalExtra.key("")] = "extra|" + strings.Join(extra, ",")
			}
		}
	}
```

- 语义边界（写进代码注释）：`extra` 的数据源是 `InspectWorkloads` 的**域内**枚举（swarm `runtime.go:702-740`、k3s `inspector.go:18-58`，两者都按 `labelManaged=true` + 域选择器过滤）——因此它是「域内多余载体」（行 8）而非「域外孤儿」（行 9）；部署窗内由裁决行 6 `Driving` 让位（优先序在前）。

#### 1.3.5 行 10：`revert` 真实信号（`converge.go` + `transition.go` + `drift.go`）

- `converges` 新增 `revert map[string]string`（appID → 触发重放的部署 ID），`init()` 建表；方法：

```go
func (c *converges) markRevert(appID, deploymentID string)      // 非成功终态且该行已可能物化
func (c *converges) revertMarked(appID string) (string, bool)
func (c *converges) clearRevert(appID string)
```

- 写点：`transition.go:transit`（唯一前门）在 `commitTransition` 成功后就地落钩子：

```go
	// 期望账本归属钩子（ADR-0082 §6）：成功终态 = 该行已重锚账本（materialize
	// 写入的条目归属它），撤销遗留标记；非成功终态且该行已进过物化相位 =
	// 账本可能被"未成为期望态"的内容写脏，落重放标记（裁决表行 10 的真实
	// 信号源）——下一拍由 revert 快信号兑现回卷，慢通道兜底不变。
	switch {
	case to == deployment.StateSucceeded:
		e.converge.clearRevert(d.AppID)
	case to.Terminal() && stateMayHaveMaterialized(d.State):
		e.converge.markRevert(d.AppID, d.ID)
	}
```

（`d.State` 是迁移前的在行状态；`StateFailed` 的迁移点已核：`drive.go:385,403`、`bluegreen.go:211`、`admission.go:295`。）

判定函数（`converge.go`）：`stateMayHaveMaterialized(s deployment.State) bool` → `releasing`/`observing`/`rolling-back` 为真（`materialize` 的三个相位；`drive.go:185-249,275-308,329-379`）。
`clearRevert` 另在两个成功面调用：`convergeSucceed`（任何成功收敛都重锚账本）与 `TeardownApp`（`teardown.go:55-77`）。

- 读点：`compareSpecs` 开头

```go
	// 非成功终态的重放标记（裁决表行 10）：取消/失败的部署从未成为期望态，
	// 下一拍以基线重放回卷（值携带部署 ID → 第二次取消 = 新签名，可再触发）。
	if depID, ok := e.converge.revertMarked(appID); ok {
		signals[convergeSignalRevert.key("")] = "revert|" + depID
	}
```

- `signalVerdictInput` 把 `revert` 映射到 `CancelLegacy=true` → `verdictReplayBaseline` → `convergeActionable` 为真 → **走 fast 通道**（事件 + 审计同时在场）。

#### 1.3.6 golden 与测试件

- `internal/application/engine/testdata/converge-verdicts.golden` → 8 行（行号留空 05/09 以便读者看出非行）：

```text
01-spec-mismatch converge
02-absent-carrier converge
03-gen-deviation converge
04-rollout-stalled-terminal converge
06-active-deployment-incl-failed-rollback skip
07-frozen suspend
08-extra-carriers-in-domain converge
10-cancel-legacy-partial-issue replay-baseline
```

- `converge_test.go:78-92` 的 rows 表同步删 05/09 两行。

### 1.4 推荐与理由

- **sound**：表的每一行都有生产构造者（或明确标注无构造者），「改真实决策不红」的形态消失：改 `signalVerdictInput` 的字段映射 → 映射测红；改 `convergeVerdict` 的优先序/返回 → golden 红；删 `extra`/`revert` 信号产生点 → 行为测红（§1.5）。
- **成本**：D1 主体约 250 行（含 5 个新测试、2 个信号产生点、1 个 outcome 类型）。
- **回归风险**：集中在「新增 `extra` 信号改变收敛触发面」与「`revert` 标记让 Cancel 提前到下一拍」（均见 §1.7），不改变任何写动词语义。

### 1.5 验收锚（逐条可测）

| # | 测试 | 断言 | 命令 |
|---|---|---|---|
| A1 | `TestConvergeVerdictGolden`（改） | 8 行裁决值与顺序对齐 golden；golden 变更必须与 ADR §1 同 commit | `go test ./internal/application/engine -run TestConvergeVerdictGolden -count=1` |
| A2 | `TestConvergeVerdictSignalMapping`（新） | 6 类信号各造一份 `convergeSignals` → 断言恰好置位对应 input 字段且 verdict ∈ {converge, replay-baseline}；**把 `gen\|` 误映射到 `SpecMismatch` 即红** | 同上 `-run TestConvergeVerdictSignalMapping` |
| A3 | `TestConvergeVerdictSignalClassTotality`（新） | 遍历 `allConvergeSignalClasses()`：每类的 `key("x")` 能被 `signalVerdictInput` 识别；枚举新增而映射未加即红 | 同上 |
| A4 | `TestConvergeVerdictGateMapping`（新） | `gateVerdictInput(true,false)`→suspend；`(false,true)`→skip；`(false,false)`→steady；`(true,true)`→suspend（优先序） | 同上 |
| A5 | `TestConvergeVerdictFieldProducersComplete`（新） | 反射 `convergeVerdictInput` 字段名集 == `verdictFieldProducers` 表键集（双向保鮮：加字段不登记即红，表项无字段即红） | 同上 |
| A6 | 既有行为测（不改断言） | `TestConvergeOnSpecMismatchRestoresBaseline` / `TestAbsenceRebuildsMissingCarrier` / `TestGenLabelRollbackConverges` / `TestTerminalRolloutStallConverges` / `TestFreezeSuspendsConvergeUntilLift` / `TestConvergeSkipsDrivingAppIncludingFailedRollbackWindow` 全绿 → 证明表接进生产后行为不变 | `go test ./internal/application/engine -count=1`（整包复跑，避免表格内正则转义） |
| A7 | `TestExtraCarriersSignalConvergesDomainRemoval`（新，S12b-2） | 注入域内多余载体（fake `ensures` 里多一个 ID）→ 一拍即出 `extra` 信号并收敛（Ensure 调用 +1）；裁决输入含 `ExtraCarriers` | `go test ./internal/application/engine -run TestExtraCarriers -count=1` |
| A8 | `TestCancelRevertSignalDrivesBaselineReplay`（新，S12b-2） | Cancel 一个已物化的部署 → 下一拍 `driftScan` 即回卷（不断言慢通道 60s）；同时 `workload.converged` 1 条 + 审计 `workload.converge` 1 条 | `go test ./internal/application/engine -run TestCancelRevert -count=1` |
| A9 | `TestExtraSignalYieldsToDrivingGate`（新，S12b-2） | 部署在途时域内"多余"（在途新代）不发起收敛（`ensureDelta==0`）但信号可见 | 同上 |

### 1.6 ADR-0082 修订草案（原句 → 新句）

**§1 表（行 5 / 8 / 9 / 10）**

原句（`:37`）：
> | spec 相符 + stopped | **告警不收敛** | Ensure 走内容等价 skip = 无效动作；swarm 自负责任务重启；自动重启显式非目标（k8s CrashLoopBackOff 同构）——ADR-0022 decision 3 实质保留 |

新句：
> | spec 相符 + stopped | **告警不收敛（信号层非行）** | 该裁决**不由判定函数承载**：稳态看门狗只发 `workload.stopped` 事件、**不产收敛信号**（`drift.go:180-208`），「无信号即无动作」在信号产生层实现——`convergeVerdict` 无此分支，本行保留为产品决策记录。Ensure 走内容等价 skip = 无效动作；swarm 自负责任务重启；自动重启显式非目标（k8s CrashLoopBackOff 同构）——ADR-0022 decision 3 实质保留 |

原句（`:40`）：
> | 域内多余载体 | **Converge（顺带）** | Ensure 域内移除既有语义（swarm runtime 域内多余载体移除分支） |

新句：
> | 域内多余载体 | **Converge** | **`extra` 快信号**：期望集 = 基线 Revision 投影（与缺席检测同源），观测集 = `InspectWorkloads` 域内枚举（两 Provider 均按 `fleetly.managed=true` + 域选择器过滤）；差集非空即信号（签名键 `extra\|<ids>`）。动作仍是 Ensure 域内移除（swarm `runtime.go:217-225`、k3s `runtime.go:218-227`）；部署窗内由行 6 让位 |

原句（`:41`）：
> | 域外/无主孤儿观测 | **只登记** | WorkloadOrphaned「只登记永不自动删」原则不动（人裁保留） |

新句：
> | 域外/无主孤儿观测 | **只登记（未落码）** | **明确未落码**：观测面只有域内枚举（`InspectWorkloads` 带域选择器），平台看不见域外载体；`WorkloadOrphaned` 事件码在 eventcode 注册表不存在；不新增集群级列举（成本与 RBAC 面大、且"只登记"在用户可见面无消费方）。「只登记永不自动删」原则保留，但**没有登记面**——本行不得计入"表即断言" |

原句（`:42`）：
> | Cancel 遗留载体（终态行、部分下发） | **回卷 LatestSucceeded 基线** | 取消的部署从未成为期望态——缺席检测把遗留载体判为「期望集外」，收敛动作回卷基线；翻案 admission N0 在本场景的适用（见翻案登记） |

新句：
> | Cancel 遗留载体（终态行、部分下发） | **回卷 LatestSucceeded 基线** | 取消/失败/被抢占的部署从未成为期望态。**真实信号源** = 非成功终态且该行已进过物化相位（releasing/observing/rolling-back）时落的重放标记（`transition.go:transit` 钩子），下一拍由 `revert\|<deployment-id>` 快信号触发统一收敛原语（基线物化）；慢通道 60s 重放仍是兜底。**机制不是缺席检测**（遗留载体是"观测有、期望无"，缺席检测按其定义看不见）——原句机制描述错误，本修订更正。翻案 admission N0 在本场景的适用不变 |

**§1 表头（`:29`）**

原句：
> ### 1. 收敛裁决表（落码 + golden 钉死 = 本 ADR 承诺执法）

新句：
> ### 1. 收敛裁决表（8 判定行 + 2 非行；判定函数被生产路径真实调用 + golden 钉死）
>
> 十行中 **8 行**由单一判定函数 `convergeVerdict` 承载，并有两个生产构造点：信号侧 `signalVerdictInput`（6 类信号 → 行）与闸门侧 `gateVerdictInput`（冻结/驱动权 → 行）；`convergeStep` 与 `attemptConverge` 都真实调用它。行 5 为**信号层非行**（无信号即无动作），行 9 **明确未落码**。行 10 的裁决字面是 `replay-baseline`（动作与 `converge` 同为基线物化，差别只在触发来源与事实面标签）。

**§2 收敛动作（`:44-51`）**

原句（`:51`）：
> → `materialize(基线 Revision, 行 Generation, isolate=true)`（boundedStep 带界）→ 审计行 `source=system`（AfterFP = 基线 digest）+ `workload.converged` 事件。复用 `replayAppBaseline` 骨架；**不新建 Deployment 行**——收敛高频，部署行会成行垃圾（「收敛走 kind=reconcile 部署行」替代案已驳回）；审计走 SourceSystem 行而非部署 lineage。

新句：
> → `materialize(基线 Revision, 行 Generation, isolate=true)`（boundedStep 带界，回传 `capability.EnsureReport`）→ **事实面按真实变更分流**：
>
> 1. **审计行**（`SourceSystem` + `AfterFP` = 基线 digest + `Action=workload.converge` + `Detail` 携带 `{source, trigger, changed, removed}`）：**只要本次收敛真的写穿了内容等价判定**（`EnsureReport.Changed`/`Removed` 非空）**或是信号驱动（fast 源）**就发——不论触发源。等价于"系统改了你的运行时必留痕"；
> 2. **事件**（`workload.converged`，outbox 流面，payload 携带 `trigger` = 聚合信号签名）：**维持 fast 源才发**——慢通道/启动重放是例行节拍，进事件流即行垃圾；它们的真实变更由审计行承载（行 8/10 在补 `extra`/`revert` 快信号后本就落在 fast 源，事件自然在场）；
> 3. `Changed` 的语义是「provider 判定的内容变更」：创建 / 整更新 / 仅副本数 / 停摆 resume 计入；内容寻址 skip 不计；**k3s 冷账本重放（进程重启首拍、本地账本为空）不计**——该形态严格等于重启前已下发内容的幂等重放，计入会让每次重启产生 N 条假变更审计；
> 4. **不新建 Deployment 行**（原裁决不变）；`Removed` 元素是 `"<kind>/<name>"` 形态（被移除者没有期望 Workload ID）。
>
> 复用 `replayAppBaseline` 骨架；审计走 SourceSystem 行而非部署 lineage。

**§5 posture（`:67`）**

原句：
> `all`（缺省——检出漂移即收敛）| `detect`（只报事件不收敛，检测面照常）。配置键承载；detect 是收敛的观察期逃生门而非缺省。上站裁决：…

新句：
> `all`（缺省——检出漂移即收敛）| `detect`（**零运行时写**：只报事件，不收敛、不物化、不隔离、不重建载体）。配置键承载；detect 是收敛的观察期逃生门而非缺省。**detect 的准确语义**：
>
> 1. 门限对**全部触发源**生效——fast / slow / **启动基线重放** / peer 撤销的即时隔离重放，一律不写；
> 2. 启动面在 detect 下只**重建期望账本**（`ensuredSpec`：期望 spec + 归属 + 声明端口，投影与缺席检测同源），**不下发 Ensure**——账本重建是检测面与路由解析面（`resolveBackend` 读 `ensuredSpec`）的前提，不是收敛动作；
> 3. **成文降级**（detect 期间的检测面）：spec 失配 / 缺席 / 滚动停摆照常；**gen 偏离与稳态 stopped 看门狗在进程重启后失明**（gen 锚只能由 Ensure 回执的 wire-gen 提供，detect 不写 → 无回执），直到姿态切回 `all` 或发生一次部署；
> 4. **撤销 peer 的隔离在 detect 下不执行**（剥离是运行时写），被拦时落一条 Warn 日志（`convergeOutcomePostureDetect`）；恢复 `all` 后下一拍剥离。
>
> 上站裁决：…（原文保留）

**§6 账本纪律（`:69-73`）**

原句（三条 bullet）：
> - ensuredSpec/ensuredGen **只由 Ensure 写入，禁止观测播种**——…
> - **重启冷窗成文**：基线重放完成前 expected 为空 → 缺席检测盲，方向安全（漏报不误报），启动重放收口即闭合。
> - **succeeded 无期望缓存 → 收敛触发**：…

新句（保留三条，追加三条）：
> - （原文保留：只由 Ensure 写入、禁止观测播种）
> - **账本条目带写入部署归属**：每条 `ensuredSpec`/`ensuredGen` 记录写入它的 Deployment ID；drift 的期望侧**只采用写入者 == 当前 `LatestSucceeded` 行的条目**。未成功终态（Cancel/failed/superseded）的物化因此不再污染期望态——那是"上次下发了什么"，不是"期望什么"。**方向安全性**：不采用 = 跳过该载体的 spec/gen 对照（漏报不误报）。
> - **非成功终态 → 重放标记**：终态且该行已进过物化相位（releasing/observing/rolling-back）→ 落标记，下一拍 `revert` 快信号以基线重放重建账本（行 10 的兑现机制）；成功终态撤销标记。
> - **重启冷窗成文**（原文保留；detect 下的冷窗形态见 §5 第 3 条）
> - **succeeded 无期望缓存 → 收敛触发**（原文保留）
> - **detect 下的账本重建**：启动面在 detect 只写 `ensuredSpec`（投影，精确）+ `ensuredBy` 归属（写入者 = 基线行），**不写 gen 锚**——gen 面失明见 §5。

**验收锚（`:89`、`:97`）**

原句（`:89`）：
> - [x] 收敛裁决表十行落码并 golden 钉死（表即断言：每行一个判定函数 + golden 快照——convergeVerdict + testdata/converge-verdicts.golden）

新句：
> - [x] 收敛裁决表 **8 判定行**由单一判定函数承载并**被生产路径真实调用**：信号侧 `signalVerdictInput`（6 类信号，`allConvergeSignalClasses` 完备性守卫）+ 闸门侧 `gateVerdictInput` + `convergeStep`/`attemptConverge` 两处调用；golden 钉裁决值与优先序（`testdata/converge-verdicts.golden`，8 行、行号留空 05/09）；`verdictFieldProducers` 反射守卫双向保鮮（字段 ⇄ 生产者）
> - [x] 行 5（stopped 不收敛）登记为**信号层非行**；行 9（孤儿只登记）登记为**未落码**（观测面无域外载体、无 `WorkloadOrphaned` 事件码）；两项不得计入上方"表即断言"

原句（`:97`）：
> - [x] Cancel 遗留回卷 LatestSucceeded 基线钉测（TestCancelLegacyConvergesBackToBaseline——慢通道重放兑现回卷）

新句：
> - [x] Cancel/失败终态回卷 LatestSucceeded 基线钉测：非成功终态落重放标记 → 下一拍 `revert` 快信号回卷（`TestCancelRevertSignalDrivesBaselineReplay`），且回卷事实双真（`workload.converged` 事件 + `workload.converge` 审计）；`TestCancelLegacyConvergesBackToBaseline` 保留为慢通道兜底形态（去掉"兑现回卷"的唯一性表述）
> - [x] 行 8 域内多余载体钉测（`TestExtraCarriersSignalConvergesDomainRemoval`；部署窗内让位行 6：`TestExtraSignalYieldsToDrivingGate`）

### 1.7 风险/回归面

| 风险 | 影响 | 翻向方案 |
|---|---|---|
| `extra` 信号在 BG 收口/回滚窗制造额外收敛 | 假收敛 → 无谓 Ensure（内容等价，通常 no-op） | 优先序行 6 让位（窗内 skip）；窗后多余载体本就该移除（行 8 本意）；`TestGhostOldGenerationMemberNotRebuiltAfterBGCollection` 与 bluegreen 族复跑为门 |
| `extra` 把"在途新代载体"当多余 | 同上 | 同上；`TestConvergeSkipsDrivingAppIncludingFailedRollbackWindow` 复跑 |
| `revert` 标记在 queued 取消时也触发一次重放 | 一次 no-op Ensure | 标记条件限"已进物化相位"（`stateMayHaveMaterialized`）；`TestRevertMarkOnlyAfterMaterialization`（E3） |
| `revert` 在回滚成功后残留 | 一次多余重放 + 假事件 | 成功终态 `clearRevert`（§1.3.5） |
| 签名空源（R02-M1）被新信号放大 | 冷却解除径②误判 | `revert`/`extra` 信号值均携带内容（部署 ID / 载体 ID 集），不再造空 `agg`；R02-M1 本身**不在本批修**（记为 S12 之外） |
| 冻结/驱动权裁决合并改变副作用序 | 冷却释放/告警沿时序 | 只在锁内复查后合并两个 return；`TestFreezeSuspendsConvergeUntilLift`、`TestConvergeCooldownLifecycle` 复跑 |
| golden 更新漏做 | 守卫红 | `go test ./internal/application/engine -run TestConvergeVerdictGolden -update` 与 ADR 同 commit |

**会变红的既有测试（预期）**：`TestConvergeVerdictGolden`（golden 变 8 行）；`TestPostureDetectSkipsConverge`（见 D3）。其余 D1 相关行为测**不得**变红。

> D3（detect 零运行时写）的 **Lead 裁决原文**记录在 §3.7（本表只覆盖 D1 风险面）；裁决结论同时写入 §0 决策表、§8 批 3 注记与 ADR-0082 §5。

---

## 2. D2 —— R16-M6/R02-M5：慢/基线收敛的静默面事实化

### 2.1 问题复述

`convergeSucceed`（`converge.go:398-437`）在 `:416-418` 对非 fast 源直接 return：慢通道（60s 强制重放）与启动基线重放**可以改载体而零事件零审计**。受影响的面（R16-M6 已列）：行 8 域内多余载体移除、行 10 Cancel 回卷、peer 撤销隔离、启动基线重放、稳态每 60s 重放中"环外改动的纠正"。R02-M5 是其子集（Cancel 回卷是用户可感知的状态回退却无痕）。

根因不是"忘了发"，而是**没有"是否真的改了"这一事实**：provider 的 `EnsureReport`（`capability/runtime.go:282-289`）只有 wire-gen，没有变更集，引擎无法区分"重放命中内容等价 skip"与"真的滚了载体"。

### 2.2 候选方案

| 案 | 内容 | sound | 成本 | 回归风险 |
|---|---|---|---|---|
| **A（推荐）** | `EnsureReport` 扩 `Changed []string` + `Removed []string`（provider 侧精确判定）；`materialize` 回传回执；`convergeSucceed` 按 **fast源 ∨ 真实变更** 发审计（Detail 带变更集），事件维持 fast-only | 高：事实来自写动词回执，不靠事后观测比对 | M（引擎 ~80 行 + 双 provider ~40 行 + 假件 + 测试） | 中：provider 三处跳过分支的归类需逐条钉测 |
| B | 引擎侧 pre/post 观测比对（`InspectWorkloads` 前后各一次） | 低：两次读之间可被外部改（竞态）、成本翻倍、provider 语义不参与 | M | 高（假阳性/漏报不可控） |
| C | 给事件加 `trigger=slow\|baseline` 并允许慢通道发事件 | 中：可发现性达成但事件流被例行节拍污染 | S | 中（事件消费者/告警面噪音） |

**推荐 A**：变更事实只有写动词自己知道。B 的比对面与时序不可靠（收敛动作本身就是"读→写"，前后读窗口里外部改动无法归因）。C 被否：慢通道每 60s 一次，事件流是操作者时间线，例行重放进流即行垃圾；可发现性由审计面承担（审计是取证面，本来就是"系统动了什么"的归宿）。

**事件 vs 审计的取舍（Lead 要求给取舍）**：事件**维持 fast-only**。理由：(i) 行 8/10 补真实信号后本就落在 fast（事件在场，R02-M5 的可发现性诉求已被 D1 满足）；(ii) 慢通道的真实变更只剩"60s 内环外改动被纠正"与"启动重放纠正重启前的漂移"两类，审计足以取证；(iii) 若未来要求"任何真实变更都进事件流"，改法是把 gate 从 `src == fast` 改成 `src == fast || HasMaterialChange()` 并给 payload 加 `trigger` 字段——成本 S，但会改变现有 13 条 e2e/apitest 的事件计数预期（R12-M4 已记），本批不做。

### 2.3 精确改动清单

#### 2.3.1 端口（`internal/domain/shared/capability/runtime.go:282-301`）

```go
type EnsureReport struct {
	Gens map[string]Generation
	// Changed 是本次 Ensure 真实写穿内容等价判定的载体（Workload ID，升序
	// 去重）：创建 / 整更新 / 仅副本数 / 停摆 resume 计入；内容寻址 skip 不计；
	// k3s 冷账本重放不计（见 ADR-0082 §2 第 3 条）。
	Changed []string
	// Removed 是本次 Ensure 从域内移除的既有载体（"<kind>/<name>"，升序去重）
	// ——被移除者没有期望 Workload，故不用 ID（ADR-0082 §2 第 4 条）。
	Removed []string
}

// HasMaterialChange 报告本次 Ensure 是否真的改了运行时（审计分流的唯一判据）。
func (r EnsureReport) HasMaterialChange() bool {
	return len(r.Changed) > 0 || len(r.Removed) > 0
}
```

#### 2.3.2 swarm（`internal/infrastructure/providers/swarm/runtime.go:40-227`）

| 分支 | 行号 | Changed/Removed |
|---|---|---|
| create（`ServiceCreate` 后、`continue` 前） | `:156-163` | `report.Changed = append(..., w.ID)` |
| no-op 断路器短路 + `resumeStalledRollout` 返回 `resumed=true` | `:170-177` | `append(w.ID)`（resume 就是一次真实更新下发） |
| `serviceSpecEqualJSON` 等价 + resume 真 | `:187-193` | `append(w.ID)` |
| scale-only（`updateServiceCAS` 后） | `:204-209` | `append(w.ID)` |
| 内容变更整更新（`updateServiceCAS` 后） | `:211-214` | `append(w.ID)` |
| 域内 stale 移除（`ServiceRemove` 成功后） | `:218-225` | `report.Removed = append(..., "service/"+svc.Spec.Name)` |

实现细节：`wireGen` 闭包旁加 `changed := func(wid string) { report.Changed = append(report.Changed, wid) }`；`Ensure` 返回前统一 `sort.Strings` + 去重（同 ID 的 create 分支只走一次，但 resume 分支与 scale 分支互斥，保险起见仍归一）。

#### 2.3.3 k3s（`internal/infrastructure/providers/k3s/runtime.go:41-264`、`:1011-1033`）

- `planContentAddressed` 返回值扩为 `(writeGen uint64, skip, coldLedger bool)`：`lastA == ""` 且 `serverObjectGen > 0` 的分支（`:1027-1031`）置 `coldLedger=true`。
- 主循环（`:177-196`）：`if !skip && !coldLedger { report.Changed = append(..., w.ID) }`。
- 域内 stale 移除（`:220-227`）：`report.Removed = append(..., obj.kind+"/"+obj.name)`。
- Service 收敛（`:241-252`）：`report.Removed = append(..., "service/"+svc.Name)`。
- 冷账本分支**不计**（理由见 §2.5 风险表与 ADR §2 第 3 条）。

#### 2.3.4 假件

- `internal/application/engine/fakeruntime_test.go:126-156`：新增注入面

```go
	// reportChanged/reportRemoved 是 EnsureReport 变更集注入面（审计分流钉测）：
	// 非空即按此回执（未注入 = 空 = 无真实变更，与真 Provider 的 skip 形态同义）。
	reportChanged []string
	reportRemoved []string
```

并在 `Ensure` 返回处 `capability.EnsureReport{Gens: ..., Changed: f.reportChanged, Removed: f.reportRemoved}`。
- `internal/application/engine/render_test.go:113`（inspectlessRuntime）：保持返回零值报告（不需要变更）。
- `internal/testsupport/apitest/harness.go:562-570`：保持零值（R12-M7 已知：全栈夹具不可达；本次不扩 apitest，理由记 §9）。

#### 2.3.5 引擎（`materialize.go` / `converge.go` / `drive.go` / `bluegreen.go`）

```go
// materialize.go:39
func (e *Engine) materialize(ctx context.Context, d *deployment.Deployment, revision string, gen uint64, isolate bool) (capability.EnsureReport, error) {
	...
	report, err := e.runtime.Ensure(...)
	if err != nil {
		return capability.EnsureReport{}, fmt.Errorf("runtime ensure: %w", err)
	}
	...
	e.recordEnsured(d, gen, ws)
	return report, nil
}
```

5 个调用点（`bluegreen.go:205`、`drive.go:212,301,354`、`converge.go:350`）改 `_, err :=`；仅 `converge.go:350` 消费回执。

> **为什么 `materialize` 回传整个 `EnsureReport` 而不是 `changed bool`**（Lead 倾向前者，此处给取舍）：审计 `Detail` 要给出**改了什么**（变更载体集/移除集），bool 只能给"改了"；回传回执 = 零新类型、零二次计算，且 `HasMaterialChange()` 就是 bool 的等价面（`convergeSucceed` 的分流判据）。代价是 `capability` 类型出现在 5 个调用点的返回值里——这层本来就在 `materialize` 的签名依赖内（`Ensure` 在同一文件），无分层新增。

```go
// converge.go:402
func (e *Engine) convergeSucceed(ctx context.Context, appID, sig, agg string, d *deployment.Deployment, src convergeSource, rep capability.EnsureReport) {
	// ...（备忘/签名/冷却/告警沿，原样）...
	if src != convergeSourceFast && !rep.HasMaterialChange() {
		return // 无真实变更的例行重放：零事实（审计不发，事件本就不发）
	}
	rev, err := e.repos.Revisions.Get(ctx, d.ToRevision)
	digest := ""
	if err == nil {
		digest = rev.Digest
	}
	team := e.eventTeamOfApp(ctx, e.repos, appID)
	fact := writes.FactRow{
		Name: eventWorkloadConverged, Aggregate: "app", ID: appID,
		Payload: convergeEventPayloadJSON(appID, d.ToRevision, d.Generation, agg, ""),
		Action:  "workload.converge", Resource: "app/" + appID,
		AfterFP: digest, Source: audit.SourceSystem, TeamID: team,
		Detail: convergeAuditDetailJSON(src, agg, rep),
	}
	if src != convergeSourceFast {
		// LedgerOnly 偏离理由（ADR-0082 §2 第 2 条）：慢通道/启动重放的
		// **真实变更**只落取证面——事件面是操作者时间线，例行节拍（60s
		// 强制重放）进流即行垃圾；R02-M5 的 Cancel 可发现性由行 10 的
		// revert 快信号承担（事件 + 审计同时在场）。
		fact.LedgerOnly = true
		fact.Name, fact.Payload = "", nil
	}
	if err := writes.Emit(ctx, writes.Repos{Outbox: e.repos.Outbox, Audits: e.repos.Audits}, fact); err != nil {
		e.log.Error("converge: emit fact", "app", appID, "err", err)
	}
}

// convergeAuditDetailJSON 构造收敛审计的 Detail（ADR-0049 面）：
// {source, trigger, changed, removed}——"改了什么"的取证锚（AfterFP 只给基线
// digest，不给变更集）。
func convergeAuditDetailJSON(src convergeSource, agg string, rep capability.EnsureReport) string

// convergeSource.String 返回 "fast"/"slow"/"baseline"（审计/日志字面）。
func (src convergeSource) String() string
```

> 守卫交互（F5）：`factdual` 只执法 `LedgerOnly: true` 字面量，上述为表达式赋值不会踩守卫；但按守卫意图已写偏离理由注释（上面这段），实现时不得删。

### 2.4 验收锚

| # | 测试 | 断言 | 命令 |
|---|---|---|---|
| B1 | `TestEnsureReportChangedOnContentChange`（swarm/k3s 双侧，扩展现有 `TestEnsureContentChangeRollsWithNewGen`） | 内容变更 → `Changed == []string{changed.ID}`；`Gens` 语义不变 | `go test ./internal/infrastructure/providers/swarm -run TestEnsureContentChangeRollsWithNewGen -count=1`；k3s 同名 |
| B2 | `TestEnsureReportUnchangedOnContentEquivalentSkip`（双侧，扩展 `TestEnsureContentEquivalenceSkipsUnchangedAcrossGen`） | 跨代未变 → `Changed` 空、`Removed` 空；wire-gen 仍回传 | 同上（同名测） |
| B3 | `TestEnsureReportReplicasOnlyCountsAsChanged`（扩展 `TestEnsureReplicasOnlyScalesPreservingTemplate`） | 仅副本数 → `Changed == [wid]` | 同上 |
| B4 | `TestEnsureReportRemovedStaleCarrier`（swarm 扩 `runtime_ensure_test.go`；k3s 新建） | 域内 stale 服务/对象被移除 → `Removed == ["service/<name>"]`（k3s 对象形态 `"deployment/<name>"`） | `go test ./internal/infrastructure/providers/swarm -run 'TestEnsure' -count=1`；k3s 同 |
| B5 | `TestEnsureColdLedgerReplayNotReportedChanged`（k3s，扩展 `TestEnsureColdLedgerReplayPinsServerGen`） | 冷账本重放 → `Changed` 空（与 ADR §2 第 3 条一致） | `go test ./internal/infrastructure/providers/k3s -run TestEnsureColdLedger -count=1` |
| B6 | `TestConvergeSlowChannelMaterialChangeWritesAudit`（engine，新） | 快通道收敛一次成功后，外部再改 → 慢通道重放（`-update` 不适用；用 fake `reportChanged` 注入或真 tamper）→ 审计 `workload.converge` 1 条、`workload.converged` **0** 条（慢通道）；审计 `Source==system`、`AfterFP==rev.Digest`、`Detail` 含 `"source":"slow"` 与变更集 | `go test ./internal/application/engine -run TestConvergeSlowChannel -count=1` |
| B7 | `TestConvergeSlowChannelNoChangeWritesNoFact`（engine，新） | 纯例行重放（无变更）→ 审计 0、事件 0 | 同上 |
| B8 | `TestConvergeBaselineReplayMaterialChangeWritesAudit`（engine，新） | 重启后基线重放且回执含变更 → 审计 1（LedgerOnly）、事件 0 | `go test ./internal/application/engine -run TestConvergeBaseline -count=1` |
| B9 | `TestConvergeFastChannelKeepsEventAndAudit`（扩展 `TestConvergeOnSpecMismatchRestoresBaseline`） | fast 源仍 1 事件 + 1 审计；审计 Detail `"source":"fast"` | `go test ./internal/application/engine -run TestConvergeOnSpecMismatch -count=1` |
| B10 | `TestConvergeAuditDetailCarriesChangedSet`（engine，新） | Detail JSON 可解析、`changed`/`removed` 与回执一致（不引入 key 顺序脆弱性——只断言 map 内容） | 同上 |

### 2.5 ADR-0082 修订草案

见 §1.6 的「§2 收敛动作」新句（本决策的全部 ADR 文本都在那一块，不重复）。补充 `eventcode` 面：

- `internal/domain/shared/model/eventcode/events.go:52` 的 `workload.converged` Summary 追加一句：
  > "…; material reconvergence of any trigger source (slow replay, startup baseline replay) writes an audit row with the changed carrier set, while the event stream stays signal-driven."
  同 commit 跑 `go test ./internal/domain/shared/model/eventcode -update`（`testdata/events.golden`）。
- `workload.converge_failed` 不动。

### 2.6 风险/回归面

| 风险 | 影响 | 翻向方案 |
|---|---|---|
| swarm 的 no-op 断路器分支漏判 `resumed` | 漏审计（真改了没记） | B1/B3 覆盖 update 分支；新增 B4 覆盖 resume（扩 `TestEnsureResumesStalledRolloutOnce` 断言 `Changed`） |
| k3s 冷账本计入 → 每次重启 N 条假审计 | 审计行垃圾（系统重启风暴） | B5 钉死"不计"；ADR §2 第 3 条成文 |
| **冷窗真实变更可能漏审计**（k3s 冷账本 + 无快信号） | 一次真实改动无审计（方向安全：只是少一条取证行） | 已知且成文（ADR §2 第 3 条）；实现后由"下一拍信号驱动"与"下次真实变更审计"兜底；若要求零遗漏，改法是冷账本分支做一次服务端内容比对——成本 M，不在本轮 |
| 审计 Detail 引入不可控 JSON 面 | 审计查询面/大小 | 只放四个短字段（source/trigger/changed/removed），不放开指针/时间 |
| `materialize` 签名改动漏改调用点 | 编译错（安全） | 5 处逐一列于 §2.3.5 |
| 假件注入缺失导致新测不可写 | 测试空转 | fakeruntime 注入面（§2.3.4） |
| apitest 夹具恒空回执 → 全栈面不可达 | 仅测试覆盖缺口 | 记入 §9 明确不做（R12-M7 既有）；engine 层新测已覆盖分流逻辑 |

**会变红的既有测试（预期）**：无（端口字段只增、事件面不变、审计只增信）。`TestConvergeOnSpecMismatchRestoresBaseline` 的审计计数断言（1 条）保持不变（fast 源仍发）。

---

## 3. D3 —— R16-M2：`posture=detect` 语义收口（detect = 零运行时写）

### 3.1 问题复述

`attemptConverge:299-301` 只拦非 baseline 源：`if src != convergeSourceBaseline && Posture == detect`。两处 baseline 源会**照写**：

- 启动基线重放：`drift.go:86` → `replayAppBaseline`（`:94-96`）；
- peer 撤销的即时隔离：`peers.go:166`；每拍兜底 `enforcePeerIsolation`（`:213-215`）。

而慢通道（`convergeStep` 的 slow 源）被同一门限拦住 → Cancel 遗留回卷在 detect 下**永不发生**（R16-M2 的反向后果）。ADR-0082 §5（`:67`）与 CONTEXT `Posture`（`:286-287`）都只写"只报事件不收敛"，例外没有法源（代码用"ADR-0005 旧行为保留"作为理由，而 ADR-0005 的 opt-in 条款正是 ADR-0082 推翻的那条）。

**关键核实（Lead 要求）——检测面是否依赖 baseline 重放**：

| 检测面 | 数据源 | 去掉 baseline 的 Ensure 后 |
|---|---|---|
| 缺席检测 | `projectRevision(LatestSucceeded)`（`converge.go:210-254`） | **不受影响**（不读账本） |
| spec 失配（扫描面 + Watch 面） | `ensuredSpecOf`（`drift.go:259-263`；`observ.go:262`） | **失明**——除非重建 `ensuredSpec` |
| gen 偏离（扫描面 + Watch 面 + stopped 看门狗） | `ensuredGenOf` / `expect.expected`（`drift.go:289-299`；`observ.go:256-264`；`drift.go:187-208`） | **失明**（gen 锚只能由 Ensure 回执的 wire-gen 提供） |
| 滚动停摆 | 观测的 `RolloutStalled`（`drift.go:272-285`） | **不受影响** |
| 路由后端解析 | `routeExpectations` 读 `ensuredSpec`/`ensuredGen`（`observdomain.go:184-222`）+ `routepublish.go:136-152` | **用户路由全失**（F1）——这是硬约束，不可接受 |
| peer 隔离不变式 | `obs.crossProjectRefs()` 读 `ensuredSpec`（`observdomain.go:259-274`） | **失明**（连"该剥离"都算不出来） |

结论：**detect 不能"什么都不做"**。启动面必须至少重建 `ensuredSpec` + 归属（路由与隔离不变式的输入面），而 gen 锚无法在不写载体的前提下取得——这是必须成文的降级。

### 3.2 候选方案

| 案 | 内容 | sound | 成本 | 回归风险 |
|---|---|---|---|---|
| **A（推荐）** | detect = 零运行时写（删 baseline 豁免）；启动面改为「只重建期望账本」：投影 → `recordProjectedSpecs`（归属 + `ensuredSpec`，不写 gen），路由/隔离输入面恢复；gen 面成文失明；隔离被拦即 Warn | 高：语义与词条一致，"观察期不写"名副其实 | M（~90 行 + 4 测） | 中：gen/stopped 检测在 detect 下失明（成文）；隔离在 detect 下不执行（安全相关，必须文档+日志） |
| B | 保留 baseline 豁免，在 ADR/代码注释显式声明「缓存重建非收敛」；慢通道仍被拦 | 低：detect 仍会**静默改载体**（R16-M2 的核心指控原样保留），且"只报事件不收敛"成为假法 | S | 低 |
| C | 删除 detect（只留 `all`） | 中：语义最干净，但配置键/词条/runbook 回退面大，且 staging 换装期可能需要观察姿态 | S（代码） | 中（配置面破坏性） |

**推荐 A**，并把 B 否决理由写进 ADR：B 的"缓存重建非收敛"是有歧义的托辞——对操作者而言 `detect` 的承诺是"不动我的载体"，而不是"动但不叫收敛"。C 的收益是省掉一套语义，但会让"观察期"只能靠冻结（freeze）表达，而冻结会拦部署（语义更重），不划算。

### 3.3 精确改动清单

#### 3.3.1 闸门（`converge.go:296-301`）

```go
	// posture 门（ADR-0082 §5）：detect = 零运行时写——门限对 fast / slow /
	// 启动基线重放 / peer 撤销隔离一视同仁（原 "baseline 豁免" 条款废止：
	// 它让观察期在启动与撤销 peer 两个场景静默改载体）。
	if e.opts.Converge.Posture == ConvergePostureDetect {
		return convergeOutcomePostureDetect
	}
```

#### 3.3.2 启动面（`drift.go:51-88` + `observdomain.go` + `converge.go`）

```go
// rebuildBaselines：posture=detect 时改走 rebuildBaselineExpectations。
	for _, a := range apps {
		...
		if e.opts.Converge.Posture == ConvergePostureDetect {
			if err := e.rebuildBaselineExpectations(ctx, a.ID, d); err != nil {
				e.log.Error("baseline expectations: rebuild", "app", a.ID, "err", err)
			}
			continue
		}
		e.replayAppBaseline(ctx, &a, d)
	}
```

```go
// rebuildBaselineExpectations 在 posture=detect 下重建期望账本而不下发载体
// （ADR-0082 §5 第 2 条）：投影与缺席检测同源（projectRevision + 代戳），
// 只落归属 + ensuredSpec（期望 spec/端口）。不落 gen 锚——wire-gen 只有
// Ensure 回执可知，detect 不写就没有回执（gen/stopped 面成文失明）。
// Route 解析（routepublish.resolveBackend）与 peer 隔离不变式
// （obs.crossProjectRefs）读 ensuredSpec —— 本函数的第二存在理由：不重建
// 则 detect 下用户路由全失。
func (e *Engine) rebuildBaselineExpectations(ctx context.Context, appID string, d *deployment.Deployment) error {
	appMu := e.lockApp(appID)
	appMu.Lock()
	defer appMu.Unlock()
	if e.converge.teardownMarked(appID) {
		return nil
	}
	driving, err := e.repos.Deployments.DrivingByApp(ctx, appID)
	if err != nil {
		return err
	}
	if len(driving) > 0 {
		return nil // 驱动权在部署链：其 materialize 会写账本
	}
	ws, err := e.expectedWorkloads(ctx, d)
	if err != nil {
		return err
	}
	e.obs.recordProjectedSpecs(appOwner(appID), d.ID, ws)
	return nil
}
```

```go
// observdomain.go 新增
// recordProjectedSpecs 写 detect 期期望账本（归属 + 期望 spec + 归属部署）：
// 投影来自基线 Revision，写入者即基线行（D4 的采用规则据此放行）——**不写
// ensuredGen**（gen 锚只能由 Ensure 回执提供，detect 不写就没有回执）。
func (o *observations) recordProjectedSpecs(owner workloadOwner, deploymentID string, ws []capability.Workload) {
	o.mu.Lock()
	for _, w := range ws {
		o.workloadApp[w.ID] = owner
		o.ensuredSpec[w.ID] = w
		o.ensuredBy[w.ID] = deploymentID
	}
	o.mu.Unlock()
}
```

> 与 D4 的衔接：detect 账本条目**必须**写 `ensuredBy = d.ID`，否则 D4 的"写入者==当前基线"采用规则会把它们全部跳过 → detect 下 spec 漂移也失明（与 C4 冲突）。`rebuildBaselineExpectations` 调用处传 `d.ID`。

#### 3.3.3 gen 对照的锚在场守卫（`drift.go:289-299`）

```go
		// 逐载体 gen 锚（ADR-0048 决策 2）：**必须**有 Ensure 回执的 wire-gen。
		// 无锚（detect 期望账本 / 冷窗）→ gen 面跳过（安全侧：漏报不误报）——
		// 原 "回落 App 级 expected" 分支在 detect 账本下会把 0 当锚，令全部
		// 载体产假 gen 偏离，故删除。
		g, hasGen := e.obs.ensuredGenOf(o.WorkloadID)
		if !hasGen {
			continue
		}
		if uint64(o.Generation) != g {
			signals[convergeSignalGen.key(o.WorkloadID)] = fmt.Sprintf("gen|%d|%d", g, uint64(o.Generation))
			continue
		}
```

#### 3.3.4 隔离面的可发现性（`peers.go:166,213-215`）

```go
	if out := e.replayAppBaseline(ctx, &apps[i], d); out == convergeOutcomePostureDetect {
		e.log.Warn("peer isolation skipped: convergence posture is detect (no runtime writes); lift the posture or freeze-lift to isolate",
			"app", apps[i].ID, "network", net.Name)
	}
```

`IsolateNetworkPeer` 的注释同步：detect 下撤销不剥离在服附件（恢复 `all` 后下一拍剥离）。

#### 3.3.5 词条与配置注释

- `CONTEXT.md:286-287`：`detect`（**零运行时写**：只报事件，不收敛、不物化、不隔离；启动面只重建期望账本；gen/stopped 检测与 peer 隔离在 detect 期间降级——见 ADR-0082 §5）。
- `options.go:250-252` 注释同款一句。

### 3.4 验收锚

| # | 测试 | 断言 | 命令 |
|---|---|---|---|
| C1 | `TestPostureDetectWritesNothing`（**替换** `TestPostureDetectSkipsConverge`，`converge_test.go:515-537`） | detect 下：漂移注入后一拍 → `len(rt.calls())` 不变（fast+slow 均不写）；`rebuildBaselines` 亦不调 Ensure；`workload.drift_detected` 照发 | `go test ./internal/application/engine -run TestPostureDetect -count=1` |
| C2 | `TestPostureDetectRebuildsExpectationsWithoutEnsure`（新） | detect + 新引擎 `rebuildBaselines` → Ensure 0 次；`ensuredSpecOf(app-web)` 存在且 image==基线；`ensuredGenOf` **缺席** | 同上 |
| C3 | `TestPostureDetectPublishesRoutesFromExpectationCache`（新） | detect + 新引擎 + 一条 Route 行 → `publishRoutes` 后 fake `Addresses` 被调用且返回端点（期望集来自 `routeExpectations`），`proxy.PublishRoutes` 收到该 route | `go test ./internal/application/engine -run TestPostureDetectPublishes -count=1` |
| C4 | `TestPostureDetectSpecDriftStillDetected`（新） | detect + 账本重建后 tamper 镜像 → 扫描面 `workload.drift_detected` 1 条、无 Ensure | 同上 |
| C5 | `TestPostureDetectIsolationSkipsWithWarn`（新，扩 `peers_test.go`） | detect 下 `RevokeNetworkPeer` → 隔离重放被拦（Ensure 0）、日志 Warn 含 "posture is detect"；切回 all 后一拍剥离（Ensure ≥1） | `go test ./internal/application/engine -run TestPostureDetectIsolation -count=1` |
| C6 | `TestPostureDetectGenDriftBlindDocumented`（新） | detect 账本重建后注入 gen 0（tamper）→ **不产** `gen\|` 信号、不调 Ensure（把"失明"钉成显式期望，反向：切回 all 后一拍收敛修复） | 同上 |

### 3.5 ADR-0082 / CONTEXT 修订草案

见 §1.6 的「§5 posture」新句（完整替换块）。验收锚追加：

> - [x] `posture=detect` = **零运行时写**（fast/slow/启动基线重放/peer 隔离四源同受门限）：`TestPostureDetectWritesNothing`；启动面只重建期望账本（`TestPostureDetectRebuildsExpectationsWithoutEnsure`）、路由仍可解析（`TestPostureDetectPublishesRoutesFromExpectationCache`）；成文降级两条（gen/stopped 面失明 `TestPostureDetectGenDriftBlindDocumented`；隔离不执行 `TestPostureDetectIsolationSkipsWithWarn`）

### 3.6 风险/回归面

| 风险 | 影响 | 翻向方案 |
|---|---|---|
| detect 下重启后 gen/stopped 检测失明 | 观察期可能漏报 gen 回退/停止 | 成文（ADR §5 第 3 条 + CONTEXT）；切回 `all` 或任何一次部署即恢复；C6 把失明钉成显式期望（反向断言"切回后修复"） |
| detect 下 peer 撤销不剥离 | 已撤销的跨项目网络访问延续到恢复 all | 成文 + Warn 日志；ADR 明确"detect 期间应配合 freeze/人工处置"；**不**做成 fail-closed（撤销已经落账，剥离是收敛动作，detect 的承诺就是不动） |
| detect 账本重建漏了 `ensuredSpec` | 路由全失（F1 形态） | C3 钉死；实现顺序要求「先 recordProjectedSpecs 再开 routeReady」——`lifecycle.go:93-95` 的开门顺序天然满足（重建在同一函数内先于 `routeReady.Store(true)`） |
| 账本重建与并发部署交错 | 账本被旧投影覆盖 | 复用 `lockApp` + `DrivingByApp` 复查（与 `replayAppBaseline` 同锁同口径），C1/C2 覆盖 |
| 冷账本形状变化影响 `TestStartupBaselineReplayRebuildsDriftCache` | 该测在 posture=all（缺省）下不变 | 只跑 detect 变体为 C2；原测不改 |

**会变红的既有测试（预期）**：`TestPostureDetectSkipsConverge`（被 C1 替换）；其余不变。

### 3.7 Lead 裁决（2026-10-19，S12b2 先延后、后裁决执行）

> **D3（detect 零写）本轮不做**；在 ADR-0082 §5 与本文档中成文为「已知降级 + staging 验证后另立批」；**不得改 detect 行为**。

据此本批只做：ADR-0082 §5 的现状/降级/待办三条成文（当前 baseline 重放不受门限 = 已知降级；D3 落地时必须接受的 gen/stopped 失明、撤销 peer 不剥离、路由依赖 `ensuredSpec` 三条降级同批成文）；代码侧保留 `attemptConverge` 的 baseline 豁免、`options.go` 的已知降级注释与现有测试不动。§3.3 的代码清单（`rebuildBaselineExpectations`/`recordProjectedSpecs`/gen 锚守卫/隔离 Warn）与 §3.4 的 C1-C6 钉测顺延到 D3 批；§3.1 的检测面依赖核实仍是那一批的施工依据。

> **后续裁决（2026-10-19，D3 批）**：Lead 复核后裁决**执行 D3，不再延后**——按本文档 §3.3 精确清单与 §3.4 C1-C6 实施；上述「顺延」表述仅作为裁决史保留，最终语义以 ADR-0082 §5 与 §3.3 为准。

---

## 4. D4 —— R16-M3：期望账本的归属与失效

### 4.1 问题复述

`materialize` 在 releasing 相位就 `recordEnsured`（`materialize.go:75-85`），与部署终态无关；`admission.Cancel`（`:284-308`）只清 releasing 备忘（`:304`），不清账。drift 的 spec/gen 期望侧直接读账本（`drift.go:243-245,259-263,289-299`）。

后果（R16-M3 已复现于代码路径）：rolling 形态 Cancel 后，账本里是**被取消部署的 gen2 + 新 spec**，观测（gen2/新 spec）与账本一致 → spec/gen 两路零信号；遗留载体是"观测有、期望无"的多余载体，缺席检测按其定义看不见 → 只能等 60s 慢通道（detect 下永不）。BG 形态因代次化 ID 会命中缺席信号，是例外。

同时 Cancel 的语义缺口在 `admission.go:279-283` 仍写"N0 不自动拆除"——与 ADR-0082 的翻案相反。

### 4.2 候选方案

| 案 | 内容 | sound | 成本 | 回归风险 |
|---|---|---|---|---|
| **A（推荐）** | ① 账本条目带**写入部署 ID**，drift 期望侧只采用「写入者 == 当前 `LatestSucceeded`」的条目（不采用=跳过，漏报不误报）；② 非成功终态（已进物化相位）落**重放标记** → 下一拍 `revert` 快信号以基线重放重建账本 | 高：把"期望态真源"从"最近写过的"改回"当前生效部署写的"，并用真实信号补齐响应性 | M（~60 行 + 5 测） | 中：部署窗内 spec/gen 对照收窄到基线条目（见下） |
| B | `admission.Cancel`/失败终态**显式删条目** | 低：条目是 last-write-wins，删掉被取消部署的条目**不会**恢复基线条目（已被覆盖），账本反而空 → 只能立刻重放，等价于 A②但少了 A① 的防污染 | S | 中（空账本 = spec/gen 全盲直到重放） |
| C | spec/gen 对照改走 `projectRevision(LatestSucceeded)` 重投影（与缺席同源） | 中：spec 面可行，gen 面不可行（wire-gen 只有 Ensure 回执可知）；且**部署窗内必然"失配"→ 每次部署都发 `workload.drift_detected`** | M | 高（事件面噪音） |

**推荐 A**。否决 B：删除无法恢复基线内容（last-write-wins 已覆盖），只会把"脏账本"换成"空账本"，失明面不变。否决 C：gen 面无解 + 部署窗假 drift（`drift.go:300-343` 会为此发事件）。

**部署窗收窄的显式取舍**：A① 生效后，在途部署写下的条目不属于当前基线 → 该窗内这些载体的 spec/gen 不参与对照。语义上这是**权利与责任一致**：窗内驱动权在部署链（裁决行 6），L1/L2 门在执法；窗口结束后基线更替、条目归属新基线，对照自动恢复。窗口内人工改载体不会被 drift 面看见（≤ 一个部署窗，通常 ≤ 3 分钟）；这是可接受且必须成文的。

### 4.3 数据流

```
materialize(d, rev, gen, isolate)
  └─ Ensure → report
  └─ recordEnsured(d, gen, ws)
        └─ expect.expected[app] = gen
        └─ obs.recordSpecs(appOwner, d.ID, gen, ws)   ← 新增 d.ID
              ├─ workloadApp[id] = owner
              ├─ ensuredGen[id]  = gen
              ├─ ensuredSpec[id] = w
              └─ ensuredBy[id]   = d.ID              ← 新增（账本归属）

drift.compareSpecs(appID, obs, d=LatestSucceeded)
  ├─ absent/extra：投影期望集 vs 观测（不读账本）
  └─ per carrier: ensuredSpecOf(id) 存在 且 ensuredByOf(id) == d.ID 才采用
        ├─ 是 → spec 对照 +（有 ensuredGen 时）gen 对照
        └─ 否 → 跳过该载体的 spec/gen（安全侧）

detect（另一条账本写径，不写载体）
  └─ rebuildBaselineExpectations(appID, d)
        └─ expectedWorkloads(d)            ← 与缺席/extra 同一投影 + memo
        └─ obs.recordProjectedSpecs(app, d.ID, ws)
              ├─ workloadApp[id] = owner
              ├─ ensuredSpec[id] = w        （期望 spec + 声明端口 → 路由解析/隔离输入）
              ├─ ensuredBy[id]   = d.ID     （D4 采用规则放行）
              └─（不写 ensuredGen / expect.expected → gen 面成文失明）
```

### 4.4 精确改动清单

#### 4.4.1 `observdomain.go`

```go
type observations struct {
	...
	// ensuredBy 是账本的写入部署归属（workloadID → Deployment ID）：drift
	// 期望侧只采用写入者 == 当前 LatestSucceeded 的条目（ADR-0082 §6）。
	// 写点两个：recordSpecs（Ensure 路径，materialize 单序列）与
	// recordProjectedSpecs（detect 启动作业的账本重建，写入者=基线行）；
	// 受管/Database 域不写（不参与 App 对照面）。
	ensuredBy map[string]string
}

func (o *observations) recordSpecs(owner workloadOwner, deploymentID string, gen uint64, ws []capability.Workload)
func (o *observations) ensuredByOf(wid string) (string, bool)
// forgetOwner / forget 同步 delete ensuredBy。
```

调用点：`observ.go:123` → `e.obs.recordSpecs(appOwner(d.AppID), d.ID, g, batch)`；`recordOwners` 不写 `ensuredBy`。

#### 4.4.2 `converge.go`（标记面）

见 §1.3.5（`revert` 表与三个写/清点）；`teardown.go:55-77` 末尾追加 `e.converge.clearRevert(appID)`。

#### 4.4.3 `drift.go:compareSpecs` 采用规则

```go
		// 账本采用规则（ADR-0082 §6）：条目必须属于当前生效基线——未成功
		// 终态（Cancel/failed/superseded）或进行中部署写下的条目是"上次下发
		// 了什么"，不是"期望什么"，不得当锚（否则遗留载体与脏账本一致 →
		// spec/gen 零信号；R16-M3 的根因）。
		if d == nil {
			continue
		}
		if by, ok := e.obs.ensuredByOf(o.WorkloadID); !ok || by != d.ID {
			continue
		}
```

> 位置：现有 `:259-263` 的 `ensuredSpecOf` 检查之后（两者都过才进入 spec/gen 对照）。

#### 4.4.4 `admission.go` 注释纠正（`:279-283`）

原句：
> // 在途取消是"停止推进"语义：当前 step 收尾后不再迁移；已下发的
> // Workload 由后续部署或 Remove 收口（N0 不自动拆除——诚实暴露）。

新句：
> // 在途取消是"停止推进"语义：当前 step 收尾后不再迁移。已下发的 Workload
> // **由收敛回卷基线**（ADR-0082 §1 行 10：取消的部署从未成为期望态；
> // 非成功终态在 transition 前门落重放标记，下一拍 revert 快信号兑现）；
> // N0「不自动拆除」条款在本场景自 ADR-0082 起废止。

### 4.5 验收锚

| # | 测试 | 断言 | 命令 |
|---|---|---|---|
| E1 | `TestExpectationLedgerSkipsForeignWriter`（新） | 用 d2（未成功）的 materialize 写账本后，把 d1 伪装为 `LatestSucceeded` → 扫描不产 `spec\|`/`gen\|` 信号（账本不采用）；同一观测下若把 `ensuredBy` 改成 d1 → 信号出现（证明是归属在裁决，不是别的分支） | `go test ./internal/application/engine -run TestExpectationLedger -count=1` |
| E2 | `TestCancelRevertMarkDrivesReplay`（新，与 A8 同一测） | Cancel 已物化部署 → 标记在位、下一拍 Ensure 回卷基线、标记被清除 | `go test ./internal/application/engine -run TestCancelRevert -count=1` |
| E3 | `TestRevertMarkOnlyAfterMaterialization`（新） | queued 取消 → **无**标记、无回卷 Ensure、无审计；releasing 取消 → 有标记 | 同上 |
| E4 | `TestRevertMarkClearedOnSucceededDeployment`（新） | 失败→自动回滚成功（succeeded）→ 标记清除，下一拍无多余重放 | `go test ./internal/application/engine -run TestRevertMark -count=1` |
| E5 | `TestCancelLegacyConvergesBackToBaseline`（改） | 保留慢通道兜底形态，但断言补强：**回卷后审计 1 条**（SourceSystem）、**事件 1 条**（revert 走 fast）；撤销"只有慢通道能兑现"的注释 | `go test ./internal/application/engine -run TestCancelLegacy -count=1` |
| E6 | `TestExpectationLedgerForgottenOnTeardown`（新） | `TeardownApp` 后 `ensuredBy` 无该 App 条目、`revert` 标记无该 App | `go test ./internal/application/engine -run TestTeardown -count=1` |
| E7 | 既有漂移族回归 | `TestGhost*` / `TestSpecDriftDetectedOnManualCarrierEdit` / `TestGenLabelRollbackConverges` 全绿（归属规则不误伤生效基线条目） | `go test ./internal/application/engine -run 'TestGhost|TestSpecDrift|TestGenLabel' -count=1` |

### 4.6 ADR-0082 修订草案

见 §1.6 的「§6 账本纪律」新增三条 + 「§1 行 10」新句。验收锚追加：

> - [x] 账本归属钉测：非当前基线的写入不被采用（`TestExpectationLedgerSkipsForeignWriter`）；非成功终态按"是否已物化"落重放标记（`TestRevertMarkOnlyAfterMaterialization`）；成功终态撤销标记（`TestRevertMarkClearedOnSucceededDeployment`）

### 4.7 风险/回归面

| 风险 | 影响 | 翻向方案 |
|---|---|---|
| 部署窗内 spec/gen 对照收窄 → 窗内人工改动不被看见 | 检测缺口 ≤ 一个部署窗 | 成文（§4.2）；窗内 L1/L2 与驱动权在执法；`TestBaselineReplayRaceWithAdmission` 复跑 |
| `ensuredBy` 泄漏（表增长） | 内存 | 与 R16-L2 同族；`forgetOwner`/`forget`/teardown 覆盖（D6） |
| 标记写入点漏（别的路径进 failed） | Cancel/failed 遗留回到慢通道 | 写点在 `transit` 单前门（所有终态迁移都过它），grep `StateFailed` 的迁移点已核（`drive.go:385,403`、`bluegreen.go:211`、`admission.go:295`） |
| `stateMayHaveMaterialized` 判据过宽/过窄 | 过宽=多余 no-op 重放；过窄=漏回卷 | 三相位枚举（releasing/observing/rolling-back）由 E3 测试钉住；后续新增相位即红（枚举表测试 `TestStateMayHaveMaterializedTotality`，可选） |

---

## 5. D5 —— R16-M5：ADR-0081 判据修订（**本轮只做文档**）

### 5.1 问题复述

ADR-0081 `:20` 的 DECLARATION 判据三句话：

> 经部署管线或实体行落期望态；引擎负责把运行时收敛到满足该状态；未变声明重放 = 零扰动 no-op

与附录近半成员冲突：

- identity（23 RPC 中 11 条写）/ governance（2）/ alerting（4）**没有任何运行时面**——"引擎负责把运行时收敛"对它们不成立；
- `:155` 的 `SetUserPassword` 理由"密码行落定即状态即事实"与 `:20` 的"未变声明重放 = no-op"直接矛盾：`identity.go:181` 每次调用 `bcrypt.GenerateFromPassword`（随机盐）→ 同明文重放产生**不同 hash**，行内容必变；
- `RevokeToken` 等"状态即事实"的写只是**幂等**（重复吊销不报错），不是"声明重放 no-op"。

守卫现状：`writeclassguard_test.go:187-231` 只查桶完备/死条目/scope 方向/从属关系——**语义判据零执法**。另有 R16-L4 的三个盲点（互斥未断言、READ 是前缀余集、FrozenVerbs 只 Logf），其中互斥断言本批可顺带补。**新发现（F2）**：附录统计 153 已落后于 proto 实数 154（`QueryScopedMetrics` 未入表）。

### 5.2 候选方案

| 案 | 内容 | sound | 成本 | 回归风险 |
|---|---|---|---|---|
| **A（推荐）** | 修 `:20` 判据为"有运行时面/无运行时面"两子类 + 新增**域级收敛面表**（不逐 RPC 加列）；`SetUserPassword` 归"状态即事实"并写明 bcrypt 反例；统计 153→154 并补 `QueryScopedMetrics` 行；顺带补 ACTION/DECLARATION 互斥断言 | 高：判据与成员自洽、新增域被迫分类、计数与守卫对齐 | S（文档）+ S（可选守卫） | 低（纯文档；守卫只加断言） |
| B | 拆四桶（实体行写 / 期望态声明写 / 动作写 / 读）：Write Class 从三桶变四桶 | 高（分类学最诚实） | M（附录 154 行重编 + 三处守卫 + CONTEXT 词条 + 下游 skills 引用） | 中（词汇冻结面：ADR-0007 更名即设计缺陷，动桶名要走显式 ADR） |
| C | 只给 `:155` 加一句"bcrypt 随机盐 → 非 no-op"，判据原文不动 | 低：判据仍自相矛盾，下一个实现者照抄错误定义 | S | 极低 |

**推荐 A**。否决 B：三桶的桶名是 CONTEXT 冻结词汇（`Write Class` 词条、ADR-0007），拆桶等于改词汇，且四桶的边界（实体行 vs 期望态声明）在机制上已由"有无收敛面"这一列表达——先立**一列**，不为分类学美观动桶名。否决 C：判据本身是第一法源，只补例外不修定义等于把矛盾留在原处。

### 5.3 精确改动清单（doc-only）

| 文件:行 | 改动 |
|---|---|
| `docs/adr/0081-write-class-author-class.md:20` | DECLARATION 判据重写（下方新句） |
| `:21`（ACTION 判据） | 一字不改（"无终态可持有的过渡/仪式/修护动词"仍正确） |
| `:23`（缺省归类规则） | 补一句：实体行写的 no-op 性是"按行内容的 upsert 幂等"，不是"声明重放零扰动"——两者不混用 |
| `:52`（字段权属推迟） | 不动 |
| `:68`（SetUserPassword 边界案例） | 重写（下方新句） |
| `:85`（统计） | `ACTION 27 + DECLARATION 53 + READ 74 = 154`（附 `QueryScopedMetrics` 归 READ 余集） |
| `:11`（背景实数） | `14 个服务文件、154 个方法` |
| `:148-168`（A.6 identity） | `SetUserPassword` 行备注改（：155）；其余不动 |
| `:253-265`（A.14 telemetry） | 加一行 `QueryScopedMetrics \| READ \| 行级 scoped 查询（S10a，ADR-0071 观测面）`；表格上方计数注记同步 |
| `:277` 验收锚 | `proto 全部 154 方法逐个落桶` |
| 新增小节（建议紧随 §1 后） | 「§1.1 DECLARATION 的两个子类与域级收敛面」 |
| `CONTEXT.md:263-264` | `Declaration` 词条补"有运行时面/无运行时面"与"重放 no-op 仅对内容可复现的声明成立" |
| `internal/interfaces/grpc/fleetlygrpc/identity.go:176-177` | 注释补一句 bcrypt 反例（代码零改动） |

**守卫建议（Lead 问"是否要守卫"）**：**要，但只加两条机械可判的**，不加"语义判据执法"（不存在可判谓词）：

1. `writeclassguard_test.go` 补 **ACTION/DECLARATION 互斥断言**（R16-L4 盲点 ①，`:206-216` 的 `continue` 顺序让它现在查不出来）：同一方法两表都命中即红。成本 ~10 行。
2. 新增 `TestWriteClassConvergenceFaceByDomain`：`writeClassConvergenceFace map[string]bool`（域 → 是否有运行时面：`delivery/structure/proxy/runtime/automation = true`；`identity/governance/telemetry = false`，每条带理由），断言 proto 每个含 DECLARATION 方法的服务文件都在表内（新域未分类即红）+ 表项双向保鮮。成本 ~30 行。
   > 这条不是"证明语义为真"，而是**强制把语义声明写下来并在新域出现时逼一次裁决**——与守卫文化一致（ADR-0081 已为字段权属采用同款"推迟但记账"策略）。
3. **不做**：`SetUserPassword` 的"每次新盐"不可能用静态断言执法（需要跑 bcrypt）；把它交给文档 + 代码注释。

### 5.4 ADR-0081 修订草案（原句 → 新句）

**§1 判据（`:20`）**

原句：
> | **DECLARATION**（声明写） | 经部署管线或实体行落期望态；引擎负责把运行时收敛到满足该状态；未变声明重放 = 零扰动 no-op | 写后存在可 diff 的期望态（Revision 或实体行） |

新句：
> | **DECLARATION**（声明写） | 落**可 diff 的持久期望态**（Revision 或实体行）。按**有无运行时面**分两个子类：①**有运行时面**——引擎欠收敛，对照该期望态持续纠正运行时，未变声明重放 = 零扰动（内容等价 skip）；②**状态即事实**——该域无运行时基底可收敛（identity / governance / alerting 全族），行内容即事实，重放语义是**行级 upsert 幂等**（同一最终状态），**不承诺 no-op**：写入不可复现内容（bcrypt 随机盐、时间戳、随机 ID）的动词归此类 | 写后存在可 diff 的期望态（Revision 或实体行），且其收敛面归属（有/无）经域级表登记 |

**新增 §1.1（紧随判据）**

> ### 1.1 收敛面归属（域级表）
>
> DECLARATION 的"有/无收敛面"是**域属性**，逐域登记（不为 154 行逐行加列——列不产生新信息，只会 53 行噪音）：
>
> | 域 | 运行时面 | 理由 |
> |---|---|---|
> | delivery（Revision/Deployment/Build/Hook/GitHub App） | **有** | 部署管线 → Ensure 收敛，材料经指纹接棒 |
> | structure（Project/App/Secret/Config/Variable/Volume/Database/Network/Peer 行） | **有** | 实体行是 Ensure 的输入面（行集收敛/材料注入/网络附件） |
> | proxy（Route 行） | **有** | 行集指纹 → Route 全量发布 |
> | runtime（Node 行） | **有** | schedulable 期望态可对照可收敛 |
> | automation（Task/Schedule 行） | **有** | desired_concurrency 与周期规则由域内环/调度器对照 |
> | identity / governance / telemetry（identity 23、governance 3、alerting 4） | **无** | 无运行时基底；行即事实，重放只有 upsert 幂等 |
>
> 新增服务文件/域未登记 = 守卫红（`TestWriteClassConvergenceFaceByDomain`）。

**§5 边界案例（`:68`）**

原句：
> - **SetUserPassword = DECLARATION**：判据 = **有无收敛面接棒**。密码行落定即状态即事实（identity 域无运行时基底可收敛，与全部 identity 实体同律）；Rotate 系（RotateDatabasePassword/RotateHookToken）铸新凭证后效果要经材料指纹进入收敛面接棒（下一拍 Ensure 携新材料）——动作改事实 + 收敛面接棒的混合范式，是 ACTION 与 DECLARATION 协作的标准形态，入册为本 ADR 学说。

新句：
> - **SetUserPassword = DECLARATION（状态即事实子类）**：判据 = **有无收敛面接棒**。密码行落定即事实（identity 域无运行时基底可收敛，与全部 identity 实体同律）。**重放语义显式降级**：`bcrypt.GenerateFromPassword` 每次新盐（`fleetlygrpc/identity.go:181`），同明文重放产生**新 hash**——行内容必变，**不是 no-op**；它的"幂等"只成立在"行最终状态 = 最后一次设置"这一语义层。这正是"状态即事实"子类必须与"可重放 no-op"子类分立的反例，入册为本 ADR 的判据样本。Rotate 系（RotateDatabasePassword/RotateHookToken）铸新凭证后效果要经材料指纹进入收敛面接棒（下一拍 Ensure 携新材料）——动作改事实 + 收敛面接棒的混合范式，是 ACTION 与 DECLARATION 协作的标准形态。

**附录 A.6（`:155`）**

原句：
> | SetUserPassword | DECLARATION | 密码行落定即状态即事实（无收敛面接棒——与 Rotate 系的判据分界） |

新句：
> | SetUserPassword | DECLARATION | 密码行即事实（无收敛面接棒——与 Rotate 系的分界）；**重放非 no-op**（bcrypt 随机盐），属"状态即事实"子类 |

**统计与验收锚**

- `:11` → 「**14 个服务文件、154 个方法**」；理由句补「S10a 增 `QueryScopedMetrics`（READ 余集）」。
- `:85` → 「统计：**ACTION 27 + DECLARATION 53 + READ 74 = 154**」。
- `:277` → 「proto 全部 154 方法逐个落桶」。
- 验收锚追加：
  > - [x] DECLARATION 判据分两子类（有运行时面 / 状态即事实）+ 域级收敛面表登记（`TestWriteClassConvergenceFaceByDomain`）
  > - [x] ACTION/DECLARATION 互斥断言（同方法两表命中即红）
  > - [x] `SetUserPassword` 的"重放非 no-op"反例在案（附录 A.6 + 代码注释）

### 5.5 风险/回归面

| 风险 | 影响 | 翻向方案 |
|---|---|---|
| 154 计数改动与守卫不同步 | `writeclassguard_test.go:201` 已断言 154，ADR 文本改错即人读不一致（守卫不红） | 本批把 `:11,85,277` 三处一起改；附录 A.14 补行 |
| 域级表把 automation 标"有"，但其收敛面是任务池环而非 Ensure | 读者误解 | 表内理由列写明"域内环/调度器对照"，与"引擎欠收敛"区分 |
| 互斥断言让当前表变红 | 需先核两表无交集 | 设计前已核：`writeClassActions`（`writeclassguard_test.go:108`）与 `writeClassDeclarations`（`:31`）当前无交集（R16-L4 自认"当前表无冲突"）→ 断言落地即绿 |
| 守卫扩面触及 `authz` 面 | 编译/测试抖动 | 只读 policy 元数据，不改 policy；`go test ./internal/guards -run TestWriteClass -count=1` |

---

## 6. D6 —— ADR-0083 追记（H15：restart 源 = 生效基线）

### 6.1 问题复述

S4（`c925ba3b`）已把 `apps restart` 声明面从"最新冻结 Revision"改为"生效基线"：

- `cmd/fleetly/cmd/verbs_structure.go:460`：`latestFrozenRevision` → `latestSucceededRevision`；
- 新纯函数 `selectRestartBaseline`（取 seq 最大的 succeeded 行 → 匹配 `to_revision` 的 Revision 行）；
- golden 变化：`apps-restart-json.golden` 的 `to_revision` `r3→r2`；`TestSelectRestartBaseline` 新增；
- 引擎侧 `markRestartSeqOnly` 本就对 `LatestSucceeded` 判定（`restart.go:35-44`），未改。

但 ADR-0083 §3（`:36`）仍写"缺省走声明面：对全部进程 bump restart_seq"，**没有写源**；验收锚（`:45-54`）也没有对应条目。第 8 条"迁移不得因重启重跑"（`:24`）的承诺在"失败/回滚后重启"路径上依赖这个源——不追记，下一位读者会按 ADR 恢复旧行为。

残余（同族、方向相反）：`--runtime` 的进程存在性校验仍取 `Revisions.Latest`（`processrestart.go:31`）——新冻结版删掉某进程时，动作面会对"正在跑的进程"误报 `unknown process`。R16-H1 已点出，S4 未修。

### 6.2 候选方案

| 案 | 内容 | sound | 成本 |
|---|---|---|---|
| **A（推荐）** | 只追记（ADR-0083 §3 补源 + 落码注记 + 验收锚 + 残余登记） | 高：法源与代码一致；残余显式在案 | S（doc-only） |
| B | 追记 + 顺手把 `--runtime` 校验改到生效基线 | 高：同族问题一次收口 | S（doc + ~10 行代码 + 1 测） |
| C | 不改 ADR，只在 plan 记录 | 低：法源仍误导 | 极低 |

**推荐**：**文档半边按 B 做（= A 的全部内容），代码半边列为 S12b-3 的可选小修（P2）**。追记是硬要求（法源必须与 S4 代码一致）；`--runtime` 校验修不修是独立取舍——它属"逃生通道"、与 S12 主线不同轴，但成本极低且是同一 H15 家族的次生缺口。**最低可接受 = 只做 A**。

### 6.3 ADR-0083 追记草案（可复制）

**头部关联行（`:5`）追加**：

> **追记 2026-10-19（H15）**：声明面重启的**源**已明确为「生效基线」——最新 `succeeded` 部署行的 `to_revision`，**不是** R 号最大的冻结 Revision；失败/取消/回滚后重启不得把未生效 spec 重新上线（S4 `c925ba3b` 落码）。`--runtime` 的进程存在性校验仍取最新冻结 Spec，登记为残余（见落码注记）。

**§3 双面操作者契约（`:36`）原句**：

> `fleetly apps restart` **缺省走声明面**：对全部进程 bump restart_seq → 正常部署管线（有 lineage、跳迁移、内容等价语义护航）；`--runtime` 旗标走 ADR-0069 零 lineage 动作面作逃逸。两动词并存、语义各自诚实：声明面是「我要求以新序数重跑进程」的期望态变更，动作面是「立即就地重排载体」的过渡动作。

**新句**：

> `fleetly apps restart` **缺省走声明面**：**以生效基线为源**（最新 `succeeded` 部署行的 `to_revision` 所指冻结体；`selectRestartBaseline` 纯函数按部署 seq 取最大），对全部进程 bump restart_seq → 正常部署管线（有 lineage、跳迁移、内容等价语义护航）。**为什么不是"最新冻结 Revision"**：`rollback` 不冻结新号、`failed`/`cancelled` 的冻结体从未生效——以其为源会把失败 spec 重新上线，且引擎的执行时判定会把它判成非 seq-only → 迁移重跑，直接违反 §1 步 8。`--runtime` 旗标走 ADR-0069 零 lineage 动作面作逃逸（进程存在性校验仍对最新冻结 Spec——见落码注记残余）。两动词并存、语义各自诚实：声明面是「我要求以新序数重跑进程」的期望态变更，动作面是「立即就地重排载体」的过渡动作。无 `succeeded` 基线（从未成功部署）时声明面诚实报错。

**新增「落码注记（2026-10-19 S4/追记）」小节（接在 `:56-60` 之后）**：

> - **restart 源修复（H15）**：`c925ba3b` 把 CLI 声明面源从"最新冻结 Revision"改为"生效基线"——`latestSucceededRevision` + `selectRestartBaseline`（`cmd/fleetly/cmd/verbs_structure.go:501-578`），golden `apps-restart-json.golden` 的 `to_revision` 由 `r3` 变 `r2`（失败/回滚后取回生效行）；`TestSelectRestartBaseline` 钉选源纯函数，`TestGoldenAppsRestart` 钉双形态。
> - **残余（同族，登记不修）**：`--runtime` 的进程存在性校验仍对 `Revisions.Latest`（`processrestart.go:31`）：若新冻结版删除了某进程而它正在基线里跑，动作面会误报 `unknown process`（逆向也会放行一个不存在的进程）。修法（若批准）= 改取 `LatestSucceeded().ToRevision` 的冻结体 + `TestRestartProcessValidatesAgainstEffectiveBaseline`；不在 S4 射程。
> - **与 H15 同路径的次生面**：以基线体为源重提交时，基线体里的 inline env 是当年值；变量面（Shared/AppVariable）由服务端合成取当前值（ADR-0072 不变）——"已删变量复活"仅限 inline env 且属 R03 变量合成面，本 ADR 不裁。

**验收锚追加（`:54` 后）**：

> - [x] restart 声明面源 = **生效基线**（失败/取消/回滚后不重上线失败 spec）：`TestSelectRestartBaseline` + `TestGoldenAppsRestart`（golden `to_revision` 取 r2）+ 引擎侧 `TestRestartSeqOnlyRecomputesBaselineAtExecution`（执行时判定同源）
> - [x] `--runtime` 进程存在性校验的源位登记（残余在案；若批准修复则改为生效基线 + `TestRestartProcessValidatesAgainstEffectiveBaseline`）

### 6.4 风险/回归面

| 风险 | 影响 | 翻向方案 |
|---|---|---|
| 追记文字与 golden 证据不符 | 文档失真 | 引用实际 commit `c925ba3b` 与测试名；`git show c925ba3b -- cmd/fleetly/cmd/apps_restart_golden_test.go` 可复核 |
| 可选修 `--runtime` 校验改变逃生面行为 | 排障动线变化 | 单独 commit；`TestGoldenAppsRestart` 双形态复跑；不修则本 ADR 只追记 |
| 追记被误读为"重启会重跑迁移" | 运维误判 | 新句明确"源=生效基线 → 执行时判定为 seq-only → PreDeployDone → 不重跑" |

---

## 7. 回归矩阵与验证命令（S12b 各批门禁）

### 7.1 现有测试的预期变红清单（逐条翻向）

| 测试 | 位置 | 变红原因 | 翻向 |
|---|---|---|---|
| `TestConvergeVerdictGolden` | `converge_test.go:77-108` | golden 与 rows 表（10→8） | `-update` 重写 golden（8 行、留空 05/09），同 commit 改 ADR §1 |
| `TestPostureDetectSkipsConverge` | `:515-537` | detect 不再写（原断言"baseline replay still runs under detect"） | 替换为 `TestPostureDetectWritesNothing` + `TestPostureDetectRebuildsExpectationsWithoutEnsure` |
| `TestCancelLegacyConvergesBackToBaseline` | `:552-571` | 回卷改由 revert 快信号（首拍即回卷，不再等慢通道） | 断言补强（审计/事件），保留慢通道兜底子例（先 `settleSlowChannel` 前的形态） |
| `TestBaselineReplaySharesCooldownLedger` | `:493-511` | 不受影响（冷却门在 posture 之前） | 不改 |
| `TestMaterializeThreePathsIdentical` | `materialize_test.go:22-86` | `materialize` 返回值多一个（`_, err :=`） | 机械改签名 |
| `TestGhostOldGenerationMemberNotRebuiltAfterBGCollection` | `converge_test.go:274-306` | 可能多一个 `extra` 信号（同一拍仍只 1 次 Ensure） | 断言按"Ensure 次数不变、目标 ID 正确"保留；若计数变化，改为"≥1 且最后一次含 newID" |
| `TestEnsureContentEquivalenceSkipsUnchangedAcrossGen` 等四例 | 双侧 provider | 新增 `Changed/Removed` 断言（同测内追加，不改现有断言） | 追加断言 |
| swarm `TestEnsureNoopBreakerSkipsIdenticalDesired` | `runtime_ensure_test.go` | 同上 | 追加 `Changed` 为空断言 |

### 7.2 命令（每批同口径）

```sh
# 定向
go test ./internal/application/engine -run 'TestConverge|TestDrift|TestPosture|TestCancel|TestExtra|TestRevert|TestExpectation|TestGhost|TestAbsence|TestGenLabel|TestTeardown|TestMaterialize' -count=1
go test ./internal/infrastructure/providers/swarm -run 'TestEnsure|TestContent' -count=1
go test ./internal/infrastructure/providers/k3s -run 'TestEnsure|TestContent' -count=1
go test ./internal/domain/shared/capability -count=1
go test ./internal/guards -count=1

# 全量门禁（与 AGENTS.md 同口径；本机无 Docker → e2e 跳过）
gofmt -l . ; go vet ./... ; go build ./... ; GOOS=linux go build ./...
mise run test       # go test -race 三 module
mise run lint       # vet/gofmt/golangci/buf lint/buf breaking
```

> e2e（dind/h2c/twonode）本机不可跑（无 Docker daemon）——D2/D3 的"detect 下路由仍可解析"只在 engine 层钉（C3），真机面记为 **待复核**（验证方法：staging 切 `posture=detect` 重启控制面，`fleetly routes list` + 外部 curl 应仍 200；恢复 `all` 后 `events --json` 无 `workload.converged` 堆积）。

---

## 8. S12b 实施顺序（三批，每批可独立 commit + 验收门）

> **实施状态（2026-10-19）**：批次 1/2 已落 S12b1 `2740a09d`（判定表接线、`EnsureReport` 变更集、`extra`/`replay` 信号、账本归属与重放标记）；批次 3 的**法源半边**由 S12b2 完成（ADR-0081/0082/0083、CONTEXT、守卫），**代码半边（D3）已实施**（见 §3.3/§3.4 与 §3.7 后续裁决）。各批「验收门」保留为原规格。

### 批次 1 —— 语义骨架：判定表接线 + 事实面（D1 主体 + D2）

- **范围**：`converge.go`（信号类枚举、表裁剪、两映射、`convergeStep`/`attemptConverge` 接线、`convergeOutcome`、`convergeSucceed` 分流、`convergeAuditDetailJSON`、`convergeSource.String`）、`drift.go`（键改用枚举、`expectedWorkloads`/`expectedCarrierIDs`/`missingIDs`/`extraIDs` 投影与集合差结构预留——`extra`/`revert` 的**信号接线**在批次 2）、`capability/runtime.go`（`Changed`/`Removed`/`HasMaterialChange`）、swarm/k3s `Ensure`、`fakeruntime_test.go` 注入面、`materialize.go` 返回值、5 个调用点、`eventcode/events.go:52` Summary + golden。
- **不做**：`extra`/`revert` 两个信号的生产接线（批次 2）。
- **验收门**：§2.4 B1-B10 + §1.5 A1-A6；`go test` 三包 + guards 绿；golden 三处（verdict/events/deployment 无变化）经逐行核对。
- **commit 建议**：`feat(engine,providers): S12b-1 裁决表接线 + EnsureReport 变更集（R02-M2/M5、R16-M6）`

### 批次 2 —— 真实信号与账本归属（D1 剩余 + D4）

- **范围**：`extra` 信号接线（`projMemo` 改型消费、`signals[extra]` 产生）、`revert` 标记（`converges.revert`、`transition.go` 钩子、`compareSpecs` 读点、`convergeSucceed`/teardown 清除）、`observations.ensuredBy` + `recordSpecs` 签名 + 采用规则、`admission.go` Cancel 注释。
- **验收门**：§1.5 A7-A9 + §4.5 E1-E7；engine/guards/apitest 绿（apitest 覆盖 Cancel 全链）。
- **commit 建议**：`feat(engine): S12b-2 extra/revert 真实信号 + 期望账本归属（R02-M2/M5、R16-M3）`

### 批次 3 —— posture 收口与法源（D3 + D5 + D6）

> **本批（S12b2 + D3 批）**：法源半边由 S12b2 完成——ADR-0081 修订（D5）、ADR-0082 全文替换块（§1/§2/§5/§6 + 验收锚）、ADR-0083 追记（D6）、CONTEXT 三条词条、`writeclassguard_test.go` 两条断言、plan/review/设计文档状态回写。**D3 代码清单（detect 零写）已实施**——删 baseline 豁免、`rebuildBaselineExpectations` + `recordProjectedSpecs`、gen 锚守卫、隔离 Warn、C1-C6 钉测；`options.go` 注释、CONTEXT `Posture` 词条、ADR-0082 §5/验收锚/变更记录、本设计稿状态行同批回写。

- **范围**：detect 零写（删豁免）、`rebuildBaselineExpectations` + `recordProjectedSpecs`、gen 锚守卫、隔离 Warn、`convergeOutcome` 的 detect/隔离两支消费（类型在批次 1 引入）；ADR-0082 全文替换块（§1/§2/§5/§6 + 验收锚）、ADR-0081 修订、ADR-0083 追记、CONTEXT 三条词条、`options.go` 注释、`identity.go` 注释、`docs/design/2026-10-18-declarative-model.md` §4.1 行 10「待裁决」改为"已裁决（ADR-0082）"。
- **可选**（P2，单独 commit）：`--runtime` 存在性校验改生效基线 + `TestRestartProcessValidatesAgainstEffectiveBaseline`；`writeclassguard` 互斥断言 + 域面表断言。
- **验收门**：§3.4 C1-C6；全量 `mise run test` + `mise run lint`；ADR/词条与代码同 commit。
- **commit 建议**：`feat(engine,adr): S12b-3 detect 零写 + ADR-0081/0082/0083 修订（R16-M2/M5、H15）`

> 批次依赖：2 依赖 1（审计分流是 2 的事实面）；3 依赖 1（`convergeOutcome`、投影 helper）。3 的文档半边可与 2 并行起草，但 ADR 与代码同 commit。

### 本轮「仅文档纠正」清单（不含代码）

1. D5 全部（ADR-0081 判据/子类/域级表/SetUserPassword/统计 154/验收锚；CONTEXT `Declaration` 词条；`identity.go` 注释）。
2. D6 全部（ADR-0083 §3 新句 + 落码注记 + 验收锚 + 头部追记）。
3. ADR-0082 的行 5/9 非行登记与"未落码"声明（**不需要代码**即可成立，但要与批次 1 同 commit 才不脱钩）。
4. `docs/design/2026-10-18-declarative-model.md:119` 的「Cancel 遗留载体 … 待裁决」→ 已裁决（ADR-0082 §1 行 10）。
5. `admission.go:279-283` Cancel 注释（代码注释，无行为改动；归批次 2 同 commit）。
6. ADR-0081 `:11,85,277` 计数 153→154 + A.14 补 `QueryScopedMetrics` 行。

---

## 9. 明确不做 / 未决与置信度

### 9.1 明确不做

- **R02-M1（冷却空签名解除）**：与 D1 信号映射相邻但独立；本批只保证新信号不造空 `agg`，不修解除径②（维持 backlog）。
- **R16-M4（撤销 peer 在请求 goroutine 内同步 Ensure）**：D3 只加告警与文档，不改写入口（需要 Kick/唤醒机制，单独立项）。
- **R16-L1/L2/L4/L5/L6**：文档状态回写、9 张表清理、守卫三盲点（互斥一条顺带在 D5 可选）、六处弱断言（除 D1/D4 已强化的三条）、Author Class 字段注册表——除 D5 明确点到的两条守卫外不做。
- **R02-M7（环 panic 无消费面）**：不在 S12。
- **apitest 夹具真实回执**（R12-M7）：新增 `Changed`/`Removed` 在 `harness.FakeRuntime` 仍恒空；全栈面不可达，engine 层已覆盖。
- **e2e 真机验证**：本机无 Docker；D3 的 detect 真机面、D2 的 provider 真机变更集均记待复核（§7.2）。
- **不新增事件码**：`WorkloadOrphaned` 不做（行 9 未落码）；不给 `workload.converged` 加慢通道触发。

### 9.2 待复核（含验证方法）

| # | 不确定点 | 验证方法 |
|---|---|---|
| 1 | detect 账本重建是否足以让 `publishRoutes` 在真机上解析出端点（fake 只覆盖结构） | `TestPostureDetectPublishesRoutesFromExpectationCache`（engine）+ staging：切 detect 重启后 `fleetly routes list` 与外部请求 200 |
| 2 | `extra` 信号在 BG 收口失败/回滚失败等边角形态是否假阳性 | 实现后跑 `go test ./internal/application/engine -count=1`（含 bluegreen/rollback 族）；新增 A9；仍不确定的形态记 staging 观测（`events --name workload.converged --json` 计数） |
| 3 | k3s 冷账本判据（`lastA == ""` 且 `serverObjectGen > 0`）是否覆盖全部重启形态 | `TestEnsureColdLedgerReplayNotReportedChanged` + `go test ./internal/infrastructure/providers/k3s -count=1` |
| 4 | 审计 `Detail` JSON 在审计查询/Console 面是否需要 schema | `go test ./internal/persistence/audit ./internal/interfaces/console`；Console 无 Detail 消费（grep `detail`） |
| 5 | `transit` 钩子在 caller-tx（Cancel）路径上"先标记后提交"的边界 | 读 `admission.Cancel:284-308` + `transition.go:208-245`；若需严格，可把标记移到 `Cancel` 事务返回后（成本 +3 行，语义等价） |
| 6 | 冻结与驱动权合并裁决后，`resolved`/冷却账本时序 | `TestFreezeSuspendsConvergeUntilLift` + `TestConvergeCooldownLifecycle` 复跑；两门都不写账本 |

### 9.3 置信度

| 决策 | 置信度 | 依据 |
|---|---|---|
| D1（表接线 + extra/revert） | **高** | 全部证据是行级代码；extra/revert 数据源就在手边；映射/字段/行为三层可测 |
| D2（EnsureReport 变更集） | **高** | 双 provider 的写分支已逐条枚举（§2.3.2/2.3.3）；冷账本例外有成文理由 |
| D3（detect 零写） | **中高** | 语义正确性与"路由依赖"已静态核实（F1）；真机路由行为待 staging 复核；gen/stopped 失明是已知代价，已钉成显式测试 |
| D4（账本归属 + 重放标记） | **高** | 根因与写点均定位到行；部署窗收窄是可解释取舍（有测试与成文） |
| D5（ADR-0081） | **高** | 纯文档；计数已用 proto 实数复核（154） |
| D6（ADR-0083 追记） | **高** | S4 代码与 golden 已在仓内 |

---

## 附录 A：复核命令记录（本会话，只读）

```text
# 1) 判定函数零引用
Select-String -Path internal\application\engine\*.go -Pattern "convergeVerdict"      → converge.go:146,148,172,176 + converge_test.go
# 2) golden 现文
Get-Content internal\application\engine\testdata\converge-verdicts.golden           → 10 行（05/09/10 形态见 §1.3.6）
# 3) proto rpc 实数（与 ADR-0081 的 153 对账）
Get-ChildItem proto -Recurse -Filter *.proto | Select-String '^\s*rpc\s+\w+\s*\('   → 154
# 4) 写类守卫计数断言
Select-String internal\guards\writeclassguard_test.go -Pattern "len\(all\) != 154"  → :201（已断言 154）
# 5) 路由解析依赖 ensuredSpec
Select-String internal\application\engine\*.go -Pattern "routeExpectations"          → routepublish.go:142 + observdomain.go:184-222
# 6) EnsureReport 实现面
Select-String -Recurse -Pattern "\) Ensure\(ctx context\.Context, ns capability\.NamespaceRef" → swarm:40, k3s:41, 3 假件
# 7) 终态迁移点
Select-String internal\application\engine\*.go -Pattern "StateFailed|StateCancelled|StateSuperseded"
```

## 附录 B：ADR 修订可复制段落（与 §1.6/§3.5/§5.4/§6.3 同文）

> 为避免两处漂移，**唯一权威副本是各决策节的"新句"块**；本附录只列替换位置清单，供实现者 mechanical 搬运。

| ADR | 行/段 | 替换为 |
|---|---|---|
| 0082 | `:5` 关联行 | 追加"**修订 2026-10-19（S12）**：§1 表由 8 判定行 + 2 非行构成并被生产调用；§2 事实面按真实变更分流；§5 detect=零运行时写；§6 账本归属与重放标记" |
| 0082 | `:29` §1 标题 | §1.6「§1 表头」新句 |
| 0082 | `:37` 行5 | §1.6「行 5」新句 |
| 0082 | `:40` 行8 | §1.6「行 8」新句 |
| 0082 | `:41` 行9 | §1.6「行 9」新句 |
| 0082 | `:42` 行10 | §1.6「行 10」新句 |
| 0082 | `:51` §2 第 3 步 | §1.6「§2 收敛动作」新句 |
| 0082 | `:67` §5 | §3.5 新句（含 4 条 detect 语义） |
| 0082 | `:71-73` §6 | §1.6「§6 账本纪律」追加三条 |
| 0082 | `:89` 验收锚① | §1.6 新句 |
| 0082 | `:97` 验收锚⑨ | §1.6 新句 |
| 0082 | 验收锚末尾 | §1.6 行 8 钉测两条 + §3.5 detect 一条 + §4.6 账本归属一条 |
| 0081 | `:5` | 追加"**修订 2026-10-19**：DECLARATION 分两子类 + 域级收敛面表 + `SetUserPassword` 重放非 no-op；统计 154" |
| 0081 | `:11` | `154 个方法`（+ S10a 说明） |
| 0081 | `:20` / `:23` | §5.4 判据新句 + 缺省归类补句 |
| 0081 | 新增 §1.1 | §5.4「收敛面归属（域级表）」 |
| 0081 | `:68` / `:155` | §5.4 两处新句 |
| 0081 | `:85` / `:277` | `27 + 53 + 74 = 154` / `154 方法` |
| 0081 | 验收锚末尾 | §5.4 三条 |
| 0083 | `:5` | §6.3 头部追记 |
| 0083 | `:36` §3 | §6.3 新句 |
| 0083 | `:56-60` 后 | §6.3「落码注记（2026-10-19 S4/追记）」 |
| 0083 | `:54` 后 | §6.3 验收锚两条 |
