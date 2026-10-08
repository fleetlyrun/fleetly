# Runbook: k3s Runtime 操作序

k3s 是第二运行时（ADR-0052 试点 → ADR-0054 资格认定 production-ready，config 缺省仍 swarm）。本文件承载 k3s 集群的装机、升级与运维操作序；swarm 现役环境见 `staging-fleetly.md`。

## 装机序（opt-in 形态）

1. 节点就绪：全部节点安装 k3s（server 起动建议 `--disable=traefik --disable=servicelb`——与平台受管 Proxy 争 80/443 hostPort）；worker 经 `k3s agent --server <控制面节点 IP> --token <node-token>` 加入（token 在 server 的 `/var/lib/rancher/k3s/server/node-token`；`fleetly nodes enroll` 材料即此命令，advertise 地址已解析为控制面节点 IP——ADR-0053 决策 5）。
2. **明文 registry 信任面（ADR-0056 决策 1，全部节点）**：装机时落 `/etc/rancher/k3s/registries.yaml`（k3s 起动期渲染 containerd hosts.toml；变更需重启 k3s）——mirror 键 = 受管仓库地址（与 fleetlyd 的 `registry.addr` 同源）：

   ```yaml
   mirrors:
     "<registryAddr>":
       endpoint:
         - "http://<registryAddr>"
   ```

   分层边界：节点级只承载传输信任（明文可达）；**认证面恒走 pod 级 imagePullSecrets**（per-Project 凭证既有链）——registries.yaml 不配 auth（节点级 auth 是全域共享凭证，会击穿 per-Project 域隔离）。
3. **控制面节点构建面（ADR-0056 决策 2）**：构建恒在控制面节点本地 daemon（ADR-0019）——控制面节点需跑 docker daemon **仅作构建面**（不 init swarm），配 `--insecure-registry <registryAddr>`（`/etc/docker/daemon.json` 的 insecure-registries，install.sh drop-in 同款；k3s 腿不经 install.sh 的装机复刻此 drop-in）。worker 节点不需要。
4. fleetlyd 与 k3s server 同机（控制面单机假设，ADR-0019 同款合法形态）；config 写 `runtime.provider: k3s`（缺省 kubeconfig `/etc/rancher/k3s/k3s.yaml`，可用 `runtime.k3s.kubeconfig` 覆写）。
5. 起动即自举 RBAC（fleetly-system/fleetly-manager SA + 最小 ClusterRole），随后工作客户端换 SA token、弃自举凭证（ADR-0053 决策 4）。

## 多 server HA（embedded etcd，ADR-0056 决策 3）

**支持形态**（e2e `e2e:k3s-ha` 四容器腿实证）：首节点 `k3s server --cluster-init` → 其余 server `k3s server --server https://<首节点>:6443 --token <node-token>` 加入（node-token 同 worker join 同源；奇数成员 quorum，3/5/…）。worker 照常经 `fleetly nodes enroll` 加入。

- **语义 = Runtime 面高可用**：单 server 失效时集群读写保持（quorum 内）、worker 载体零滚动（kubelet autonomy——与 apiserver 失联不杀容器；NotReady taint 的默认驱逐容忍窗 300s，失效窗超此即进入灾难恢复形态）。
- **fleetlyd 单实例诚实边界**：fleetlyd（控制面 sqlite + 数据根）不在 k3s HA 覆盖内——apiserver 失效对它是断连重连，fleetlyd 自身高可用是独立话题（触发条件：首个要求控制面无停机窗的真实部署）。
- **无 LB 诚实边界**：fleetlyd kubeconfig 与 worker join 各钉单 apiserver 端点——该 server 长死则控制面不可达直至恢复。多 apiserver 前置 LB/DNS 多记录是部署形态选择（k3s 官方建议），按需实施。
- **server 扩缩容 = 装机级手工序，不经平台 Enrollment**（etcd 成员变更是 quorum 风险面；与孤儿处置同文化：一次性动作不进常驻 API 面）。缩容注意：etcd 死成员残留不自动清理，quorum 按成员总数计——**缩容必须先 etcd member remove 再停机**，否则 quorum 永久受损（`k3s etcd-member-list` 核对）。

## RBAC 规则升级序（单向门）

平台升级若扩展 Provider 动词面（ClusterRole 规则集与在位不一致），SA 自身无权改写——fleetlyd 起动报 RBAC 收敛错误。处置：以 admin kubeconfig（k3s.yaml 原文）为自举身份重跑一次 fleetlyd（环境变量或临时 config 指回 admin kubeconfig），收敛完成后恢复 SA 形态。ensureRBAC 幂等：对象在位且规则一致即零写。

## 长期 SA token 边界

fleetly-manager 的 token 是长期 Secret（`kubernetes.io/service-account-token` 型），**不自动轮换**（后续批）。泄漏处置 = 删 token Secret 重建（controller 重发新 token）+ 以 admin kubeconfig 重跑一次 fleetlyd 取新 token；k3s node-token 的轮换需 server 重启介入（Enrollment rotate 诚实失败的对应面）。

## relay_online 的载体事实（exec 集中形态）

k3s 上 exec 经 apiserver 原生通道（SPDY→kubelet），**无节点侧平台代理**——relay agent 是 manager 侧 per-node 回环注册（每节点一条到自身 gateway 的 WS 连接，hello 帧携带 k8s 节点名）。因此：

- `relay_online=true` 的语义 = "该节点上 pod 的 exec 可服务"（回环连接在 + 节点在锚定表），与 swarm（节点侧代理在连）载体事实不同、平台语义等价。
- 节点失联的会话收口由 engine dropAgentConn 承载（回环连接随 relay 循环节拍消亡）。

## 场景 3 真机迁移序（swarm → k3s，ADR-0055 决策 5 / 缺省翻转前置③）

以 `e2e/dind-runtimeswitch.sh` 两段式为蓝本的真机形态（同机两段或跨机均可；跨机时第 5 步经 Platform Backup 重放迁数据根）。**placement 绑定不跨 Runtime 复用、节点 ID 永不复用**：k3s 集群节点是新节点（新铸平台节点 ID），旧 swarm 节点行退役非删除。

1. **Platform Backup 前置（硬停分支——备份没成 = 不得动手）**：旧平台 `fleetly platform backup` + `fleetly platform backups` 核对在场。
2. **数据面 Backup**：逐数据库 `fleetly databases backup <id>` 至 succeeded（对象随 ObjectStore 配置走；local provider 时在数据根内——跨机迁移优先评估 s3 五元组）。
3. **停旧 fleetlyd**：`systemctl stop fleetlyd`（30s 排水窗）。
4. **显式载体处置**（平台失明即显式处置，决策 3 同判）：`docker service ls --filter label=fleetly.managed=true` 枚举 → 逐个 `docker service rm`；**卷与备份保留**（数据兜底）。
5. **k3s 就绪**：按装机序节安装 k3s server（控制面单机）+ fleetlyd（config `runtime.provider: k3s`）；跨机形态先在旧机 `fleetly platform backup` 后于新机恢复数据根（restic restore，`staging-fleetly.md` 失败回滚路径同款序）。
6. **身份保持断言**：Project/App/Revision/Route ID 与切换前逐位一致（`fleetly --json` 对照）。
7. **基线重放**：App 载体经 drift 基线重放在 k3s 重建（`kubectl get pods -A -l fleetly.managed=true` 观察，ns 名 = `fleetly-<projectID 小写>`）。
8. **数据库闭环**：旧 Database 行显式处置（`fleetly databases delete <id>`，幂等）→ `fleetly databases create --restore-from-backup <backupID>` 新库 → 行数/内容断言（恢复是异步任务，等 `database.restore_*` 收口）。
9. **旧 Runtime 孤儿处置**：按本文件"旧 Runtime 孤儿载体处置"节收尾（排空确认/卷处置/节点退役）。
10. **回滚序**：迁移窗内失败 = 回到第 3 步前状态：k3s 侧停 fleetlyd（k3s 可留待重试），旧 fleetlyd 起回（数据根未动）、旧载体由平台 reconcile 重建——**goose 只前滚的约束仅在第 5 步换了二进制后成立，窗内回滚零迁移面**。

## staging k3s 生产实证环境（ADR-0055 决策 1）

staging node2（143.198.234.68 / VPC 10.124.0.5）是 k3s 形态生产实证环境（与 manager 的 swarm dogfooding 分立）：装机实录、生产形态实跑记录与挂账 12 实证见文末实录节。回滚序（还原 node2 为 swarm worker）：k3s 卸载 → 重启 docker → manager 取 `docker swarm join-token worker` → node2 join（平台铸新节点行，旧 ID 退役）。

## 网络隔离模型（ADR-0054 决策 1）

- 项目 Namespace 内按**网络附件集**隔离：pod 带 `fleetly.net.<k>` 成员资格 label，每网一条入站 policy（同网成员 + fleetly-system + 已批准跨项目引用放行）；零附件载体入站全拒；hostPort 发布载体豁免（节点级可达语义）。
- egress:none = 载体级强隔离（出站仅同 ns + DNS）。非 egress 载体出站不限——跨域隔离由目标侧入站 policy 收口。
- 排障：`k3s kubectl get netpol -A`（fleetly-netisolate-* / fleetly-peer-* / fleetly-egress-deny）；`kubectl get pods --show-labels` 查成员资格。
- 诚实边界两行：项目域 pod 直连系统域载体维持全通（弱于 swarm）；撤销 peer 的隔离生效时点 = 挂靠方下一次 isolate Ensure 完成（engine 隔离环 + 漂移扫描兜底）。
- 残留 grant（声明方项目/App 消亡后接收方 ns 的 no-op policy）由 RuntimeHygiene 周期清扫（ADR-0055 决策 3）：owner ns 无活 pod 持 key 且无该 key 成员 policy 即删。
- 空域收尾（ADR-0056 决策 5）：项目删除后，最后拆除的域在 Remove 尾部判空（ns 内零活 fleetly 载体 + 零 PVC）即删除项目 Namespace（ns 删除级联带走域内一切，含挂靠方遗留 grant）。**有 PVC 在场即不拆**——卷是数据兜底（swarm 形态项目删除后 `fleetly-vol-*` 残留同款文化）；空 ns + 卷的残留处置序：确认数据不要 → `kubectl delete pvc -n <ns> --all` → `kubectl delete ns <ns>`（下次任一域 Remove 也会自动收尾）。

## 旧 Runtime 孤儿载体处置（场景 3 迁移后，ADR-0054 决策 3）

跨 Runtime 迁移后旧集群载体对平台**失明**（k3s 模式无 docker 连接面）——平台不自动搬也不自动删。迁移完成、新集群验收后的运维处置序：

1. 旧集群排空：`docker node ls` 核对；逐服务 `docker service rm <fleetly-...>`（按 `fleetly.ns.project` label 过滤枚举：`docker service ls --filter label=fleetly.managed=true`）。
2. 旧卷处置（显式数据处置）：确认 Backup 对象已在新集群 Restore 验收后，`docker volume rm fleetly-vol-*`。
3. 旧节点退役：`docker node rm`（先 drain）；平台侧节点行已是退役态（节点 ID 永不复用）。

平台侧无登记面是裁决本体（静态登记立即陈旧；跨 Runtime 双连接是为一次性窗口引入常驻面）——本节即诚实形态。

## 升级与钉版

- k3s 版本平台常量单源（`internal/providers/k3s` 的 k3sVersion；e2e 下载段与守卫 TestK3sPinConstantAndE2EAgree 同 commit 一致）。
- fleetlyd 升级 = 常规平台升级序（Backup 前置不变）；载体 pod 模板 label/policy 变更随 Ensure 收敛滚动。

## staging node2 装机实录（2026-10-08，ADR-0055 决策 1 兑现）

- **前置**：manager `docker node update --availability drain fleetly-node2`（20s 排空，dogfooding 滚动迁 manager 无断流；sec-test 等手工载体续跑 manager）→ node2 `docker swarm leave` → manager `docker node rm` → node2 `systemctl stop/disable docker docker.socket`（释放 ~150MB + 防 mesh 监听复活；回滚序内重启）。
- **k3s**：sha256 验过的钉版二进制直放 `/usr/local/bin/k3s` + 手写 `k3s.service`（`--disable=traefik --disable=servicelb`；原生 fs 无 snapshotter 旗标 = 默认 overlayfs）——52s ready、CNI ~40s、受管镜像在线拉（生产形态，无 airgap 预载）。
- **fleetlyd**：systemd `fleetlyd.service` env 组（provider/kubeconfig/绑面三键钉 VPC 10.124.0.5/Proxy 端点/registry+logging+metrics 三址）+ restic 0.19.1 先装（install.sh 4d 节钉版）。CLI 凭据 `FLEETLY_ADDR=10.124.0.5:9080`。
- **doctor 零 fail 形态**：带 daemon 同组 env 跑（manager 先例）——`FLEETLY_RUNTIME_PROVIDER=k3s` 跳过本地 docker 探针、绑面三键让端口探测打生效地址。
- node2 内存 2GB 峰值 ~1.5GB（k3s server + fleetlyd + 受管五域 + 验证负载）——无 swap，扩负载前留意。

## 生产形态实跑记录（缺省翻转前置①的累积面，逐批追加）

| 日期 | 面 | 断言锚 | 结果 |
|---|---|---|---|
| 2026-10-08 | 装机+自举 | k3s 52s ready；RBAC 自举（faces=logs,admin,inspector,hygiene,utility,exec）；doctor 8 ok/0 fail（暴露面自证：三面钉 VPC listening non-public） | ✅ |
| 2026-10-08 | deploy | `deploy --image nginx:1.27` → succeeded；载体 pod 1/1（label 定位） | ✅ |
| 2026-10-08 | Route | sslip 域 + tls none：node2 本机 curl 200 + **工作站经公网 IP 直探 200**（hostPort 80 生产形态外测） | ✅ |
| 2026-10-08 | exec | relay_online（集中形态）+ 输出透传 + 退出码 23 透传 | ✅ |
| 2026-10-08 | egress/成员隔离 | DNS 放行 + 跨 ns 拒 + 同 ns 异网拒（成员 policy 表在场）——kube-router 拒绝形态 = Connection refused（非超时） | ✅ |
| 2026-10-08 | db backup/restore | 6000 行随机种子 → backup succeeded → 新库 restore 行数 + md5 checksum 逐位一致 | ✅ |
| 2026-10-08 | **大档 hostPath（挂账 12）** | 60k 行（dump 1,749,351B > 900KB 阈值）→ restore utility pod 输入卷 = **hostPath**（`/var/lib/fleetly/utility/restore-*`，jsonpath 实证）→ 60,000 行 + checksum 逐位一致——原生 fs 无 dind mount 遮蔽 | ✅ |
| 2026-10-08 | grant hygiene | declare→approve→挂靠部署（grant 在场）→删声明方 App+项目→retention janitor 拍后 grant 收敛消失 | ✅ |

### 实施期咬出的修复链（全在仓，2026-10-08 staging 实录）

1. **SkipMaterials k3s 缺席**（3e1c265）：受管 cadvisor 的材料 projected 卷要在 /run/secrets 建挂载点，与只读 hostPath 绑定冲突 → runc EROFS 起容器炸；swarm translate 既有判据，k3s secretFilesOf 补齐。e2e 受管域断言同步扩五域全 Running（此前只等 traefik——cadvisor 同病在 dind 无人看）。
2. **RBAC `pods/logs` 单复数 typo**（84eeea4）：`pods/logs` 不命中任何子资源 → log collector 全程 403；can-i 断言补 `get pods/log=yes`（e2e 6b 节第四断言）。
3. **doctor 两假红/假警**（5bfecc9）：k3s 形态 docker 探针恒红（env 门控跳过）+ 端口探测打 127.0.0.1 假警（生效绑址探测 + 三键 env 兜底）。
4. **备份链 CNI 准入竞态**（7978b27 终判）：utility pod 是秒级一次性载体，kube-router 对新 pod 的成员 label ipset 准入传播在本集群实测分钟级（t=0 拒/t=75s 通）→ pg_dump 4/4 拒连。**修 = 工具 Pod hostNetwork + ClusterFirstWithHostNet**：连接以节点本机源出发，过目标 pod per-pod FW 链 src-type LOCAL 无条件放行（kube-router 标准），与 CNI 传播解耦；utility peer 放行路线被证伪（FROM podSelector 一律 ipset 源集，同样有传播迟滞）。
5. **putNetpol create-only 锁死旧形态**（01447b8）：policy 形状漂移（平台升级改放行集）在存量集群永不落地；升 create-or-update（相等零写）。

### 诚实边界（本环境实证补录）

- **CNI 准入传播窗**：kube-router 对新建 pod 的成员 label ipset 准入在繁忙集群可达分钟级（本集群实测 ~75s；e2e 新鲜集群秒级故未咬出）——长命载体的入站放行窗内不可达会自愈，一次性短命载体必须走 hostNetwork（备份链已修）；后续若再引入秒级工具载体，同款形态是唯一安全面。
- from_build → 受管 zot（HTTP 明文 registry）拉取需节点 containerd hosts.toml 配置面——本环境未实证（ADR-0055 决策 1 记档，后续批裁决）。〔2026-10-08 随 ADR-0056 收口：拉取面 = registries.yaml 装机序承载 + 构建面 = 控制面 docker daemon（insecure-registry drop-in）；staging 实录见文末记录表。〕
- 受管 traefik 证书面/ACME 未在本环境演练（route 全 tls none 明文形态）。

- ~~项目删除不拆 Namespace（k3s Provider 只建不删——ensureNamespace 单向；空 ns Active 残留是已知形态，cosmetic，后续批可随卫生清扫收口）~~（2026-10-08 随 ADR-0056 决策 5 收口：域 Remove 尾部空域收尾——零活载体 + 零 PVC 即删 ns；有 PVC 的空 ns 维持残留（数据兜底），处置序见网络隔离模型节末行。）
