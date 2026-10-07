# ADR-0053: k3s 试点补面批——Exec 集中形态、Hygiene、NetworkMaintenance 语义性缺席定型、RBAC 最小权限、两节点 Enrollment

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-08 | ADR-0052（k3s 试点与 §9 挂账 1/2/5/6——本批工单）、ADR-0049（Exec 子面契约/反向中继先例）、ADR-0046（网络重建动词）、ADR-0014（材料分发）、ADR-0039（RuntimeUtility 材料通道）、架构 §5（Runtime 契约）/§10（能力只增不减）、CONTEXT.md（Exec Session/Relay 词条） |

## 背景

ADR-0052 决策 4 明文"三子面补齐前 k3s 不升格为可缺省 Runtime"。本批消化 §9 挂账的核心面：RuntimeExec（挂账 1）、RuntimeHygiene（挂账 2）、Enrollment 两节点（挂账 5）、kubeconfig RBAC 最小权限（挂账 6）；NetworkMaintenance（挂账无编号，决策 4 已断言前置病灶不存在）一并定稿。挂账 3/4（Task Network Group 隔离、跨 Project peer）、7（多 server）、8（孤儿登记）、12（大档 hostPath 生产实证）不在本批。

盘点事实（决策依据）：

- **k8s exec 本就经 apiserver**：exec API 是 apiserver 的子资源（SPDY/WS 流，apiserver→kubelet:10250 通道是 k8s 自身基础设施——watch/logs/exec 同走，非平台新增入站面）。持有 kubeconfig 的 fleetlyd 可直达**任意节点**上的 pod，无 node-local API 缺口。
- **swarm 反向中继是编排器缺口补偿**（ADR-0049：docker exec 只落在持有容器的 daemon 上，manager 无法代发）——补偿的拓扑前提（exec 是节点本地操作）在 k8s 不成立。
- **engine 会话路由模型以平台节点 ID 为锚**（agents[nodeID] 注册表；受理时锁定路由）：Provider 无法绕过，也不应绕过（限额/TTL/票据/审计全住引擎）。
- **k3s node token**（`/var/lib/rancher/k3s/server/node-token`）正是 agent join 材料（Enrollment 同源）：活 token 等价集群成员权，C3 敏感度锚在 k8s 侧的对应物。
- **Enrollment 的 server 地址缺口**：kubeconfig 的 server URL 是 `https://127.0.0.1:6443` 形态（e2e 与生产 k3s 缺省皆然）——worker 节点执行 `k3s agent --server 127.0.0.1` 必失败，需解析控制面节点可达地址。
- **ensureSecrets 是 create-only**（收官批落定域唯一命名时未动值更新面）：域内材料值轮换（如密码轮换）静默失效——secret 已存在即沿用旧值。k3s Secret 是 mutable 对象，与 swarm 不可变 secret 按指纹版本化的语义分叉点。
- **试点 kubeconfig = /etc/rancher/k3s/k3s.yaml 是 cluster-admin**：载体侧 automountServiceAccountToken=false 已随收官批落地（正交面），但 fleetlyd 自身持全权凭证。

## 决策

### 1. RuntimeExec = apiserver 原生集中形态；否决 per-node relay agent kubeconfig

- **执行面**：`ExecWorkload` 经 client-go `remotecommand`（SPDY）对目标 pod 直exec——TTY 形态 PTY 合流、非 TTY 双路、resize 经 TerminalSizeQueue、stdin 读至 EOF。退出码从 SPDY 错误流还原（"command terminated with exit code N" 文本解析——client-go 无结构化退出码面，kubectl 同款机制；解析失败如实上抛错误）。
- **会话路由 = 管理侧 per-node 回环注册，engine 零改动**：`RunRelayAgent` 实现为集中形态——枚举锚定节点，**每个节点一条**到自身 gateway `/v1/relay` 的 WS 连接（hello.carrier_node_id = k8s 节点名），会话帧经 apiserver exec 服务。engine 的 agents[platformNodeID] 路由、relay_online 节点视图、限额/TTL/票据/审计全部原样成立（relay_online 语义 = "该节点的 exec 可服务"，诚实）。
- **节点生命周期**：relay 循环节拍列节点——新节点起连接（未锚定即退避重试，锚定后自愈）、消失节点停连接（engine 侧 dropAgentConn 收口其名下会话）。
- **凭证**：`ExecClusterToken` = k3s node token 文件对照（与 Enrollment 同源；C3 等价成立）。`RunRelayAgent` 的 JoinToken 缺省走本地 token 文件（k3s 无 swarm rotate 面——node token 轮换需 server 重启，Enrollment rotate 已诚实失败，同口径）。
- **否决 per-node relay agent kubeconfig**（ADR-0052 挂账 1 的原始倾向）：为不存在的问题（节点本地 exec API 缺口）引入部署面（每节点常驻容器 + kubeconfig 分发）；kubelet 通道本就是 k8s 基础设施，平台不再叠加第二通道。**挂账 5 的"AgentCommand kubeconfig 装载"随之溶解**：k3s EnrollKit.AgentCommand 恒空（无节点侧代理面），worker enrollment = k3s agent join 命令（唯一装载面，k3s 对节点 = docker 对 swarm 节点的运行时前提）。
- E_EXEC_UNSUPPORTED 退役路径：errcode 注册表只增不删（engine 对无 exec 面 Runtime 的受理拒绝语义不变）；k3s 侧的退役 = 实现 RuntimeExec 接口本身——编译期断言 + FacesOf/Offered 自动扩面 + Describe Notes 撤"exec subface is not implemented"声明（TestDescribeNotesHonesty 同步）。
- **TTY 会话的 stdin EOF 诚实边界**（e2e 实证后补录）：PTY 形态下消费端收流只关闭 stdin 流，**不终止载体进程**——k8s 通道的 stdin 关闭不等价 swarm hijack 连接关闭的 daemon 收口（swarm 的"daemon 随连接关闭杀 TTY exec"是编排器侧语义，非平台契约）；契约允许会话活到 idle/hard TTL 收口。非交互消费面（e2e/脚本）用显式 argv 自退出形态（进程退出即会话收口），交互 TTY 用户照常 exit。

### 2. RuntimeNetworkMaintenance = 永久语义性缺席定型（不实现接口）

- ADR-0052 决策 4 的断言升格为定论：RebuildNetwork 动词面的前置病灶（swarm overlay attachable flag-day）在 k3s **不存在**——Namespace 恒存在、无载体网络对象、无 attachable 概念。这是能力边界的语义性事实，不是实现排序缺口，**永不补齐**。
- k3s 不实现 RuntimeNetworkMaintenance（编译期断言维持缺席）；RebuildNetwork 动词在 k3s 集群上的诚实失败是**正确行为**（engine 侧既有降级语义，E_INTERNAL 信封）。
- ADR-0046 补录追记：动词是 swarm attachable flag-day 的修复原语；无载体网络对象的 Runtime 上语义性缺席（从"假想 k8s 未实现"升格为"k3s 定型裁决"）。
- Describe Notes 增一行声明（network rebuild verb has no carrier-network object to repair——semantically absent）；TestDescribeNotesHonesty 同步。

### 3. RuntimeHygiene = Secret 真扫 + PVC 诚实 no-op；附带收口 ensureSecrets 值轮换缺口

- **SweepOrphanSecrets（实现）**：跨全部 namespace 扫 `fleetly.managed=true` 的 Secret；现役引用集 = 同 namespace 内全部 **Deployment/DaemonSet 的 pod template + 独立 Pod 的 spec** 的 secret 引用面（卷 projected/secret、envFrom、valueFrom、imagePullSecrets）——pod 列表不充分（缩容到零的 Deployment 仍引用着 secret，是现役）。出生超宽限窗（1h，swarm 同款）才判孤儿。删除按 namespace/名字典序（确定性），maxDelete 限流，单体失败不中断，NotFound 视作已清。utility 材料 Secret（`fleetly.utility` 标签）不带 managed 标记，天然出局（且已有按标签的 per-run 清理）。
- **SweepOrphanVolumes（诚实 no-op，返回 0,nil）**：k8s 无 docker 匿名卷对应物（镜像 VOLUME 遗产面不存在——emptyDir 随 pod 生命周期自动回收）；PVC 是显式数据面（Remove 不删、场景 3 显式数据处置），"孤儿 PVC"判据（平台 Volume 行已删）超出 Provider 载体面视野——宁可漏扫不可误删。注释 + Notes 声明。
- **附带收口（咬出的缺口）**：ensureSecrets 从 create-only 升为 create-or-update——已存在且值不同即更新（材料值轮换面：域内密码轮换到达载体）。值相同跳过（幂等重放零写入，与 lastIssued 断路器文化同向）。
- 孤儿来源背景：k3s Remove/域收敛不删 Secret（收官批语义：材料与数据面不走域收敛）——域删除后 Secret 残留由本子面收口；swarm 侧 secret 版本化指纹的孤儿来源（值变=新名）在 k3s 不存在（mutable 单名），域残留是主孤儿面。

### 4. RBAC 最小权限（挂账 6）= Provider 自举专用 ServiceAccount，工作客户端换 SA token

- **形态**：`New()` 以给定 kubeconfig 为**自举身份**，同步 ensureRBAC（带界重试）——Namespace `fleetly-system` + ServiceAccount `fleetly-manager` + ClusterRole `fleetly-manager`（最小规则集，见下）+ ClusterRoleBinding + 长期 token Secret（`kubernetes.io/service-account-token` 型，controller 填充，轮询等 token 到位）；随后**工作客户端整体换为 SA token**，自举客户端即弃（进程内不再持有 admin 凭证）。
- **ClusterRole 规则集**（= Provider 全部动词面的精确清单，动词按现有调用面逐一点验）：

| 组.资源 | 动词 | 消费面 |
|---|---|---|
| core/namespaces | create, get | ensureNamespace、RBAC 自举 |
| core/nodes | get, list, update | 锚定扫描/label 写回、DescribeCluster、Admin（cordon）、utility 钉住、relay 节点枚举 |
| core/pods | create, delete, get, list, watch | 载体 Ensure/收敛/Remove、Watch 流、utility Pod、ExecTarget 解析 |
| core/pods/exec | create | ExecWorkload（SPDY） |
| core/pods/logs | get | RuntimeLogs、utility 日志跟随 |
| core/services | create, delete, get, list, update | Addressing Service |
| core/secrets | create, delete, deletecollection, get, list, update | 材料分发/值轮换、imagePullSecrets、utility 材料与清理、Hygiene |
| core/persistentvolumeclaims | create, get | Volume PVC |
| core/events | get, list | utility 失败诊断 |
| apps/deployments, apps/daemonsets | create, delete, get, list, update | 长运行/全局载体 |
| networking.k8s.io/networkpolicies | create, delete, get | egress 强隔离 |
| policy/pods/eviction | create | Drain（eviction API 尊重 PDB） |

- **自愈与升级路径**：ensureRBAC 幂等——对象在位且 ClusterRole 规则与期望一致即跳过写入（SA kubeconfig 直接起动的形态零 admin 需求）；规则漂移（平台升级改动词面）需要写权限，错误文本带可行动指引（"以管理 kubeconfig 重跑一次以收敛 ClusterRole"，runbook 记录）。token Secret 轮换 = 后续批（长期 token 的诚实边界入 Notes/runbook）。
- **e2e 锚**：`kubectl auth can-i --as=system:serviceaccount:fleetly-system:fleetly-manager` 三断言——在册动词 yes（create deployments）、越权动词 no（create clusterroles / delete namespaces）+ 全链在本断言下照常绿（充分性由 e2e 全链承载）。
- 与载体侧 automountServiceAccountToken=false 正交：SA 是 fleetlyd 的身份，不挂任何载体。

### 5. 两节点 Enrollment（挂账 5）= advertise 地址解析 + 专用 e2e 腿

- **Enrollment 修**：Command 的 server 地址从 kubeconfig 直通（127.0.0.1 形态）改为**控制面节点可达地址**——列 Node 取 control-plane 角色节点的 InternalIP（端口取 kubeconfig server 的端口）；解析失败回退 kubeconfig 原文（单节点形态语义不变）。Command 形态保持 `k3s agent --server <advertise> --token <token>`。k3s server 证书默认含节点 IP SAN（直连节点 IP 的 TLS 面成立，e2e 实证承载）。
- **AgentCommand 恒空**（决策 1 的溶解结论）——EnrollKit 契约"空 = Provider 无代理面"既有语义，零契约变更。
- **e2e 专用腿**（`e2e/dind-k3s-two-node.sh` + mise `e2e:k3s-tw` + CI job）：第二 dind 容器（fuse 形态同款四 apk + /dev/fuse + 代理注入）经 `fleetly nodes enroll` 材料原样执行 join → 双节点 Ready + 平台锚定断言 → 卷钉住/分布载体 → **worker 落点 pod 的 exec 全链**（集中形态跨节点实证：apiserver→kubelet 通道 + per-node 回环注册的路由正确性）→ 双节点 relay_online 断言。swarm 双腿（smoke/two-node 分立）同款组织。

### 6. Notes 诚实边界（能力发现面同步）

Describe Notes 变更四行：撤"exec subface is not implemented"；增 exec 集中形态声明（apiserver-native，worker 节点零平台代理面）；增 network rebuild 语义性缺席声明；hygiene 卷面 no-op 并入既有孤儿声明措辞。TestDescribeNotesHonesty 同批钉死新措辞。

## 后果

- engine/api/CLI/Console 面零改动（子面扩面经 FacesOf 自动生效；exec 会话消费链 ADR-0049 既有）。E_EXEC_UNSUPPORTED/E_NODE_AGENT_OFFLINE 语义不变（无 exec 面 Runtime 的受理拒绝 + agent 失联收口）。
- k3s Provider 文件面 +4（exec.go/relayagent.go/hygiene.go/rbac.go）；swarm Provider 零改动（回归底座不动）。
- 会话执行的节点身份语义：k3s 上"哪个节点承载 exec 会话"的答案是 apiserver（集中）；relay_online=true 的节点视图含义相应为"该节点 pod 的 exec 可服务"——与 swarm（节点侧代理在连）载体事实不同、平台语义等价（runbook 注记）。
- fleetlyd 在 k3s 集群的权限从 cluster-admin 收敛为单 ClusterRole（verbs 仍含 nodes update 与 pods delete——编排器语义所必需，非最小特权理论的"最小"，是最小职责的诚实清单）。
- 长期 SA token 不轮换（轮换 = 后续批）；RBAC 规则升级需 admin 重跑一次（runbook 操作序）。
- 挂账收口：ADR-0052 §9 的 1（Exec）、2（Hygiene）、5（两节点 kubeconfig 装载——溶解形态）、6（RBAC）四项闭；3/4/7/8/12 维持挂账。

## 验收锚

- [x] RBAC 自举：ensureRBAC 幂等收敛（SA/ClusterRole/binding/token 在位 + 规则一致时零写——TestEnsureRBACIdempotentZeroWrite）+ 规则集单测（表格逐行断言 TestDesiredClusterRoleTable）+ 工作客户端换 SA token（New 构造期换装，自举客户端即弃）+ e2e auth can-i 三断言（yes create deployments / no create clusterroles / no delete namespaces；dind-k3s.sh 6b 节，2026-10-08 本机 fuse 形态两轮全绿）
- [x] Exec 子面：编译期断言 + ExecTarget（label 快照解析/字典序确定性/无在跑实例哨兵 TestExecTarget*）+ ExecWorkload（argv/tty/resize/stdin-EOF/退出码管道经执行器接缝 fake——TestExecWorkloadSeam）+ 会话多路复用帧序（open→ack→stdout→exit / error 帧——TestAgentSessionFrameFlow/TestAgentSessionErrorFrame）+ 退出码还原走 v4 协议结构化 CodeExitError（e2e 咬出字符串解析形态不成立，exit 23 还原为 1 的实证后修复）+ Notes 撤缺席声明（TestDescribeNotesHonesty 断 NotContains "not implemented"）
- [x] e2e `e2e:k3s` 增 exec 节全绿（2026-10-08 本机 fuse 形态 `K3S E2E PASSED`：relay_online 回环注册 + 输出透传 + 退出码 23 透传 + shell 会话（显式 argv 自退出——PTY stdin EOF 不终止载体进程的诚实边界）+ exec.session_opened 事件）
- [x] e2e `e2e:k3s-tw`（两节点腿）全绿（2026-10-08 本机 fuse 形态 `K3S TWO-NODE E2E PASSED`：enroll 材料原样执行 join（advertise 地址断言——join 命令携带 manager IP 非 127.0.0.1）+ worker Ready + 双节点平台锚定 + 双节点 relay_online（集中形态）+ 卷钉住 worker 落点（pod 落 k3s-tw-w 断言）+ worker pod 跨节点 exec（输出 + 退出码 7））；CI e2e-k3s-tw job 常态化
- [x] Hygiene：SweepOrphanSecrets 判据单测（引用集含缩容到零的 Deployment template/宽限窗/字典序/maxDelete 预算/零预算——TestSweepOrphanSecrets*）+ SweepOrphanVolumes no-op + ensureSecrets 值轮换单测（值变更新/值同零写——TestEnsureSecretsRotatesValue）+ registry 凭证轮换同面（TestEnsureImagePullSecretsRotatesValue）
- [x] NetMaintenance 定型：不实现接口的编译期断言维持（provider.go 断言块注释）+ Notes 声明（"network rebuild verb is semantically absent"——TestDescribeNotesHonesty 锚）+ ADR-0046 追记（2026-10-08 永久语义性缺席定型段）
- [x] 全门禁：`mise run test` 三 module + `mise run lint`（golangci 0 issues + buf breaking 过）+ `go test -count=1 ./internal/guards/`（含双腿 k3s 钉版一致守卫扩展）+ `generate:verify` + `console:verify` 全绿；swarm 全套零回归（providers/swarm 零代码改动）
- [x] ADR-0052 §9 挂账 1/2/5/6 划线注日期（2026-10-08）；checklist F4.1 条目补本批实录
