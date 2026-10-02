# ADR-0031: 首批 Agent Skills——仓内 skills/、字面命令围栏与 CLI 一致性守卫

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-02 | 架构 §3"Agent 面 = CLI + 专属 Skills，不做 MCP"（2026-09-30 用户直裁）、ADR-0006（CLI 机器契约：--json/golden/退出码/错误信封）、ADR-0012（Task 双形态）、ADR-0026（事件面）、ADR-0029（Database） |

## 背景

F1.13 后半：落地首批 Skills。Agent 面的形态裁决已在架构书（用户直裁，
词汇冻结级）：**CLI 承载机器契约**（稳定 JSON、错误信封、退出码、--wait、
events follow），**Skills 承载程序性知识**（一段段 CLI 工作流），`skills/`
随仓版本化、与平台版本同批演进。本 ADR 裁决落地形态与"一致性如何守"。

## 决策

1. **形态**：仓根 `skills/<name>/SKILL.md`，YAML frontmatter
   （`name`、`description`——宿主按 description 决定何时装载）；正文英文
   （用户可见文本规则）。目录可携带辅助文件（未来模板/样例），首批只有
   SKILL.md。**skills 不携带任何环境假设**：一律假设 `fleetly` CLI 已配置
   （`fleetly init`/`login` 产物），不带地址/Token 字面量。
2. **正文章节约定**：When to use（触发场景——症状与入口命令）/
   Command sequences（命令序列——围栏代码块）/ Failure triage（失败分诊
   ——症状 → 探针 → 动作）。分诊表把 errcode/事件名当一等公民（错误信封
   与事件流是可编程观测面，ADR-0006/0026）。
3. **字面命令围栏（静态执法的前提）**：围栏代码块内以 `fleetly ` 开头的
   行是**字面可执行调用**——占位符用全大写 token（`APP_ID`、`PROJECT_ID`
   值形态任意，对旗标解析是普通字符串）；数值型旗标用真实数字 token
   （`--from 3`）；**旗标一律在位置参数之前**（Go flag 解析在首个位置参数
   停止——守卫按此逐字执行，乱序即红）；**禁止**管道/续行/命令替换/重定向
   /shell 变量（`|`、`$`、反引号、`;`、`\`、`>`、`<`）与行内注释以外的
   语法（` # ` 起的尾注释允许，守卫剥除）。需要表达管线时写在散文里，
   围栏只放机器可复核的调用。可选的 `$ ` 提示符前缀允许（守卫剥除）。
4. **一致性守卫（可静态执法，同批开守卫——本 ADR 的验收锚）**：
   `cmd/fleetly/cmd` 包内守卫测试遍历 `skills/**/SKILL.md`，抽取全部围栏
   fleetly 行，逐行经**进程内真实 CLI 逐字执行**（`dialClient` 接缝毒化
   ——拨号即拒：守卫永不触网、零副作用，连 localhost 开发实例也不会碰）
   断言退出码：**0/1 = 契约成立**（动词路径 + 旗标名 + 值类型 + 旗标/
   位置参数序全部在册；1 = 解析通过后在拨号处确定性失败），**64 = 红**
   （动词消亡、旗标改名/删除、值类型错、旗标落在位置参数之后——stderr
   进失败信息）。命令面变更破坏 skill 围栏 = 与 golden 同款纪律：
   **同 commit 修 skill**。守卫同时校验 frontmatter（name 与目录名一致、
   description 非空）。守卫证明的是"命令路径与旗标解析成立"（parse 级
   契约）；"必填旗标齐全"是写作纪律不归守卫（拨号失败先于动词内校验）。
5. **不执法的方向**：反向覆盖（"每个 CLI 动词须出现在某 skill"）不执法
   ——skills 是工作流不是命令目录（命令目录的真源是 `fleetly --help` 与
   `fleetly schema`）；散文里的旗标描述不逐字校验——**围栏承载机器真相，
   散文保持最小**是写作纪律（围栏红 = 契约漂移；散文错 = 文档 bug，评审
   面）。守卫执行不产生副作用（拨号接缝毒化，永不触达任何服务端）——
   skills 内容永远不在守卫里"跑真调用"。
6. **版本演进（随版本演进说明）**：skills 无独立版本字段——**仓库版本即
   skills 版本**：消费方按仓 pin；skill 行为对应同 commit 的 CLI（守卫在
   CI 逐 commit 执法，不存在"skill 落后于 CLI"的发布形态）。平台行为变更
   影响 skill 工作流的（如状态机加态、事件改名），skill 随该变更同 commit
   更新，与 golden/守卫同一纪律。
7. **首批三个**（架构 §3 点名的前两批工作流 + F1.12 真实可用面）：
   - `deploy-diagnose`：部署未达 succeeded / 应用不可达的事件面诊断流程
     （deployments/events/logs/builds/drift → rollback 决策树）；
   - `task-pool`：Task/Run/Schedule 池语义与 Owner Lease（补足/排空/
     TTL/吊销排空；tasks/runs/schedules 命令族）；
   - `database-provision`：数据库开通全链（前置网络 → databases create →
     凭证 Secret 注入 App → db-<id> 连接验证）。

## 后果

- skills/ 是文档不是生成物：无 golden 快照，唯一机器校验面是围栏守卫。
- 后续批次新增 skill 不需要新 ADR（形态已冻结）；**偏离形态**（新章节
  约定、新围栏语义、目录结构变更）才需要修订本 ADR。
- Console（N2/N3）出现后 skills 仍是 CLI 面工作流——不复制 Console 操作
  路径（两条面各自的程序性知识各自演进）。
- MCP 维持不做（用户直裁冻结；发现新的机器契约需求先扩 CLI 契约面）。

## 验收锚

- [x] `skills/{deploy-diagnose,task-pool,database-provision}/SKILL.md` 落盘：
  frontmatter（name=目录名、description 非空）+ 三章节（触发/命令/分诊）
- [x] 守卫绿且双向保鲜：全部围栏 fleetly 行经真实 CLI 逐字执行（拨号接缝
  毒化，零网络零副作用）退出码 ∈ {0,1}；守卫开发期即咬住两处真实漂移
  （Go flag 位置参数后停析——旗标必须前置；数值旗标占位符必须真实数字）
- [x] 红灯实验常驻化（TestSkillsGuardRedLight）：死动词/死旗标/旗标落在
  位置参数后/围栏 shell 元字符/frontmatter 名不符五种漂移全被咬住——
  守卫自身失明即红
- [x] 围栏纪律执法：围栏内 shell 元字符（管道/变量/续行）= 红，错误信息
  指明"字面调用约定"
- [x] 三个 skill 的命令序列全部真实可用（37 条围栏调用过守卫；占位符
  全大写 token + 数值旗标真实数字 + 旗标前置位置参数）
