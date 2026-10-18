# 严格声明式模型程序计划（2026-10-18）

真源：设计母本 `docs/design/2026-10-18-declarative-model.md`（唯一规格源，含全部裁决与三路对抗审查修订）。立法真源：ADR-0081（写面三桶 + 作者学说）/ ADR-0082（缺省收敛）/ ADR-0083（restart_seq）。本文件是**执行与进度真源**：批次结构、裁决索引、验收门禁。前提：开发期按最优设计激进更新、不承担向后兼容（2026-10-18 用户裁决）。

## 目标

把平台精炼为严格声明式模型：CLI、Console、将来的配置面只声明期望状态，核心引擎保证满足。补齐四项无理由欠账（设计 §6.4）：App 域收敛 opt-in、缺席盲区、冻结只拦 RPC 面、restart 无声明面。

## 批次结构（小步 commit，守卫/测试同批）

| 批 | 内容 | 状态 |
|---|---|---|
| **Batch 0 预检** | 核对 drift.go 未提交改动（并行会话）；复核 ADR 编号；mise install | [x]（0081/0082/0083 空闲已核） |
| **Batch A 立法** | CONTEXT.md 词条五新增 + Converge 修订 + restart_seq 注记；ADR-0081/0082/0083（含翻案登记与验收锚）；本计划文档 | [x] |
| **Batch B 写面立法** | `writeclassguard_test.go`：ADR-0081 附录 153 方法完整分类（27 ACTION + 53 DECLARATION + 73 READ）+ 双向保鲜 + 单向 scope 断言 + FrozenVerbs/idem 从属断言；proto 零触碰 | [x]（四测全绿；分类表与附录三处分项计数/FrozenVerbs ACTION 条数勘误同 commit） |
| **Batch C 收敛升格** | C1：缺席检测（LatestSucceeded 重投影 + digest 签名短路）+ App 域双通道接线（快通道四类信号 + 60s 慢通道 + 签名生命周期 + 冷却 + 冻结 fail-closed + 审计行 + 两事件 + doctor/告警）。C2：ADR-0082 裁决表落码 golden + posture 旗标。钉测族与 skills deploy-diagnose 翻向同批 | [x] |
| **Batch D restart_seq** | spec.proto + Workload.RestartSeq + 投影 + swarm/k3s contentequiv 四用例 + 执行时 seq-only 判定 + CLI golden 族（含 selfdescription/schema/explain）+ console:gen && build dist 同 commit + skills restart 双面指引 | [x]（k3s 在树内核实：账本 canonical key 同批纳入；e2e:upgrade 断言留收尾批判读） |
| **Batch E 可选** | E1 受管/route 收敛 memo 持久化；E2 projects 项目级逃生门；E3 ring 域 ensuredSpec 记录 + spec 对照——按需另批（2026-10-18 裁决 4：A-D 收官即收本程序） | [ ] |
| **收尾门禁** | mise run test / lint / console:verify 全绿；e2e:dind + e2e:upgrade（预记两分支）；eventcode golden + usage 反扫；runbook 手动面盘点（freeze 优先逃生序）；runbook 记录条目 | [ ] |

## 裁决记录索引（2026-10-18 四项，均按推荐案；全文见设计 §10）

1. **收敛上站**：金丝雀绿后 staging 换装即 posture=all 缺省生效，不设 detect 观察期（runbook 手动面盘点作对冲）。
2. **重启双面契约**：`apps restart` 缺省声明面；`--runtime` 逃逸走 ADR-0069（ADR-0083 推翻其决策 6）。
3. **Cancel 遗留载体**：回卷 LatestSucceeded 基线（ADR-0082 翻案 admission N0 在该场景的适用）。
4. **程序收口**：A-D 收官即收，E 三切片按需另批。

## 验收门禁清单

- [x] Batch B：writeclassguard 全绿（153 全分类 + 三从属断言）；`go test ./internal/guards/` 绿
- [x] Batch C：收敛裁决表 golden + 钉测族五类以上全绿；两事件注册三链；doctor 行 + 内置告警
- [x] Batch D：contentequiv 四用例（swarm+k3s）+ 升级零滚用例 + CLI/console golden 族
- [ ] 收尾：`mise run test`（三 module -race）/ `lint` / `console:verify` / `generate:verify` 全绿；e2e:dind + e2e:upgrade 判读（升级首拍零滚为绿；现役载体带非零 seq 形态则一次性收敛为预期红，人工判读——BB 事故重演红线）
- [ ] 文档收口：eventcode usage 反扫、runbook 手动面盘点与记录条目、staging 换装另窗挂账

## 非目标

配置面（terraform/HCL，另立计划——proto 注解随其立项同批）、autoscale 实现（只立 ADR-0081 学说）、变量轮换自动重部署、stopped 自动重启、五种作者形态合并重写、Task/Browse 域收敛面、Component restart 声明面。
