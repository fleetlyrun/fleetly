# ADR-0102: Functions Phase 1 实施——常驻函数 MVP 的契约面、聚合与装配

日期：2026-11-17 ｜ 状态：Proposed（设计 v3 定稿后的开工 ADR；实施随批逐任务落地并回勾验收锚）

关联：docs/design/2026-10-16-functions-api.md（**v3**——形态真源，本 ADR 不重复立法）、ADR-0077（词汇批）、ADR-0101（Revision 保留窗——本批建表即带）、ADR-0088（生长律：新模块 = 新包 + module.go + assembly 装配）、ADR-0075（四环与 Repo 接口层）、ADR-0064（内容寻址/scale-only）、ADR-0029（受管聚合先例 = Database）

## 背景

设计文档 v3（2026-11-17）已收敛全部前置裁决：竞品对照（§17）、Revision 保留窗（ADR-0101）、exposure 双向转换三律（§6）、Phase 2 拆分（§12）、容量平面收编方向（§15.4 已裁）。Phase 0（词汇）2026-10-16 已落。本 ADR 只立 Phase 1 实施的**执法面承诺**：任务序、表/环/动词的落点、守卫与验收的批次绑定——字段面与机制细节以设计 v3 为真源，不一致处以设计文档或新 ADR 显式收敛。

## 决策

1. **任务序（九任务，依赖序即提交序，小步各自成 commit）**：
   ① proto+生成：`proto/fleetly/delivery/v1/functions.proto`（FunctionsService 十动词面照设计 §5）+ scope `functions` + 幂等面（Create/Deploy）+ 冻结桶（六写动词全 frozen）+ gateway 挂载；`mise run generate:all` 与 proto 同 commit。
   ② state 迁移：`functions` / `function_revisions` 两表（列面照设计 §4.1）+ 迁移链锚；**建表即带 ADR-0101 保留律**（current 指向不可删 + 落账同事务清最旧非活跃至 100 + 聚合终局全清）。
   ③ engine functions 模块：新包 `module.go` 实现 `contract.Module`（ADR-0088 生长律，engine 零改）、assembly 显式装配；投影 `ProjectFunction`、functionLoop（databaseLoop 同族）、TeardownFunction 收口序（设计 §4.3）、看门狗期望（replicas>0 才登记）；复数普通名词 struct + 白盒测试（ADR-0075 决策 4）；NamespaceRef 增 Function 轴。
   ④ api 层：受理位（配额/名字/活跃网络在场 fail-closed/ValidateFunction 叶子校验/host 预检）+ 审计六动词 + 事件五枚（eventcode 只增带 Source 锚）+ WaitFunction（事件信号 + 行重读真源）+ **回滚前置校验**（secret_refs 缺引用 E_CONFLICT）。
   ⑤ CLI 动词组 + 双形态 golden（旗标前置铁律组内自查）。
   ⑥ Console Functions 页（列表/详情/写面 sheet + exposure 翻转确认框披露可达域变化）；`console:gen && console:build` 与源同 commit。
   ⑦ e2e dind-functions 腿（swarm 常驻 + k3s 变体；断言清单 = 设计 §14 全项，含 exposure 双向四向与保留窗）。
   ⑧ skills：`skills/functions-deploy/SKILL.md`（字面命令围栏守卫喂食；public 函数验收 = 一条 curl）。
   ⑨ staging dogfood 迁移（torchwood ≥1 public + ≥1 private 函数全链 200）。
2. **容量平面收编条款（§15.4 裁决的立法面）**：「容量 = 行面旋钮（replicas/min/max 不进冻结 Revision，scale = 行 CAS 零重部署）」在本批立法为**通用平面**：命名、校验单源（CheckFunctionQuota 镜像 CheckTaskQuota）、受理位单点执法（收敛环盲目）均按可泛化形态落地、不含 function 私有语义；App 侧收编（Replay 语义调整）走未来显式 ADR，本批不动 App 面。
3. **诚实边界随批钉死**：v1 无唤醒/自动扩缩/upload·git 源/per-invocation 观测（private 侧结构性缺失，Phase 3 前）；并发隐含 1；revision 窗外不可回滚。以上由 e2e 与文档同批承载，不做事后补。
4. **零新面承诺**：errcode 零新码（复用六码 + suggestion）；无 InvokeFunction 平台转发；无 Route 行（public = 合成路由，发布签名并入 + SetExposure/删除即时重发布）；数据服务轨不涉。
5. **Phase 2 开工条件（本 ADR 预埋，不在本批）**：唤醒 ADR 携带前置四问 + Knative 三件套（§17 要点 4）；决策环 ADR 携带 swarm/k3s 分叉裁决 + dogfood 负载数据。

## 后果

- 工作树内按任务序推进；每完成一个 F 项同步勾 `docs/plan/2026-09-30-feature-checklist.md`（AGENTS.md 提交纪律）。
- ADR-0101 的 App 侧实施（迁移 00048 + GC 契约测试）与本文 ② 可并行分批，互不阻塞；App 侧先行落地时 Function 建表直接复用同律。
- dogfood 从第一天可迁入（torchwood 函数 = 自产镜像，image-only 恰覆盖）。

## 验收锚

- [x] ① proto/生成/scope/幂等反扫/冻结三桶反扫全绿（fb0da371）
- [x] ② 迁移 + ADR-0101 保留律契约测试（dd981828）
- [x] ③ 模块装配编译期断言 + 收敛环白盒测试（③A f36fcd8d Function 轴 + ③B 6c2b3d47 应用模块+第 13 环）
- [x] ④ 受理面反扫 + 事件五枚三链（70b630ec）
- [x] ⑤ CLI 双形态 golden（d4c3565d）
- [x] ⑥ Console Functions 页 + dist 同 commit（b6a452a2）
- [x] ⑦ e2e dind-functions 全断言 ALL GREEN（d96d027a；P2 批续腿：唤醒双链+决策环自动缩零，ADR-0103 验收锚）
- [x] ⑧ skills 围栏守卫绿（552c98bb——24 围栏条+守卫咬出 14 处旗标后置全修）
- [ ] ⑨ staging dogfood ≥2 函数（public+private）全链 200（随 F6.9 staging 窗）
- [x] §2.1 deletion test 复跑记录（per-invocation 轴未兑现的诚实标注随 P2-4 观测批复核——traefik metrics 输入面已落，b547ed4d）
