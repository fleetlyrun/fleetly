# ADR-0056: k3s 构建链消费面收口（from_build → 受管 zot）与多 server HA 裁决（挂账 7）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-08 | ADR-0055（决策 1 记档 from_build 的 containerd hosts.toml 面"后续批裁决"——本批直系授权面；决策 5 缺省翻转前置②HA 裁决收口）、ADR-0052（§9 挂账 7 多 server HA；决策 8 e2e 通道蓝本）、ADR-0054（决策 2 翻转前置三条件——本批收口②续喂①）、ADR-0019（附录 B 构建链：控制面节点本地 daemon 构建 + 受管仓库推送 + digest 下发）、ADR-0036（N2 兑现节 per-Project 凭证域隔离）、ADR-0053（决策 5 Enrollment AgentCommand 空形态——server join 分立裁决的既定口径）、`docs/runbooks/k3s-runtime.md`（装机序承载面）、`e2e/dind-h2c-route.sh`（构建链 e2e 唯一先例：insecure-registry drop-in + 容器重启时序） |

## 背景

缺省翻转前置三条件（ADR-0054 决策 2）在 N7 后的状态：①生产形态实跑（累积面已开，本批续喂）、③迁移序 runbook（已落地）、**②多 server HA 裁决（挂账 7，未收口）**。另一硬缺口是功能性的：**k3s 形态 from_build 构建链从未走通**——ADR-0055 决策 1 诚实边界记档" kubelet 从受管 zot（HTTP 明文）拉镜像需节点 containerd hosts.toml 配置面，e2e k3s 腿同样未覆盖（镜像全走公网 digest）"。

代码事实（本批裁决依据）：

1. **构建链全链现状**：engine `prepareBuildInput`（buildsource.go）组装推送目标 `<addr>/<projectID>/<appID>:r<seq>` + per-Project 推送凭证；builder（dockerfile/railpack/static）经**本机 Docker daemon 内嵌 buildkit** 构建（daemon.go /session h2c hijack）→ daemon `ImagePush` 推受管 zot → digest 冻结 → 下发引用 `<addr>/<repo>@sha256:...` → 拉取凭证 per-Project（materials.go）→ k3s `ensureImagePullSecrets`（network.go）→ 载体 `imagePullSecrets`（translate.go）。**认证面全链在位，缺口在传输信任面两处**：
   - 拉取侧：kubelet → containerd 对 HTTP 明文 registry 拒绝（k8s 侧对应物：`/etc/rancher/k3s/registries.yaml` 的 mirrors + `http://` endpoint，k3s 自动渲染 containerd hosts.toml）；
   - 构建侧：builder 直连本机 dockerd，推送明文 registry 需 daemon `--insecure-registry` 信任（install.sh 的 drop-in 面在 swarm 形态已有；**k3s 形态控制面节点的 docker daemon 是构建面部署依赖**——staging node2 按 ADR-0055 决策 1 已 stop/disable）。
2. **zot 受管形态**：明文 HTTP 是集群内网既有口径（zot Notes：swarm 侧同款明文 + dockerd insecure-registry 信任）；per-Project 凭证域隔离（用户名=projectID、仓门禁 `<projectID>/**`）经 imagePullSecrets（pod 级）分发。
3. **HA 事实**：k3s 单 server sqlite 是控制面单点；多 server 形态 = embedded etcd（`--cluster-init` + server join，node-token 同源，奇数节点 quorum）；fleetlyd 自身是单实例（控制面 sqlite 数据根单机）——k3s 多 server 提供的是 **Runtime 面高可用**，不覆盖 fleetlyd 自身。staging 无第三台机（ADR-0055 背景事实 4 维持）——e2e 承载，不扩硬件。

## 决策

### 1. 构建链拉取面 = runbook 装机序承载 registries.yaml（k3s 原生通道）

- **候选裁决**：(a) 装机序承载 registries.yaml ✅｜(b) Provider 下发/维护节点 containerd 配置 ❌——越过 k8s API 的节点文件侵入面，且 hosts.toml 由 k3s 从 registries.yaml 渲染管理，直接写会被覆盖；(c) zot 上 TLS ❌——VPC 内网明文是 swarm/k3s 两侧一致既有口径，TLS 引入证书生命周期管理面（LE 不能签 IP 地址、自签 CA 仍需节点侧分发信任面，复杂度高于收益）；传输加密需求出现时另批裁决（记档）。
- **形态**：装机时落 `/etc/rancher/k3s/registries.yaml`（server 与 agent 节点同款）：

  ```yaml
  mirrors:
    "<registryAddr>":
      endpoint:
        - "http://<registryAddr>"
  ```

  registryAddr 取 fleetlyd 同源值（`FLEETLY_REGISTRY_ADDR` / config `registry.addr`，唯一契约源 ADR-0036）。变更需重启 k3s（registries.yaml 在起动期渲染 hosts.toml）。
- **分层边界（必答项）**：节点级 registries.yaml **只承载传输信任面**（mirror endpoint scheme = 明文可达声明）；**认证面恒走 pod 级 imagePullSecrets**（per-Project 既有链）。registries.yaml **不配 auth**——节点级 auth 是全域共享凭证，会把 per-Project 凭证域隔离（ADR-0036 N2）静默击穿成全域读写。两层正交：mirror 键 = 镜像引用 host（不改写 host 名），kubelet 的 CRI 拉取链中 imagePullSecrets 凭证按引用 host 匹配，不受 endpoint 重写影响。
- worker 节点：不构建、只拉取——装机（enroll join 前）落同款 registries.yaml，runbook 记档。

### 2. 构建面依赖 = 控制面节点 docker daemon（仅构建面）

- builder 三 Provider 的实现形态（本机 daemon 内嵌 buildkit）是 ADR-0019 裁决本体（"构建恒在控制面节点，BuildKit + 本机 daemon"），k3s 形态不改判——**控制面节点必须跑 docker daemon，仅作构建面**（不 init swarm）。
- daemon 配 `--insecure-registry <registryAddr>` 信任推送目标（install.sh 的 daemon.json drop-in 同款；k3s 形态装机序记入 runbook——不经 install.sh 的 k3s 腿由装机序复刻该 drop-in）。
- **staging node2 改判 ADR-0055 决策 1 的"docker daemon 停用"**：恢复启用（enable + insecure-registry 信任 10.124.0.5:5000），语义从"退出 swarm 后无用途"修正为"构建面部署依赖"。内存成本 ~150MB（node2 峰值预算重估见后果节）。
- e2e 面（`e2e/dind-k3s.sh` 增 from_build 段，蓝本 = dind-h2c-route.sh 构建腿 + k3s 腿既有序）：
  - 时序：dind 起动 → insecure-registry drop-in 落 /etc/docker/daemon.json → **容器重启（k3s 未起，无损；h2c 先例）** → 断言 `docker info` 含 :5000 → `/etc/rancher/k3s/registries.yaml` 落 mirror（DIND_IP 已知）→ k3s 起 → 既有全链。
  - from_build 段：scratch 基础镜像（零外网依赖，h2cserver 二进制 COPY 形态）→ `deploy --from-dir` → build succeeded → 部署 succeeded → 断言 pod image 引用 = `<DIND_IP>:5000/<projectID 小写>/<appID 小写>@sha256:...`（digest 形态；该镜像只可能在受管 zot——airgap 预载清单不含它）→ 活体探针。
- staging node2 实跑记录追加（runbook 生产形态实跑记录表）。

### 3. HA = embedded etcd 多 server 形态支持（e2e 承载收口挂账 7）

- **形态判定**：k3s 多 server HA（`--cluster-init` 首节点 + `k3s server --server https://<node>:6443 --token <node-token>` join，embedded etcd，奇数节点 quorum）为**支持形态**——e2e 三 server 腿实证；单 server sqlite 维持小规模缺省形态（staging node2 不变）。
- **平台语义 = Runtime 面高可用**：apiserver/datastore 多副本下单 server 失效，集群读写保持、worker 上载体零滚动（kubelet autonomy）。**诚实边界：fleetlyd 自身单实例（控制面 sqlite + 数据根单机）不在 k3s HA 覆盖内**——fleetlyd 的可用性由 Backup/恢复序承载（平台既有口径）；fleetlyd HA 是独立话题（多实例选主/外置 datastore），触发条件：首个要求控制面无停机窗的真实部署，不预设。
- **无 LB 诚实边界**：e2e 与 runbook 均为直连形态（fleetlyd kubeconfig 与 worker join 各钉单 apiserver 端点）；LB/DNS 多记录是部署形态选择（k3s 官方建议 LB 在多 apiserver 前），runbook 记档不实施——staging 单机无需求。
- **e2e 腿（`e2e/dind-k3s-ha.sh`，拓扑仿 k3s-tw：三 server 容器 + 一 worker 容器）**：
  1. server1 `--cluster-init` 起 → server2/3 经 node-token join → 三 server Ready（etcd quorum）；worker join → 4 节点 Ready。
  2. fleetlyd（连 server1）部署载体 → 卷钉住 worker 落点 Running（k3s-tw 先例）。
  3. **server2 失效**（rm -f 容器）：集群读写保持（fleetlyd 部署第二载体成功 = 控制面可用性活体）、存量载体零滚动（pod uid 不变 + RESTARTS 零增量）、探针持续通。→ server2 重建 re-join → etcd 三成员恢复、载体零滚动。
  4. **server1 失效**（fleetlyd 的 apiserver 端点）：载体零滚动（worker autonomy——kubelet 与 apiserver 失联不杀容器）、探针持续通；fleetlyd 断连窗（日志断线重连面）。→ server1 恢复 → fleetlyd 重连收口 → 载体零滚动。
  5. k3s 版本/资产纪律同既有腿（下载缓存 + sha256 钉版；三 server 一 worker 同版本）。
- **Enrollment server join 分立裁决（必答项）**：平台**不封装 server join**。Enrollment 维持 worker-only（ADR-0053 决策 5 的 AgentCommand 空形态不动）；server 扩容/收缩是 etcd 成员变更（quorum 风险面，运维决策 + 装机级一次性动作），runbook 记录手工序（node-token + `--cluster-init`/`--server` 形态）——与 ADR-0054 决策 3（孤儿处置 runbook 承载）同文化：一次性动作不进常驻 API 面。k3s 侧 server 与 agent join 用同一 node-token 文件（实现事实），平台不引入"server-token"第二契约。

### 4. 前置①续喂（不阻塞主轴）

- **TLS/ACME 演练（staging node2）**：LE staging CA（非生产配额面）+ `*.143.198.234.68.sslip.io` 域名形态（dev.fleetly.run 泛解析指 manager 不指 node2，sslip 是 node2 公网 IP 的可用通配域）；route tls auto（ACME 缺省语义）求证 + 证书链验证（LE staging 根 `-k` 形态）。实录落 runbook。
- **换装零扰动演练（staging node2）**：HEAD 二进制替换 + systemctl restart——断言受管五域 pod 不滚（pod uid 不变）、用户载体零滚动、goose 前滚（schema 版本推进）；k3s 形态升级序的真机首录（runbook 升级与钉版节补实跑形态）。顺带观察 janitor Loop 首拍（N7 未复现观察项）。

### 5. 小账 = Remove 链收尾拆空 Namespace

- **候选裁决**：(a) Remove 链收尾拆 ns ✅｜(b) hygiene janitor 扫描拆 ❌——ns 删除是全 ns 级联毁灭，janitor 自动面的误判半径（ns 名与项目 ID 的映射是单向推断）不可接受，"宁可漏扫不可误删"纪律下重几个量级；｜(c) 维持记档 ❌——项目删除语义 = 域销毁，ns 是域的载体，拆除是语义正确收尾，残留是缺口不是选择。
- **实现**：k3s Provider `Remove` 尾部——**项目删除路径没有单一项目级 Remove 调用点**（App/Database/Browse 各自域 Remove 收口，DeleteProject 只做行级联），所以收尾判据挂每次域 Remove 尾部：ns 内**零活 fleetly 域载体**（deployments/daemonsets/pods 带 `fleetly.managed=true` 的宽列，deletionTimestamp 非空视为已拆）**且零 PVC（全部，不只 fleetly 卷）**时删除 Namespace——最后一个拆完的域触发。**PVC 零判据与 swarm 卷残留文化对齐**（平台文化：卷是数据兜底，项目删除后卷残留待显式处置——swarm 形态 `fleetly-vol-*` 同款）；ns 删除的 k8s 级联会连带删除域内一切（含挂靠方遗留 grant——接收方项目删除形态的 grant 随 ns 级联走，SweepOrphanPeerGrants 的信号③判据不变），有 PVC 在场即不拆（空 ns + 卷的残留形态记 runbook 处置序）。
- **失败语义**：ns 删除失败（含 RBAC 未收敛的 403 形态、terminating 卡住的集群侧运维面）**不阻断 Remove**——收尾是 best-effort，判据不满足或删除失败即跳过（下次任一域 Remove 重判）；App/项目删除不因 ns 异步收口回滚。RBAC 未收敛的存量集群经既有升级序收敛（admin kubeconfig 重跑一次，runbook 单向门节）。
- **RBAC**：`namespaces` 动词面 `create,get` → `create,get,delete`（6b 断言的第二个 no 相应改写——它钉的是"越权动词必须 no"，换成 server join 等真越权面）。
- **e2e 锚**：单节点腿 12b 节扩断言（挂靠方项目删除 → ns 收敛消失带界轮询）；staging 真机同锚。

### 6. 翻转批界面预案（本批收口后前置三条件全就绪的声明面）

本批完成（②HA 收口 + ①续喂 + from_build 功能缺口闭合）后，ADR-0054 决策 2 的翻转前置三条件全部就绪。**缺省翻转本体（config `runtime.provider` 缺省 k3s）不随本批启动**——届时另开 ADR（既定约定），输入清单：①生产形态实跑记录累积面（runbook 表）；②HA e2e 实证 + fleetlyd 单实例诚实边界；③迁移序 runbook；④"缺省 swarm 升级零扰动"硬锚的翻转语义（新装缺省翻转 ≠ 存量升级路径变更）；⑤from_build 构建链装机序的文档面（控制面 docker daemon 依赖 + registries.yaml）。

## 后果

- k3s 装机序（runbook）增两步：registries.yaml（拉取信任面）+ docker daemon 构建面（含 insecure-registry drop-in）；staging node2 恢复 docker daemon（内存预算重估：~150MB 回来，峰值 ~1.65GB/2GB，无 swap 警觉项维持）。
- e2e 面变化：`e2e/dind-k3s.sh` 增 from_build 段 + insecure/registries 前置序（k3s 起动前两文件落位）；新增 `e2e/dind-k3s-ha.sh`（四容器：三 server + worker）+ mise 任务 `e2e:k3s-ha` + CI job（fuse 形态，k3s 资产缓存同款）。
- capability 契约零变化（构建链/HA 均无新面）；RBAC 动词面 +delete namespaces（单向门：存量集群经 RBAC 升级序收敛——runbook 既有节）。
- ADR-0052 §9 挂账 7 划线；ADR-0054 决策 2 前置②兑现注记 + 前置①续喂注记；ADR-0055 决策 1 的"from_build 记档"与"docker daemon 停用"两处随本批改判注记。
- swarm 侧零行为变化（from_build 构建链共用 builder/zot 面，已在 swarm 形态现役）。

## 验收锚

- [x] ADR-0056 落档 + ADR-0052 §9 挂账 7 划线注日期 + ADR-0054 决策 2 前置②兑现注记 + ADR-0055 决策 1 两处改判注记（from_build 授权面兑现 / node2 docker daemon 恢复启用）
- [x] e2e k3s 腿 from_build 段全绿：dockerd 信任断言（docker info 含 :5000）→ registries.yaml 落位 → `deploy --from-dir` → build succeeded → 部署 succeeded → pod image 引用 = `<addr>/<projectID 小写>/<appID 小写>@sha256:...` digest 形态 → 活体探针
- [x] e2e HA 腿全绿：三 server quorum + worker 4 Ready → server2 失效集群读写保持 + 载体零滚动 → server2 re-join 恢复 → server1 失效载体零滚动（worker autonomy）+ fleetlyd 断连恢复 → 全程 pod uid 不变
- [x] staging node2 实跑：registries.yaml + docker daemon 构建面（insecure-registry）→ from_build 全链真机绿（记录表追加）；TLS/ACME 演练（LE staging + sslip）实录；换装零扰动演练实录（五域不滚/载体零滚/goose 前滚）
- [x] Remove 拆空 ns：单测（零载体删/在场保留/删除失败不阻断）+ e2e 12b 扩断言（项目删除 → ns 消失带界轮询）+ RBAC delete namespaces 收敛（can-i 断言同步改写）
- [x] runbook 扩节：k3s 装机序两步（registries.yaml + docker 构建面）/HA 装机与运维序（server join 手工序 + LB 诚实边界）/生产形态实跑记录表追加/升级节补换装实录
- [x] 全门禁：`mise run test` + `mise run lint` + `go test -count=1 ./internal/guards/` + `generate:verify` + `console:verify`；e2e 四腿回归（k3s 含 from_build 段 / tw / ha / runtimeswitch）；swarm 零回归

验收实录（2026-10-08）：e2e 三腿本机 fuse 形态全绿——k3s 腿 `K3S E2E PASSED`（run7：insecure drop-in 起动前时序 + registries.yaml 落位 + from_build 段〔build succeeded + digest 冻结 + pod image = `<dind-ip>:5000/<pid 小写>/<aid 小写>@sha256` digest 引用逐位 + Route 活体〕+ 12b 空域收尾断言）；HA 腿 `K3S HA E2E PASSED`（run2：三 server quorum + worker 4 Ready → server1 进程失效〔kill 面扩子进程——主进程 TERM 后 apiserver 孤儿形态 run1 咬出〕载体零滚动 + 持续服务〔经 server3 旁路，kubelet autonomy〕→ 恢复重连 uid 不变 → server2 永久失效新部署 succeeded）；tw 腿 `K3S TWO-NODE E2E PASSED`（回归零漂移）。runtimeswitch 本批零触碰面，CI job 兜底。staging node2 真机：registries.yaml + hosts.toml 渲染断言（**k3s restart 零扰动实证——containerd 分立单元，五域 pod age 连续未断**）→ docker daemon 构建面（旧 drop-in 冲突清障）→ from_build 全链（pod image = `10.124.0.5:5000/<pid>/<aid>@sha256` + Route 工作站公网外测 200）→ TLS/ACME（LE staging 证书签发 + HTTPS 端到端；**咬出 ACME 缺省邮箱缺口**：`fleetly@localhost` 被 LE 400 invalidContact 拒 → 改空 contact 注册，仓内修复）→ 换装零扰动 ×2（五域 uid 逐位不变、goose 27 零前滚）→ 空域收尾真机锚。**实施期咬出三修复链全在 main**：owned pod GC 链异步窗（收尾判据跳过有 owner 的 pod）/RBAC 表漏 persistentvolumeclaims list（fake clientset 不执法 RBAC，真 apiserver 403 静默跳过——dind lab 持久环境定位）/events list 缺省从头取 100 条（e2e 变长后高 seq 事件挤出窗）。全门禁绿（mise test 三 module -race / lint / guards -count=1 / generate:verify / console:verify 零漂移）。
