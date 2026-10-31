# fleetly zeabur-oss 借鉴程序设计（B1 变更确认闭环 + B2 诊断引擎 + B3/B4 纪律件，2026-10-30）

> 状态：**设计稿 v3.2——提案已裁决（2026-10-30 首轮 13 项 + 第二轮无兼容约束重裁 2 项/纯改良 4 项），本文随裁决定形**；各实施批开工时各自开 ADR 并携带守卫任务（AGENTS.md 纪律）。v1→v2 = 事实校验轮；v2→v3 = 四侧独立审查轮（§8.1/§8.2）；v3→v3.1 = 首裁定形；v3.1→v3.2 = 第二轮（前提：开发阶段/staging 可重建/无向后兼容）——**B1 强制度翻为默认强制+逃生门**（webhook/Restart·Scale/--yes 豁免）、强制集表驱动 ConfirmableVerbs+confirmguard、**终态计数单源化事件账本**、ScaleProcess 升一等 RPC、pending 窗直接改列。
> 裁决基础（本文以此为前提）：总原则「抄机制不抄赌注」；fleetly 是收敛模型（确认闭环绑逻辑基线而非 runtime 版本钉）、控制面单进程（进程内票据店）、proto 唯一契约（RPC 先行）。
> 事实基线（2026-10-30 fleetly 全仓核查 + 独立审查轮修正，file:line 为当日 HEAD 实况）：确认面纯 UI 层（`console/src/components/domain/confirm-dialog.tsx:14`）；部署受理前半在 **api 层**（`internal/interfaces/grpc/fleetlygrpc/delivery.go:36-135`：授权→spec_file 解析→upload 归属→变量 lookup→`normalizeDeploySource`→hook 闸门/merge 裁决），engine `SubmitSpec`（`internal/application/engine/deployspec.go:115`）= merge→合成→冻结→Admission（**两个独立事务**：冻结相/admission 相）；admission 预检 `CheckPeerRefs`/`CheckPreDeployNetworks` 按**冻结行寻址**（`peers.go:120`、`predeploy.go:297`），在 appMu 下执行（`admission.go:96-120`）；latest-wins 只合并 queued 行（`admission.go:190-197`）；只读投影底座现成（`render.go:60/28`——**InspectAppWorkloads 是 spec 对照面**，State 恒 Running、Replicas 取期望（`swarm/runtime.go:723-757`），活状态只在未导出 obs 缓存 `observdomain.go:21-33`，**全仓无 restart 计数器**）；票据店现成（`eventtickets.go`——单用途烧票 + purpose 绑定，**只有「兑换即删」单步原语**）；拦截器链序 = authn→protovalidate→freeze→idem→limiter→timeout→handler（`internal/assembly/grpc.go:236-257`）；铸票 RPC 全数 **ACTION 桶**（`writeclassguard_test.go:245-248`）；仓内零 AES-GCM（age 信封 `material/material.go`；`crypto/hmac` 仅 webhook.go/githubapps.go）；Secret 值 write-only；幂等指纹 `idem.fingerprint`（`enforcer.go:255-265`）；**事件读面只有 team 维前向扫描**（`outbox/repo.go:46-73` ListAfter，无 App/aggregate 索引）；组件健康聚合+10s TTL 缓住在 systemgrpc 未导出方法（`systemgrpc.go:103-176`），managed reconciler 状态未导出（`managed.go:69-76`）；DeleteApp 行级联在 API 层 tombstone 事务（`structure.go:518-535`），teardown 预检三层同谓词（`structure.go:459` → `teardown.go:30-38` → repo 复查）。

## 1. B1：变更预览与确认闭环（Change Preview + Confirm Ticket）

### 1.1 领域定位

fleetly 的确认现状是 **UI 约定**：弹窗文案与执行物之间无机制绑定，CLI/API 直达。B1 立的是**机制事实**：一次破坏性/高影响变更被确认的，恰是操作者预览到的载荷，且**事后可证**（审计落点）。边界先行——B1 **不是**原子性机制（执行侧幂等/收敛/内容锚定由 ADR-0024/0064/0082 既有面承载），只解决「确认的正是展示的」；也**不是**审批流（不立 Approval 实体，deletion test 同 v2）。

### 1.2 API 面（proto 先行）

```
rpc PreviewDeploy(PreviewDeployRequest) returns (PreviewDeployResponse)
  // ACTION 桶 + mint-family（铸票；BrowseDatabase/IssueEventTicket/CreateExecSession 先例）。
  // 冻结期：不进 FrozenVerbs（只读投影 + 铸票；冻结期铸造的票解冻后 600s 内可消费——属预期，
  // 解冻即恢复部署权；冻结期内 Deploy 在 FrozenVerbs 先拒，票不可消费）。
  // PreviewDeployRequest **内嵌 DeployRequest 同一 message**（四源/CEL descriptor 规则单源继承，
  // 防形状层重现「确认后才发现拒收」）+ 预览专用可选字段。
rpc PreviewTeardown(PreviewTeardownRequest) returns (PreviewTeardownResponse)
  // ACTION 桶同上；**scope 钉 apps:admin**（镜像 DeleteApp 的 apps:admin——预演动词不得低于
  // 被预演动词的权限门槛；deployments:write 族会放窄权 token 枚举拆除计划，独立审查轮 A4）。
// Deploy / DeleteApp 请求体各加 confirmation { ticket } —— **重裁 §8.2'（无兼容约束）：默认强制 + 逃生门**。
// 强制集 = Deploy + delete 族（App/Database/Route/Project）+ Rollback；Restart/Scale 豁免（轻且可逆，
// 与 B4a 风险分级间档）；webhook/git 面豁免（材料归 git ADR-0072，代码评审即确认）；脚本/CI `--yes`；
// 无票直发 = E_CONFIRMATION_REQUIRED 类拒绝（新码入册）。
// **ConfirmableVerbs 表驱动立法**（freezeguard 同型 + confirmguard 守卫对账）：拦截器做通用验票
// （存在/TTL/purpose/请求者身份——③④步的通用半边），Deploy 族的内容绑定（digest vs 冻结体、
// base 多锚）留 engine 受理临界区（1.4 既有裁决）——两段分工，per-handler 散写消失。
// confirmation 是 SubmitSpec 的入参透传（见 1.4 验票下沉）。
// **Rollback 强制验票**（重裁后从「optional」升格）：单目标验票——票绑目标 id + 当前 revision
// digest + 操作者，同临界区验烧（B2a Fix 动词的确认闭环依赖）。RestartProcess 豁免（见上）。
```

- **ScaleProcess 一等 RPC**（第二轮定稿，随第 2 批）：`ScaleProcess(app_id, process, replicas)` 对称 ScaleTask（scope/幂等桶/writeclass 表项齐；服务端读当前生效 Spec → 改 replicas → MergeOverlay 提交；ADR-0064 保证 scale-only 零任务替换；Functions 三平面分立先例）——B2a 现场漂移 Fix 的 verb 落点；CLI `fleetly apps scale <app> <process> N` 为其皮。豁免强制确认（见上）。

- **REST 映射**（路径段形态，仓内零冒号后缀先例）：`POST /v1/deployments/preview`、`POST /v1/apps/{app_id}/preview-teardown`；swagger/console:gen 同批；gatewayguard 注解对账同批。
- **幂等面**：Preview 不进（无终态可持有）；Deploy/Delete 幂等语义不变——幂等重放不烧票（enforcer 在 handler 前重放，handler 不执行）。
- **新 errcode（只增入册；文案不内插票串/digest，沿票据店不分理由纪律）**：`E_CONFIRMATION_STALE`（基线漂移，含服务端输入漂移——变量/hook 行变化归此腿，suggestion 指向重预览）、`E_CONFIRMATION_MISMATCH`（请求与票面指纹不符 / **请求者与票面操作者不符**）。
- **审计（独立审查轮 A8）**：兑换成功落 **LedgerOnly 审计行**（actor/team/preview_digest/request_digest/base 锚 → deployment ID），或并入 deployment.create FactRow Detail——否则带票部署与裸部署事后逐字节不可区分，「机制事实」无可查证落点。
- **守卫连坐（七件同 commit）**：writeclassguard 三桶/台账实数、ADR-0081 附录分类表、FrozenVerbs 计数、AssertAllRegisteredHavePolicy（无 policy 注解启动即败）、scopeguard 方向、gatewayguard REST 注解、e2eciaccount（新 e2e 腿）。

### 1.3 预览管线：部署受理的只读孪生（三段单源抽取）

抽取边界**三段**（漏段一则预览不复执法，违反「确认的正是展示的」）：

```
段一（api 受理前半）：delivery.go 的 authorize → spec_file 解析 → upload 归属/blob 在盘 →
      变量 lookup → normalizeDeploySource → hook 闸门（E_APP_HOOK_OWNED）与 merge 裁决
段二（engine 合成）：SubmitSpec 的 merge（MergeNone/MergeMaterial/MergeOverlay）→ 三级变量合成
段三（engine 投影前段）：projectRevision(strict) —— 不 stamp 代戳、不 pinVolumes（render.go 同款只读纪律）
    Deploy 写半边不动：Revision 冻结 → Admission → …
```

- **防漂移守卫（命门）**：同输入预览投影 == Deploy+首次 materialize 投影**逐字节**（预览 golden 与部署投影 golden 同源生成）。**守卫域限定**：`projectRevision` 按冻结行取数（loadSpec + buildDigests 解析 from_build digest）——预览无冻结行无 Build 行，需新增「内存 spec 投影」变体（真实工作量）；from_build 源预览 = **digest-pending 形态**（镜像引用位标注「构建后落定」），守卫域 = image 直投 + 既成 digest 源。
- **形状层单源**：PreviewDeployRequest 内嵌 DeployRequest（CEL descriptor 规则零手工复制）+ 守卫加 **descriptor 规则零漂移断言**（预览与部署的拒绝面逐规则对账，独立审查轮 #5）。
- **口径差进 fixture**：部署链投影恒 strict、RenderWorkloads 恒 isolate——预览（strict）与对照半边（isolate）差异写进断言。
- **diff 序列化铁律**：合成后 Spec 按 marshalSpec 同参确定性序列化后 diff/digest；**验收锚含序列化同参断言**。
- **admission 预检的 spec 参数变体**（独立审查轮 #6）：`CheckPeerRefs`/`CheckPreDeployNetworks` 现按冻结行寻址——抽 spec 参数变体供预览与 Submit 单源共用（「未批准 peer 引用」「preDeployJobs 裸网名」是入队即拒项，预览期必须透出）。
- **首次部署基线**：空基线形态（spec_diff 全量 added + 渲染侧空集），不照抄 render 的 ErrNotFound 路径。
- **预览载荷**：
  - `spec_diff`：复用 `spec.Diff` 轴，base 见 §1.4 多锚；
  - `workload_changes`：逐 Workload 摘要（image/command/replicas/探针/卷/网络）+ **滚动后果判定 `rollout: zero_touch | scale_only | replace(策略)`**（内容锚定等价使三态纯 spec 侧可判——fleetly 模型独特红利，操作者第一问「会不会动我在跑的容器」由它回答；时长给窗口级粗估：替换数 × L1/observe 窗）；
  - `warnings`：结构化只增——replicas 变化、blue-green 策略窗、卷钉住变更、hook 所有权合并、preDeployJobs 在场、**受理位只读预检**（配额/队列容量/父资源/peer 引用/preDeploy 网络——zeabur plan 期 dry-run 同型）、**在途部署预警**（「在途/排队部署 X 存在：其完成将使本票过期；本次确认将排队/合并/抢占@位次 N」——预览展示静态世界，admission 结局才是真实执行史，独立审查轮 #2 与产品轮共识）、**现场漂移预警**（活观测 vs 声明态对照：「现场副本 2 ≠ 声明 1——本次部署将按声明收敛回 1；要保留 2 请在本次部署声明 replicas=N（rollout=scale_only 零替换）」——spec_diff/workload_changes 基线是期望态渲染**看不见现场漂移**，此条必须显式对照 GetAppWorkloadObservations/drift 扫描事实；2026-10-30 真机案例收编：手动 docker/kubectl 扩副本被重部署回拉——模型按设计执法〔ADR-0081/0082；drift.go:398 副本比对 + workload.drift_detected 事件在册〕，B1 收掉的是惊讶不是收敛语义）；warnings 可并入修正（如受影响 restart 一键并入）登记 v2 档；
  - Teardown 面（**新装配非单源**：行级联在 API 层、保留是缺席语义）：级联清单（将拆载体、将删 hook/route 行）+ 保留清单带理由（databases/volumes/secrets/上传产物）+ **DrivingByApp 冲突透出**（三层同谓词预检的只读版）；一致性 golden 钉；
  - `confirmation { ticket, expires_in }`。
- **脱敏红线（显式化，独立审查轮 A1）**：预览载荷脱敏 = **ADR-0071 决策 5 同款**——不含 env 值集（env 至多列 name 集）、不含 `FLEETLY_DB_*`（材料注入发生在 materialize 尾段 `databaseenv.go:77-82`，预览投影变体**停在 projectRevision、绝不进 materialize 段**——此为红线不是巧合）、不含代戳/材料值。验收锚加「预览载荷零 FLEETLY_DB_*/材料值」断言。
- **诚实边界**：预览展示**渲染意图**非 runtime 现场（真跑着什么 = `GetAppWorkloadObservations` 既有面，同页并列）。

### 1.4 Confirm Ticket（design-it-twice，v3 修订）

| | 甲'：进程内票据店 + **验票/烧票下沉 SubmitSpec 受理临界区**（v3 推荐） | 乙：无状态 token（zeabur = AEAD 密封；完整性变体） |
|---|---|---|
| 形态 | 票据店第四 purpose；**confirmation 作为 SubmitSpec/DeleteApp 入参，验票+烧票在 appMu 临界区内、与冻结/admission 同事务边界** | keyring 新钥签名 {payload, exp} |
| 单用途 + 原子性 | **天然闭合**：单临界区内「验票→冻结→比对→admission→烧票」，并发同票第二请求在 appMu 外排队后撞已烧票（独立审查轮三方收敛：handler 侧验票迫使合成双跑，两跑之间 webhook 落新冻结体即击穿「确认的正是展示的」；「先验后烧」两步原语有 TOCTOU 同票双执行——单临界区一次关闭两类洞） | 拿不到单用途（须服务端 jti 状态，即退化为甲） |
| 重启 | 票失（重启重预览；单进程可接受，browse grant 先例） | 票在（无收益） |
| 裁决 | **从甲'**：v2 的「handler 内先验后烧」整体作废 | 否决 |

- **票面绑定物**：`request_digest` = sha256(marshalSpec(合成后 Spec 规范形))——**比对对象是同临界区产出的冻结体 `rev.Spec`**（非 handler 侧重算，消灭合成双跑）；`semantics` = 提交语义位（supersede/override_hook 显式入指纹——同一 spec 不同提交语义的用户可见后果分叉，v2 漏绑，独立审查轮 #3）；`base` = **多锚**（最新冻结体 digest + LatestSucceeded digest + **活跃变量环境摘要 + hook 行版本**——服务端合成输入漂移由此归 STALE 而非误判 MISMATCH，#7）；`preview_digest`；操作者与 Team；TTL **600s**。
- **teardown 票绑定**（v2 未定义，#8）：base = app 行版本 + hook 行 digest + **route 行 ID 集摘要**（预览后新增的 route/hook 若不锚，确认时会被清单从未展示地删除）。
- **验票序（两段分工：①②③ 通用半边在拦截器〔ConfirmableVerbs 表内动词〕，④⑤ 内容绑定在 appMu 受理临界区）**：① 票存在未过期 → ② purpose → ③ **请求者身份比对**（authn TokenID/UserID+TeamID vs 票面操作者——票不得为纯 bearer 物；events 票据的身份解绑理由〔匿名 SSE 无身份〕不迁移到已认证面，A3）→ ④ request_digest 与冻结体比对（含 semantics）→ ⑤ base 多锚与当前状态比对，漂移即 STALE。**无票直发（ConfirmableVerbs 内动词且无 --yes 通道）即拒**（新码，重裁 §8.2'）。
- **烧票契约**：discard 仅在 **Deployment 行落库后**（SubmitSpec 是冻结/admission 两事务——冻结成功而 admission 拒（queue full）时**不烧**，票保留可重试，冻结体内容寻址复用保证重试幂等）；burn 后保留**短窗 tombstone**——不同幂等键重发同票时回「已确认过，请查部署状态」而非与过期/伪造不可分辨（#4）。
- **幂等降级窗（诚实边界）**：idem complete 落库失败的降级窗（enforcer.go:213-215）内同键重跑撞已烧票 = 预期 MISMATCH，入 CLI 文案清单。

### 1.5 与 zeabur 的两处刻意偏离（修正记录）

1. **不引入 runtime 对象版本钉**。zeabur 按对象有无 generation 分钉（workload 钉 generation、ConfigMap/Secret/Namespace 回退 resourceVersion），因其 apply 立即执行且共享可变集群；fleetly 确认后走 Admission→收敛，集群侧变化由 Drift + Converge（ADR-0082）兜底，预览新鲜度绑逻辑基线多锚即足（swarm `Version.Sequence`/k3s resourceVersion 现状不上交引擎，`swarm/runtime.go:255` 仅内部 CAS）。
2. **不走 AEAD 密封**。zeabur 加密因 request 可能含用户手输密钥要过浏览器；fleetly Secret 值 write-only、注入发生在 materialize——预览载荷与票面均无密文（脱敏红线见 §1.3），完整性用规范形指纹 + 进程内票即足。

### 1.6 CLI / Console / Agent 面

- CLI（第二轮定稿）：**默认 = 交互式预览确认**（打印预览 → y/N；非 TTY 无票即拒）；`--yes` 逃生门（脚本/CI，跳过预览直发）；`--preview`（打印预览 + ticket id 后退出，两调用流的第 1 步——Agent 形态）；`--ticket T`（带票部署，可配 `--wait`）；三形态 + 票拒绝 + 幂等降级窗 MISMATCH 全进 golden。Rollback 同形（强制）；Restart/Scale 无票面（豁免）。**`fleetly apps scale <app> <process> N`**（ScaleProcess 的皮，随本批）——真机案例动线闭环终点。
- Console：DeploySheet 加预览步（diff + 滚动后果徽章 + warnings + 确认钮发带票 Deploy）；**重预览增量展示**（STALE 后重预览交代「这次会写而上次不写的 / 上次写而这次不写的」）；拆除面 confirm-dialog 升级为预览清单形态（B4a 联动）；IAB 走查进 DoD。
- Agent/skills：预览面进 deploy 相关 skills 的可选动线（散文守卫同步）；B1 外独立小修——DeleteDatabase console 弹窗保留语义文案自相矛盾（「卷保留」vs "Data is destroyed"，`databases.tsx:1042-1049`），随 B1 批或提前单独修（文案单源化）。

## 2. B2：诊断引擎（Diagnosis / Finding / Fault Class / Platform Incident）

### 2.1 分层与边界

| 层 | 回答 | 输入（facts） | 输出 | 与既有面分界 |
|---|---|---|---|---|
| **Diagnosis** | 为什么现在坏 | 观测事件快照（**新导出面**，见 2.4）+ 事件近窗（**新 App 索引**）+ 告警 firing 行（**+pending 行，随 B2a 持久化**）+ spec 对照事实 | per-App/per-Process 诊断条目 | Alert Rule=阈值趋势通知导向；Diagnosis=读时计算解释导向（不评估趋势、不收敛；**已裁投递两类**：crash-loop 类 Finding 复用 Notification Channel 投递，其余纯读） |
| **Platform Incident** | 平台侧单因波及谁 | 组件健康快照（**新共享导出**）+ engine health 行 + managed 状态（**新只读导出**）+ Route 归属 | 事故条目 + 受影响 App/Route 面 | Drift=收敛导向；Incident=解释与波及，绝不触发收敛 |
| **unexplained 兜底** | 没规则命中也要有话说 | 同上 | 最小陈述（观测态 + 最近相关事件 + 建议动作） | 完备性承诺：unknown 永远不是全部答案（守卫承载） |
| （后期）Advice | 缺什么（还没坏） | Spec/投影事实 | 建议条目 | severity 预留 advice 档，v1 不实施 |

边界五分界（D0 随批入册）：Event=事实订阅；**doctor/GetStatus=平台自检**；**Explain=资源自描述**；freezeguard 在册 "diagnostics face"（exec/browse 冻结豁免义，第四义——随 D0 改称「排障面」或澄清）；**部署失败解释=deployment error 与 build log 承载，Diagnosis 不重复**（首批规则集全稳态面的取舍由此显式化；Route/证书面（502/ACME）是否进首批随裁决，与 external 枚举自洽性挂钩——独立审查轮 B2-2）。

### 2.2 Finding 注册表（errcode 同构三链）

`internal/domain/shared/model/finding/`（与 errcode/eventcode 同座）：字段 `ID / Summary / Severity（已裁两级：warning|advice）/ FaultClass / Source（file path + 函数 + ADR/提案锚）/ Fix`。三链执法同款（fail-fast panic + golden TSV + usage 反扫双向保鲜）。**Fix 是结构化动作**：`{ verb（可选）, params 预填, console_path 深链 }`——**纯值类型**（domainpurity 红线）；**动词面（已裁 §8.3 闭环）**：终归 Deploy 的动作（edit-healthcheck 类经 sheet→Deploy 走 B1 预览）+ rollback/restart（已裁随 B1 v1 带 optional confirmation——Fix 按钮直发带票动作，不再退回纯 UI 确认）；apps scale（已裁 §8.10，现场漂移 Fix 的 verb 落点）；platform 类对 project 操作者 Fix = Components 深链或显式空 + 说明文案（「等管理员修」形态，B2-1）。errcode 静态 Suggestion 不动。

### 2.3 Fault Class（X-Class 族新词条，候选枚举）

| 枚举 | 含义 | 自托管真实语义（独立审查轮修正：单管理员下「谁来修」坍缩，价值在「往哪看」） |
|---|---|---|
| `app` | 用户代码/镜像/依赖（崩溃循环、退出码、OOM、健康检查不过） | 往我这看（改代码/镜像） |
| `declaration` | 声明/配置（healthcheck 缺失、端口不符、变量残留、探针路径错） | 往我的声明看 |
| `platform` | 受管组件/控制面/收敛环/集群（zeabur 的 Cluster/Component/平台自身三分在此合一——**已裁 §8.1③ 定形**，不再切 platform/managed 两档；修复动线差异由 evidence 与事故条目表达） | **不是我的问题**——「需要你」页单列「平台造成」组，等管理员修 |
| `external` | 上游外部（镜像源、ACME/DNS、Provider API——若不进首批规则，与规则集的自洽性随 §2.1 分界裁决） | 往外部看 |

产品原则（守卫承载）：**platform 的错永远不读成用户的错**——Platform Incident 能解释的受影响 App，其诊断 fault_class 改写为 platform 且 evidence 链回事故条目（zeabur link() 同型：改写为事故自己的归属）。

### 2.4 架构落位（ADR-0075 四环 + modules 三分离参照）——**facts 面现实修正（独立审查轮核心纠正）**

- **规则 = domain 纯函数**：`internal/domain/services/diagnosis/`（domainpurity 三红线内）；Facts 值结构 + 「读不了 ≠ 不存在」三态。
- **facts 不是「现成」而是三件新造 + 两件新导出**（v2 声称「经 Engine 导出面装配」过于乐观，独立审查轮 #9/#10/#11 逐项核实现状）：
  1. **观测事件快照导出**：活状态（`WorkloadEvent.State/ExitCode/Reason`）只在未导出 obs 缓存——新增只读导出面（WorkloadEvent 快照）；`InspectAppWorkloads` 降级为 spec 对照补充事实（其 State 恒 Running、Replicas 取期望，喂不动崩溃循环规则）；
  2. **per-workload 终态计数 = 事件账本派生（第二轮单源化定稿）**：全仓无 restart 计数器——**砍掉内存计数候选**，唯一事实源 = 新增 container 终态 eventcode（workload 终态/退出码入事件账本，三链纪律入册）+ 3 的 (team, aggregate, aggregate_id) 索引；计数纯派生、无独立计数器状态（重启安全、受 Event 保留窗约束——与 pending 窗落行同哲学：事实持久化，派生不留状态）；
  3. **事件 (team, aggregate, aggregate_id) 索引查询**：outbox 现只有 team 维前向扫描（ListAfter ≤1000）——「近窗事件」要么新索引要么诊断专用按 App 分组装配面；
  4. **组件健康共享导出**（B2b）：systemgrpc 的探活聚合 + 10s TTL 缓存在未导出方法——加共享 TTL 缓存快照导出（不得绕开缓存自行探活，复刻匿名放大）；
  5. **managed 状态只读导出**（B2b）：`manageds{gens, ensure memo}` 现仅写侧 `KickManagedLoop`。
- **id 空间中心收口**：规则可按域分文件，Finding id 注册表单点。
- **evidence = fleetly 域引用，闭式清单（独立审查轮 A7）**：DiagnoseApp evidence = 本 App 域引用 + **组件名**（不含组件 Details/内部地址、不含他队 App/Route——波及面明细只存在于 ListPlatformIncidents 平台面）；事件原文引用与事件面同口径不转写放大。
- **「需要你」页成本口径**（#12）：ListProjectFindings 定页大小 + 扇出并发上限，或廉价先行筛（firing 行/非健康观测命中才深诊断）——单 App 诊断含一次 runtime 往返，N App 串行不可接受。

### 2.5 API / Console / Agent 面

- RPC：`DiagnoseApp(app_id)`、`ListProjectFindings(project_id)`（**已裁 §8.9：v1 挂 project 域**，授权沿 project 链；平台级跨 App 视图后置，届时 platform:read + RequirePlatformAdmin）、`ListPlatformIncidents()`——**授权钉死 platform:read + RequirePlatformAdmin 腰带（ListPlatformBackups 先例；显式不采用 GetStatus 的已认证即见口径——事故载荷含跨 Team 波及面，独立审查轮 A5）**。grpcapi policy 注解齐全；守卫连坐与 B1 镜像（writeclassguard/ADR-0081 附录/AssertAllRegisteredHavePolicy/scopeguard，入验收锚——独立审查轮治理 #5）。
- **CLI 动词**（独立审查轮遗漏-1：skills 动线全靠 CLI 动词，无动词则 Agent 消费不到）：`fleetly apps diagnose <app>`（双形态 golden）、`fleetly findings list`——skills/deploy-diagnose 升级为**新命令序列**而非措辞同步。
- Console：App 详情「诊断」段（四层形态）；「需要你」页（fault_class 分组 + **platform 组「平台造成（等管理员修）」特殊形态** + 平台事故单因下挂受影响 App + unreadable 诚实展示）。
- **v1 首批规则集**（小而准，稳态面）：崩溃循环（终态计数 + 终止态推断，不信单一 waiting reason）、OOM（区分有无 limit）、健康检查不过、镜像拉取失败、稳态 stopped（看门狗语义解释化）、四组件事故（波及面：公网 Route 集/构建部署面/日志面/图表面）。Route/证书/ACME 面是否进首批随 §2.1 分界裁决。**Advice 档首条候选预登记**：现场漂移建议（`workload.drift_detected` 事件在册零新事实源——「你手动的现场变更（如扩副本）是漂移，收敛姿态 all 会拉回；要保留请声明化」，fault_class=declaration，Fix = 预填 replicas 的部署面/§7 scale 动词）——与 B1 现场漂移预警同源规则（预警收部署时刻，Advice 收平时），Advice 档实施时首条即它。
- **fixtures**：`case.yaml + input.yaml + expected.json` partial-compare 三件套；负向用例为主体（≥6 条「这不是 X」起步）；真实事件样本进 fixture。

## 3. B3：测试两件（设计从简）

- **B3a corpus**：`internal/application/composeintake/testdata/corpus/`（外部样例逐文件头标注来源与 license）+ `manifest.json`（Spec digest 或精确拒绝码）。断言四层：永不 panic、逐文件 golden 相等（漂移须 `-update` + commit 理由）、manifest↔目录一致性、**再生成双跑防随机**（同文件两次受理 digest 必同，钉归一化确定性）。语料优先仓内（模板目录 compose + dokploy 语料），外部 Apache-2.0/MIT 起步 30-50 文件。
- **B3b 契约双跑**：`internal/testsupport/runtimecontract` 断言清单模块（Ensure/Remove 幂等、scale-only 零替换、代戳恒同移、观测上报口径），单测对 apitest FakeRuntime、e2e `dind-runtime-contract.sh` 对真 swarm/k3s 执行同一清单；守卫断言两侧同名零缺席；新腿同步 mise+CI+e2eciaccount。不开新测试带、不动 testruntime 收窄边界。

## 4. B4：console 两件（设计从简）

- **B4a risk 分级**：`console/src/model/risk.ts`（纯函数：触发面 × 影响面 → 形态档）三档——`standard` / `wide`（宽框 + 后果先行 + 级联/保留清单，B1 Teardown 预览联动）/ `type_to_confirm`（输入资源名，仅 Project 删除与平台级操作）。执法：confirm-dialog 强制收 risk 元数据 prop（反模式守卫扩条：裸 AlertDialog 零容忍）；risk 模块 vitest 分组单测。**Undo 哲学登记不实施**（部署面已有 Rollback/Replay 对应物；轻操作 undo 通知为后续档）。
- **B4b 体积预算**：`console/scripts/check-ui-budget.mjs`（node:zlib brotliCompressSync 零依赖，量 dist 产物）对 `console/budget.json`（committed；首期现产出定基线）；入口 `console:verify` build 后执行；**上涨须同 commit 改预算文件且 commit message 带理由**（强于 zeabur 注释纪律的自设加码；执法挂 consolecommit 族守卫）。口径双层：entry JS/CSS + 懒加载 chunk 逐项上限（提案 §8.7 终裁）。

## 5. 诚实边界汇总

B1 不是原子性机制、不是审批流；**确认默认强制（重裁）+ webhook/git、Restart/Scale、`--yes` 三类豁免**；预览是渲染意图非 runtime 现场；不引入 runtime 版本钉；from_build 源 digest-pending（逐字节守卫不覆盖）；验票/烧票在 appMu 临界区内（进程内票，重启即失需重预览；通用验票半边在拦截器）；幂等降级窗撞已烧票属预期；Teardown 预演靠 golden 钉；预览载荷脱敏是红线（ADR-0071 决策 5 同款）非巧合。B2 v1 读时解释层：不收敛、Advice 档不实施；**通知已裁投递两类**（Platform Incident + crash-loop Finding，App 级全量后置）；**Alert Rule pending 窗直接改列**（首轮裁持久化，第二轮定形态：无双写过渡——开发阶段无兼容包袱）；**终态计数 = 事件账本派生，无独立计数器状态**；facts 面其余两件新造两件新导出；首期规则集稳态面，部署失败解释归既有 error 面。B3 corpus 不改 fail-closed 受理律。B4 不引入 lint 配置；undo 不实施。

## 6. 验收锚（与提案 §10 同源，实施批细化）

- [x] B1/B1s：三段单源 + 「预览=执行」守卫（image/digest 源逐字节；from_build 另断；strict/isolate 进 fixture；**marshalSpec 同参断言**；**CEL descriptor 零漂移断言**）；**脱敏红线断言**（预览载荷零 FLEETLY_DB_*/材料值/env 值集）；admission 预检 spec 变体复用单测；**验票/烧票临界区测试**（并发同票第二请求拒绝；webhook 落新冻结体后确认拒绝）；票拒绝腿全谱（过期/漂移/不匹配/请求者不符/并发同票/**无票直发**）+ 幂等降级窗 MISMATCH 腿（CLI 文案清单含 already-redeemed 指引 + apitest 同票重发腿；同键降级窗注入口径随 B2a 故障注入批补 e2e）；**豁免腿**（webhook/git、Restart/Scale、`--yes`）与 **ConfirmableVerbs 表↔confirmguard 对账**；幂等重放不烧票；烧票契约（queue-full 不烧可重试；tombstone 可辨因）；**LedgerOnly 审计行**断言（deployment id + preview_digest 关联可回溯）；滚动后果判定字段 golden；**Rollback 验票腿**（验票在、铸票面留 B2a——--yes/--ticket 通道 + 伪造票拒绝 golden，裸调用本地指引）；**ScaleProcess 双形态 golden + scale-only 零替换断言（任务 ID 不变）**；七件守卫连坐 + confirmguard 同 commit；CLI 三形态 golden（交互预览默认/--yes/--ticket，含非 TTY）；e2e 腿 `e2e/dind-confirm.sh` 八腿全绿（无票直发 428/STALE/MISMATCH/webhook 豁免/teardown TOCTOU 零孤儿/审计行/scale 零替换）；console 走查记录随 B2a 批（DeploySheet 预览步 + STALE 自动重预览已实装）。（2026-10-30 第 2 批实施，ADR-0089）
- [x] B2a：Finding 三链绿 + 纯值类型 + severity 两级定形（golden 形状冻结）；负向 fixtures ≥ 6；unexplained 兜底守卫；platform 改写断言（纯函数在册，B2b 接事故面）；**facts 断言**（container 终态 eventcode 三链入册 + 事件 (team, aggregate, aggregate_id) 索引查询 + WorkloadEvent 快照导出 + **计数纯派生无独立计数器状态** + 零旁路：observ/managed/组件健康不经导出面即红）；**Alert Rule pending 窗直接改列迁移 + 重启不重置断言 + 诊断吃 pending 态断言**；**投递两类断言**（Platform Incident/crash-loop Finding 投、App 级其余不投，通道复用 Notification Channel）；守卫四件连坐（writeclassguard/ADR-0081 附录/AssertAllRegisteredHavePolicy/scopeguard）；CLI `apps diagnose`/`findings list` 双形态 golden；skills 动线升级为新命令序列；「需要你」platform 组形态走查（待 staging）；ListProjectFindings 分页/预算。（2026-10-31 第 3 批实施，ADR-0090；fixtures 实落 18 组，unexplained 守卫 1575 组）
- [ ] B2b：dind 故障注入腿（traefik 停 → 波及面 + 诊断改写）；组件健康/managed 导出面单测；ListPlatformIncidents = platform:read + RequirePlatformAdmin。
- [x] B3a：corpus 四层断言零漂移。（2026-10-31 第 3 批实施，ADR-0090：49 文件五层断言；实测咬出受理面确定性缺陷——六拒绝面 map 遍历序使多违反报错不定，已按排序键执法修复，gitea-postgres 双违反原形恢复，26 条 accept digest 逐字节不动；连带补钉 corpus digest 的 detrand 空白归一〔json.Compact，gateway 同先例〕——跨构建稳定）
- [ ] B3b：清单两侧同名对账守卫 + e2e CI 对账。
- [x] B4：risk 单测 + 反模式扩条双向；预算上涨无理由即红（机制=超额不改 budget.json 即红，理由纪律=ADR-0086 同 commit 律）；基线冻结。（2026-10-30 第 1 批实施，ADR-0086，console:verify 373 测试绿）
- [x] D0：词条 + 非资源行/非实体声明（Confirm Ticket/Finding/Diagnosis/Platform Incident）+ Diagnosis 四义划界 + wording 双向保鲜 + 引述词 per-site 豁免（政策已入册 ADR-0085；实际豁免条目随 B1/B2 批代码落地时逐条登记）。（2026-10-30 第 1 批实施，ADR-0085）

## 7. 开放问题

全部已裁（2026-10-30 首轮 13 项 + 第二轮 2 重裁 4 改良，记录见提案 §8）：程序四批全量 / B1 强制度**第二轮翻为默认强制+逃生门**（原 opt-in 作废；webhook/Restart·Scale/--yes 豁免）/ 范围两面 + delete 族轻量确认 + Rollback 强制 / ACTION+不进 FrozenVerbs / 词汇全套候选名 / Fault Class 四档合一 / severity 两级 / 通知投递两类 / pending 窗持久化且**直接改列** / 聚合域 project / 预算双层 / 语料 Apache·MIT+30-50 / ScaleProcess 一等 RPC 随第 2 批 / ConfirmableVerbs+confirmguard / 终态计数单源事件账本。**余留实施批定稿项**：预览载荷 Workload 摘要粒度（倾向摘要 + GetAppWorkloads 深链，走查定稿）。

## 8. 交叉验证记录

### 8.1 事实校验轮（2026-10-30，v1→v2）

**流程**：v1 落盘后派两份独立校验（A = zeabur 机制事实对源码核对；B = fleetly 贴合度对代码/ADR/CONTEXT 挑刺）。**A 9 条勘误全采纳**（generation/resourceVersion 按对象分钉、risk.ts 表+分支形态、「涨须理由」是注释纪律、incident 含组件类、FaultOrigin 六源含平台自身、AEAD≠HMAC 标注、「完整 YAML」是裁剪形、file:line 承诺改诚实口径、restartedAt 断言不在册）；**A 补充 6 条全采纳**（Undo/重预览增量/warnings 可并入/plan 期 dry-run/拒收证据化/corpus 双跑）。**B 6 条设计风险全采纳**（READ→ACTION 桶、三段抽取、Teardown 新装配、base 双锚、from_build digest-pending、空基线）+ 硬约束全入册（拦截器链序、先验后烧原语、idem 降级窗、request_digest 绑规范形、observ 导出面、纯值类型红线、doctor/Explain 划界、守卫七件连坐、wording per-site 豁免、skills 消费方、marshalSpec 同参、strict/isolate fixture）。

### 8.2 独立审查轮（2026-10-30，v2→v3）

**流程**：四侧独立审查（①产品/操作者价值 ②引擎机制不变量 ③安全 ④治理/词汇/守卫），纪律 = 不复述文档自认项、只报错漏。共 39 条发现（①9/②12/③9/④9），采纳 33 条（其余为验证通过项或分诊为缺省值写死）。**三方独立收敛（最高置信，全部采纳）**：
- **票据原子性**（②#1 P0 + ②#4 + ③A2 P1，从引擎与安全两侧打到同一洞）：handler 侧验票迫使合成双跑（两跑间 webhook 落新冻结体即击穿主承诺）；「先验后烧」两步原语 TOCTOU 可并发同票双执行——**裁决：v2 的 handler 验票整体作废，验票/烧票下沉 SubmitSpec 受理临界区（appMu 内、与冻结/admission 同边界），一次关闭两类洞**（§1.4 甲'）。
- **在途部署预警**（①B1-3 + ②#2）：STALE 在「预览后确认前有部署推进」时必然触发属诚实拒绝但高频动线白付重预览——warnings 增在途/位次预警 + 重预览增量展示覆盖（§1.3/§1.6）。
- **B2 facts 面不现成**（②#9/#10/#11 内部一致）：事件无 App 索引、崩溃循环无 restart 计数器、活状态只在未导出缓存、组件健康/managed 均未导出、InspectAppWorkloads 是 spec 对照面——§2.4 改「三件新造两件新导出」，v2 的「经 Engine 导出面装配」表述修正。

**单侧高价值采纳**：①滚动后果判定字段（zero_touch/scale_only/replace——内容锚定红利，§1.3）+ CLI 双旗标（--preview/--ticket，§1.6）+ 通知前提修正（Alert Rule 仅 cpu/mem 盖不住可用性故障，§8.5 改推荐投递 Platform Incident + crash-loop）+ B2 无 CLI 动词则 Agent 消费不到（§2.5）+ platform 组「等管理员修」形态（§2.3）+ Fix verb 与 B1 范围的依赖倒挂（提案 §8.3 追加裁决）+ DeleteDatabase 弹窗文案自相矛盾（既有小修，§1.6）；②提交语义位入指纹（#3）+ 变量/hook 行入 base 多锚且服务端漂移归 STALE（#7）+ teardown 票绑定定义（#8）+ CEL 规则单源（#5）+ admission 预检 spec 变体（#6）+ ListProjectFindings 分页预算（#12）；③脱敏红线显式化（A1，ADR-0071 决策 5）+ 请求者身份比对（A3）+ PreviewTeardown 钉 apps:admin（A4）+ ListPlatformIncidents 钉 platform:read+RequirePlatformAdmin 拒 GetStatus 口径（A5）+ 确认审计行（A8）+ 冻结残余形态文档化（A6 验证通过）；④§8.1⑨→§8.9 引用修正 + 「诊断」第四义（freezeguard "diagnostics face"）入 D0⑤ + 非资源行/非实体声明（Confirm Ticket/Finding/Diagnosis/Platform Incident）+ severity 形状与开放问题冲突标注 + B2 守卫连坐进验收锚 + 守卫清单三处互不一致对齐（七件套）+ 「预览阶段」引述词 + 裸 Incident 改全称 + 两条自declared硬约束落锚。
**验证通过项**（如实记录，不改文档结论）：③A6 冻结推演（freeze.go:59,77-78,89 + grpc.go:248-251 证实）；②admission latest-wins 不吞确认请求、idem 重放在 handler 前、链序、queue-full 组合安全性；④ADR 16 项对号全符、先例实存、`apply` 六处全在引述语境。
**残余风险共识**：三段抽取工程面（api 前半重构）+ 验票下沉对 SubmitSpec 签名的侵入（confirmation 入参）——两者叠加使 B1 实施批的 engine 改动比 v2 估计再大一档；票据原子性方案依赖「appMu 临界区贯穿冻结+admission」的既有事实（admission.go:96-98 持锁范围），实施批首项须验证该前提。
