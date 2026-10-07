# Runbook: k3s Runtime 操作序

k3s 是第二运行时（ADR-0052 试点 → ADR-0054 资格认定 production-ready，config 缺省仍 swarm）。本文件承载 k3s 集群的装机、升级与运维操作序；swarm 现役环境见 `staging-fleetly.md`。

## 装机序（opt-in 形态）

1. 节点就绪：全部节点安装 k3s（server 起动建议 `--disable=traefik --disable=servicelb`——与平台受管 Proxy 争 80/443 hostPort）；worker 经 `k3s agent --server <控制面节点 IP> --token <node-token>` 加入（token 在 server 的 `/var/lib/rancher/k3s/server/node-token`；`fleetly nodes enroll` 材料即此命令，advertise 地址已解析为控制面节点 IP——ADR-0053 决策 5）。
2. fleetlyd 与 k3s server 同机（控制面单机假设，ADR-0019 同款合法形态）；config 写 `runtime.provider: k3s`（缺省 kubeconfig `/etc/rancher/k3s/k3s.yaml`，可用 `runtime.k3s.kubeconfig` 覆写）。
3. 起动即自举 RBAC（fleetly-system/fleetly-manager SA + 最小 ClusterRole），随后工作客户端换 SA token、弃自举凭证（ADR-0053 决策 4）。

## RBAC 规则升级序（单向门）

平台升级若扩展 Provider 动词面（ClusterRole 规则集与在位不一致），SA 自身无权改写——fleetlyd 起动报 RBAC 收敛错误。处置：以 admin kubeconfig（k3s.yaml 原文）为自举身份重跑一次 fleetlyd（环境变量或临时 config 指回 admin kubeconfig），收敛完成后恢复 SA 形态。ensureRBAC 幂等：对象在位且规则一致即零写。

## 长期 SA token 边界

fleetly-manager 的 token 是长期 Secret（`kubernetes.io/service-account-token` 型），**不自动轮换**（后续批）。泄漏处置 = 删 token Secret 重建（controller 重发新 token）+ 以 admin kubeconfig 重跑一次 fleetlyd 取新 token；k3s node-token 的轮换需 server 重启介入（Enrollment rotate 诚实失败的对应面）。

## relay_online 的载体事实（exec 集中形态）

k3s 上 exec 经 apiserver 原生通道（SPDY→kubelet），**无节点侧平台代理**——relay agent 是 manager 侧 per-node 回环注册（每节点一条到自身 gateway 的 WS 连接，hello 帧携带 k8s 节点名）。因此：

- `relay_online=true` 的语义 = "该节点上 pod 的 exec 可服务"（回环连接在 + 节点在锚定表），与 swarm（节点侧代理在连）载体事实不同、平台语义等价。
- 节点失联的会话收口由 engine dropAgentConn 承载（回环连接随 relay 循环节拍消亡）。

## 网络隔离模型（ADR-0054 决策 1）

- 项目 Namespace 内按**网络附件集**隔离：pod 带 `fleetly.net.<k>` 成员资格 label，每网一条入站 policy（同网成员 + fleetly-system + 已批准跨项目引用放行）；零附件载体入站全拒；hostPort 发布载体豁免（节点级可达语义）。
- egress:none = 载体级强隔离（出站仅同 ns + DNS）。非 egress 载体出站不限——跨域隔离由目标侧入站 policy 收口。
- 排障：`k3s kubectl get netpol -A`（fleetly-netisolate-* / fleetly-peer-* / fleetly-egress-deny）；`kubectl get pods --show-labels` 查成员资格。
- 诚实边界两行：项目域 pod 直连系统域载体维持全通（弱于 swarm）；撤销 peer 的隔离生效时点 = 挂靠方下一次 isolate Ensure 完成（engine 隔离环 + 漂移扫描兜底）。

## 旧 Runtime 孤儿载体处置（场景 3 迁移后，ADR-0054 决策 3）

跨 Runtime 迁移后旧集群载体对平台**失明**（k3s 模式无 docker 连接面）——平台不自动搬也不自动删。迁移完成、新集群验收后的运维处置序：

1. 旧集群排空：`docker node ls` 核对；逐服务 `docker service rm <fleetly-...>`（按 `fleetly.ns.project` label 过滤枚举：`docker service ls --filter label=fleetly.managed=true`）。
2. 旧卷处置（显式数据处置）：确认 Backup 对象已在新集群 Restore 验收后，`docker volume rm fleetly-vol-*`。
3. 旧节点退役：`docker node rm`（先 drain）；平台侧节点行已是退役态（节点 ID 永不复用）。

平台侧无登记面是裁决本体（静态登记立即陈旧；跨 Runtime 双连接是为一次性窗口引入常驻面）——本节即诚实形态。

## 升级与钉版

- k3s 版本平台常量单源（`internal/providers/k3s` 的 k3sVersion；e2e 下载段与守卫 TestK3sPinConstantAndE2EAgree 同 commit 一致）。
- fleetlyd 升级 = 常规平台升级序（Backup 前置不变）；载体 pod 模板 label/policy 变更随 Ensure 收敛滚动。
