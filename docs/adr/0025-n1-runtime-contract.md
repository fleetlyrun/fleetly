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

## 验收锚

- [ ] one-shot Workload 退出即终态、不被重启；七枚举停止原因全映射可观测
- [ ] 已完成 task 在 Watch 流可见（ExitCode / Reason / 实例身份）
- [ ] 同输入 Workload 两次翻译逐字节稳定（确定性守卫 E 扩到新字段序）
- [ ] 两级 DNS 在 dind 实证：池级 RR + per-Run 稳定名（swarm alias RR 行为结论落档）
- [ ] swarm API 压测锚点（per-Run service × torchwood 池规模）
- [ ] taskGroup 翻译在投影层有单测；translate.go 谎言注释消灭
- [ ] N4 k3s 推演复跑：核心 6 方法零改动吸收全部新字段
