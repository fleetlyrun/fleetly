# ADR-0054: k3s 网络语义补面（成员资格入站隔离）与升格缺省裁决

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-08 | ADR-0052（k3s 试点，§9 挂账 3/4/8——本批工单；决策 3/6 的事实修正）、ADR-0053（子面补齐——升格前置满足）、ADR-0013（Network 成员模型/peer 声明与批准）、ADR-0025 决策 5（taskGroup 投影翻译）、ADR-0046（网络重建动词——语义性缺席维持）、CONTEXT.md（Network/Task Network Group/Peer 词条）、架构 §10（能力只增不减） |

## 背景

ADR-0053 补齐 Exec/Hygiene/RBAC/两节点四子面后，ADR-0052 决策 4"三子面补齐前不升格"的前置已满足。剩余网络语义挂账（§9 挂账 3/4）与升格口径是本批主轴；挂账 8（旧 Runtime 孤儿登记）一并裁决。

盘点事实（裁决依据，2026-10-08 契约与实现追查）：

1. **ADR-0052 决策 3 的"跨 Project 隔离 = Namespace 间默认不通（k8s 原生）"记载不成立**。k8s Namespace 是 DNS 与管理边界，不是网络边界；NetworkPolicy 语义是"无 policy 选中 pod 即全通"（纯增量模型）。当前（本批前）k3s 形态：跨 Namespace、跨 Project、Task 组对项目域、同项目异网进程之间**全部互通**——唯一例外是 egress 标记载体（自身出站受限）。e2e 的"跨 ns 拒"断言全部站在 egress 载体的出站面上，从未实证过 Namespace 间入站隔离。这比挂账 3 记载的"Task 组弱化"更大：多租户 k3s 集群上项目间网络层零隔离。
2. **swarm 的语义真源是"附件即互通、不附件即隔离"**（docker 跨 overlay 隔离）：App Process 按 spec networks[] 附件；Task Run 只挂自身网络组（taskgrp-\<name\>）；App 可显式跨挂（taskGroup:\<name\>）；跨 Project 引用经 ADR-0013 声明/批准后附件；受管 Proxy 附件全部活跃项目网；零附件载体不可达（Proxy 也触达不了——Proxy 经项目网触达后端）。附件集投影在 `Workload.Networks`（同域）与 `Workload.NetworkRefs`（跨域），k3s Provider 此前全部忽略（仅消费 EgressNetworks 子集）。
3. **k8s netpol 无法按"共享至少一网"表达单一命名空间级规则**（规则是 ns 级静态集合，不能参数化到被选中 pod 的附件集），但**每网一条 policy 的并集恰好等价**：policy_k 选中挂 k 网的载体、放行同网成员——载体挂 {X,Y} 时被 policy_X 与 policy_Y 同时选中，放行集并集 = 共享至少一网。组级聚合（per-network，非 per-Task/per-Run）。
4. **hostPort 发布载体必须豁免入站隔离**：外部→hostPort 的 DNAT 流量过 FORWARD 链、源无 pod 身份，被 policy 选中的载体会对之落默认拒——受管 Proxy（80/443）/zot（5000）/cadvisor（8080）的宿主发布面会整体断流。发布即节点级可达是编排器中立语义（swarm host 端口同判），豁免是对该语义的忠实翻译而非弱化。
5. **出站方向维持"仅 egress:none 受限"**：netpol 无法表达"出站限集群外"而不误伤私网端点（集群 CIDR 不可知；拒绝 RFC1918 会断用户内网服务与节点级 zot）。入站模型已保证隔离目标侧收口（跨域发起被目标 policy 拒）。
6. **升格口径的三选项**：(a) 缺省翻转 k3s——破坏"缺省 swarm 升级零扰动"硬锚，且无生产实跑记录与迁移 runbook；(b) 资格认定 production-ready、缺省维持 swarm、新装显式 opt-in；(c) install.sh 面分叉。挂账 7（多 server HA）与 12（大档 hostPath 生产实证）未收，生产形态实跑为零。
7. **挂账 8 的场景**：场景 3 迁移后旧 Runtime 载体对平台失明（k3s 模式下无 docker 客户端连接面）。平台登记面要么持有旧集群连接（跨 Runtime 双连接 = 新面），要么记静态行（立即陈旧）。

## 决策

### 1. 事实修正 + 网络成员资格入站隔离（挂账 3+4 同一子系统收口）

**模型**：把 swarm 的附件语义翻译为 k8s 的成员资格 label + per-network 入站 policy——

- **成员资格 label**：载体附件集（`w.Networks` 同域名 + `w.NetworkRefs` 跨域引用）翻译为 pod label `fleetly.net.<k>=true`。k 是 (projectID, networkName) 复合的确定性净化名（名段可读 + 项目段哈希消歧，同 addressingLabelKey 截断+哈希模式）——**同域与跨域同公式**：接收方自己的网与挂靠方的引用推导出同一 k，跨 ns 规则两侧天然对齐。零附件载体带 `fleetly.net.none=true`。
- **成员 policy（每网一条，组级聚合）**：`fleetly-netisolate-<k>`，podSelector 选挂 k 网载体，policyTypes=[Ingress]，放行集：
  - 同 Namespace 挂同一网的载体（podSelector 同 label）；
  - fleetly-system Namespace（namespaceSelector——受管 Proxy 触达后端；系统域全是平台载体，整 ns 放行是对 swarm "Proxy 附件全部项目网"的等价宽放，zot/VL/VM 的例外是节点端口级、无净值，诚实边界注记）；
  - 引用衍生网（k 来自 NetworkRef）：目标 Project Namespace + 该网成员 label（双向互通的挂靠方半边）。
- **零附件载体**：`fleetly-net.none` 锚的 policy 无放行规则（入站全拒）——与 swarm 零附件不可达逐位对齐（Proxy 也触达不了）。
- **taskgrp-\* 网络同公式**：Task Run 挂 taskgrp-\<g\> label；App 跨挂（taskGroup:\<g\> 投影为同名平台网）同 label——组内互通、组外与项目域双向拒。**挂账 3 收口**。
- **跨 Project peer（挂账 4 收口）**：peer 的网络层收口 = 上述引用衍生规则（挂靠方 ns）+ **peer grant policy**（接收方 ns，挂靠方 Ensure 持有写入、**按声明方域（app 轴）键控**）：`fleetly-peer-<hash>`，选中接收方该网成员，放行 {挂靠方 ns + 引用 label}——批准的互放行是双向的，policy 由声明方（唯一知情方）持有，两侧写。撤销收敛：engine isolate 剥离引用（ADR-0013 附录 A.4 既有）→ 挂靠方 Ensure 的**纯意图集**收敛当拍删 grant（grant 只放行携 key 的载体，删除只会更早拒绝——安全方向，不等旧 pod 终止）；引用衍生成员 policy 走活 label 半边（终止窗内保留、滚动完成后删）。挂靠方项目消亡后接收方残留 grant = 指向空集的 no-op（诚实边界，后续卫生清扫批可收）。
- **hostPort 发布载体豁免**：声明 Publish 的 Workload 不带成员资格 label（不被任何 policy 选中 = 入站不隔离）。理由见背景 4；用户面不暴露 Publish（受管域专用声明）。
- **受管域（fleetly-system）不设隔离 policy**：受管载体要么发布宿主端口（豁免面）要么只被 fleetlyd host 流量触达（host→pod 不过 netpol 链）；项目域→系统域 pod 直连维持全通，弱于 swarm 的诚实边界（系统域载体全是平台自有面），Notes/本 ADR 声明。
- **Ensure 收敛序（活 pod label 派生，实施期定稿）**：隔离 policy 与 grant 是**项目级共享资源**——同项目多域（App/Task/Database/Browse）各自独立 Ensure，按"本拍期望集"删除会让一域收敛掉别域成员的 policy（e2e task drill 咬出的实锤：task 域 Ensure 删掉 app 域的 default 成员 policy）。收敛判据改为集群事实：**policy 选择器 key 不被本 ns 任何 managed pod 持有即删**（grant：挂靠方 ns）——跨域天然安全（他域成员 pod 在场即保留）、滚动窗口安全（替换中的旧 pod 仍持有 label）、重启安全（无内存态）。成员 label 恒随 spec 滚动更新——policy 与 label 同拍收敛。Remove 拆域内 managed netpol（他方 grant 跳过——挂靠方收敛面）。
- **姊妹 bug 修复（预存，e2e 同源咬出）**：k3s 的 Service 收敛标签只有 team+project 无域主体轴——**同项目第二域的 Ensure/Remove 会把别域的 Service 当 stale 删除**（task/db 域删 app 的 web Service → 全域 NXDOMAIN；此前批次 e2e 绿是 app reconciler 重建对拍的运气，现役多域项目一直在隐性闪断）。修复：serviceLabels 补六轴锚（nsSelector 同构），收敛/拆除按"宽列 + 轴裁决"——他域（2）不可见亦不可删，无轴遗留（补轴前形态）首拍清除（升级收敛面）。
- **RBAC 动词面**：networkpolicies 增 `list`（收敛对照）；create/delete/get 既有。

**语义对照表（本 ADR 的裁决口径）**：

| 语义面 | swarm | k3s（本批后） |
|---|---|---|
| 同网成员互通 | overlay 附件 | 成员 policy 同 label 放行 |
| 异网（同项目） | 跨 overlay 隔离 | 各自 policy 互不选中 |
| Task 组对项目域 | taskgrp 独立 overlay | taskgrp policy 只放组员 |
| 跨 Project 未批准 | 无附件路径 | 引用不进期望集（engine strict/isolate 既有）+ 无 grant policy |
| 跨 Project 已批准 | 双向声明 + 附件 | 引用衍生规则 + 双侧 grant policy |
| 撤销即时隔离 | service update 剥附件 | isolate 剥离 → Ensure 收敛删 policy（隔离生效时点 = 该次 Ensure 完成，A.4 同判） |
| egress:none | 弱隔离（Notes 声明） | 强隔离（ADR-0052 决策 6 既有） |
| 出站（非 egress:none） | 不限 | 不限（目标侧入站承载跨域收口） |
| 宿主发布载体 | host 端口 = 节点级 | hostPort 豁免隔离（节点级） |
| 系统域→项目域 | Proxy 逐网附件 | fleetly-system 整 ns 放行 |
| 项目域→系统域 pod 直连 | 不可达（未附件） | 全通（诚实边界） |

### 2. 升格口径 = 资格认定（production-ready，缺省维持 swarm）

- **k3s 认定 production-ready**：核心面 + 七子面齐（ADR-0053 收口后），网络语义补面（决策 1）后与 swarm 的语义差收敛到上表两行诚实边界（项目域→系统域 pod 直连、出站方向目标侧收口）。
- **config `runtime.provider` 缺省维持 `swarm`**（"缺省 swarm 升级零扰动"硬锚不动）；新装显式 opt-in（`runtime.provider=k3s`，装机序入 runbook）。
- **否决缺省翻转 (a)**：无生产形态实跑记录（挂账 12 未收）、无多 server HA（挂账 7）、无迁移序 runbook——翻转条件不成熟。**缺省翻转的前置条件显式记录**：①首个生产形态 k3s 集群实跑一段（覆盖大档 hostPath 通道）；②多 server HA 裁决收口；③迁移序与 flag-day runbook 落地。届时另开 ADR。
- **否决 install.sh 分叉 (c)**：install.sh 的 swarm init 步骤与 k3s 形态正交（e2e 既证：fleetlyd 手起不走 install.sh）；k3s 装机序（k3s server 起 → config 写 provider=k3s → fleetlyd）是 runbook 面，不是安装器面。分叉会制造双安装器维护负担。
- staging 不换装维持（ADR-0052 决策 1 同判）；k3s 专用 staging 环境若建，另批裁决。

### 3. 挂账 8 = runbook 承载（不建平台登记面）

- 平台对旧 Runtime 载体**无连接面**（k3s 模式下 docker 客户端未配置）——"孤儿登记"要么跨 Runtime 双连接（新面，为一次性迁移窗口引入常驻连接）、要么静态行（立即陈旧，比没有更糟）。**诚实形态 = 迁移 runbook 的显式处置序**（与 ADR-0052 决策 1"平台失明即显式数据处置"同判）：排空旧集群、清旧载体与卷的运维命令清单。
- `fleetly nodes`/doctor 不加只读报告——它们回答"在管集群的现役状态"，不回答"曾经管过什么"。

### 4. 顺手收口：runbook k3s 节

`docs/runbooks/k3s-runtime.md` 新建：装机序（opt-in）、RBAC 规则升级序（admin kubeconfig 重跑一次收敛 ClusterRole）、长期 SA token 不轮换边界（泄漏处置 = 重建 token Secret）、relay_online 载体事实注记（集中形态：连接全在 manager，语义 = 该节点 exec 可服务）、旧 Runtime 孤儿载体处置序（挂账 8 兑现面）。

## 后果

- k3s Provider 文件面：translate.go（成员资格 label）、network.go（成员/peer policy 收敛）、runtime.go（Ensure/Remove 挂钩）、utility.go（工具 Pod 成员 label）、rbac.go（+list 动词）。swarm Provider 零改动（回归底座）。
- engine/api/CLI/Console 零改动（附件集投影既有；隔离是 Provider 载体面）。
- 升级面：载体 pod 模板新增 label = 一次受控滚动（k3s 域 Ensure 的代次机制承载）；e2e 升级腿不受牵动（runtimeswitch 是冷迁移）。
- 隔离不变式自愈：netpol 是 Ensure 收敛产物（期望集对照），载体漂移/人工删 policy 由下拍收敛兜底；peer 撤销由 engine 隔离环（A.4）+ Ensure 收敛双承载。
- 挂账收口：ADR-0052 §9 的 3（Task Network Group）、4（跨 Project peer）、8（孤儿登记——runbook 承载）闭；7/12 维持挂账（触发条件不变）。ADR-0052 决策 3 的事实修正以追记落档（不改写历史决策文本的裁决内容，修正的是其前提记载）。
- 守卫承载（ADR 模板验收锚纪律）：Notes 诚实边界措辞锚（TestDescribeNotesHonesty 扩展）+ RBAC 规则表锚（TestDesiredClusterRoleTable）+ e2e 活体锚（CI 三腿）——行为级承诺由测试面执法，不新开静态扫描任务。

## 验收锚

- [ ] 成员资格翻译单测：Networks/NetworkRefs/零附件/Publish 豁免四形态的 label 断言（workloadLabels 扩展）+ 复合 key 同域/跨域同值 + label key 上限截断哈希（TestNetMembershipLabels）
- [ ] 成员 policy 收敛单测：每网一条（组级聚合非 per-carrier）+ 放行集三规则 + stale 删除（活 label 派生：无成员 pod 即删/有成员 pod 保留/egress deny 与他方 grant 不触碰/非 managed 不触碰）（fake clientset，TestReconcileNetIsolation*）
- [ ] 跨域安全双锚（e2e 咬出后的回归面）：task 域 Ensure 不删 app 域成员 policy（TestReconcileNetIsolationCrossDomainSafe）+ task 域 Ensure 不删 app 域 Service/无轴遗留清除（TestEnsureServiceConvergenceIsDomainScoped——预存 Service 轴缺失 bug 的钉板）
- [ ] peer grant 单测：双侧 policy 形态（挂靠 ns 引用衍生规则 + 接收 ns grant）+ 按.owner label 清 stale + 撤销（refs 消失）双侧收敛删除（TestReconcilePeerGrants*）
- [ ] 工具 Pod 成员 label 单测（buildUtilityPod 挂 req.Networks 附件集——备份链可达性锚）
- [ ] RBAC 表格测试更新（networkpolicies create,delete,get,list）+ e2e auth can-i 断言扩展（list networkpolicies yes）
- [ ] Notes 诚实边界更新：撤"task network group isolation is relaxed"行，入成员资格隔离声明 + 两行诚实边界（项目域→系统域 pod 直连全通、出站方向目标侧收口）——TestDescribeNotesHonesty 同批钉死
- [ ] e2e `e2e:k3s` 网络段全绿：跨 ns 活体（P1 常规载体 → P2 服务拒 / 同网成员通）+ 成员 policy 在场 + peer drill（declare→approve→挂靠部署→跨项目通→revoke→隔离收敛→拒）+ task-group drill（task pod → 项目服务拒 / 跨挂 app ↔ task 组内通）
- [ ] e2e 双腿回归（k3s/k3s-tw 现有段零漂移——route/exec/db/backup 链在隔离模型下照常绿）
- [ ] 全门禁：`mise run test` + `mise run lint` + `go test -count=1 ./internal/guards/` + `generate:verify` + `console:verify`；swarm 全套零回归
- [ ] ADR-0052 §9 挂账 3/4/8 划线注日期 + 决策 3 事实修正追记；runbook k3s 节落地；checklist F4.1 追记
