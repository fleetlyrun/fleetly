# Swarm 作为多节点底座的可行性评估（专项技术验证）

| 状态 | 日期 | 关联 |
|---|---|---|
| 已完成 | 2026-09-17 | 已采纳（D2/D12 改写，v0.1 起 Swarm 底座，V1-V7 为采纳门）：[平台架构设计](../design/2026-09-17-architecture.md)；修正[竞品调研](2026-09-17-competitive-landscape.md)第 2 节相关表述。**后续修订注记**：本文 §1/§5 的「原生单版自动回滚」建议已被否决（清空唯一 PreviousSpec、不覆盖 PENDING），发布失败动作改为 `pause` + 平台快照重放，见[发布失败与回滚语义](../design/2026-09-17-release-semantics.md) D-REL-1；第 6 节验证清单沿用并扩展为 V1-V7 + 扩展项（V5b/V6a/V6b，见交付流水线 §6）。**采纳门实测通过（2026-09-17）**：V1–V7 经 Spike A/B/C 全部实测验证（B3 端点时机关闭：带 healthcheck 端点晚于 healthy 45–87ms；B2 归位零成本成立；V4 以「优雅退出为主键」条件成立；DOWN 判定实测 13.5s 快于理论 15–16.5s）——证据与意外发现见 `spike/{a,b,c}/README.md`，结论已回写架构文档 |

## 0. 评估边界与方法

**评估问题**：Docker Swarm 能否在「不自研分布式核心、平台轻量、五年可用」三条约束下作为 fleetly 的多节点底座。

**验证范围**（4 组）：发布语义（health gate/失败不切流/回滚）、路由（Traefik + Swarm）、单节点与镜像/存储、故障语义与五年可用性。

**来源**：Docker 官方文档、moby/swarmkit 与 Traefik 源码（master，2026-09-17 抓取）、Dokploy 源码、Docker Engine 29 release notes、Mirantis 公告、CNCF 调查。源码级结论标注 [源码]，未文档化行为一律列入第 6 节 Spike 实测清单。

## 1. 发布语义：health gate / 失败不切流 / 回滚

| 结论 | 证据 | 强度 |
|---|---|---|
| 定义 healthcheck 时，新任务必须通过健康检查才进入 RUNNING：Docker 执行器 `Start()` 阻塞等待 healthy 事件，unhealthy 则杀掉容器并报错；swarmkit updater 只在新任务 RUNNING 且未删除旧任务时继续 | moby `daemon/cluster/executor/container/controller.go` L247-302；swarmkit `update/updater.go` L421-437 | 强[源码] |
| health 失败 → 容器 Shutdown → 任务 FAILED → 在 `update-monitor` 窗口内计入更新失败并触发 failure action | moby controller.go L714-733；swarmkit updater.go L200-252 | 强[源码] |
| `start-first` + 默认 `pause` = **失败不切流**：旧任务保留、更新冻结（CLI 退出码非 0）；`rollback` 把整份 Spec 回退到 PreviousSpec **并清空它** | swarmkit updater.go L592-614；proto 注释 | 强[源码] |
| Swarm 只保存**一个**历史版本；自动回滚后 PreviousSpec 为 nil，手动回滚报错；手动 rollback 在最近两版间来回 | swarmkit `objects.proto` previous_spec；controlapi L890-919 | 强[源码] |
| 默认值：parallelism=1、delay=0s、order=stop-first、monitor=5s、max-failure-ratio=0、failure-action=pause（官方文档「30s」说法过期） | swarmkit `api/defaults/service.go` L29-40 | 强[源码] |
| healthcheck 定义：`docker service --health-cmd/--health-start-period/...`；Compose 为服务级 `healthcheck:`（**不存在 `deploy.healthcheck`**） | Docker CLI 参考；Compose 规范 | 强[文档] |

**原生覆盖**：健康门、失败不切流（start-first）、单版本自动回滚。
**必须自研**：多版本历史（我们保留 N 个 digest 重放）、发布后验证窗口（monitor 默认 5s 之外的崩溃不算失败）、PENDING 与超时处理（updater 对新任务起不来**没有超时**，源码 TODO）、聚合健康查询（Manager API 的 ContainerStatus 无健康字段）、原子全量切换/金丝雀。

**已知坑**：host 端口/global + 同节点 start-first 端口冲突无校验；无连接 drain；VIP/DNS 残留（29.5.0/29.7.0 修复）；镜像拉取失败判定（29.7.0 修复 #53212）。

来源：https://github.com/moby/swarmkit/blob/master/manager/orchestrator/update/updater.go · https://github.com/moby/moby/blob/master/daemon/cluster/executor/container/controller.go · https://github.com/moby/swarmkit/blob/master/api/defaults/service.go · https://docs.docker.com/engine/swarm/services/

## 2. 路由：Traefik + Swarm

| 结论 | 证据 | 强度 |
|---|---|---|
| Traefik v3 有 **HTTP provider**：`providers.http.endpoint`、pollInterval 默认 5s、自定义 Header、mTLS；抓取/解析失败保留上一份成功配置并指数退避 | Traefik 官方文档；`pkg/provider/http/http.go` 源码 | 强[文档+源码] |
| Traefik 的 **Swarm provider 不检查健康**：task 进入 running 即注册，健康字段在 swarm 模式下永远为空（源码 `parseTasks` 不 inspect 容器）→ 与 Dokku 502 同类；改用控制面全量下发可规避 | Traefik `pswarm.go`/`config.go` 源码；官方论坛佐证 | 强[源码] |
| 后端寻址：`<service>` → VIP（默认），`tasks.<service>` → task IP 列表；VIP 属 service 级、预期随 service 生命周期稳定，但**无官方承诺**，社区有残留案例 | Docker networking 文档；Dokploy #3480 | 中 |
| Dokploy 参考实现：Traefik replicated 1 固定在 manager、host 模式发 80/443、应用路由走 **File YAML 指向 `http://<service>:<port>`**；#5189 修复 = 空 routers/services 不落盘（防止 watcher 全局阻塞） | Dokploy `traefik-setup.ts`、`application.ts`、PR #5202 源码 | 强[源码] |
| **keep-alive 陈旧后端连接**：Traefik 连接池跨过 task 替换，请求打到已退出成员；#5281 仍 open、无修复；`dnsrr` 不能解决已入池连接 | Dokploy #5281 | 强[issue] |
| Swarm 无连接 drain：仅 `--stop-signal`（默认 SIGTERM）+ `--stop-grace-period`（默认 10s） | Docker CLI 参考 | 强[文档] |
| 入口升级：host 模式无法同节点 start-first，只能逐节点 drain 升级 + `requestAcceptGraceTimeout`/`/ping` 503 缓解（≤10 节点影响秒级） | Traefik 官方 Swarm 教程；社区实测 | 中 |

**结论**：路线整体可行（可用但需自建两件事）——健康门控必须由控制面在**路由发布时机**上兑现（沿用「路由严格晚于 health」不变量）；keep-alive 连接治理需要 Traefik `serversTransport`（maxIdleConnsPerHost/idleConnTimeout）+ 应用侧 SIGTERM 后 `Connection: close`。

## 3. 单节点 / 镜像 / 存储

| 结论 | 证据 | 强度 |
|---|---|---|
| **v0.1 单节点无需 registry**：本地构建取 `RepoDigests` 的 digest 引用，任务零 pull 尝试（29.8.1 实测）；tag 引用会每次尝试 pull（失败可继续运行，需 ≥29.7.0） | moby #53212；29.8.1-dind 实测 | 强[源码+实测] |
| 多节点必须 registry（业界一致：Dokploy 文档「集群=需要」、CapRover 同） | Dokploy/CapRover 官方文档 | 强 |
| containerd 镜像存储为 29.0 新装默认；Swarm 相关影响与修复：#52698→#53212（本地镜像被误拒）、#52441（大状态 raft 快照损坏）、#52235（secrets 重挂 EBUSY）；回退 overlay2 可行但已弃用 | Engine 29 release notes；Docker 博客 | 强 |
| `docker swarm init` 副作用：ingress overlay、docker_gwbridge、2377 监听、iptables 规则；既有 `docker run` 容器不受影响；**不能 100% 透明**（`docker info` 显示 Swarm: active、网络列表多两项） | 官方文档；29.8.1 实测 | 中 |
| 备份恢复：停 Docker 冷备 `/var/lib/docker/swarm`（含 raft 与加密密钥）；恢复 = 清空 → 回填 → `docker swarm init --force-new-cluster`；autolock 需 unlock key；CA 用 `docker swarm ca --rotate` | 官方 admin guide | 强 |
| 卷：local 卷按节点各自创建、**任务被重调度后得到空卷**（数据不跟随）；删服务不删卷；bind mount 必须预存在于目标节点 | 官方 service/volume 文档 | 强 |
| CI 可行：dind 支持 `swarm init` 与服务测试（29.8.1 实测，需 `--privileged`）；矩阵应覆盖 containerd 存储与 overlay2 两条腿并锁版本 | dind README；实测 | 中 |

**建议基线**：Engine ≥ 29.8.1；无 registry 时一律 digest 引用；保持 iptables 后端（nftables 暂不支持 Swarm 节点）；init 时固定 `--advertise-addr` 与 `--default-addr-pool`。

## 4. 故障语义与五年可用性

**Swarm 故障语义（源码级）**：

- 节点失联：心跳 5s × 3 ≈ **15–16.5s 判定 DOWN**；leader 变更期先 UNKNOWN（TTL ≈30–33s）；DISCONNECTED 语义不同。
- 重调度：DOWN 后 manager 在可用节点重建任务；**约束不满足 → 停留在 PENDING 而非迁移**；**本地卷不跟随任务**；24h 后任务转 ORPHANED；节点恢复后不自动回迁（需 force update/scale）。
- manager：单 manager 故障 → 服务继续运行，但需 `--force-new-cluster`（或备份恢复）重建控制面；3 manager quorum=2；失去 quorum 时管理操作不可用、任务继续运行。
- **对我们设计的修正点**：「有卷就不迁移」不成立——stateful 应用必须用 node label + `--constraint` 主动钉住，否则 Swarm 会迁移并产生空卷。此前文档中「degraded 不迁移」的语义需要按此重写。

**五年可用性事实**：

- Docker Engine 29.x（2025-11→2026-09）约 **21 条** Swarm/overlay 修复（约 2/3 在网络层，含 DNS 残留、gossip 抖动、加密 overlay 跨版本断裂修复）；min API 1.44 在 29.3 回调至 1.40；**Swarm 未进 deprecated 清单**；nftables 的 Swarm 支持在官方路线图上。
- Mirantis 2025-07 延长支持至**至少 2030**（MKE 3，含 CSI 支持有状态负载）；>100 家企业生产使用；swarmkit v2.1.2（2026-04）。
- 无 Docker Inc. 的正式 LTS 承诺；Engine 破坏式升级是已知且将 повтор的风险类别。
- 对比：**k3s** 官方最低 server 2C/2GB、实测整机基线 ~1.6GB（含 workload），升级须守版本 skew、手动 drain、有配置丢失风险；**Nomad** 官方小集群 sizing 2-4C/8-16GB（保守）、CE 为 BSL 1.1（4 年后转 MPL）、IBM 生命周期到 2032。

**三维评分**（1-5）：

| 维度 | Swarm | k3s | Nomad |
|---|---|---|---|
| 不自研分布式核心 | 5 | 5（但要求写 controller） | 5 |
| 轻量（平台自身） | 5（内嵌引擎，无新组件） | 2（2C/2GB 起 + ~1.6GB 基线） | 2（官方 sizing 8-16GB 级） |
| 五年可用（期望值） | 3.5（维护但冻结；无 LTS；有付费兜底） | 4.5（生态最强，但升级节奏与运维成本高） | 3（生命周期明确但许可与生态受限） |
| 有状态能力 | 2（local 卷 + 约束钉住；CSI 实验性） | 4 | 4 |
| 生态与工具 | 3 | 5 | 3 |

来源：https://docs.docker.com/engine/release-notes/29/ · https://www.mirantis.com/blog/mirantis-guarantees-long-term-support-for-swarm/ · https://docs.k3s.io/installation/requirements · https://developer.hashicorp.com/nomad/docs/deploy/production/requirements · https://developer.hashicorp.com/nomad/docs/ce-license-support

## 5. 若采纳 Swarm：需要修改的设计点

1. **D2/D12 改写**：底座 = Docker Swarm（引擎内置）；控制面运行在 manager 节点；自研 node 协议取消；k3s 保留为将来可选 driver；退出预案 = 应用定义保持 Compose 兼容 + k3s driver。
2. **§2.6 失联语义重写**：stateless 服务采纳 Swarm 自动重调度（相对原设计是能力升级）；stateful 用 node 约束钉住并文档化「不迁移」；degraded 判定窗改为对齐 Swarm 心跳（15s 量级）。
3. **发布流程**：`start-first` + healthcheck + ~~`failure-action=rollback`~~（已被 D-REL-1 否决：改为 `pause` + 平台快照重放）；多版本历史、发布后验证窗口（默认 5s→ 平台建议 60s+ 观察）、PENDING 超时中止，均属平台自研层。
4. **路由**：Traefik 关闭 swarm/docker provider 自动发现；用 HTTP provider（控制面下发全量配置）或每节点文件；严格保持「路由晚于 health」+「空配置不落盘」两个不变量；绑定 `serversTransport` 治理 keep-alive。
5. **镜像**：v0.1 digest 引用、免 registry；v0.2 多节点引入 zot；构建产物一律以 digest 入库。
6. **引擎门禁**：≥29.8.1、iptables 后端、升级回归矩阵（服务名 DNS、ingress、secrets 挂载、卷、containerd 存储双模式）。
7. **备份与 DR**：控制面备份基线中加入 swarm raft 冷备与 `--force-new-cluster` 恢复演练；节点 label 反向重建（A1）继续适用。
8. **Spike 重写**：Spike B 增加 health gate 与连接池实测；Spike C 改为「join + 重调度 + 约束/卷语义 + manager 恢复」实测。
9. **风险表新增**：Engine 破坏式升级、keep-alive 陈旧连接、单 manager SPOF、有状态能力边界。

## 6. Spike 验证清单（不可退让项，源码结论转实测）

| # | 验证 | 通过标准 |
|---|---|---|
| V1 | health 失败时更新行为 | `--health-cmd` 指向必失败命令 + start-first：新任务 FAILED、更新 paused/rollback、旧任务不中断 |
| V2 | 本地 digest 镜像免 pull | 29.8.1 无 registry：service 以 `app@sha256:` 创建，任务日志无 pull 尝试、重启正常 |
| V3 | 更新期间路由稳定性 | 用 HTTP provider 下发路由：更新窗口内 curl 持续探测零失败；VIP 在更新前后不变 |
| V4 | keep-alive 陈旧连接 | 复现 #5281 场景（HTTP/1.1 连接池 + start-first 更新），用 serversTransport 参数是否消除 |
| V5 | 单 manager 故障恢复 | 备份 → `--force-new-cluster` 恢复演练，应用不中断、管理面恢复 |
| V6 | 卷与约束语义 | 有卷服务无约束时迁移得空卷（复现）；加约束后任务钉在节点 |
| V7 | 引擎升级回归 | dind 矩阵（containerd 存储 + overlay2）跑 V1-V6 的子集 |

## 7. 结论与建议

**建议：采纳 Swarm 作为多节点底座**，但以第 6 节 V1-V7 作为采纳门——任一验证不通过且无缓解手段，则回退到 k3s driver 或自研 node 方案（两者设计均在文档/Git 历史中，回退成本有限）。

**采纳后的三个最大风险**（横向验证结论）：
1. Docker Engine 破坏式升级（v29 类事件会重演）→ 锁版本 + hold + 升级回归矩阵 + `min-api-version` 兜底。
2. 有状态能力弱（local 卷不跟随、CSI 实验性）→ 状态外置优先（外部 DB/S3），必须跟卷的服务钉节点，文档明示无迁移。
3. 无正式 LTS + 单 manager SPOF → 冷备 + force-new-cluster 演练 + Mirantis MKE 作为付费兜底选项。

## 8. 不确定项

1. 「有 healthcheck 时更新等待 healthy」无官方文档背书（源码链推断），必须实测（V1）。
2. Swarm 控制面 135MB idle 为第三方单来源数据（Docker 27.1.1），无第二来源，需自测。
3. VIP 在 service update 期间的稳定性无官方承诺（V3 实测确认）。
4. 29.x Swarm 修复条目「约 21 条」为人工去重计数，口径见正文。
5. k3s/Nomad 资源数字为官方整机基线或保守 sizing，与进程 RSS 口径不同，不可直接对比。
6. Dokploy 应用侧 Swarm 部署参数未逐行核对，其 issue 仅作行为证据。
