# ADR-0025: N1 Runtime 契约定形（Workload/WorkloadEvent 加宽、NamespaceRef Task 轴、engine 铸名、混合池拓扑）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-01 | D-1/D-2/R-3/R-6 裁决（批 0.5）、ADR-0012（Task 两形态/Owner Lease/停止原因七枚举）、ADR-0022（drift 口径）、ADR-0001（运行时中立）、深审报告 §1/§5 |

## 背景

深审 D-1/D-2：Workload 无生命周期声明（swarm 硬编码 `RestartPolicyConditionANY`——one-shot Run 投影后进程退出即被无限重启）；WorkloadEvent 无终态原因/退出码/实例身份（已完成的 swarm task 整个不可见，container die 事件被订阅却在 mapEvent 丢弃）；观测槽 per-Workload 单槽 last-write-wins，实例级缺位。NamespaceRef 无 Task 轴（WorkloadID 是 App 形态公式；`.App` 语义漂移）；taskGroup→网络名翻译责任悬空（`translate.go:148` 注释声称 engine 已翻译、实况原样透传）；两级稳定 DNS 无契约承载；resident 池载体拓扑未拍板。

## 决策

1. **Workload 加宽（字段只增）**：`Restart`（语义按 ADR-0012 停止原因映射：长运行=any，one-shot=never）+ `StopGrace` + `addressing` 声明（平台标准 DNS 名，见第 6 条）。
2. **WorkloadEvent 加宽（字段只增）**：`ExitCode` + `Reason`（ADR-0012 七枚举）+ 实例身份（task ID / slot）；WorkloadState 补"完成"终态（failed 不再被 degraded 吞并）。
3. **观测两轨**：Workload 级（现有 L1/L2/L3 部署门）与 Instance 级（Run 状态机）分轨；缓存形状分家随 P1-7 设计批。
4. **NamespaceRef 显式加 Task 轴**：拒把 Task ID 塞 `.App` 字段（ADR-0007 词汇污染）；`.App` 注释澄清为 App 域主体。
5. **taskGroup 翻译责任 = engine 投影层**：`projection.go` 把声明的网络组名翻译为实际网络名再下发；`translate.go:148` 谎言注释随批消灭。
6. **engine 铸名（R-3）**：稳定 DNS 名公式住 engine——名字是平台 API 面（用户/Agent 直连），N4 换 Runtime 名字必须不变；Workload 以 `addressing` 只增声明字段携带，Provider 映射为自己的原语（swarm=alias、k8s=Service）。跨服务 alias 的 DNS RR 行为需 dind e2e 实证后定稿。
7. **resident 池 = 混合拓扑（R-6）**：池级 DNS 用 1 service×N replicas（天然 RR）；per-Run 控制（TTL / 排空 / per-Run DNS）用 per-Run service。swarm API 压力（Ensure/Watch ×N）需压测，torchwood 实际池规模作输入。
8. **swarm 侧同批**：container 终态事件映射入 Watch、已完成 task 观测可见、firstBootJobs 诚实拒绝解锁（`drive.go:105` 的解锁条件即本 ADR）。
9. **核心 6 方法签名不动**（窄面纪律维持，tsuru 扩张路径继续被挡）。
10. JobSpec 重塑（深审 C-13：TaskSpec 嵌 ProcessSpec，消灭 JobSpec 重复定义）随本批零成本窗口执行。

## 后果

- F1.5 设计批落地字段集；schemaVersion 评审随 proto 变更。
- C5（swarm watcher/reconcile 拆分 + dockerAPI seam 抬高）排本批之后（同文件动刀，一次到位不付二次返工）。
- 停止原因枚举值表以 ADR-0012 七枚举为准，字段集细则（枚举映射表）随 F1.5 proto 评审定稿进本 ADR 附录。
- JobSpec 重塑实况（F1.5 落地）：`ProcessSpec process = 8` 嵌套形态为唯一读取面；旧标量字段（image_origin/command/env/secret_refs）`deprecated` 退役但保留号——buf breaking FILE 档下零消费者字段删除亦红，彻底删除待 breaking 基线策略（如首个发布版 tag 基线）变更。firstBootJobs 执行接线（部署链等待/回滚编排）随后续部署链批——一次性 Run 机制已就绪，解锁条件成立。

## 附录 A：停止原因映射表（F1.5 定稿，ADR-0012 七枚举）

终态对（Run.state, stop_reason）与触发源——事件 payload（run.stopped/run.failed）携带同款字段：

| Run 终态 | stop_reason | 触发源 | 观测/判定锚 |
|---|---|---|---|
| stopped | completed | 进程自然退出（退出码 0） | WorkloadEvent.State=completed + ExitCode=0（swarm task complete） |
| failed | failed | 进程失败（退出码非 0 / rejected） | WorkloadEvent.State=failed + ExitCode≠0 |
| stopped | stopped_by_user | StopTask(force)/StopRun/DeleteTask | stopping 起因预写；观测 stopped 确认或停止兜底 deadline |
| stopped | ttl_expired | TTL 绝对 deadline 到期（janitor） | 行 deadline（ADR-0018 墙钟）→ stopping → 终态 |
| stopped | lease_expired | Owner Lease 超宽限未续期 | task 行 lease_deadline + TaskLeaseGrace → drainTask |
| stopped | owner_revoked | 属主 Token 吊销 → 宽限排空（可配置跑完 TTL） | task 环拉式扫 revoked Token（P1-8；owner_token_id 行引用） |
| stopped | platform_drained | 平台侧移除载体（节点排空/人工拆载体——非用户起因的 stopped 观测） | 活跃 Run 收到 stopped 观测且无 stopping 起因 |

载体生命周期声明映射（决策 1）：

| Workload.Restart | 语义 | swarm | k8s（N4 推演） |
|---|---|---|---|
| 零值/always | 长运行（退出由编排器重启） | RestartPolicyConditionAny | Always |
| never | 一次性（退出即终态；Run 一律 never——池补足由平台承担） | RestartPolicyConditionNone | Never |

DNS 铸名公式（决策 6，engine 真源）：per-Task 池级稳定名 `task-<taskID 小写>`（活 Run 别名轮询）；per-Run 稳定名 `run-<runID 小写>`；Task Network Group 平台网络名 `taskgrp-<group>`（App Process `taskGroup:<name>` 跨挂经投影层翻译至同名）。

## 验收锚

- [x] one-shot Workload 退出即终态、不被重启（RestartNever → swarm none；契约测试 + engine 终态镜像测试）；七枚举停止原因全映射可观测（映射表附录 A + run 终态事件 payload 携带）
- [x] 已完成 task 在 Watch 流可见（swarm pollTasks Task 域豁免：终态任务含 exit code/原因原文/实例身份上报；映射测试钉死）
- [x] 同输入 Workload 两次翻译逐字节稳定（确定性守卫 E fixture 扩到 StopGrace/Addressing 新字段序）
- [ ] 两级 DNS 在 dind 实证：池级 RR + per-Run 稳定名（swarm alias RR 行为结论落档）
- [ ] swarm API 压测锚点（per-Run service × torchwood 池规模）
- [x] taskGroup 翻译在投影层有单测（TestProjectTranslatesTaskGroupRefs）；translate.go 谎言注释消灭
- [ ] N4 k3s 推演复跑：核心 6 方法零改动吸收全部新字段
