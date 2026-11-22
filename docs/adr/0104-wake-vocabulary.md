# ADR-0104: 唤醒词汇与执法面——activator 组件、Kind 扩面与请求面语义

日期：2026-11-22 ｜ 状态：Accepted（Phase 2 实施随批兑现的词汇收口；ADR-0103 挂账项清偿）

关联：ADR-0103（Phase 2 实施——决策 1 唤醒、决策 5 词汇挂账的本 ADR 清偿）、ADR-0077（Functions 词汇批：Task 让渡 function/Function 词条/lambda 入 banned/唤醒组件禁 gateway/edge/ingress/load balancer/relay）、设计 v2 §1.2（组件形态）与 §8（swarm DNS 实证）、CONTEXT.md（词汇真源）

## 背景

activator 已全链落地（manageds-decl 部署、wake face 9082、别名交接、e2e 双链 ALL GREEN），但三处词汇面留了挂账：①manageds decl 的 kind 字段用字面量 `"activator"`（健康行命名消费）——capability.Kind 七类端口不在册；②WakeFunction 的冻结豁免语义（Workload 变更族照冻 vs 容量行写）在 freeze.go 已带注释但未经 ADR 立法；③唤醒面三段式（429/503/413）的 HTTP 状态语义未经立法钉扎。

## 决策

1. **组件名 = activator**（Knative 同构词；ADR-0077 词汇红线的正名兑现——禁 gateway/edge/ingress/load balancer 与 relay，activator 表「激活休眠载体」的本义，与 proxy/registry/logging/metrics 受管组件命名族并列）。词条入 CONTEXT.md：activator 是平台的请求扣留组件——缩零期函数的请求在唤醒组件扣留（hold-and-forward）、唤醒落账后透传给函数载体；不是网关（不终结 TLS、不做鉴权、不持路由表）、不是 relay（不代理常驻流量——唤醒后函数路径直接由载体应答，RR 窗的快径透传是交接窗过渡形态）。
2. **capability.Kind 扩面 = `KindActivator`**（词面 "activator"）：manageds decl 的 kind 字段从字面量收编为常量——健康行命名（"managed activator"）与受管域枚举的词汇单源。Kind 是「能力种类」不是「实体」：activator 不是第九类 Provider 端口（不新增 capability 接口——它经 manageds-decl 声明消费通用 reconciler 通道，与其他受管组件同形），是受管组件在 Kind 词汇表的在册位。守护进程名（cmd/activator）、镜像名（ghcr.io/fleetlyrun/activator）、受管 ns（fleetly/system/activator）三处同词同源。
3. **WakeFunction 冻结语义 = 容量行写（非 Workload 变更族豁免）**：freeze.go 现行形态（field=function_id、kind=KindFunction 锚）立法确认——Wake 是 `RequestWake` 条件容量 CAS（`WHERE replicas=0`），与 ScaleFunction 同族（容量平面行写、零重部署、零 spec 变更），**不在 Workload 变更冻结族**（Deploy/Rollback 走的换轨冻结面）。变化冻结（ADR-0082 维护窗）期间唤醒仍可用：唤醒是请求驱动的可用性动作，维护窗拦它会把维护窗变成缩零函数的请求黑洞。此语义由 writeclassguard 的 WRITE_CLASS=DECLARATION 判据执法（P2-1a 第二刀已落）。
4. **唤醒请求面三段式 = HTTP 状态即契约**：activator 请求面的 429（per-function 扣留帽）/503（唤醒 deadline 超限，携 Retry-After）/413（体帽 4MiB）/504 语义（host 映射失败 502）是 HTTP 传输面语义，**不铸 gRPC errcode**——调用方是任意 HTTP 客户端（private 调用语义归函数），平台错误信封（E_* 码）只在控制面 API（gRPC/REST /v1）存在。X-Fleetly-Wake 自证头是冷路径观测锚（响应经唤醒组件的标记），非 API 契约（头缺席 ≠ 语义差异——稳态路径本来就不带）。
5. **parked 词面立法确认**：replicas=0 的显示映射词面 `parked`（P2-1a 第一刀落码）——与 `stopped` 分立（stopped = 载体失败；parked = 行面期望即 0）。决策环 park 动作的事件 cause 词面 `park`（function.scaled cause=park）同源。

## 后果

- CONTEXT.md 增 activator 词条 + KindActivator 常量收编（manageds/managed.go 单行改 + capability.go 常量块）；wording 守卫零新禁词（activator 是正名非禁词面）。
- e2e「activator 停机：常驻 200/缩零诚实 503」断言腿随本批落地（验收锚）。
- 决策环观测、唤醒 ADR-0103 决策 1 的形态承诺不变——本 ADR 只收词汇与语义执法面。

## 验收锚

- [x] CONTEXT.md activator 词条入册（Avoid 面四词已在册；relay 入 skippedTokens——exec 节点中继技术本义白名单人工评审面）
- [x] capability.KindActivator 常量收编 + manageds decl 单源（健康行命名不变）
- [x] freeze.go WakeFunction 锚注释引本 ADR（语义立法面）
- [ ] e2e activator 停机腿（常驻函数 200 / 缩零函数诚实 503——activator 单点软肋的诚实边界）
