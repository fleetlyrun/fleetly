# ADR-0103: Functions Phase 2 实施——唤醒、缩零决策环与构建就绪门

日期：2026-11-22 ｜ 状态：Proposed（设计 v2 定稿后的实施 ADR；实施随批落地并回勾验收锚）

关联：docs/design/2026-11-19-functions-phase2.md（**v2**——形态真源，本 ADR 不重复立法）、ADR-0102（Phase 1 九任务 + §2 容量平面收编条款的兑现载体）、ADR-0101（Revision 保留窗——pending 锚并入 protect 集）、ADR-0064（内容寻址/scale-only——唤醒与伸缩零重部署的结构性红利）、ADR-0068（配额定律——defer 语义的执法源）、ADR-0088（生长律：capacity 第 14 环）、设计 v3 §8/§12（Phase 2 拆分与 swarm DNS 实证 §8 形态门槛）

## 背景

Phase 1（ADR-0102 九任务）已全量落地并 e2e 全绿。设计 v2（2026-11-19，design-it-twice 双独立交叉验证）收敛了唤醒方向（乙案：fn-<id> 别名解析点切换）、决策环形态（独立 capacity 模块）与构建就绪门（Build 行复用 + pending 意图锚）；§8 swarm DNS 实证（2026-11-21）把乙案的别名交接从假设升格为实证结论（0 副本不摘别名注册/双挂 RR 劈半被证伪/手递手交接成立）。本 ADR 立三项的执法面承诺与批次绑定。

## 决策

1. **唤醒（P2-1，activator）**：activator 是请求扣留面守护进程（hold-and-forward：快径直转/冷路径 Wake 恰一次 + 轮询/三段式 429/503 + 413 体帽），部署 = 受管 Workload（manageds-decl 路线：系统域单载体 attachNetwork 挂全部活跃项目网——traefik 同款 reconciler 通道）。唤醒写径 = `WakeFunction` 条件容量 CAS（`WHERE replicas=0`，**容量列即唤醒标记**——双独立一致否决 wake_pending 布尔列）；activator 永不触 Runtime、永不自拼载体名（host/唤醒/解析三窄接口经 9082 内网面，免认证 posture 与 proxy config 同族）。**别名交接（P2-1b）**：fn 投影 Addressing 按 replicas 分支（0 副本摘 fn-<id>，0 副本 spec 变更零滚动成本）；activator 别名集 = parked 行集派生（manageds Ensure 随拍批更新）；组件任务在函数声明口同端口监听（`ACTIVATOR_EXTRA_PORTS` env 注入——private 调用经 fn-<id>:<声明口>，swarm VIP 按端口透传）；`FunctionIDForHost` 双分支（fn-<id> DNS 形优先、public_host 次之）。交接窗 NXDOMAIN 与唤醒收敛窗由调用方 fail-fast 重试承载（调用语义归函数——可重试请求面假设）。
2. **通用伸缩决策环（P2-2，capacity 模块）**：「容量 = 行面旋钮」平面的自动写者独立成包（ADR-0088 生长律第 14 环 autoscale），v1 单消费者 Function（App 侧接入走未来显式 ADR）。决策三分支：**缩零 park**（min=0 且观测 running 且活跃信号静默满 idle 窗 → 行 CAS 归 0）/ **扩容**（per-副本请求压力 > 阈值 → +1）/ **缩容**（静默满窗单拍至多减半、地板 min）。防抖 = Knative scale-down-delay 同款（idle 行旋钮 0=缺省 300s；唤醒即活动重置窗口；running 门防收敛赛跑）。**配额 defer 语义**（§15.5 兑现）：引擎自发写绝不静默超限——配额超限本拍放弃 + `function.scale_deferred` 事件沿上明示（沿 = 外生变化清位重计）。观测双面：traefik per-router 请求率（public，RouteMetricKey 分账键与 router 命名同源单链）+ 负载词汇 function 轴 CPU（private 结构性补位）；观测面缺席 = 信号缺失（伸缩保守不动，park 退化唤醒活动判定）。**单写者纪律**：决策环写行（(replicas, updated_at) 乐观对锚 CAS）、收敛环只读行面；swarm/k3s 双 Runtime 都走平台环（k3s HPA 原生接入是后续优化批——单源纪律）。
3. **构建源与就绪门（P2-3）**：revision 保持不可变冻结体**不加状态列**——就绪态的载体是 Build 行（queued→building→succeeded|failed 全量复用，不平行造第二份）。机制三件：①意图锚 `functions.pending_revision_id`（DeployFunction(upload) 事务 = 冻结 revision + 铸 queued Build + 落 pending 不换轨；幂等重放同锚原样返回；指向行并入 ADR-0101 protect 集）；②环内兑现步（无回调面）：pending × Build 终态三分支——succeeded → 一事务 SwapCurrent + 清位 + `revision_changed(cause=deploy)`；failed/cancelled/expired → pending 保留 + `E_FUNCTION_NOT_READY` 呈现（预留码兑现，REST 503）；queued/building → D-4 幂等重建输入登记（daemon 重启安全——App 侧同纪律）；latest-wins（pending 被新 deploy 覆写后旧 Build 兑现放弃）。③「铸 Build + inputs 登记」提为 contract.Intake 导出出口（模块间禁 import——IntakeBuildFace 窄端口，PinVolumes 先例）；Build owner 泛化（owner_kind/function_id 两列，app_id 可空化——两类 owner 共享同一 Build 状态机与构建循环，拾取前置检按 kind 分支）。v1 upload 单源（railpack 钉版必填受理位执法）；git 源是 P2-4 同期件（FunctionSpec.source oneof 位已留）。
4. **per-invocation 观测轴（P2-4 第一件，随批落地）**：traefik `--metrics.prometheus` 开启 + 宿主发布 9091（cadvisor :8080 同 posture——VPC-only 节点端口）；采集循环 manager 节点抓取透传（job=fleetly-traefik）；负载词汇（ADR-0065）补 Function 轴。private 函数无请求观测是结构性断口（诚实标注，Phase 3 binding 式寻址再议）。
5. **词汇与事件随批承诺**：`function.scaled` payload 携 cause（manual|wake|park|autoscale）+ origin=autoscaler；`function.scale_deferred`、`function.build_queued` 两新码（eventcode 只增带 Source 锚 + schemareg 归拥有方包）；capability.Kind 的 activator 扩面挂账随唤醒词汇 ADR（kind 现只服务健康行命名）。parked 显示映射（replicas=0 → status 渲染 parked；缓存层四值法不动）+ parked 行状态缓存诚实化（0 副本 = stopped——stale running 会骗过决策环 running 门，wake↔park 振荡 e2e 实证咬出随批根修）。

## 后果

- activator 软单点：常驻函数不受影响故告警面弱——双副本 + 健康行 + 看门狗期望随决策环后续批（e2e「activator 停机：常驻 200/缩零诚实 503」断言挂账）。
- 伸缩参数（targetRequestsPerReplica/cpuActiveFloor）是循环本地常量——dogfood 负载数据回流后进 Options 端口（ADR-0102 决策 5 的收尾）。
- e2e 100+ 部署序列腿（ADR-0101 挂账）与 ⑨ staging dogfood 不在本批阻塞面。

## 验收锚

- [x] P2-1a 唤醒链 e2e 四断言（parked 显示映射/parked 路由应答/Wake CAS 0→1/载体重接管）——d8ebdceb
- [x] P2-1b 别名交接 e2e 四断言（同网探针经组件 PROTO-LINE/ wake CAS/别名回挂 running/直挂接管）——a950c71a（前置：swarm DNS 实证 §8 三结论 aefca85e）
- [x] P2-2 决策环模块表驱动测试九件（park/行旋钮窗/唤醒重置/CPU 与请求活动防 park/查询失败退化/地板/扩容/defer 沿上单发+外生变化清沿/减半地板）+ CAS 竞态注入——5fa03674
- [x] P2-2 e2e 自动缩零腿（create --wait → 静默满窗 parked → fn-<id> 探针复活）全链 ALL GREEN——5fa03674
- [x] P2-3 兑现链模块测试两件（queued→succeeded SwapCurrent+清位+事件一拍 / latest-wins 覆写放弃）——c922d6a0
- [x] 事件三链（Source 锚/golden/usage 反扫）+ errcodeToHTTP 全量覆盖 + self-description golden 同批——c922d6a0
- [x] 三表守卫同批登记（interSatFileOwnership/metaFileClass/engineFuncExemptions——intakeface.go/functionbuild.go）——c922d6a0
- [ ] 唤醒词汇 ADR（capability.Kind activator 扩面 + 冻结豁免 wake 写 + 唤醒面 errcode 语义复核）
- [ ] e2e「activator 停机：常驻 200/缩零诚实 503」断言腿
- [ ] e2e 100+ 部署序列腿（ADR-0101 挂账随批）
- [ ] ⑨ staging dogfood ≥2 函数（public+private）全链 200 + 决策环负载数据回流
