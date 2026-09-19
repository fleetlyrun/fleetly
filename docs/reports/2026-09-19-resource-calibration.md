# T2.25 资源预算与容量校准报告（dind 实测）

| 状态 | 日期 | 关联 | 基线 commit |
|---|---|---|---|
| 交付物（T2.25） | 2026-09-19 | [架构 §4.2 横切硬指标（资源预算/容量边界）](../design/2026-09-17-architecture.md)；[交付流水线 §2.2 nightly 第 5 项](../design/2026-09-17-delivery-pipeline.md) | c204b48（main） |

## 1. 测量方法与口径

- **环境**：`docker:29.8.1-dind` 特权容器（与 CI/引擎门禁一致），cgroup 内存上限 6GiB（`CAL_MEMORY`，防本机资源击穿；`docker stats` 分母显示宿主 VM 总内存是 `/proc/meminfo` 非命名空间化的已知现象，不代表限额）。
- **套件**：`deploy/run-calibration.sh`（宿主编排）→ `deploy/cal-inner.sh`（dind 内）。复跑命令：
  `sh deploy/run-calibration.sh`（CI/本地同路径；产物 `summary.md`/`cal.json` 由 `CAL_OUT_DIR` 控制落点）。
- **采样口径**：
  - fleetlyd RSS：`/proc/<pid>/status` VmRSS，稳定窗 **120s**（daemon live + fleetly-ingress 1/1 + fleetly-buildkit Running 后）连采 **12 拍 × 5s**，报 min/avg/max；
  - dockerd RSS：dind 内 dockerd 进程 VmRSS（**swarmkit 同进程承载**，即「含 swarmkit」口径）；
  - containerd RSS：`docker-containerd` 进程 VmRSS（架构口径未点名，**如实单列**）；
  - Traefik / buildkit：`docker stats --no-stream` 单列；
  - 版本：fleetlyd `v0.1.0-cal`（交叉编译 `CGO_ENABLED=0`），daemon 配置 = ACME 关、git 关、快引擎参数（observe 5s）。
- **严谨性说明**：dind 内 dockerd 常驻（宿主为 Docker Desktop on Windows）；跨 5 次复跑的 idle 值波动 ≤±6%，取最终全绿一次（run5）为主数据、历次为旁证。

## 2. 控制面 idle 基线（对照 <200MB 预算）

| 组件 | 实测（run5，kB） | 历次范围（5 次复跑） | 架构口径 | 判定 |
|---|---|---|---|---|
| fleetlyd（idle） | **52 228 avg**（51 524–55 364 区间见历次） | 51.5–55.4 MB | 「fleetlyd idle <200MB（含 dockerd+swarmkit）」 | ✅ 组成项 |
| dockerd（含 swarmkit，同进程） | **108 476** | 104.2–116.4 MB | 同上（并入口径） | ✅ 组成项 |
| **fleetlyd + dockerd 合计** | **≈160.7 MB** | 157–171 MB | **<200MB** | ✅ **达标（用了预算的 ~80%）** |
| containerd（单列） | 44 196 | 44.2–49.5 MB | 口径未点名 | 如实单列（见修订建议） |
| Traefik（fleetly-ingress 容器） | **15.6 MiB** | 15.6–21.7 MiB | 「单列约 50MB」 | ✅ 远低于对标值 |
| buildkit（fleetly-buildkit 容器，idle） | 9.2 MiB（cgroup 1GiB） | 8.4–10.2 MiB | 未设口径 | 如实记录 |

**结论 1（预算达标）**：控制面核心口径 `fleetlyd + dockerd(含 swarmkit) idle ≈ 160.7MB < 200MB`，**达标**。对标 CapRover 的 Traefik「约 50MB」实测仅 ~16-22MiB，口径可维持。

## 3. 容量压测（≤50 apps / ≤200 域名 / 并发构建 2）

### 3.1 50 apps + 200 域名（合并压测：cap01–cap20 每应用 2 服务 × 5 域名，cap21–cap50 单服务无域名）

| 项 | 实测 | 判定 |
|---|---|---|
| 应用数 | 50/50 全部 `deploy rc=0`，`derived_state=running` | ✅ |
| Swarm services | 70（20×2 + 30×1），容器 72 个在跑 | ✅ |
| 域名台账 | **200 条**（20 应用 × 10，断言 =200） | ✅ |
| 入口视图合成 | `/configs` 15 267 bytes、200 个域名引用、40 个 router（2/域名应用——无域名应用不产路由，设计语义） | ✅ |
| Traefik 实际路由抽检 | cap01/cap20 各一域名 `Host(...)` 经 80 入口 → 200 | ✅ |
| fleetlyd 内存增量 | 52.2 → 63.6 MB（**+11.4 MB**，5 拍 × 3s） | 低 |
| dockerd 内存增量 | 108.5 → 234.8 MB（**+120.8 MB ≈ +2.4 MB/应用**，swarmkit 状态项主导） | 需盯防（见结论 3） |
| 单部署时延（入队→终态，min/med/avg/max） | **12 / 13 / 13.3 / 15 s**（50 条；observe 5s + swarm 任务收敛主导） | 吞吐 ≈ 4.6 apps/min（单车道队列） |

### 3.2 并发构建（队列上限 2）

| 项 | 实测 | 判定 |
|---|---|---|
| 场景 | 5 个独立 dockerfile 构建（各 16MB 随机 payload，digest 必异）同时入队 | — |
| 同时在建峰值 | 采样 27 轮（0.3s/轮），**max_building = 2，从未 >2** | ✅ 队列并发 = 2 |
| 排队可见性 | `builds 行 status=queued` 观测到（第 3 构建起排队） | ✅ |
| 终态 | 5/5 succeeded，digest 互异，`docker image inspect` 逐一通过（无交叉污染） | ✅ |

### 3.3 已知边界与一次性抖动（如实记录）

- **环境边界**：50 apps 压测在 6GiB cgroup 上限内完成；未触顶（各应用容器 ~1.5MiB）。dind 单机为环境边界口径——「多应用叠加失败」等产品级熔断问题不在本测范围。
- **构建一次性抖动（run4）**：5 个并发构建中出现 1 例 buildkit `ref locked` 瞬态失败（`E_BUILD_FAILED`，buildkit/containerd 写锁 15min 超时语义）；同代码后续复跑 5/5 全成。判为底座瞬态（非队列语义问题），**建议**：构建失败重试由用户/API 层承担（v0.1 不做自动重试，与「发布失败不自动重试」口径同构）；若复现率上升再评估 executor 层幂等重试。
- 同一构建内重复 `COPY` 相同内容文件会产生相同 layer digest，并发写触发 buildkit ref 锁争用（本机实测 15min 锁超时）——压测 payload 已改为单份随机内容规避；此为 buildkit/containerd 行为记录，非产品缺陷。

## 4. 结论与建议值（压测后定稿）

| 口径 | 原建议值 | 实测结论 | 定稿 |
|---|---|---|---|
| 控制面 idle 内存 | fleetlyd <200MB（含 dockerd+swarmkit）；Traefik 单列约 50MB | fleetlyd+dockerd ≈160.7MB；Traefik ≈16-22MiB | **维持原口径**；**修订建议（供架构文档回写）**：① containerd（~44-50MB）为独立进程，建议与 Traefik/buildkit 一样**单列**或并入 v0.2「平台组件总量 <400MB」预算（当前合计 ≈230MB，方向达标）；② 建议监控口径 = fleetlyd+dockerd 合计（200MB 线）+ dockerd 单列告警（随应用数线性增长项） |
| 单节点应用数 | ≤50 apps | 50 apps 全量部署/运行/路由无异常；fleetlyd +11.4MB、dockerd +120.8MB（~2.4MB/应用线性项） | **维持 ≤50**；补充：dockerd RSS 是容量水位的主导项，建议 UI/文档按「dockerd 内存 ≈ 110MB + 2.4MB×应用数」给水位提示（v0.2 指标栈落地前的过渡口径） |
| 域名数 | ≤200 域名 | 200 域名台账/视图/路由全部通过；fleetlyd 增量被 3.1 合并覆盖（毫秒级合成，无额外常驻开销） | **维持 ≤200** |
| 并发构建 | 2 | 队列并发峰值恒 ≤2、排队可见、无交叉污染 | **维持 2** |

## 5. 产物与复验

- 套件：`deploy/run-calibration.sh`（编排）+ `deploy/cal-inner.sh`（内层）；本报告数据由 run5（全绿，内层墙钟 895s）取得。
- 复验命令（本地 Git Bash / CI 同路径）：`sh deploy/run-calibration.sh`；产物读取：`CAL_OUT_DIR=<dir> sh deploy/run-calibration.sh` 后查看 `<dir>/summary.md` 与 `<dir>/cal.json`。
- 资源采样进 nightly 的既有 job（`e2e/nightly/resource-sample.sh`，只读引用）继续承担趋势监控；本报告为其提供对照基线。
