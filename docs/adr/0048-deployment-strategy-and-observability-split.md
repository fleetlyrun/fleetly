# ADR-0048: 部署策略面定稿——blue-green 编排变体（P5）+ 观测域垂直切（P15）+ 进程双别名（P16）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted（裁决先行：词汇面随本 ADR 落地；实施随 F3.1 写面批，验收锚待实施批勾选） | 2026-10-06 | proposals §0.1 裁决 5/7（P5/P15/P16 预答，本 ADR 终审）、ADR-0016（admission/supersede）、ADR-0022（Drift 对照）、ADR-0030（firstBootJobs 相位）、ADR-0034（进程别名=进程名，P16 地基）、ADR-0017（配额）、ADR-0025 决策 6（Addressing 铸名）、架构 §5（Runtime 契约窄面）、F3.1（Console 全功能——实施载体批） |

## 背景

`docs/design/2026-10-03-optimization-proposals.md` P5（部署策略面：rolling
默认 + blue-green 可选）已于 2026-10-03 预裁"采纳、N3 伴随"，四个开放问题
均有预答（§0.1 裁决 5）；同批挂 P15（engine 观测域垂直切，与 P5 共用
resolveBackend/期望缓存动土面）与 P16（集群 DNS 不内置——维持领域模型 §7；
双别名消歧挂 P5 同批定稿）。预裁明文"本裁决 N3 开工 ADR 时终审（deletion
test 复审）"。本 ADR 即 N3 开工终审：四预答落定、deletion test 复审、交互
语义收口、验收锚可勾选化。**本批是裁决先行**——词汇面（CONTEXT.md 词条
+ 守卫分诊）随本 ADR 落地，实施随 F3.1 写面批（Console 部署详情页"当前
代/上一代"叙事是蓝绿的价值最大化面）；三案共用 alias 通道与观测域动土面，
一次定稿避免三次动土。

## 决策

### 1. P5 蓝绿终审：Runtime 契约不变，蓝绿是 engine 的编排变体

四预答（§0.1 裁决 5）全部落定，逐条终审记录：

1. **Process 级 `strategy` 字段**（`AppSpec.processes[].strategy`，值
   `rolling`（默认）| `blue-green`）。投影单位一致；"web 蓝绿 + worker
   滚动"的混合是真实需求（torchwood：server 换代必须零中断，packer 无所谓）。
   App 级字段否决维持。**值域住叶子校验**（spec 包 ValidateProcess，与
   Builder 名值域同款"词条单源冻结"纪律）；`rolling` 是缺省零值兼容——
   存量 Revision 无该字段重放即 rolling，无迁移面。
2. **双代窗执行序列**（Deployment 状态机 releasing 的变体路径，状态枚举
   不变）：
   - releasing(双代窗)：Ensure 期望集 = 旧代 ∪ 新代（期望集语义天然不移除
     任何一方）；两代载体各自携带自身 Generation 锚（label 面本就 per
     载体），Ensure 幂等收敛不变；
   - L1 健康门：新代全部就绪。失败 → Ensure 期望集 = 仅旧代 → failed。
     **这就是"回滚零重建"**：旧代从未被触碰，无 Replay；
   - 切换：Route 后端解析（resolveBackend）从旧代地址切到新代地址——
     Proxy 发布行集指纹机制复用；
   - observing（L3 观察窗）：窗内手动切回 → Route 切回旧代 → 移除新代 →
     rolling-back 终态路径复用；
   - 收口：观察窗过 → Ensure 期望集 = 仅新代 → succeeded；stable 进程别名
     （ADR-0034 `{进程名}`，本 ADR 决策 3 扩双值）随旧代退役、新代继承。
3. **stable 别名双代窗 = 方案①（轮询双代）**：无状态跨进程调用本就接受
     共存窗（与滚动语义一致，非倒退），接流量走 Proxy 精确切换；诚实边界
     写进架构文档与 Console 叙事（双代窗内跨进程 DNS 引用可能打到旧代）。
     方案②（引用方感知代次）否决维持——把代次泄漏进引用方契约复杂度
     不成比例。
4. **代次名 `{proc}.g{gen}`，stable 名为主引用面**：代次名只服务 Proxy
   后端解析与调试（resolveBackend 消费代次化地址——该面已是独立解析点，
   P15 决策 2 同批显式化），不鼓励用户直接引用。gen 是单调整数、名字随
   部署漂移——这正是"只作调试面"的理由。

**Database 轨明确不适用**（结构性保证）：DatabaseSpec 无 strategy 面
（ADR-0029：模板渲染、无 Revision 冻结、gen 指纹收敛轨）——有状态负载
双代 = 数据分叉，数据面维持 stop-first + 宽 grace 修复批裁决。受管域
（Proxy/zot/VL/VM）同款不适用：受管 Workload 无用户 strategy 面。

**deletion test 复审（终审义务，2026-10-06）**：砍掉 blue-green 后
rolling + 健康门在多数场景够用的判断维持不变；独占价值三条（①切流量前
新代全量就绪，容量谷消失 ②观察窗内切回零代价 ③启动慢负载的安全默认）
仍成立且均有实证背书（DX 报告高频抱怨 + staging 升级事故 + JVM/冷缓存
类负载常识）。N2 期间预裁例外口（"滚动语义与 stop-first 修复存在必须
蓝绿才解的交互"）**未触发**——F2 各批（备份/升级矩阵/Provider 化）零
此类交互实录。**结论：保留设计、维持 N3 实施排期（F3.1 批），不提前。**

### 2. 与既有机制的交互语义收口

- **supersede 抢占（ADR-0016）**：扩展为"收口在途双代"。被抢占部署处于
  双代窗时，其新代载体由抢占者首个 materialize 的期望集移除（期望集不含
  即移除——与切换/收口步同一机制），其旧代 = 抢占者的 from 基线，由抢占
  者序列照常接管（rolling 或自身蓝绿）。孤儿防残留锚：双代窗内被抢占 →
  孤儿清扫零新增（验收锚）。
- **配额（ADR-0017）**：双代窗内 Workload 计数双代都计（诚实，不豁免）。
  配额读的是载体观测/期望集，天然覆盖双代——maxApps 语义是"并行载体数"
  而非"逻辑进程数"，文档口径随实施批写清。
- **firstBootJobs（ADR-0030）**：在新代 L1 之前执行，序列不变——jobs 是
  部署相位（releasing 前半）不是代属性；失败回滚语义不变（旧代零扰动）。
- **Drift（ADR-0022）**：双代窗 = 两个已下发 Generation 并存，对照锚按
  代次分别记录（Watch 流滚动窗口 gen 归因既有）；spec 对照（期望缓存
  ensuredSpec）在双代窗内含两代期望——**这正是 P15 的动土点**（决策 2）。
  启动基线重放只针对 succeeded 收口形态（单代），双代窗是部署在途形态、
  重启恢复走既有在途部署恢复路径。
- **回滚（Replay）与蓝绿的关系**：蓝绿切换/切回是**在途部署的相位**，
  不是 Replay；Replay（rollback 动词）对 blue-green App 照常成立——
  Replay 铸新 Deployment、走该 App strategy 的新双代窗（基线=当前在服代）。

### 3. P16 双别名定稿：`进程名` + `进程名.应用名` 双值 Addressing

维持 P16 主裁决（不内置集群 DNS——解析器归 Runtime，平台只持命名声明）。
吸收的真问题按预裁落定：

- **App Process 的 Addressing 升为双值**：`[{Name: 进程名}, {Name:
  进程名.应用名}]`。裸名保 compose 单栈兼容（ADR-0034 语义不变）；全名
  `进程名.应用名` 消歧同网多 App 同名进程（跨 Project 挂靠同网同理）。
  应用名取 App 行 name（Project 内唯一——同名进程不同 App 的全名必不同）。
- **纯投影/alias 通道变化，零解析器**：Provider 侧 Addressing→Aliases
  既有单通道消费（ADR-0034 验收锚 2），无新分支。
- **stable 名主引用**与 P5 代次名同批实施（别名词汇面一次定稿：裸名/
  全名/代次名三种形态都在本 ADR 与 ADR-0034 的词汇框架内）；受管域
  （db-\<id\> 等专有公式）不参与双别名（ADR-0034 决策 5 不变）。
- **词汇不扩**（P16 维持项）：不新增 DNS server 类词条；本节即"不做内置
  DNS"记录真源的引子（proposals P16 节为记录真源，维持类裁决不另开 ADR）。
- **重开触发器维持**（proposals P16 末节三条）：swarm RR 语义在双代窗
  真机验证被证明不可控 / 集群外实体必须用集群内服务名 / N4 k3s 命名翻译
  出现缺口——任一满足再议。

### 4. P15 观测域垂直切（与 P5 同批定稿，同批实施）

- **动土面重合是并批理由**（§0.1 裁决 5）：P5 的切换步要求 resolveBackend
  消费代次化地址——该面与 `appWorkloadExpectations`/`ensuredSpec` 期望
  缓存同住 observ 域（Engine 汇聚结构，全部收敛环共写的锁竞争面）。分两
  批拆 = 同一文件两次动土。
- **目标形态**：观测域（归属解析/期望缓存/Route 后端解析）从 Engine 汇
  聚结构独立为域内组件（自有锁、自有快照面），`resolveBackend` 对蓝绿的
  代次解析成为该组件的显式接口而非缓存内部例外（现状"观测缓存不参与
  决策"口径在 resolveBackend 处的例外，借此显式化）。
- **结构裁量非行为变更**：拆分零行为差异（全测试绿 + golden 零漂移即锚）；
  sizeguard 1200 红线顺带看守 observ.go 拆分结果。
- **当前锁竞争不是小微规模痛点**（预裁原文）——本项价值是结构与 P5 联动，
  非性能修复；实施批不得以性能为由扩射程。

### 5. 词汇面（随本 ADR 落地）

- CONTEXT.md 新增词条 **Deployment Strategy**（交付节，Deployment 词条
  邻位）：Process 级部署切换策略 rolling|blue-green；_Avoid_: canary
  （显式不做，T1 后再议——proposals P5 原文）、slot（zane 的 slot 别名
  机制词，本仓不引入）。
- wording 守卫分诊同步：canary 入 banned（全仓扫描面零命中，2026-10-06
  实测）；slot 入 skipped（泛义英文词误伤面大——"free a slot/占位"义
  在 Task/errcode 文案在用，zane 机制义人工评审把关）。
- 蓝绿/滚动词汇遵守 proposals 词汇纪律：滚动写"rolling"，双代窗表达复用
  **Generation** 词条，不引入 slot/canary。

### 6. 实施时序与守卫义务

- **本批（裁决批）落地件**：本 ADR + CONTEXT.md 词条 + wording 守卫分诊。
- **实施随 F3.1 写面批**（Console 全功能：部署详情页"当前代/上一代"叙事
  与蓝绿面同批最大化价值），携带：strategy 归一化 golden 双形态、双代
  收口守卫（孤儿清扫断言——proposals 附表"随批守卫任务"）、P16 projection
  断言 + 同网撞名消歧 e2e、P15 结构拆分。**ADR-0001 式静态执法承诺在此
  批兑现**：strategy 值域词条单源（叶子校验注释锚本 ADR）。
- F3.5（API 扩展批）与本 ADR 无实施交集：DeployRequest 不加 strategy 面
  ——strategy 是 ProcessSpec 字段，intake 通道（compose 扩展键或
  spec_file）随 F3.1 设计，不在 F3.5 的 spec_file 最小面里预发。

## 后果

- 资源账：blue-green 双代窗内副本数翻倍（决策 1 已明示资源代价，用户
  显式选择；Console/文档诚实标注）。
- 载体命名：blue-green 模式下 Workload 载体名/ID 带代次成分（Provider
  私有公式扩展，如 swarm `...-web-g42`）；平台 ID 标记锚定归属不受影响
  （唯一性本就以平台 ID 兜底，架构 §5）。rolling 维持现名——存量零变化。
- 跨进程引用诚实边界：双代窗内裸名/全名 DNS 轮询双代（方案①）；接流量
  走 Proxy 精确切换。Console 部署详情页应展示双代窗状态（F3.1 面）。
- 本 ADR 不触碰 Runtime 契约（Ensure/Remove/Watch 形态不变）——蓝绿全部
  落在 engine 编排层，N4 k3s Provider 天然继承同一策略面（决策 1 序列是
  Runtime 无关表述）。
- 防重议：slot 别名机制（zane 形态）与 canary 已在 Avoid 表；重开条件
  = T1（Token ability 三级，P6 已裁不引入）后真实需求出现，届时走显式
  ADR。

## 验收锚（实施批勾选；本批落地件随勾）

- [x] 本 ADR + CONTEXT.md Deployment Strategy 词条（_Avoid_: canary,
      slot）+ wording 守卫分诊（canary banned / slot skipped）绿
- [ ] strategy 归一化 golden 双形态（rolling 缺省零值兼容存量零漂移 +
      blue-green 值域执法拒绝文本）〔F3.1 批〕
- [ ] blue-green e2e：Route 探针在切换步零 5xx（ADR-0015 同款探针口径）
      〔F3.1 批〕
- [ ] blue-green e2e：旧代载体在观察窗内始终存活（载体 ID 不变）〔F3.1 批〕
- [ ] 新代 L1 失败：旧代零扰动（载体 ID 不变、零重启），Deployment 终态
      failed，无 Replay 发生〔F3.1 批〕
- [ ] 观察窗内手动切回：Route 指回旧代，请求恢复，旧代载体未重建〔F3.1 批〕
- [ ] supersede 在双代窗内抢占：无双代残留（孤儿清扫零新增）〔F3.1 批〕
- [ ] 双节点拓扑下蓝绿两代可分节点调度（Placement 约束对两代一致）〔F3.1 批〕
- [ ] P16：projection 断言 Addressing 双值（进程名 + 进程名.应用名）+
      同网撞名消歧 e2e（双 App 同名进程，全名各自可达、裸名 RR）〔F3.1 批〕
- [ ] P15：观测域组件化落位（期望缓存/resolveBackend 域内组件 + 蓝绿代次
      解析显式接口），全测试绿 + golden 零漂移〔F3.1 批〕
- [ ] 双代收口守卫（孤儿清扫断言）入 internal/guards〔F3.1 批，AGENTS
      纪律：可静态执法承诺同批开守卫任务〕
