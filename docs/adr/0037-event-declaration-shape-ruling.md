# ADR-0037: 事件/审计声明面形状维持现状（架构评审候选拒绝裁决）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-03 | 架构评审第二轮候选 6（候选报告 2026-10-03）、ADR-0026（API/事件惯例与 schema 钉形）、AGENTS.md 守卫文化（eventcode 只增注册表）、ADR-0007（词汇冻结的"显式裁决而非局部混用"同款精神） |

## 背景

架构评审第二轮候选 6 提出「事件与审计声明单源」：一个事件名（如
`database.created`）要在四处声明——eventcode 注册表条目（internal/model/
eventcode/events.go，只增 + golden）、handler 文件内常量、发射点、
schemareg.go 的 payload 注册行；写入面另有 13 处手写 audit 字面量。候选
主张收编为单一 declare 点。

## 决策

**拒绝收编，声明面维持现状。** 实况调查（grilling 事实面）修正了候选的
前提：

1. **「四处声明」实况是「一个符号 + 两处只增登记」**：发射点、freeze/
   idem 等执法面、schemareg 注册行消费的是同一个包内常量（如
   `eventDatabaseCreated`）——符号级单源已成立，常量改名/漏用是编译错，
   不存在字面量漂移面。真正重复编辑的只有 eventcode 注册表条目与
   schemareg 注册行两处，且都是**只增登记面**：eventcode 是词汇真源
   （summary/锚点/golden 的数据源），schemareg 是 payload schema 的完备
   性面（internal/assembly 守卫对账）。两者是注册表文化的刻意形状，
   不是散落的重复。
2. **收编 = 搬迁守卫对账物**：把 schemareg 的 per-face 表搬进各 face 文件
   只是把守卫的对账对象从一张集中表搬成 N 张散射表；「declareEvent 运行
   时铸造常量」在 Go 不成立（常量必须字面量）。收拢的收益（新事件少跨
   一个文件）低于扰动（init 注册面散射 + 守卫/golden 随批动）。
3. **审计字面量已是数据形态**：`auditFact{action, resource, afterFP,
   actorCtx}` 一字面量一行，identityAudit 式构造器只能省字段名不省行数；
   无 shallow 面可收（deletion test：删掉任何一层，复杂度原位重现）。

## 后果

- 未来架构评审不再重提「事件/审计声明单源」形状（除非前提变化：如
  eventcode 注册表改为运行时注册制、或事件面出现跨包字面量漂移实害）。
- 新事件的既定编辑集保持：eventcode 条目 + face 常量 + schemareg 注册行
  + golden 双更新（守卫钉死完备性）。
- 本 ADR 不改变 F2.2 备份事件族（backup.*）的落地方式——按上述编辑集
  正常落。

## 验收锚

- [x] 无代码变更（拒绝裁决；候选 7 起继续）——本 ADR 是唯一产物
