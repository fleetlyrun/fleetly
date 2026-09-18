# Spike C — 底座风险清零 FINDINGS（swarm init / 重调度 / 卷与绑定 / manager 恢复）

状态：**已完成**（2026-09-17 单段执行；全部 7 组必验实验在多节点 `docker:29.8.1-dind` 拓扑中完成，证据原文见 `spike/c/artifacts/logs/`）。

环境基线：本机 Docker Desktop 29.7.2（宿主）+ 特权 `docker:29.8.1-dind`（inner engine **29.8.1**）。多节点拓扑 = 同一宿主 bridge 网络 `spike-c-br`（10.10.0.0/24，静态 IP：mgr=.10 / w1=.11 / w2=.12 / v5m 与恢复后 v5m2=.20 / v5w=.21）上的多个 dind 容器，`swarm init --advertise-addr eth0` + join token 组网。所有毫秒时间戳出自同一 WSL2 内核时钟（宿主 `docker inspect .State.FinishedAt/StartedAt` 与 dind 内 `/opt/probe ts` 可直接对齐）。探针：独立 Go module `spike/c`（stdlib-only 单二进制 `cmd/probe`，模式 `ts`/`parse`/`stamp write|read`，交叉编译 linux/amd64 静态链接）。fixture = `alpine:3.20` + bind 挂载 `/opt/probe` + 命名卷；**service 命令不做自动写戳**——所有卷写入/读取由实验脚本显式 exec 执行，防止重调度任务覆盖证据。节点身份 label 按设计文档预置：`fleetly.node-id = mgr|w1|w2|v5m|v5w`。

设计依据：`docs/design/2026-09-17-architecture.md` §4.1 Spike C 行（验收真源）、§2.6 多节点模型与 HA 边界；`docs/design/2026-09-17-stateful-placement.md` §1/§2（钉住、漂移矩阵、错误码）；`docs/research/2026-09-17-swarm-substrate-assessment.md` §3/§4（init 副作用、心跳判定、raft 恢复、V5/V5b/V6a/V6b 定义）。

---

## 0. 实验矩阵总览

| # | 假设 | 方法 | 结果 | 结论 | 设计影响 | 执行者 |
|---|------|------|------|------|----------|--------|
| C1 | `swarm init` 对既有普通容器/卷透明，副作用可枚举 | 单 dind：先 `docker run` 带卷容器 → init 前后全量快照（ps/卷/网络/监听/iptables/links） | ✅ 容器仍 Up、卷数据可读、不挂 swarm label；副作用 = Swarm:active + 2 网络 + 3 监听端口 + iptables 49→56 行 + links 4→6 | 用户视角透明成立；副作用清单完整 | 安装器隐式 `swarm init` 可行，需钉 `--advertise-addr`；§3 init 副作用清单逐项实测锚定 | 本段 |
| C2 | 节点 DOWN 判定 15–16.5s；stateless 自动重调度；恢复后不回迁 | 双节点 replicas=2 → 宿主 `docker kill` worker dind → 1s 分辨率时间线 | ✅ **Down 判定实测 ~13.5s（窗口 12.4–13.5s）**，低于假设下限；重调度完成（2/2 Running）≤18.7s；恢复后**不回迁**；重调度瞬时 replicas 过冲 3/2 | 心跳判定与自动重调度成立且比假设更快 | §2.6「15s 量级判定」下修为 13s 量级（下界）；重调度窗口内不可用时长有实测锚点 | 本段 |
| C3a | 有卷服务无约束 → 节点死 → 迁移得**空卷**（数据丢失） | drain-mgr 控制初始落点（服务零约束）→ 写戳 → kill → 在新节点读卷 | ✅ **空卷事故复现**：新节点 c3vol 全新创建（CreatedAt 不同）、读戳 MISS rc=3；原节点卷原戳完好（kill 前后对照齐） | 「有卷不迁移」不成立再次实测锚定；数据不跟随 | 放置专项 §1「必须主动钉住」+ 前哨 409 设计的实测地基 | 本段 |
| C3b | 加 `node.labels.fleetly.node-id==` 钉住 → 节点死任务停 **PENDING**；原容器重启后任务回绑、数据在 | 同上 + 约束；kill 后 `docker start` 同一 dind（预清理 pidfile） | ✅ PENDING `no suitable node`、NODE 列为空、无任何新任务；重启后 agent 自动重连（证书即身份），任务回 w1（restart+0.7s 观测到 Running），卷 CreatedAt 不变、原戳原值读回 | 硬钉住=唯一安全语义成立；worker 证书持久 ⇒ 引擎重启自动归队 | 放置专项绑定语义全链路实测；§2.2 节点身份=证书内嵌 ID 得证 | 本段 |
| C4a | drain 绑定节点 → 应用 blocked（PENDING）；active 回岗 → 自动回绑数据在 | pinned 服务 → `node update --availability drain/active` → 秒级时间线 | ✅ drain→Pending ≤0.9s（控制面动作，无心跳等待）；drain 期间卷数据可读（helper）；active→回绑 **1.1s**；数据完整 | 维护窗口语义实测：drain 期间停机、回岗零人工 | §2.6 drain 行为矩阵逐格锚定；「drain 期间该应用停机」量级=秒级 | 本段 |
| C4b | `node rm` 后绑定任务**永久 PENDING**、无自动迁移；人工重绑=改约束，**数据不跟随** | kill→Down→`node rm`→60s×3 快照 → 新 worker join → 约束换绑 | ✅ 三快照均 Pending；旧任务转 **Orphaned**、节点列显示裸 ID；换绑后任务落 w2、读戳 **MISS**、w2 卷全新；原戳经 `docker cp` 从死节点抢救成功 | rm 后无自愈，重绑必配 data-restored 确认 | 放置专项 §2.6/§2.7/§2.8 全部锚定（Orphaned 立即出现，非 24h）；`E_PLACEMENT_NODE_GONE`→rebind 流程有可执行步骤 | 本段 |
| C5 | 单 manager 死亡 → 冷备回填 + `--force-new-cluster` 恢复；worker 侧应用不中断 | 停 manager → 停止态 `docker cp` 冷备（sha256 校验）→ rm → 同 IP 新 dind 回填 → 重启 → force-new-cluster | ✅ 冷备字节级有效；恢复后 **daemon 自动 active 且继承死亡 manager NodeID**；force-new-cluster 仍 rc=0 并轮换 join token；worker 任务 **task ID 跨死亡不变**、容器 Up 计时单调（零中断）；manager 上的服务在 v5m2 真实重建 | 「2 台=1 manager+1 worker+冷备」HA 口径成立；单 manager 场景比文档流程更顺（自举，无需 force 也可） | §2.6 管理面 SPOF 边界 + 状态模型专项 L1/L2 恢复阶梯的实测锚点；冷备操作规程可写进 runbook | 本段 |
| C6 | raft 回退到旧备份：备份后创建的服务与删除的服务命运明确 | 备份后 `rm c6-old` + `create c5-new` → 死亡 → 回填旧备份 → t0/t5/(t20) 采样 | ✅ **恢复态双向获胜**：已删的 c6-old 复活（新任务，卷数据原样）；备份后的 c5-new 服务定义消失、其容器在 worker 重连时**立即被回收**（非 24h 孤儿）、其卷残留为孤儿卷（数据仍在） | raft 回退后无半态：以 raft 为准双向对账；「孤儿」是卷不是容器 | 状态模型专项：恢复后对账器必须按 raft 反向清理/复活；orphaned 卷语义实测 | 本段 |

通用结论标注：✅=实测证据支持；⚠️=有证据但有局限（局限明示，见 §7/§8）。

---

## 1. C1 — swarm init 透明性（副作用清单）

**方法**：`scripts/c0-up.bat`（全新 dind `spike-c-m0`）→ `scripts/in-c1-init.sh`：init 前先 `docker run -d --name c1-ctr -v c1vol:/data alpine:3.20 sh -c "echo stamp-c1-plaintext > /data/stamp.txt && sleep 31536000"`，快照 ps/卷/网络/`docker info`/netstat/links/iptables-save，然后 `docker swarm init --advertise-addr eth0`，再同位快照 + 逐项断言。

**原始输出**（`artifacts/logs/c0-run.log`，节选）：

```
===== C1.1 pre-init state =====
c1-ctr Up 2 seconds alpine:3.20
stamp-c1-plaintext                                  ← 卷数据可读
NETWORK ID     NAME      DRIVER    SCOPE
f9bd9b819dc6   bridge    bridge    local
a0ab0f019d23   host      host      local
d43d051ea09f   none      null      local
-- docker info swarm state before: inactive
tcp        0      0 :::2376                 :::*        LISTEN      ← init 前
-- links before: 4
-- iptables rule lines before: 49

===== C1.2 docker swarm init --advertise-addr eth0 =====
Swarm initialized: current node (o5ti8wlcrmmqtv8m3fn7htgbs) is now a manager.

===== C1.3 post-init =====
c1-ctr Up 4 seconds alpine:3.20
C1-ASSERT-EXISTING_CONTAINER_RUNNING: PASS
stamp-c1-plaintext
C1-ASSERT-EXISTING_VOLUME_DATA_INTACT: PASS
c1-ctr 的 com.docker.swarm.task.id label = <no value>   ← 仍是普通容器
LocalNodeState=active ControlAvailable=true NodeID=o5ti8wlcrmmqtv8m3fn7htgbs
C1-ASSERT-INFO_SWARM_ACTIVE: PASS
o5ti8wlcrmmqtv8m3fn7htgbs *   m0   Ready   Active   Leader   29.8.1
f9bd9b819dc6   bridge            bridge    local
9a5cf5324779   docker_gwbridge   bridge    local     ← 新增
2rt2kh66dnxi   ingress           overlay   swarm     ← 新增
C1-ASSERT-NETWORKS_INGRESS_GWBRIDGE: PASS
tcp  :::2376 …  :::2377 LISTEN  :::7946 LISTEN          ← 新增 2377/7946
C1-ASSERT-LISTEN_2377: PASS
-- links after: 6
-- iptables rule lines after: 56
（diff：raw PREROUTING 对 docker_gwbridge 子网 DROP；filter 增 DOCKER/
 DOCKER-BRIDGE/DOCKER-CT/DOCKER-FORWARD 的 gwbridge 规则各 1–2 条）
-- volume list after: local c1vol      ← 卷原样
C1-OK
```

补充取证（`artifacts/logs/c1-udp-links.txt`）：`netstat -lnu` 显示 **4789/udp（vxlan）与 7946/udp（gossip）** 在 init 后监听；DOCKER-INGRESS iptables 链在 init 时**不存在**（`iptables-save | grep -c DOCKER-INGRESS` = 0）——它在首个发布端口的服务出现时才创建。

**结论**：✅ 成立。init 对既有容器/卷零影响（进程不动、数据可读、不被纳管），副作用完整清单 = ①`docker info` Swarm 字段 active + NodeID；②网络 +ingress/+docker_gwbridge；③监听 +2377/tcp +7946/tcp+udp +4789/udp；④iptables +7 行（全为 gwbridge 管道）+ links +2；⑤`/var/lib/docker/swarm/` 出现。用户可见的变化只有 `docker info`/`network ls`——「透明」成立但不能宣称零痕迹（评估报告 §3「不能 100% 透明」逐项落实）。

**设计影响**：架构 §2.6/v0.1「安装时隐式 `docker swarm init`，对用户透明」——通过。安装器必须显式 `--advertise-addr`（多网卡/重启换 IP 场景，V5 亦依赖地址稳定）；对外文档如实列出上面五项可见副作用。

**重跑**：`spike\c\scripts\c0-up.bat`（自动重建 m0 并断言，~2 分钟）。

---

## 2. C2 — 节点 DOWN 判定与 stateless 重调度时间线

**方法**：`scripts/c2-prep.bat`（mgr+w1 组网 + label）→ `c2-run.bat`：`c2-app` replicas=2（基线 c2-app.1@w1、c2-app.2@mgr）→ mgr 内 watcher 1s 采样 `node ls` + `service ps`（JSONL，毫秒时戳）→ 宿主 `docker kill spike-c-w1`，取 `docker inspect .State.FinishedAt` 为 kill 时刻 → watcher 末尾自动算 delta。

**原始时间线**（`artifacts/logs/c2.jsonl` + `c2.kill`；kill = 1789674549854 = 2026-09-17T19:49:09.854Z）：

```
1789674562294 nodes mgr=Ready/Active w1=Ready/Active     ← kill+12.44s 最后一次 Ready
1789674563347 nodes mgr=Ready/Active w1=Down/Active      ← kill+13.49s 首次 Down
1789674568545 svc c2-app.1|mgr|Running …                 ← kill+18.69s 替代任务已在 mgr Running
（旧任务行：kill 后 40s 内仍显示 |w1|Running，节点回岗后才被 agent 上报为 Failed）
--- placement before kill ---
c2-app.1 w1 Running 5 seconds ago
c2-app.2 mgr Running 5 seconds ago
--- C2.5 post-kill（watcher 95s 窗口结束时）---
zr8xr8htg5cy   c2-app    replicated   3/2   alpine:3.20   ← 重调度过冲：旧任务尚未确认死亡
ccjrpsbngv3d   c2-app.1       mgr  Running  Running about a minute ago
dl9ww2i2xban    \_ c2-app.1   w1   Shutdown  Running about a minute ago
n3wyhmo5kd8q   c2-app.2       mgr  Running  Running about a minute ago
--- C2.6 w1 重启回归后（手工快照，见 §8 执行记录）---
mgr=Ready/Active  w1=Ready/Active
c2-app.1 mgr Running 7 minutes ago      ← 两个任务都留在 mgr
c2-app.1 w1 Failed 15 seconds ago       ← 旧任务此刻才获得终态（exit 137 上报）
c2-app.2 mgr Running 7 minutes ago
```

**判读**：
- **Down 判定 = 13.5s（采样窗口 12.4–13.5s）**——比「心跳 5s×3 ≈ 15–16.5s」假设更快。假设的量级与机制（dispatcher 心跳缺失计数）成立，但下限偏保守：实际判定落在第 3 个心跳周期结束之前（对齐相位时约 3×5s−相位偏移）。
- **stateless 重调度全程 ≤18.7s**（kill → 2/2 Running；含判定 + 调度 + 容器启动）。期间服务实际少一个副本约 19s。
- 瞬时 **replicas 3/2 过冲**：判定 Down→标记旧任务死→替代任务 Running 之间存在窗口，manager 同时把新旧任务计为实际副本。
- **恢复后不回迁**：w1 回归 Ready 后两个任务原样留在 mgr，无任何自动 rebalance（架构 §2.6 断言实测成立）。
- 观测口径：1s 采样，绝对值受采样分辨率限制（±1s）；kill 时刻取自 daemon 的 FinishedAt（纳秒），非轮询插值。

**结论**：✅ 成立（判定实测快于假设）。架构 §2.6「失联 15s 量级判定 + 自动重调度 + 不回迁」三条全部有实测锚点；「重调度窗口内该 app 短暂不可用」的对外口径可写实为「秒级（本例 ≤19s）副本缺口」。

**设计影响**：§2.6 失联语义行与放置专项 §2.6「观测（心跳 15s 量级）」→ 建议改为「实测 ~13.5s 判定（3×5s 心跳对齐相位，上限 16.5s 保守成立）」。平台 UI 的 `node.down` 事件延迟预算可按 13–17s 设计。

**重跑**：`spike\c\scripts\c2-prep.bat && spike\c\scripts\c2-run.bat`（~4 分钟；C2.6 的 w1 重启段若遇脏退出，用 `noderestart.bat spike-c-w1 w1 10.10.0.11`，见 §8#2）。

---

## 3. C3 — V6a 卷与绑定语义（空卷事故 + 硬钉住）

### 3.1 C3a 无约束 = 空卷事故（数据丢失前后对照）

**方法**：`c3a-run.bat`。有卷服务**零放置约束**部署（用「临时 drain mgr → 创建 → active」控制初始落点在 w1，服务 spec 不含任何 constraint）→ 显式写戳 → kill w1 → 等任务迁到 mgr → 在 mgr 读卷。

**原始输出**（`artifacts/logs/c3a-run.log` + `c3a-rescued-read.log`）：

```
C3A.1  WAITSVC-OK c3-app running=1 want=1 after=0s        ← 落在 w1（drain trick）
C3A.2  c3vol-on-w1 CreatedAt=2026-09-17T20:12:09Z         ← BEFORE：卷诞生于 w1
       STAMP-WRITTEN { … }   stamp-rc=0                   ← 写戳 token=c3a-initial-worker
       STAMP-OK { "host":"f5f567b3dfb2","ms":1789675935954, … }  stamp-rc=0
C3A.3  kill done, FinishedAt=2026-09-17T20:12:19.464031234Z
C3A.5  WAITSVC-OK c3-app running=1 want=1 after=0s        ← 任务已在 mgr（w1 Down 唯一可选）
       c3vol-on-mgr CreatedAt=2026-09-17T20:12:33Z        ← AFTER：mgr 侧卷是全新的！
       STAMP-MISS (no stamp.json in volume = empty-volume evidence)   stamp-rc=3
C3A.6  c3vol-on-w1 CreatedAt=2026-09-17T20:12:09Z (must differ from mgr volume)
       STAMP-OK { "host":"f5f567b3dfb2","iso":"…20:12:15…","ms":1789675935954,
                  "token":"c3a-initial-worker" }  stamp-rc=0   ← 原节点卷数据原样
C3A.7  mgr volume ls: local c3vol / w1 volume ls: local c3vol  ← 删服务不删卷（两侧均在）
```

**结论**：✅ 空卷事故完整复现——**同一名卷 c3vol 在两个节点各有一份**（CreatedAt 20:12:09 vs 20:12:33），重调度后的任务拿到 mgr 侧的空卷，写戳（c3a-initial-worker，20:12:15）没有跟随；原数据仍在 w1 卷里（kill→重启→原戳原值读回）。数据丢失的「写入→重调度→读出为空」三段证据齐。

### 3.2 C3b 硬钉住 = PENDING 不迁移 + 重启回绑数据在

**方法**：`c3b-run.bat`。同构造但加 `--constraint node.labels.fleetly.node-id==w1`；kill 后等待断言，再 `docker start` 被 kill 的同一 dind 容器（保留内层 `/var/lib/docker/swarm` 证书 = swarm 节点身份），等任务回绑后读卷。

**原始输出**（`artifacts/logs/c3b-run.log` + watcher 分析）：

```
C3B.2  STAMP-WRITTEN … stamp-rc=0；STAMP-OK {host:dee2ac9ed4c8, ms:1789676205682, token:c3b-initial-worker}
C3B.4  kill done, FinishedAt=2026-09-17T20:16:49.216617758Z
C3B.5  w1  Down  Active                                   （kill+25s 快照）
       j5zsat9ll71p  c3b-app.1  alpine:3.20  [NODE 空]  Running  Pending 12 seconds ago
                     "no suitable node (1 node not …)"
       jbuqinvsxpbk  \_ c3b-app.1  w1  Shutdown  Running 36 seconds ago
       running-task count = 0                              ← 集群内无任何 Running 副本
C3B.6  restart done, StartedAt=2026-09-17T20:17:16.405636778Z
C3B.7  （watcher）pending-observed-at-ms: 1789676223648   （kill+27.8s）
       task-back-on-node-at-ms: 1789676237145             （restart+0.7s）
       node-ready-again-at-ms: 1789676238183              （restart+1.8s）
C3B.8  c3bvol-on-w1 CreatedAt=2026-09-17T20:16:39Z (must equal pre-kill) ✓
       STAMP-OK { "host":"dee2ac9ed4c8","ms":1789676205682,"token":"c3b-initial-worker" }
       ← 与 BEFORE 逐字节同值：同一文件，从未被覆盖
```

**结论**：✅ 成立。约束钉住后节点死亡：任务停 PENDING（NODE 列为空 + `no suitable node`），**不产生任何新任务**；同一 dind 容器重启后 worker 凭持久化证书**自动归队**（无需重新 join），任务自动回到 w1，卷与数据原样。对照 C3a：「有卷必须钉住」的两组语义（不钉=空卷事故 / 钉=可用性换数据安全）同时有实测锚点。

**设计影响**：放置专项 §1「必须主动钉住」、§2.1 执行层（适配器编译为 `fleetly.node-id` 约束）、§2.6「绑定节点 DOWN→PENDING；恢复→自动回绑」全部通过。§2.2「Swarm node ID 由证书承载、引擎重启身份不变」实测成立——平台 `runtime_node_refs` 映射可安全依赖它。

**重跑**：`c3a-run.bat`、`c3b-run.bat`（各 ~3–4 分钟；依赖 c2-prep 的集群）。

---

## 4. C4 — V6b 绑定保持与漂移（drain/回岗 + rm/重绑）

### 4.1 C4a drain → blocked；active → 自动回绑

**方法**：`c4a-run.bat`。pinned 卷服务写戳后 `docker node update --availability drain w1`（毫秒里程碑入 `c4a.drain`），20s 后断言 blocked 语义与卷存活，再 `active`（`c4a.active`）计时回绑。

**原始输出**（`artifacts/logs/c4a-run.log` + `c4a.jsonl`）：

```
C4A.2  STAMP-WRITTEN/STAMP-OK（token=c4a-initial-worker）rc=0
C4A.3  drain 里程碑 ms=1789676628885
C4A.4  w1   Ready   Drain                                   ← 节点仍 Ready 但 drain
       oq55ozpdbzjx  c4-app.1  [NODE 空]  Running  Pending 20 seconds ago  "no suitable node …"
       ewef08ywy0zj  \_ c4-app.1  w1  Shutdown  Shutdown 9 seconds ago
       running count = 0；w1 docker ps 任务容器已消失（应用 blocked）
       helper 读卷：STAMP-OK rc=0                            ← drain 期间卷数据安然
C4A.5  active 里程碑 ms=1789676650397
       WAITSVC-OK c4-app running=1 want=1 after=1s           ← 回绑
C4A.6  STAMP-OK rc=0（token/ms 与 C4A.2 一致 = 数据完整）
--- watcher 分析 ---
milestone drain-ms: 1789676628885    pending-after-drain-at-ms: 1789676629792  （≤0.9s）
milestone active-ms: 1789676650397   delta-active-to-task-back-s: 1.1
```

**结论**：✅ 成立。drain 是控制面动作：旧任务 Shutdown、新任务 PENDING 在 **≤0.9s** 内到位（与节点 DOWN 的 13.5s 心跳判定形成量级对照）；drain 期间应用停机但卷数据在本节点原样；`active` 回岗后任务 **1.1s** 自动回绑同一节点，数据完整。架构 §2.6 维护窗口语义（有状态节点 drain→停机、回岗→自动回绑数据不丢）逐格成立。

### 4.2 C4b node rm → 永久 PENDING；人工重绑路径（数据不跟随）

**方法**：`c4b-run.bat`。pinned 卷服务写戳 → kill w1 → Down 后 `docker node rm w1` → 60s 内 3 次 `service ps` 快照断言无自愈 → 全新 dind w2 join + 打 `fleetly.node-id=w2` → 人工重绑 = **换约束**（`--constraint-rm …==w1 --constraint-add …==w2`）→ 读 w2 卷 → `docker cp` 从死节点抢救原戳。

**原始输出**（`artifacts/logs/c4b-run.log` + `c4b-rescued-stamp.json`）：

```
C4B.1  STAMP-WRITTEN/OK（token=c4b-initial-worker, ms=1789676766599, host=696a7e119245）
C4B.2  kill FinishedAt=2026-09-17T20:26:10.025663394Z
C4B.3  w1 Down（watcher：delta-kill-to-down-s: 10.3）
       docker node rm w1 → 输出 "w1"（Down 节点无需 --force）
       node ls 只剩 mgr
C4B.4  三快照（+20s/+40s/+60s）完全一致：
       4ilm8k84bbkl  c4b-app.1  [NODE 空]  Running  Pending …  "no suitable node (scheduling …)"
       3cr95stywnp7  \_ c4b-app.1  7bug0ewlfh0yq4yfx6mmfd1qq  Shutdown  **Orphaned** …
       ← 节点被 rm 后：旧任务立即转 Orphaned（非 24h），节点列显示裸节点 ID；无自动迁移
C4B.5  w2 join 成功 + label；node ls 出现 w2 Ready
C4B.6  service update --constraint-rm node.labels.fleetly.node-id==w1
                   --constraint-add node.labels.fleetly.node-id==w2 c4b-app
       WAITSVC-OK c4b-app running=1 want=1 after=0s（任务落 w2）
C4B.7  STAMP-MISS  stamp-rc=3                                 ← w2 得到空卷
       c4bvol-on-w2 CreatedAt=2026-09-17T20:27:32Z（全新卷，非 w1 那份）
C4B.8  docker cp spike-c-w1:/var/lib/docker/volumes/c4bvol/_data/stamp.json → 成功
       { "host":"696a7e119245","ms":1789676766599,"token":"c4b-initial-worker" }
       ← 与原写入一致：数据只能这样人工搬
```

**结论**：✅ 成立。绑定节点被 rm 后：任务永久 PENDING（60s 观察窗零变化）、旧任务**立即**转 Orphaned；恢复只有人工路径——新节点 join + 改约束重绑，且**重绑即空卷事故**，原始数据只能由用户从死节点人工抢救（docker cp 卷目录）。这正是放置专项把 `rebind --data-restored` 设计为破坏性确认 + `E_VOLUME_NODE_MISMATCH` 前哨的实证依据。

**设计影响**：放置专项 §2.6 漂移矩阵四行（DOWN/drain/恢复/rm）全部有实测锚点；§2.7「唯一受支持迁移路径=备份恢复迁移」的另一半（空卷路径 `--discard`）有事故对照；§2.8 `E_PLACEMENT_NODE_GONE` 后人工二选一的步骤清单可直接引用 C4B.5–C4B.8。补充发现：**Orphaned 状态在节点 rm 时立即出现**，评估报告 §4「24h 后转 ORPHANED」指的是无主容器 GC 时限，任务态的 Orphaned 是即时的——文档措辞建议区分。

**重跑**：`c4a-run.bat`、`c4b-run.bat`（各 ~3–5 分钟；c4b 结束后 w1 已被 rm，后续实验需重跑 c2-prep）。

---

## 5. C5 — V5 单 manager 故障恢复演练

**方法**：`v5-up.bat`（v5m=Leader@10.10.0.20 + v5w；服务集：c5-app-worker 钉 worker、c5-app-mgr 经 drain-trick 落 manager、c6-old 带卷 c6vol 并写戳）→ `v5-backup.bat`（热 tar 兜底 + `docker stop -t 30` → **停止态 `docker cp` 冷备** + sha256 字节校验 → start 回）→ `v5-death-restore.bat`（备份后变更：`rm c6-old` + `create c5-new`；`stop`+`rm` manager；同 IP 新 dind v5m2 回填冷备；重启 dockerd 载入 raft；尝试 force-new-cluster；等 worker 重连；逐项断言）。

**备份证据**（`v5-backup.log` + `v5-swarm-hashes-before.txt` + host certutil）：

```
DOCKER-CP-FROM-STOPPED-OK            ← manager 停止态 docker cp 可用（宿主→dind 方向的坑不适用此方向）
swarm-cp\  docker-state.json 205B / state.json 66B / certificates\{crt,key} / raft\ / worker\
state.json          52cfebda9c896966ee6885ff07ecc7ec66caf683dedbc99098ff09ef97d2e766  ← 与
swarm-node.crt      a9220ed259201ba655c3c7b15fda4e08c2dea21d54dca7921b2bca13ef9c5a2a  ← manager 内
swarm-node.key      96b7fb73a6050510f3eabb60f9614281aebae0aba2641aa981e0b02fea652e4f  ← sha256 完全一致
热兜底 swarm-hot.tgz = 172032B（本演练未使用）
```

**死亡与恢复**（`v5-death-restore.log` + 证据文件组）：

```
V5D.1  变更后真相：service ls = {c5-app-mgr, c5-app-worker, c5-new}（c6-old 已删）
       c5-new 写戳+读戳 OK（token=c5new-original-after-backup）
       c5-app-worker 任务 = ko7pqx09g0zz（v5w，Running）
V5D.2  manager dead at 2026-09-17T20:31:57.499606984Z；docker rm spike-c-v5m
V5D.3  worker 视角（v5managerdown-1/2/3.txt）：
       info=active/false（LocalNodeState 全程保持 active——worker 对管理面失联无可见翻转）
       docker node ls → "This node is not a swarm manager. …"（恒定报错，非失联信号）
       容器连续 Up：c5-app-worker Up 2 minutes；c5-new Up 19s→29s→39s
V5D.4  v5m2 同 IP(10.10.0.20) 启动 → 回填冷备 → sha256(state.json)=52cfebda… 复核一致
V5D.5  docker stop/start v5m2 → dockerd 载入 raft 后：
       LocalNodeState=active ControlAvailable=true NodeID=tcvjcv69llui7izrgarsn60uo
       ← 自动成为 active，且 NodeID=死亡 manager 的原 ID（单 manager raft 自举，无需人工干预）
V5D.6  docker swarm init --force-new-cluster → rc=0
       "Swarm initialized: current node (tcvjcv69llui7izrgarsn60uo) is now a manager."
       且打印新 join token（SWMTKN-1-5xry090… ≠ 原 SWMTKN-1-1pbany…）→ force 语义=重建单 manager
       集群并轮换 token；对已自举的节点不报错
V5D.7  v5w=Ready（重连完成，等待门通过）
V5D.8  service ls = {c5-app-mgr, c5-app-worker, c6-old}；V5B-C5NEW-IN-SERVICE-LS: ABSENT
V5D.9  c5-app-worker 任务 = ko7pqx09g0zz Running（与死亡前完全相同的 task ID）
       c5-app-mgr 任务 = wyyyq3jhsbc6 Running 15 seconds ago @ v5m2（真·随 manager 死掉、由恢复
       方重建——该服务的实际中断窗 ≈ 整个演练的 manager 缺席期）
V5D.10 c6-old 复活：c59v27dc524s Running 13 seconds ago @ v5w（旧任务 p7g1iagxev4r Shutdown）
       新容器内读 c6vol：STAMP-OK { host:4a7cd5777f12, ms:1789677001731,
       token:c6-original-on-v5w } ← 原始写入原样（卷同节点、从未被删）
```

**应用连续性铁证**（`v5w.jsonl` worker 侧 3s 采样，跨死亡+恢复全程）：

```
1789677047746 ps … e90a2804461c|c5-app-worker.1.ko7pqx09g0zz…|Up 52 seconds
1789677311337 ps … e90a2804461c|c5-app-worker.1.ko7pqx09g0zz…|Up 5 minutes
← 同一容器 ID、Up 计时单调递增，跨越 20:31:57 的 manager 死亡与整段恢复——零重启、零中断
```

**结论**：✅ 恢复演练成功，且比文档流程更顺。三层「不中断」如实分层：①worker 上的应用（本产品的主场景）**进程级零中断**——容器 Up 计时连续、task ID 跨死亡不变，中断的是管理面（期间无法部署/变更）；②manager 上的服务（c5-app-mgr）真实死掉，由恢复方在 v5m2 重建（中断窗=manager 缺席全程）；③冷备→恢复的操作面：停止态 `docker cp`（字节级校验通过）→ 同 IP 回填 → daemon 重启即自举（单 manager 场景 auto-active；`--force-new-cluster` 仍可执行且轮换 join token——恢复后若要加新节点必须用新 token）。

**设计影响**：架构 §2.6「2 台得不到管理面 HA；正解=1 manager+1 worker+冷备」——实测支持，且「应用运行不依赖控制面」有容器连续性证据。状态模型专项恢复阶梯 L1（raft 冷备+force-new-cluster）——流程实测通过，建议 runbook 按「停 daemon → 冷备（或停止态 cp）→ 清空→回填→起 daemon→（可选）force-new-cluster（会轮换 join token）」固化；`--advertise-addr` 的 IP 稳定性是 worker 自动重连的前提（本次以静态 IP 复用实现，生产对应「manager 主机 IP 不变/漂移需改 advertise」）。

**重跑**：`v5-up.bat` → `v5-backup.bat` → `v5-death-restore.bat`（~10 分钟；v5-backup 起的 25min worker watcher 在 `artifacts/logs/v5w.jsonl`）。

---

## 6. C6 — V5b raft 回退后孤儿命运（0/5/30min 观察窗）

**方法**：与 C5 同一次演练（备份 B0 → 备份后变更 `rm c6-old`+`create c5-new` → 死亡 → 回填 B0）→ 恢复完成后 t0 / 约+3min / 约+9min / 约+22min 四个采样点（脚本标签 t0/t5/t20/t22 为调用序号，实际时刻按毫秒时戳换算如实标注）：worker 侧 c5-new 容器存续、manager 侧服务定义、任务表。

**原始输出**（`v5b-sample-t0.log` / `v5b-sample-t5.log`）：

```
t0（恢复完成 +13s，1789677167661）：
V5B-C5NEW-CONTAINER: GONE            ← 备份后创建的 c5-new 容器已被回收
volume ls: local c5newvol / local c6vol   ← 其卷残留（孤儿卷，数据还在）

t+2.9min（1789677344148，脚本标签 t5）：
V5B-C5NEW-CONTAINER: GONE
907d5eb8d49a c6-old.1.c59v27dc524s… Up 3 minutes     ← 复活服务稳定运行
e90a2804461c c5-app-worker.1.ko7pqx09g0zz… Up 5 minutes
-- service ls from restored manager -- = {c5-app-mgr, c5-app-worker, c6-old}
-- c5-new service def? -- "no such service: c5-new"
-- c5-app-worker task -- ko7pqx09g0zz … Running 5 minutes ago（同一任务 ID）
```

**判读（V5b 的核心答案）**：raft 回退后**不存在「半态」或 24h 孤儿容器期**——恢复出的 raft 是唯一真源，双向对账：
1. **备份后创建的服务（c5-new）**：定义消失；其容器在 worker 重连到恢复方 manager 的瞬间即被 agent 回收（assignment 对账：不在新分配集 → shutdown），t0 已 GONE，5 分钟后仍 GONE，无僵留进程。
2. **备份后删除的服务（c6-old）**：定义复活，调度全新任务；因卷同节点且从未删除，**原数据原样**（原戳读回）——「删服务」删的只是定义，raft 回退把定义找回来，卷数据自然回归。
3. **遗物 = 孤儿卷**：c5newvol 留在 v5w（内含「消失」服务的完整数据）——与放置专项 `volumes(orphaned)` 语义吻合，平台 `GET /v1/volumes` 应能发现它。

**结论**：✅ 有明确观察记录（t0/+3min/+9min 三采样 + t0+22min 延长采样，见 §8 说明）。局限（如实标注）：①计划的 30min 整点采样以 t0+22min 延长采样替代（`artifacts/logs/v5b-sample-t22.log`）；②评估报告的「24h 后任务转 ORPHANED」GC 时限在本窗内不可观测（本实验中孤儿是卷、容器即死）；③观察对象为任务型 workload，无 queue/外部副作用可比对。

**设计影响**：状态模型专项「控制面灾难恢复：固定恢复顺序 + 人工处理差异」——raft 回退的差异处理可由平台自动完成大半：恢复后对账器按 raft 反向清理（本次 swarm agent 原生已做容器侧）+ 把「复活服务」呈现给用户（UI 需提示 `placement/service` 回归）；孤儿卷并入 `volumes(orphaned)` 清单。备份文档需写明：**回退 = 之后的一切变更（部署/删除）一并回滚**，不只是数据。

**重跑**：同 C5（v5-death-restore.bat 的 V5D.11 即 t0 采样；随后按分钟调 `v5-sample.bat t5|t30`）。

---

## 7. 意外发现（坑与数据点）

1. **`docker:dind` 镜像声明 `VOLUME /var/lib/docker`**（`docker inspect --format {{.Config.Volumes}}` = map[/var/lib/docker:{}])——内层引擎的全部状态（swarm 证书、卷、镜像）住在**匿名卷**里。后果：`docker commit` 不含卷 → 「commit 后重建同名容器」得到全新引擎；对 CI 的含义是 dind 状态要持久必须挂命名卷/bind mount，不能靠 commit。
2. **SIGKILL dind 后的 dockerd 重启是「PID 碰撞彩票」**：脏退出遗留 `/run/docker/containerd/containerd.pid`（与 `.sock`）和 `/var/run/docker.pid`；下次启动若这些 PID 恰好存在 → `timeout waiting for containerd to start` → 容器 Exited(1)，或恢复 >90s。本次两种形态都出现（C2 慢恢复 / C3a 崩溃）。**确定性修法：kill 前 `docker exec <dind> rm -f /run/docker/containerd/containerd.pid /run/docker/containerd/containerd.sock /var/run/docker.pid`**（脚本已内置；清理后重启 ~2s 即可服务）。
3. **dind 内 `/tmp` 不跨容器重启存活**（/opt 等常规目录正常）——实验助手脚本一律落 `/opt` 或每次自拷贝。
4. **Go flag 包的「子命令在后」陷阱**：`probe stamp write -dir X -token Y` 中 flag 解析在遇到首个位置参数 `write` 时停止，`-dir/-token` 被静默吞掉（token 空、dir 取默认值， rc=2 才暴露）。探针调用格式已改为 flags 在前（`stamp -dir X -token Y write`）。给平台的教训：CLI 参数校验必须对「flag 被吞」这类静默错误有显式测试。
5. **`docker cp` 从停止容器到宿主方向字节保真**（三文件 sha256 宿主↔容器一致）——spike/a §7#5 的坑只在宿主→dind 方向；V5 冷备因此可以直接用 `docker cp`（停止态 = 满足文档「停 Docker 冷备」要求）。
6. **worker 对 manager 失联无感知**：manager 死亡窗口内 worker 的 `docker info LocalNodeState` 保持 `active`——v5w watcher 25 分钟全窗采样零翻转（`first-info-not-active-at-ms:` 为空，`v5w.jsonl` 末行 ANALYSIS），`docker node ls` 报的又是恒定的「not a swarm manager」（非失联信号）——worker 上判断管理面可用性只能靠实际尝试集群操作（`service ls` 等）。对「应用不依赖控制面」是最强实证，但也意味着平台的失联检测（node.down 事件）必须挂在 manager 侧。
7. **节点 rm 后旧任务立即转 `Orphaned`**（CURRENT STATE 列），且 NODE 列退化为裸节点 ID（名字解析随节点记录消失）；替代任务 PENDING 无限期。评估报告「24h 后转 ORPHANED」指的是无主容器 GC 时限——两个「Orphaned」不是一回事，文档措辞需区分。
8. **`swarm init --force-new-cluster` 对已自举（auto-active）的恢复节点不报错**：rc=0 重建单 manager 集群并**轮换 worker/manager join token**（本次 token 前缀 SWMTKN-1-5xry090… ≠ 原 1pbany…）。恢复 runbook 必须写「恢复后 join token 已变」。
9. **单 manager 冷备回填是自举的**：daemon 载入 raft 后无需人工干预即 active（单成员 raft 自我选举），`--force-new-cluster` 是多 manager 丢 quorum 场景的工具。文档流程（恢复→force-new-cluster）在单 manager 场景属于「冗余但无害」。
10. **重调度瞬时副本过冲**：C2 中 kill 后 `service ls` 短暂显示 `replicated 3/2`（旧任务未确认死亡 + 替代任务已 Running）。平台做「副本数」展示/告警时要容忍这种过冲窗口。
11. **cmd.exe 批处理长跳转的三个坑**（复现脚本相关，平台无涉但记录在案）：`echo rc=%ERRORLEVEL% >> file` 的行尾数字被当 fd 重定向（数字消失）→ 改 `>>file echo …`；`find "str"` 经管道引号易被吞 → 用 `findstr /C:`；`timeout /t` 在重定向下不可用 → 用 `ping -n`。
12. **Bash 工具返回后 cmd 子进程仍在后台继续跑**（孤儿编排进程）——两次与手工介入相互踩踏。对策已固化：单 .bat 单调用，跑完以日志末行 done 标记为准。
13. **swarm init 的 DOCKER-INGRESS iptables 链不在 init 时创建**（首个发布端口的服务出现时才有）——「init 副作用」清单里 iptables 部分应按「gwbridge 管道 7 行」口径陈述。

---

## 8. 执行过程如实记录

- C2 的编排 .bat 在首次执行时被「Bash 工具返回后 cmd 孤儿进程继续跑」与手工介入相互纠缠（§7#12）；其 C2.6（重启 w1）由手工完成并快照（w1 Ready + 两任务留 mgr + 旧任务 Failed 行），时间线证据以 `c2.jsonl`（watcher 自动采集）为准。C2 的 w1 重启经历了 >90s 的脏恢复（pidfile 彩票，§7#2），随后所有 kill 实验统一加 pre-kill pidfile 清理。
- C3a 第一轮因 probe 的 flag 吞没缺陷（§7#4）写戳未发生，该轮作废重跑；第二轮（证据即上文）完整通过。
- V5 备份的 certutil 校验首跑路径写错（cp 落盘为平铺结构，脚本预期多一层 `swarm\`），已按实际结构重新校验（三文件一致）并修正 `in-v5-restore.sh` 与 `v5-backup.bat` 供重跑。
- V5b 的 30 分钟观察窗以 **t0+22min 延长采样**替代（`v5-sample.bat t22` → `artifacts/logs/v5b-sample-t22.log`；期间另有 +3min/+9min 两采样，标签 t5/t20 为调用序号），30min 整点未观测——局限已在 §6 声明。
- C1 的脚本内 `tee` 与宿主重定向争抢同一日志文件（Windows 文件锁，Permission denied）——证据经宿主重定向完整落盘（c0-run.log）；脚本已改为纯 echo。

---

## 9. 复现指引

前置：本机 Docker Desktop；仓库根目录；cmd。probe 与全部 inner 脚本由 c0-up 交叉编译/自举。所有 .bat 在仓库根目录运行，输出重定向到 `spike\c\artifacts\logs\<步骤>-run.log` 后以末行 done 标记确认完成。

```bat
:: C1 swarm init 透明性（自建 m0，~2 分钟；含 probe 交叉编译 + spike-c-br 网络）
spike\c\scripts\c0-up.bat

:: 双节点集群（mgr+w1 组网 + fleetly.node-id label，~2 分钟）
spike\c\scripts\c2-prep.bat

:: C2 节点 DOWN 重调度时间线（~4 分钟）
spike\c\scripts\c2-run.bat

:: C3a 空卷事故（~4 分钟）→ C3b 钉住+重启回绑（~4 分钟）
spike\c\scripts\c3a-run.bat
spike\c\scripts\c3b-run.bat

:: C4a drain/回岗（~4 分钟）→ C4b node rm/人工重绑（~6 分钟）
spike\c\scripts\c4a-run.bat
spike\c\scripts\c4b-run.bat

:: C5+C6 V5 恢复演练 + raft 回退孤儿命运（~12 分钟）
spike\c\scripts\v5-up.bat
spike\c\scripts\v5-backup.bat
spike\c\scripts\v5-death-restore.bat
spike\c\scripts\v5-sample.bat t5        &:: 采样点可任意时刻补打

:: 清理（拆全部 spike-c-* 容器/网络并核查四类资源为空）
spike\c\scripts\c9-down.bat
```

证据索引（全部入库）：`artifacts/logs/` 下 c0-run.log（C1 全量）、c1-udp-links.txt、c2.jsonl（时间线）+ c2-run.log、c3a-run.log + c3a-rescued-read.log、c3b-run.log + c3b.jsonl、c4a-run.log + c4a.jsonl、c4b-run.log + c4b.jsonl + c4b-rescued-stamp.json、v5-up.log、v5-backup.log、v5-swarm-hashes-before.txt、v5-death-restore.log、v5-{state-at-death,containers-at-death,taskid-at-death,taskid-after-restore,services-after-restore,nodels-after-restore}.txt、v5-restore-info.log、v5-managerdown-{1,2,3}.txt、v5w.jsonl（25min worker 连续性）、v5b-sample-{t0,t5,t20,t22}.log。`artifacts/hostbak/`（swarm 冷备，含私钥）已加入 .gitignore 不入库。

主仓回归（围栏验证）：`go build ./... && go test ./... ./sdk/go/... -race`——2026-09-17 实测全绿（见执行汇总）。

产物索引：探针 module `spike/c/cmd/probe/main.go`；脚本 `spike/c/scripts/`（宿主 .bat 编排 + dind 内 in-*.sh；`noderestart.bat`/`rescue-dind.bat` 为脏退出 dind 的恢复工具）。
