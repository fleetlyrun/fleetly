# ADR-0055: staging k3s 专用环境裁决与生产形态实证批（挂账 12、grant 残留清扫、SA token 定型）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-08 | ADR-0052（k3s 试点，§9 挂账 7/12；决策 1"staging k3s 专用环境若建，另批裁决"——本批授权面）、ADR-0053（决策 4 的 SA token 轮换留口）、ADR-0054（决策 1 grant 残留诚实边界 + 决策 2 缺省翻转前置三条件——本批推进①③）、`docs/runbooks/k3s-runtime.md`（装机序/孤儿处置承载面）、`docs/runbooks/staging-fleetly.md`（现役 swarm 操作真源） |

## 背景

ADR-0054 决策 2 记录的缺省翻转前置三条件：①首个生产形态 k3s 集群实跑一段（覆盖大档 hostPath 通道——即 ADR-0052 §9 挂账 12）；②多 server HA 裁决收口；③迁移序与 flag-day runbook 落地。e2e dind 是唯一验证场的形态下①永不满足（dind 的 mount 命名空间遮蔽使大档 hostPath 恢复输入不可实证，挂账 12 原文即以"随首个生产形态部署实证"为触发）。本批裁决 staging k3s 专用环境形态，并随环境收口三个留口：grant 残留卫生清扫（ADR-0054 决策 1）、SA token 轮换面（ADR-0053 决策 4）、真机迁移序 runbook（前置③）。

staging 实测事实（2026-10-08，裁决依据）：

1. **node2（143.198.234.68 / VPC 10.124.0.5）的受管发布面被 swarm 全占**：80/443/5000/8428/9428 由 docker-proxy（swarm routing mesh 对 manager 受管域的发布）监听、8080 由 cadvisor host 直绑——共 6 端口。k3s 形态 fleetlyd 的受管域（Proxy 80/443、zot 5000、VL 9428、VM 8428、cadvisor 8080）必须同端口宿主发布（ADR-0052 决策 5）——**候选 (a) 共存形态在受管发布面上结构性互斥**，除非阉割受管栈（即阉割生产形态，本批目的自毁）。6443/10250 虽空闲，但孤立可让不构成共存成立。
2. **node2 现役 swarm 负载可整体迁离**：fleetly 任务全是小容器（7-90MB 级实测），受管大件（pg/zot/traefik/VL/VM）全钉 manager；drain 吸收后 manager 余量 ~700-900MB（4GB 无 swap）。另有手工服务 `sec-test`（postgres:17-alpine 五分钟循环，非 fleetly 管理——**并发使用在案的活跃面**）——drain 将其迁 manager 继续跑，非破坏。
3. **node2 具备生产形态节点条件**：Debian 13.7 / x86_64 / systemd / 公网出站可达（github 200）/ 2C/2GB/49GB 空盘——k3s server（sqlite 单机）+ fleetlyd + 受管栈 + 验证负载峰值 ~1.4GB 可承载；原生文件系统（overlayfs 默认 snapshotter 直接可用——挂账 9 的生产形态分支）。
4. **本机 VM/全新节点不可得**：无新硬件；WSL2 VM 非"生产形态"（翻转前置①要的是生产形实跑记录，笔记本 VM 无凭据力）。候选 (c) 否决。
5. 候选 (d)（推迟）= 翻转前置①③继续悬置、挂账 12 永不触发——环境事实已排除 (a)/(c) 而 (b) 成本可控，推迟无正当性。

## 决策

### 1. staging k3s 专用环境 = node2 退出 swarm 改纯 k3s（候选 b）

- **形态**：node2 `docker swarm leave`（manager 侧先 drain——dogfooding 负载滚动迁 manager，torchwood-pg 等受管大件钉 manager 不受扰）→ manager `docker node rm`（节点行退役，平台侧旧节点 ID 永不复用）→ k3s server 直跑 systemd（钉版 v1.36.5+k3s1，平台常量单源；`--disable=traefik --disable=servicelb`；原生 fs 用默认 overlayfs snapshotter——不带 snapshotter 旗标，挂账 9 生产分支）→ fleetlyd 同机 systemd（config `runtime.provider: k3s`）。**staging 从此双平台分立**：manager = swarm 形态现役 dogfooding（torchwood/messageloop，零扰动红线），node2 = k3s 形态生产实证环境（独立数据根/独立平台身份）。
- **部署面自举依赖（e2e dind-k3s.sh 起动段 + staging 现役对照）**：fleetlyd 自举零外部依赖——控制面 sqlite（数据根内）+ ObjectStore local provider（数据根内）+ 受管 zot（集群内 hostPort 5000）。postgres/外部 objectstore 均非依赖。**from_build 构建链不在本环境实证面**：kubelet 从受管 zot（HTTP 明文 registry）拉镜像需节点 containerd hosts.toml 配置面，e2e k3s 腿同样未覆盖（镜像全走公网 digest）——诚实边界记档，后续批裁决。
- **资源分立**：node2 的 k3s 平台与 manager 的 swarm 平台零共享（独立数据根、独立受管域、独立端口面——node2 上 swarm mesh 让位后 80/443/5000/8428/9428/8080 全部归 k3s 受管域）。node2 的 docker daemon 在 swarm 退出后停用（`systemctl stop docker docker.socket`——释放内存且防旧 mesh 监听复活；回滚序内重启）。
- **并发使用处置**：sec-test 等手工 swarm 载体随 drain 迁 manager 继续运行（平台外负载，迁移非删除）；runbook 记档。
- **回滚序（可逆性）**：k3s 侧 `k3s-killall.sh && k3s-uninstall.sh` → `systemctl start docker docker.socket` → manager `docker swarm join-token worker` 取材料 → node2 `docker swarm join`（平台侧铸新节点行，旧 ID 退役——ID 永不复用语义）。全程无数据丢失面（k3s 平台数据根独立，swarm 卷不动）。
- **挂账 7（多 server HA）维持**：本环境单节点 k3s，不构成多 server 触发条件（ADR-0052 §9 原文）；HA 裁决仍待独立批。
- staging manager 的 swarm 现役操作序（升级/备份/走查）**不受本裁决影响**——`staging-fleetly.md` 追记 node2 改造注记与回滚指针。

### 2. 挂账 12 随环境收口：大档 hostPath 恢复通道生产实证

- **通道设计（ADR-0052 决策 4 原设计）**：Database Backup 的 dump 对象在恢复期作为 Utility Pod 的输入——小档（≤900KB 阈值）走 Secret 投影（dind 实证过），大档走 hostPath（控制面节点 `/var/lib/fleetly/utility/` 暂存 → Utility Pod 挂载）——dind 的 mount 命名空间遮蔽使后者不可实证（挂账 12 原文）。
- **实证序（staging k3s）**：建 postgres 库 → 种子 ~5-10MB 数据（dump 稳超小档阈值）→ Backup succeeded（断言对象 size/digest）→ 新库 `--restore-from-backup`（空卷起家）→ 行数/内容闭环断言 + Utility Pod hostPath 挂载形态断言（kubectl 投影卷面）。原生 fs 无遮蔽——通道回归原设计。
- 断言锚随实录落 runbook（k3s-runtime.md staging 实录节）；ADR-0052 §9 挂账 12 划线。

### 3. grant 残留卫生清扫 = RuntimeHygiene 第三面 SweepOrphanPeerGrants（ADR-0054 留口兑现）

- **残留面（代码事实）**：peer grant policy 落接收方 ns、由声明方 Ensure 持有（owner/domain 双 label 键控）；声明方 App 删除（Remove 只拆本 ns 非 grant netpol——grant 在他 ns 不随走）或项目删除（ns 拆除）后，该 (owner, domain) 再无 Ensure 拍——grant 永残留为指向空集的 no-op。
- **判据（三信号全满足才删，宁可漏扫不可误删——与 Secret/卷清扫同纪律）**：
  1. **owner ns 无活 pod 持有该 grant 选择器 key**（pod 对象任意相位均计——CrashLoop/终止中也在场，滚动窗安全）；
  2. **owner ns 无该 key 的成员 policy**（声明方意图的 Provider 可见锚：成员 policy 与 grant 同拍同源创建（policy 先于 grant）、仅由声明方 Remove/Ensure 收敛删除——在场即意图可能仍活，保留）；
  3. owner ns 不存在时 ①② 平凡成立（项目删除形态）。
  无出生宽限窗：出生竞态由信号②覆盖（grant 创建拍成员 policy 先落），信号是结构性的而非 eventual。删除前对成员 policy 复核一次（TOCTOU 收窄——与并发声明方 Ensure 的微秒窗残余，删除方向恒为"更早拒绝"安全侧，自愈面 = 声明方下一次部署重铸 grant；诚实边界记档）。
- **接入面**：`capability.RuntimeHygiene` 增第三方法 `SweepOrphanPeerGrants`（k3s 实现 / swarm 诚实 no-op——docker 无 grant 载体对象）；engine 透传 `SweepOrphanPeerGrantCarriers`；retention janitor 节拍（10min）带预算调用；RBAC 动词面零增（networkpolicies list/delete 既有）。
- **e2e 锚**：declare→approve→挂靠部署（grant 在场）→删挂靠方项目→janitor 拍后 grant 消失（接收方 ns 断言）。

### 4. SA token 轮换面 = runbook 承载维持（否决自动化）

- **否决 Provider 自动检测/自愈**：token 失效自愈要求 fleetlyd 在运行期保留 admin 自举凭证（违反 ADR-0053 决策 4"自举客户端即弃"的裁决本体）；且长期 token 泄漏是管理员处置事件，不是平台自愈面。失效的表现形态（watch/API 401 日志风暴）已可观测，runbook 的重建序（删 token Secret → controller 重发 → admin kubeconfig 重跑一次 fleetlyd 取新 token）步骤闭合。
- k3s node-token 轮换需 server 重启介入——Enrollment rotate 诚实失败的既有口径维持（ADR-0053 决策 1）。
- `k3s-runtime.md` 长期 SA token 边界节维持现状，本决策为其定稿背书。

### 5. 缺省翻转前置推进：①实跑记录 + ③迁移序 runbook（②HA 维持）

- **前置①起点**：staging k3s 环境落地即开积累——runbook k3s-runtime.md 增"生产形态实跑记录"节（逐批追加：部署/db backup-restore/egress 隔离/route/exec/hygiene 的真机断言锚与日期）；**前置①的"实跑一段"语义 = 跨批持续记录，非单批一次性动作**。
- **前置③兑现**：k3s-runtime.md 增"场景 3 真机迁移序"节——以 `e2e/dind-runtimeswitch.sh` 两段式为蓝本写真机形态：Platform Backup 前置（硬停分支）→ 旧 Runtime 数据面 Backup → 停旧 fleetlyd → 显式载体处置（label 过滤枚举）→ k3s 装机（装机序节）→ `runtime.provider=k3s` 起（跨机形态经 Platform Backup 重放迁数据根）→ 身份保持断言 → 基线重放 → 旧库显式处置 + restore 数据闭环 → 旧 Runtime 孤儿处置（引用本文件既有节）→ 回滚序。placement 不跨 Runtime 复用、节点 ID 永不复用（e2e 同锚）。
- **前置②（多 server HA）不随本批**：单节点环境不构成触发条件（挂账 7 维持，决策 1 同段）。

## 后果

- staging 拓扑变更：node2 从 swarm worker 改 k3s 生产实证节点（manager swarm 单节点化——dogfooding 现役负载经 drain 全迁 manager，可用性经滚动维持）；`staging-fleetly.md` 拓扑节与端口矩阵追记。
- capability 契约 +1 方法（RuntimeHygiene 三面）；swarm 零行为变化（no-op + 注释）；k3s Provider hygiene.go 增扫描；engine/assembly 透传面各 +1。RBAC 规则表零变化。
- e2e k3s 腿增 grant hygiene 锚（janitor 节拍窗 ~10min——断言带界轮询）；三腿回归零漂移。
- 挂账收口：ADR-0052 §9 的 12 划线（生产实证后）；ADR-0054 决策 1 的 grant 残留边界兑现注记。挂账 7 维持（触发条件不变）。
- 环境成本在案：manager 内存余量收窄至 ~700-900MB（drain 吸收后）、无 swap——后续 staging 换装批的容量警觉项记 runbook。
- from_build → 受管 zot 拉取在 k3s 的 containerd hosts.toml 面记档（本批不裁决、不实证）。

## 验收锚

- [ ] ADR-0055 裁决落档 + ADR-0052 §9 挂账 12 划线注日期 + ADR-0054 决策 1 grant 残留留口兑现注记
- [ ] SweepOrphanPeerGrants 单测：判据矩阵（owner ns 缺失删/活 pod 持 key 保留/成员 policy 在场保留/双缺删/删除前复核/字典序+预算/单体失败不中断/零预算——TestSweepOrphanPeerGrants*）+ engine 透传锚 + swarm no-op 锚
- [ ] e2e k3s 腿 grant hygiene 锚全绿（declare→approve→grant 在场→删挂靠方项目→janitor 拍后 grant 收敛消失）+ 三腿回归零漂移
- [ ] staging k3s 环境落地实录：node2 drain+leave 零丢失（sec-test 迁 manager 续跑、dogfooding 滚动迁移无断流）、k3s+fleetlyd systemd 上线、doctor 零 fail（暴露面自证）
- [ ] 挂账 12 生产实证：大档（>900KB）Backup → 新库 restore hostPath 通道数据闭环断言（原生 fs 默认 snapshotter 形态）
- [ ] 生产形态实跑记录节首录（deploy/route/egress/exec/db backup-restore/hygiene 真机锚）+ 场景 3 真机迁移序 runbook 落档（前置③）
- [ ] 全门禁：`mise run test` + `mise run lint` + `go test -count=1 ./internal/guards/` + `generate:verify` + `console:verify`；swarm 全套零回归
