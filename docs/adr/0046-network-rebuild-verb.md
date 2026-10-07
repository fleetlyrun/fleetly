# ADR-0046: 网络重建动词——平台中介的受监督 flag-day 通道

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-05 | N2 评审批 P1-4（docs/reviews/2026-10-05-n2-review.md；挂账台账 #5）、ADR-0013（Network 成员模型）、ADR-0039 决策 4（Attachable=true + 存量网精确错误路径）、ADR-0024（幂等执法面）、ADR-0017 附录 A.3（冻结分类）、ADR-0035（行级授权）、ADR-0005（drift 只发事件、Ensure 唯一写动词）、F2.2 换装实录（runbook 2026-10-04 记录 §177/记录·五 #4） |

## 背景

ADR-0039 决策 4 把项目网络的载体形态定为 **Attachable=true**（create 面，
ensureNetworks）——工具容器（Database Backup/Restore 的 utility 执行载体）附着
项目 overlay 解析 `db-<id>` DNS 依赖该位。**存量非 attachable 网络不改不炸**：
附着失败报精确错误（文本自带重建序指引），runbook 记 flag-day 操作序。

staging 真机把这个缺口咬实（2026-10-04 起，N2 评审批 P1-4）：torchwood/messaging/
n0reg/n0probe 四个 pre-F2.2 项目网无 Attachable 位 → torchwood-pg 定时备份自
2026-10-04 起持续失败——唯一 dogfooding 数据库的数据库级备份 = 零。而手工修复
路径不存在：`docker network update --attachable` 不存在（docker 无该子命令）；
手工 `docker network rm` + recreate 需要先拆全部附着服务，而 **service 级
network-rm/add 会被平台 reconcile 回滚**（Ensure 全量替换语义把载体 spec 拉回
基线）——错误文案承诺的"等动词落地，勿手工拆网"即此。平台因此需要一个
**平台中介的重建动词**：orchestration 在平台手里，reconcile 才不会互踩。

## 决策

1. **动词形态**：NetworksService +`RebuildNetwork`（request：project_id +
   name；response：network 引用 + detached/reattached 计数）。scope=
   `networks:write`（既有资源，SchedulesService 复用 `tasks` 同款先例）；
   REST `POST /v1/networks/rebuild`。幂等面纳入（`idem.EnforcedMethods`
   显式登记——Rebuild 不在创建型前缀集，DeclareNetworkPeer 同款先例）；
   冻结分类 = **frozen**（变更型维护动词：detach/重建载体网络是 Workload 面
   变更，ADR-0017 冻结语义内；拒绝信封带冻结 reason）。事件 +1
   `network.rebuilt`（payload：project/network/detached/reattached）；
   errcode 全复用（E_NOT_FOUND/E_CONFLICT/E_INTERNAL——无新码）。

2. **执行序（平台中介的受监督重建）**——orchestration 住 engine
   （`Engine.RebuildNetwork`，API 层同步执行），载体操作经 Runtime 可选子面：
   1. 受理：Project 存活 + network 行在场（同族受理检查，404 语义）；
   2. 载体枚举：经 Runtime 列出附着该网络载体名的全部 swarm service；
      **存在平台无法归属的附着（无 fleetly 域标签的外来 service，或有标签但
      域锚不在期望集——App/Task/Database 行已删的残留载体）→ E_CONFLICT 拒绝
      并列出载体名**（诚实拒绝，不碰不认识的载体；期望集 = 域锚可解析到活跃
      行：App 轴查 apps 表、Task 轴查 tasks 表、Database 轴查 databases 表、
      受管域（fleetly/system）恒期望——Edge 挂全部活跃项目网）；
   3. 平台归属载体逐个 detach（service update 摘网络目标）→ 删网络（带界
      排水等待：附着端点清空前 rm 会报 in use，有界重试）→ 经 ensureNetworks
      同源 create 路径复建（overlay + Attachable=true + 标签同源）→ 载体逐个
      re-attach（service update 加回网络目标）；
   4. 每步带界 + 诚实错误；失败中断时网络/载体停在可重试的中间态——**动词
      幂等**：重跑从任意中间态收敛（已 attachable 且标签在位的载体网 →
      快速路径跳过全部 detach/rm/attach，零扰动返回）。

3. **串行化（本批最大设计点）**：重建不得与部署 Ensure / 受管 reconciler /
   数据库环 / Task 环 / 备份 utility 附着竞态（detach 窗内 Ensure 会把附件
   拉回基线使网络 rm 报 in use；rm 后 Ensure 的网络名解析失败面不可控）。
   - **选型：engine 全局 maintenance 读写锁**（`Engine.maintenanceMu
     sync.RWMutex`）。重建动词持**写锁**贯穿 detach→rm→create→attach 全序；
     全部 Ensure 族调用点（materialize（部署链/基线重放/peer 隔离三消费面单
     序列真源）、databaseStep、managedStep、driveEnsure、backup 环的
     RunUtility 附着）持**读锁**——读锁之间照旧并发（既有并发度不变），
     只有重建排他。
   - **否决 per-project 锁**：受管 Edge 的单次 Ensure 引用**全部活跃项目的
     网络**（activeProjectNetworks）——任一项目的重建都必须挡住 Edge 的
     Ensure，per-project 锁对受管面要么全取（等价全局写锁 + 遍历取锁的死锁
     序面）要么漏挡。全局锁是受管面耦合下的诚实粒度；重建窗（分钟级，见
     下）内其他项目的部署排队等待，是"排队能力"语义的直白实现。
   - **锁等待不占步预算**：读锁获取在 boundedStep 派生 ctx 之前（materialize/
     各环的既有超时预算量的是收敛步本身，不量排队——否则重建窗内的部署会
     以超时失败而非排队，违背"排队"语义）。写锁整体受
     `Options.NetworkRebuildTimeout`（缺省 5m）硬界约束： detach 排水 +
     rm 重试 + 复建 + re-attach 全序一个预算；超时即中断在可重试中间态。
   - **drift 扫描为何不会假报**：spec 对照面（compareSpecs）读的是
     `WorkloadObservation` 的 Image/Command/Replicas 三字段——**网络附件
     不在对照面**（ADR-0022 的字段冻结），detach/re-attach 不触发
     `workload.drift_detected`；drift 只发事件、Ensure 是唯一写动词
     （ADR-0005），扫描本身不会反过来干扰重建。稳态看门狗同样免疫：被替换
     的旧 task DesiredState=shutdown 不计观测（pollTasks 的活槽位过滤），
     last-write-wins 槽由新 task 的 running 填充。**测试钉死"重建期间零
     假 drift 事件"**（重建中途跑一拍 driftScan 断言 outbox 无
     workload.drift_detected）。

4. **载体级操作住 Runtime 可选子面 `RuntimeNetworkMaintenance`**（Runtime
   第 7 子面，FacesOf/Offered 同批扩面；RuntimeAdmin/Utility 同款装配语义）：
   `InspectNetwork`（载体网在场/attachable/平台标签 + 附着载体清单的平台
   中立投影）、`DetachNetwork` / `RemoveNetwork`（带界排水重试，in-use 类
   错误有界退避）/ `EnsureNetwork`（ensureNetworks create 半边同源抽出）/
   `AttachNetwork`。swarm 实现：detach/attach = service update 改
   TaskTemplate.Networks（CAS 版本重试同 updateServiceCAS 先例）+ 同步作废
   no-op 断路器账本条目（载体 spec 被平台外改动，账本必须让下一拍 Ensure
   重新走 serviceSpecEqual 门）；re-attach 目标 = 新网络 ID——与 fresh
   Ensure 的 resolveNetworkTargets 产物一致，故重建后下一拍 Ensure 等价
   跳过（账本回填），**不产生额外滚动**。

5. **API/事件/审计/CLI**：受理 = 参数校验 + 行级授权（authorizeProjectID，
   他 Team 403）→ engine 同步执行 → 成功后事件 + 审计一事务（action=
   `network.rebuild`，source=cli/api 随调用面）；失败无事件（与冻结拒绝同
   口径——重试风暴自放大防护），错误信封如实携带 engine 的中断点描述。
   CLI：`fleetly networks rebuild --project PROJECT_ID NAME`（对齐 networks
   组既有 `--project` + 位置参数风格），双形态 golden + groups 清单更新。
   **长执行豁免**：重建是分钟级同步动词（detach 排水以 stop-first +
   StopGrace 量级推进），通用 30s unary 预算（assembly grpcUnaryTimeout）
   会腰斩真实排水窗——RebuildNetwork 进服务端 `longRunningUnary` 豁免面
   （拦截器原样放行，NetworkRebuildTimeout 5m 硬界承担防挂死），CLI 侧
   拨号豁免 120s 默认 deadline（platform backup 同款先例）。

6. **e2e 回归锚（P1-4 场景端到端）**：`dind-backup.sh` 主腿开头插入——项目
   创建后（出生网 attachable）先 `docker network rm`（尚无服务附着，允许）
   再手工 `docker network create` 同名**不带** attachable（模拟 pre-F2.2
   legacy 形态）→ 部署库 → `databases backup` 断言精确失败（attachHint 文案
   锚）→ `networks rebuild` → 重试 backup 断言 succeeded。legacy 形态的
   制造走 docker 原面（平台 create 面恒 attachable，无法用平台动词造出
   病态——这正是本 ADR 的点）。

## 后果

- **重建窗 = 分钟级受维护窗**：detach 与 re-attach 各触发一轮 per-service
  滚动替换（swarm 网络附件变更语义）；torchwood-pg 实测量级 = stop-first +
  60s grace 的两轮替换。操作纪律：重建前 Platform Backup 不强制（网络/载体
  spec 不是数据面），但换装批对四项目逐网执行的序 = 低峰窗（runbook 记）。
- **两枚真机语义（dind e2e 实录，实现已内置）**：① swarm overlay 的
  `network rm` 返回成功≠已消失（端点排空后网络才真退役）——RemoveNetwork
  删除后轮询 inspect 至 NotFound 才返回，否则复建的 create-or-get 会 get
  半边复用残留载体（非 attachable 旧网）；② re-attach 的滚动收口窗内，
  detach 时代的无网任务对容器本地探测（docker exec）照常应答、对网络 DNS
  却无 endpoint（备份的 pg_dump 寻址 db-\<id\> 走 overlay DNS）——e2e 的
  收口断言用网络 DNS 可解析锚（容器内 getent），生产面同理：重建后立即
  触发的备份可能撞窗报 could not translate host name——重试即愈（秒级窗）。
- 重建窗内全部部署/受管/数据库/Task 收敛与备份执行排队（读锁等待）；窗有
  硬界（NetworkRebuildTimeout），不会永久卡环。
- Runtime 子面 +1：非 swarm Runtime（假想 k8s）未实现时动词诚实失败
  （E_INTERNAL 信封），降级文化与 Inspector/Utility 一致。
- 外来附着（无平台标签的 service 挂进项目网）使重建拒绝——这是设计不是
  缺陷：平台对不认识的载体零动作（WorkloadOrphaned 只登记原则的网络面
  同款）；操作者先自行处置外来载体。
- 事件/审计面：`network.rebuilt` 一枚；errcode 零新增。
- staging 换装批消费链：对 torchwood/messaging/n0reg/n0probe 四项目网逐个
  `fleetly networks rebuild` → torchwood-pg 定时备份自愈（runbook 闭合注记
  随批回写）。

## 验收锚

- [x] proto only-add：RebuildNetwork + 消息；buf breaking 过；scope=
      networks:write 注解在位（mise run lint 全绿含 lint:proto breaking）
- [x] RuntimeNetworkMaintenance 子面：swarm 实现（inspect/detach/rm 带界
      排水/ensure 同源/attach + 断路器账本作废），FacesOf/Offered 扩面
      （capability faces 测试 + swarm maintenance 单测）
- [x] 串行化：maintenanceMu 读锁覆盖 materialize/databaseStep/managedStep/
      driveEnsure/RunUtility 全部 Ensure 族调用点；写锁贯穿重建全序
      （NetworkRebuildTimeout 缺省 5m 硬界 + 服务端 longRunningUnary 豁免
      30s 通用预算 + CLI noDeadline 拨号）
      （TestRebuildNetworkSerializesWithDeployEnsure）
- [x] apitest 全链：成功序（fake runtime 断言 detach→rm→ensure→inspect→
      attach 顺序与计数）/ 非平台附着拒绝（E_CONFLICT 带列表）/ project 或
      network 404 / 幂等重放同响应（Idempotency-Key）/ 冻结窗拒绝 / 行级
      授权（他 Team 403）/ 事件载荷与审计行（source=api）
      （internal/apitest/networkrebuild_test.go 五测试）
- [x] 重建期间零假 drift 事件（TestRebuildNetworkZeroDriftDuringWindow：
      重建前后扫描拍零 workload.drift_detected + 真镜像失配对照面仍报 +
      快速路径幂等零扰动）；归属轴面（受管域/Task 轴通过、失锚拒绝——
      TestRebuildNetworkAttributionAxes）
- [x] eventcode 三链同 commit：network.rebuilt（schemareg + golden +
      usage 反扫）；errcode 零新增（E_NOT_FOUND/E_CONFLICT/E_INTERNAL 复用）
- [x] CLI `networks rebuild` 双形态 golden + groups 清单更新
      （networks-rebuild(-json).golden + networks.golden 组清单 + events
      follow golden 含 network.rebuilt 事件行）
- [x] e2e dind-backup.sh 插腿：legacy 非 attachable 形态制造 → backup 精确
      失败（attachable 文案锚）→ rebuild（detached 1/reattached 1 + carrier
      Attachable=true 原面断言 + 网络 DNS 收口锚）→ backup succeeded
      （2026-10-05 本地 dind 全量演练 ALL DRILLS GREEN：四引擎恢复 +
      Platform Backup roundtrip + S3 离机腿含本腿全绿；CI e2e-backup job
      常态）
- [x] runbook 回写：177 行挂账段闭合注记 + 记录·五 #4 追记（换装批执行序）
- [x] 全门禁：mise run test（三 module -race）+ lint + guards +
      generate:verify 全绿

## 追记：staging 实录与失败回滚收口（2026-10-05）

### staging 四刀实录（动词第一次真机洗礼）

#1/#2/#3 均在 RemoveNetwork 排水 deadline 失败（300s 全程 "active endpoints"
重试），根因三层（runbook 记录·七全文）：

1. **crash-loop 服务卡死排水**：torchwood app 进程 crash-loop → task 高频
   替换使网络端点永不清零；且 detach 触发的滚动更新被失败替换卡住、健康
   副本不被替换（swarm 滚动暂停语义）——操作解 = 删健康 task 容器让 swarm
   按已 detach 的现行 spec 补无网 task。
2. **半死节点卡死 overlay 退役**：swarm overlay 删除要全集群节点放行，
   node2 dockerd agent 半死（journal 高频错误 + node 抖动）卡住退役——
   操作解 = 重启该节点 dockerd（worker 自动重入）。
3. **app 域无周期 ensure 重放面**：失败尝试的半 detach 态只有受管/数据库
   域会自愈（每拍 ensure 重放），app 服务要等下次部署——本批补**失败回滚**
   （见下）。旧版（a57de18 之前语义）遗留的半 detach 态需 app 重部署收敛。

#4 成功收敛：detached 2/reattached 2、attachable=true → torchwood-pg 备份
自 F2.2 以来首次成功、`last_backup_at` 首次推进（P1-4 现场闭环）。

### 本批收口（随批三补丁中的动词侧）

- **失败回滚**：detach 之后的任何一步失败（remove/recreate/verify），把已
  detach 的载体尽力挂回（AttachNetwork 幂等；独立 1m 带界 ctx——bctx 此刻
  可能已耗尽，utility remove 的 WithoutCancel 同款）。错误文本从"stay
  detached until their domain's next ensure replay"改为"detach rolled
  back"——原语义对 app 域不成立。
- 关联事故（同日诊断，backup 域收口见 ADR-0039 射程内的本批 commit）：
  备份失败无退避的 1s 重试 × utility 删容器不带 RemoveVolumes = 33h 累计
  11.6 万枚匿名卷；runbook 记录·七为复盘真源。
- 教训入操作序：重建遇排水超时的分诊序 = 先查 crash-loop 服务、再查半死
  节点、处置后重试（动词幂等收敛）。

### 追加验收锚

- [x] 失败回滚：detach 后 remove 失败 → 已 detach 载体全部挂回 + 错误文本
      含 "detach rolled back"（TestRebuildNetworkRollbackReattachesDetached
      Carriers；fake InspectNetwork 补排序契约对齐真源）
- [x] e2e 匿名卷零增量双窗锚（失败面 + 成功面，dind-backup.sh P1-4 腿内）
      ——utility RemoveVolumes 修复的行为锚

## 追记：k3s 侧永久语义性缺席定型（2026-10-08，ADR-0053 决策 2）

后果节曾预记"非 swarm Runtime（假想 k8s）未实现时动词诚实失败（E_INTERNAL
信封）"——k3s 试点后升格定论：**缺席不是排序缺口，是语义性事实，永不补齐**。
动词的修复对象是 swarm overlay 的 attachable flag-day 病灶（pre-F2.2 存量
网不可附着、手工修复被 Ensure 回滚）；k3s 的域与载体映射是 per-Project
Namespace（ADR-0052 决策 3）——Namespace 恒存在、无载体网络对象、无
attachable 概念，前置病灶在 k8s 形态下不成立。RebuildNetwork 在 k3s 集群
上的诚实失败是正确行为（engine 侧既有子面缺席降级语义承载）；k3s Provider
的 Describe Notes 声明该边界（TestDescribeNotesHonesty 钉死措辞）。
