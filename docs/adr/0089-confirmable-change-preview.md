# ADR-0089：变更预览与确认闭环（ConfirmableVerbs 强制 + 票据临界区 + ScaleProcess）

日期：2026-10-30 ｜ 状态：已实施（zeabur-oss 借鉴程序第 2 批 B1+B1s，裁决记录见 `docs/proposals/2026-10-30-zeabur-oss-borrowing.md` §0/§8〔含 §8.2' 重裁与 §8.10〕，设计规格 `docs/design/2026-10-30-zeabur-oss-borrowing.md` §1；词汇面 ADR-0085、console 分级 ADR-0086 为第 1 批前置。任务编号原拟 0087，`ls docs/adr/` 复核发现 0087/0088 已被并行引擎分解程序文档批占用，顺延取 0089）

## 背景

fleetly 的破坏性/高影响变更确认此前是**纯 UI 约定**：console 弹窗文案与执行物之间无机制绑定，CLI/API 直达；操作者看不到「这次部署会不会滚动、动几个、断不断流」的后果视图。真机案例（2026-10-30）：手动扩副本后重部署被声明值回拉，全程无预警。zeabur-oss 参照（引述语境）：plan→密封 change token→apply 的确认闭环 + 每对象 before/after 评审输出；其两条架构赌注（无状态投影、每请求用户凭据）已被提案 §7 明确不抄。B1 立的是**机制事实**：一次变更被确认的恰是操作者预览到的载荷，且事后可证（审计落点）——不是原子性机制（执行侧幂等/收敛由 ADR-0024/0064/0082 承载），也不是审批流。

裁决基线（提案 §8 已全裁，本文不重议）：**默认强制 + 逃生门**（重裁 §8.2'，opt-in 作废）；强制集 = Deploy + delete 族 + Rollback；webhook/git 天然豁免（材料归 git ADR-0072，代码评审即确认）；Restart/Scale 豁免（轻且可逆，与 ADR-0086 风险分级间档）；脚本/CI `--yes`；两处刻意偏离 zeabur：不引入 runtime 对象版本钉（收敛模型 + Converge 兜底，确认新鲜度绑逻辑基线多锚即足）、不走 AEAD 密封（Secret write-only、预览无密文，完整性 = 规范形指纹 + 进程内票）。

## 决策

1. **ConfirmableVerbs 表驱动立法（默认强制 + 三类豁免）**：`internal/application/governance/confirm.go` 的 `ConfirmableVerbs`（FullMethod → 立法理由）钉强制集 = `Deploy`、`Rollback`、`DeleteApp`（delete 族先落 App 面，Database/Project 轻量确认载荷随 B2a 批）；ConfirmGuard 拦截器承担通用验票半边（①票存在未过期 → ②purpose=confirm → ③Verb 匹配 + 请求者身份比对——票不得为纯 bearer 物），无票且无 skip 即拒 `E_CONFIRMATION_REQUIRED`。豁免族：webhook/git 面（不经 Deploy 动词，engine 侧 `ConfirmInput nil` 路径天然短路）、Restart/Scale（不入表）、`--yes`（`Confirmation{skip:true}` 显式通道——skip 不验票不烧票，受理照常）。新码三枚入册（`E_CONFIRMATION_REQUIRED/STALE/MISMATCH`，FailedPrecondition；REST 面显式钉 428 Precondition Required——RFC 9110「先预览再确认」的前置动作未完成，errcodeToHTTP 显式决策）。幂等重放不烧票（idem 拦截器在 confirm 之前，重放不达 handler）。
2. **三段单源抽取（预览 = 受理的只读孪生）**：PreviewDeploy 与 Deploy 共用同一段链，per-handler 散写消失——段一 `deployIntake`（api 受理前半：授权 → spec_file 解析 → upload 归属 → 变量 lookup → normalizeDeploySource → hook 闸门与 merge 裁决）；段二 `composeSpec`（engine 合成：MergeNone/MergeMaterial/MergeOverlay + 三级变量合成，预览与 SubmitSpec 同一函数）；段三 `projectSpecMem`（projectRevision 的内存 spec 投影变体——不冻结、不 stamp 代戳、不 pinVolumes、**绝不进 materialize 段**）。`PreviewDeployRequest` 内嵌 `DeployRequest` 同一 message（四源/CEL descriptor 规则单源继承，防「确认后才发现拒收」）。受理位只读预检抽 spec 参数变体（`CheckPeerRefsSpec`/`CheckPreDeployNetworksSpec`，与 Submit 单源谓词——入队即拒项预览期透出为 warning）。防漂移守卫：同输入预览投影字节 == Deploy 冻结体字节（apitest 逐字节断言；marshalSpec 同参——diff、digest、冻结咽喉同序列化）。
3. **票据临界区验烧（甲'方案，v2 的 handler 验票整体作废）**：验票 + 烧票下沉 SubmitSpec 的 appMu 受理临界区——「合并 → 合成 → 冻结 → [appMu] ④request_digest 比对（vs 本临界区冻结体，非 handler 侧重算——消灭合成双跑）→ ⑤base 多锚比对（admission 事务 checks 内，与落行同快照）→ admission 落库 → [仍在 appMu] 烧票 + LedgerOnly 审计行」。一次下沉关闭两类洞：handler 侧验票迫使合成双跑（两跑之间 webhook 落新冻结体即击穿主承诺）；「先验后烧」两步原语的 TOCTOU 同票双执行。票据店第四 purpose `confirm`（进程内，重启即失需重预览——单进程可接受，browse grant 先例）；原语 = peek（不删，锁内重 peek 无害）/discard（单用途烧 + **120s tombstone**——「已确认过，请查部署状态」与过期/伪造可分辨，覆盖并发同票第二请求与幂等降级窗重发高发段）。**烧票契约**：discard 仅在 admission 落库后；冻结成功而 admission 拒（queue full）不烧、票保留，重试经冻结体内容寻址复用（同 digest 命中既有 rev，④仍成立）。辨因序：世界先核对（base 漂移 → STALE），世界一致而 digest 不符才是请求被改（MISMATCH）。
4. **票面多锚（base 绑逻辑基线而非 runtime 版本）**：deploy 票 = `request_digest`（sha256 over marshalSpec 规范形 + 提交语义位 supersede/override_hook）+ `preview_digest` + base 四锚（最新冻结体 digest〔排除「自己就是最新」形态〕、LatestSucceeded digest、活跃变量环境摘要、hook 行版本——服务端合成输入漂移由此归 STALE 不误判 MISMATCH）+ 操作者/Team + 目标 App + TTL 600s。teardown 票 = app 行版本 + hook 行 digest + **route 行 ID 集摘要** + RequestDigest 绑预演清单本体（级联条目同序拼接——票面确认的恰是展示的清单）；PreviewTeardown scope 钉 apps:admin（预演动词不得低于被预演动词的权限门槛）。
5. **预览载荷与脱敏红线（ADR-0071 决策 5 同款）**：`spec_diff`（vs LatestSucceeded 冻结体；空基线 = 全量 added，不照抄渲染面 ErrNotFound）+ `workload_changes`（逐 Workload 摘要，无 env 字段）+ 结构化 `warnings`（受理位只读预检/在途部署预警/现场漂移预警等）+ `confirmation{ticket, expires_in}`。红线：零 env 值集（至多 name 集）、零 `FLEETLY_DB_*`（注入在 materialize 尾段，投影停在 projectRevision）、零材料值、零代戳。**diff 输入侧脱敏（如实记）**：两侧 JSON 树的 env 值面先替换脱敏标记再 diff——若在输出侧脱敏，空基线全量 added 会以顶层子树条目（blob 形态）展开，env 值会借塌缩条目泄露；输入侧先行使值不因路径形态而漏，语义代价 = env **值**变化不出条目（键增减照常 diff），这是「至多 name 集」的诚实取义。
6. **rollout 判定纯 spec 侧（内容锚定红利）**：`rolloutOf` 三态 `zero_touch | scale_only | replace`——内容锚定等价（ADR-0064）使滚动后果在 spec 对照面即可判，不触 runtime；REMOVED = replace（rollout 轴破坏端）。操作者第一问「会不会动我在跑的容器」由预览直接回答；真跑着什么 = `GetAppWorkloadObservations` 既有面（诚实边界：预览展示渲染意图非 runtime 现场，现场漂移经 warnings 显式对照）。
7. **审计行（事后可证）**：兑换/跳过落 **LedgerOnly** FactRow——`confirm.redeem`（AfterFP 携 verb/preview_digest/request_digest/base 四锚 → deployment id）与 `confirm.skip`（proof=skipped）；取证面单行无流面孪生（操作者动词的流面事实由 deployment.create 承载）。带票部署与裸部署事后可辨——「机制事实」有可查证落点。
8. **ScaleProcess = ACTION 裁量追认（W1 钉的 ADR-0081 §5 边界，本文追认）**：`ScaleProcess(app_id, process, replicas)` 一等 RPC（对称 ScaleTask 的 scope/幂等/writeclass 表项），服务端读当前生效 Spec（LatestSucceeded 的 to_revision——「最新冻结≠生效」分野锚）→ 改目标进程 replicas → MergeOverlay 提交；动词自身无可持有的行（期望态落在部署管线 Revision 线上）故 ACTION（vs ScaleTask 直接落池期望态 = DECLARATION）；豁免确认强制集与冻结封禁面（Restart/Scale 轻量可逆族）。CLI `fleetly apps scale APP PROCESS N` 为其皮（ADR-0064 保证 scale-only 零任务替换）；B2a 现场漂移 Fix 的 verb 落点、真机案例动线闭环终点。
9. **诚实边界（本批不做到的，防回头重议）**：Rollback 在强制集内但**尚无预览铸票面**——CLI 只有 `--yes`/`--ticket` 两形态（裸调用本地诚实报错而非交互——交互确认后无票可带），console 为重发确认双击 skip 形态（`ConfirmSkipDialog`）；Rollback 铸票面留 B2a 修护动词批。from_build 源预览 = digest-pending 形态（镜像引用位标注「构建后落定」，逐字节守卫不覆盖）。TTL 无倒计时（expires_in 静态值，无续期面）。DeploySheet 外的 console 直发面（Redeploy 等）为双击 skip 非 preview——升级路径随 B2a。冻结期语义：Preview 不进 FrozenVerbs（冻结期可预览铸票，解冻后 600s 内可消费属预期；被预演动词本身照冻）。幂等降级窗内同键重跑撞已烧票 = 预期 MISMATCH（CLI 文案清单在案）。

## 后果

- 破坏性/高影响变更的确认从 UI 约定升为机制事实：无票直发 428、漂移 STALE、改请求/换操作者 MISMATCH、webhook/git 免票放行——全部服务端执法，CLI/console 只是呈现。
- Deploy/delete/Rollback 的调用方（人/Agent/脚本）动线三分：交互预览 y/N（TTY 默认）、`--preview`→`--ticket` 两调用流（Agent 形态）、`--yes`（脚本/CI）；非 TTY 无旗标本地拒（不发请求）。golden 三形态 + 票拒绝全谱钉死（`cmd/fleetly/cmd/confirm_golden_test.go` + apps scale 双形态）。
- 守卫七件连坐同批：writeclassguard（三 ACTION 154→157 + ADR-0081 附录）、FrozenVerbs 计数、AssertAllRegisteredHavePolicy、scopeguard、gatewayguard（REST `POST /v1/deployments/preview`、`POST /v1/apps/{app_id}/preview-teardown`、`POST /v1/apps/{app_id}/processes/{process}/scale`）、**confirmguard**（ConfirmableVerbs ↔ proto 双向：带 confirmation 字段的 RPC ⊆ 表；表内 RPC 请求必有通道；mint/Restart/Scale 族不得入表）、e2eciaccount（e2e:confirm 腿计数 12→13）。
- 预览管线与 Deploy 受理永久单源：受理前半/合成/投影三段的任何行为变更自动被「预览=执行」逐字节守卫钉住，回归即红。
- 债与后手：Rollback 铸票面与 delete 族（Database/Project）轻量确认载荷、console 直发面升级 preview、重预览增量展示的 UI 深化——B2a 修护动词批；诊断引擎（B2）消费本批的 Fix verb 落点（apps scale）。

## 验收锚（对照设计稿 §6 B1/B1s 行；本地已验勾选）

- [x] 三段单源 + 「预览=执行」守卫（image/digest 源逐字节；from_build 另断 digest-pending；strict/isolate 口径差进 fixture；marshalSpec 同参断言；CEL descriptor 零漂移断言——PreviewDeployRequest 内嵌继承）。
- [x] 脱敏红线断言（预览载荷零 FLEETLY_DB_*/材料值/env 值集；diff 输入侧脱敏含空基线塌缩形态）。
- [x] admission 预检 spec 变体复用单测（CheckPeerRefsSpec/CheckPreDeployNetworksSpec 与 Submit 单源）。
- [x] 验票/烧票临界区测试（并发同票第二请求拒绝；webhook 落新冻结体后确认拒绝；queue-full 不烧可重试；tombstone 可辨因）。
- [x] 票拒绝腿全谱（过期/漂移/不匹配/请求者不符/并发同票/无票直发）+ 幂等降级窗 MISMATCH 腿（CLI 文案清单含 already-redeemed 指引 + apitest 同票重发；同键降级窗的故障注入 e2e 随 B2a 批）。
- [x] 豁免腿（webhook/git、Restart/Scale、`--yes`）与 ConfirmableVerbs 表 ↔ confirmguard 对账。
- [x] 幂等重放不烧票（idem 拦截器序断言）。
- [x] LedgerOnly 审计行断言（deployment id + preview_digest 关联可回溯；e2e confirm.redeem/confirm.skip 双行在场）。
- [x] 滚动后果判定字段 golden（zero_touch/scale_only/replace 三态）。
- [x] Rollback 验票腿（--yes/--ticket 通道 + 伪造票拒绝 golden；**验票在、铸票面留 B2a**——裸调用本地指引、console 双击 skip）。
- [x] ScaleProcess 双形态 golden + scale-only 零替换断言（任务 ID 不变；e2e 真 swarm：原任务原样 Running + 2 Running + 0 Shutdown）。
- [x] 七件守卫连坐 + confirmguard 同 commit（writeclass/freeze/policy/scope/gateway/confirm/e2eciaccount 全绿）。
- [x] CLI 三形态 golden（交互预览默认/--yes/--ticket，含非 TTY 拒绝与互斥 usage）。
- [x] e2e 腿 `e2e/dind-confirm.sh`（mise `e2e:confirm` + CI `e2e-confirm` job）：预览-确认两调用流、无票直发拒（本地 + REST 428）、STALE（变量锚）、MISMATCH（伪造 + 同票二发）、webhook 豁免、teardown 预演 + TOCTOU + 零孤儿、审计行、scale 零替换——本地 2026-10-30 首跑全绿。
- [ ] console IAB 走查记录（DeploySheet 预览步 + STALE 自动重预览增量展示已实装，正式走查随 B2a 批文档化）。
