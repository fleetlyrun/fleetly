# 批 0.5 裁决台账（2026-10-01，用户 16 项逐条澄清落定）

来源：深审报告 §5 裁决项（R-2~R-8、D-1~D-3、P1-4、P1-1）+ 架构深挖评审 6 候选（C1~C6，报告见临时目录 `architecture-review-20261001-zq4f.html`；关键证据已亲验：api 29 处内联 Tx、pinVolumes 三路不对称、register.go 20 项手清单）。全部裁决取推荐项。

## 裁决 → ADR 映射

| 裁决 | 结论 | ADR |
|---|---|---|
| C1 + R-8 + P1-8 + R-4 + D-3 | 受理位 + 统一写原语；幂等 header 为主 + 单表拦截器 | **ADR-0024** |
| D-1 + D-2 + R-3 + R-6 | Workload/WorkloadEvent 加宽；NamespaceRef Task 轴；engine 铸名；混合池拓扑 | **ADR-0025** |
| R-2 + P1-4 + P1-1 | payload 不版本化·Schema 钉扎；after_seq 游标惯例；SSE 短时票据 | **ADR-0026** |
| R-5 | 两级变量归一化期合成 | **ADR-0027** |
| R-7 | Team 轴 F1.8 前接实 + 索引改 (team_id, name) + Q-16 | **ADR-0028** |

CONTEXT.md：新增 **Acceptance（受理位）** 词条（随 ADR-0024）。

## 代码批次落点（裁决的执行序，非本批产出）

| 项 | 时点 | 说明 |
|---|---|---|
| C2 materialize module | **已完成（2026-10-01）** | engine 三份物化序列合一 + 三路一致性质测试（TestMaterializeThreePathsIdentical）|
| C4 RegisterAll 反向守卫 | **立即先行** | 镜像 guard A，纯守卫独立小 commit |
| C3 观测 verdict owner | N1 Task 设计批 | 与 P1-7 缓存形状分家同批，一次重排 observ |
| C5 swarm dockerAPI seam | F1.5 之后 | D-1 字段定形后拆 watcher/reconcile，一次到位 |
| C6 契约上提 capability | F1.11 设计批 | registry host 归一 + 防清空哨兵；Source seam（git clone 住 engine）随 F1.10/同批裁决 |
| ADR-0024 实现 | F1.1 起 | 幂等表+拦截器 → webhook 收口 → 受理位抽取；同批守卫：受理面反扫 + 幂等覆盖反扫 |
| ADR-0025 实现 | F1.5 设计批 | 字段集细则（停止原因映射表）随 proto 评审进 ADR 附录 |
| ADR-0026 实现 | F1.2/F1.4 | outbox 断档三件套随 F1.2；Schema 钉扎随 F1.4 |
| ADR-0027 实现 | N1 Variable 引入批 | |
| ADR-0028 实现 | F1.8 前 | 含 user repo FK 归一（既有挂账并入） |

## 实证项（验收锚的前置，未验证不得闭项）

- swarm 跨服务 alias 的 DNS RR 行为（ADR-0025 addressing 定稿前置）。
- per-Run=service 的 swarm API 压力（torchwood 池规模作输入，ADR-0025）。
- lynx 流式长流不被优雅关停超时误杀（ADR-0026 SSE）。

## 既有挂账保留（不在本批，防丢）

- 守卫 C 子面随 RuntimeExec（N3 前补空接口时扩枚举面）。
- swagger/openapi 远端生成通道翻回；B2/C2/C4 追认（N0 验收遗留）。
- `E_IDEMPOTENCY_KEY_CONFLICT` 入册随 F1.1（ADR-0024 已引用）。
- appLocks 惰性锁条目回收（Task 同构互斥设计时一并，深审裁决表 #16）。
