# e2e/nightly — V1-V7 永久回归骨架（T1.4 / 交付流水线 P3）

把 Spike A/B/C 的验证脚本提炼为 nightly 回归：每天（及手动触发）在
`docker:29.8.1-dind` 内重放 Spike 依赖的 Swarm 底座行为，红则阻断发版证据。
设计依据（只读）：`docs/design/2026-09-17-delivery-pipeline.md` §2.2 nightly
轨道、§6 V1-V7 映射、P3；`docs/design/2026-09-17-architecture.md` §6。

来源纪律：**不复制 spike 代码**——探针二进制从 `spike/b/cmd/probe` 与
`spike/c/cmd/probe`（独立 Go module）现编译；fixture 模板原样引用
`spike/b/dockerctx/Dockerfile.fixture`；本目录脚本为从 spike 实验脚本提炼
的最小转译，头注均注明来源 spike 脚本。`spike/**` 只读。

## 文件

| 文件 | 层 | 作用 |
| --- | --- | --- |
| `run.sh` | 宿主 | 回归 runner：起 dind、exec+stdin 送入脚本/二进制（大小+sha256 校验）、跑断言、失败 dump dind 日志、`always` 清理。CI 与本地同一入口 |
| `v6.sh` | 宿主 | V6 双 dind 组网后的实验编排（纯 `docker exec`，不碰外层引擎的服务面） |
| `lib.sh` | dind 内 | 共享助手 + PASS/FAIL 记账（源自 spike/b in-b1 助手段） |
| `infra-b.sh` | dind 内 | spike/b 式基建：swarm init、overlay、traefik v3.5（钉 v3 线）、3 个 fixture 镜像、cfgsvc、edge 服务、DNS 自检 |
| `v1.sh` / `v2.sh` / `v3.sh` / `v4.sh` | dind 内 | 每个 V 项一个断言入口，输出 spike 同风格 `NAME: PASS/FAIL` 行，失败非零退出 |
| `n-waitsvc.sh` / `n-stamp.sh` | dind 内 | V6 节点侧助手（源自 spike/c in-waitsvc.sh / in-stamp.sh） |

## 入口与拓扑

```
bash e2e/nightly/run.sh <suite>...      # suite = v1 | v2 | v3 | v4 | v6 | all
env: DIND_IMAGE（默认 docker:29.8.1-dind）
     DIND_EXTRA_ARGS（透传 docker run，如 --storage-driver overlay2）
```

| suite | 拓扑 | 内层脚本 |
| --- | --- | --- |
| v1 | 单 dind | infra-b.sh → v1.sh |
| v2 | 单独全新 dind（dockerd 日志取证需无噪声） | v2.sh + 宿主侧 dockerd 日志 grep |
| v3 | 单 dind | infra-b.sh → v3.sh |
| v4 | 单 dind | infra-b.sh → v4.sh |
| v6 | 同宿主 bridge 网三 dind（mgr/w1/w2，swarm join 组网 + `edgefleet.node-id` label） | run.sh 组网 → v6.sh |

传文件一律 exec+stdin（docker cp 宿主→特权 dind 静默丢文件，见
`e2e/README.md` 已知问题），送入后做大小 + sha256 双校验，文本脚本再
`sed -i 's/\r$//'`（Windows CRLF 工作区防护）。

## V 项 ↔ 断言 ↔ 来源 ↔ 局限（清单表）

| V 项 | 断言（本目录脚本） | 来源 spike 脚本 | 本期覆盖 | 已知局限 |
| --- | --- | --- | --- | --- |
| V1 | `V1-ASSERT-1 UPDATE_PAUSED` / `2 NEW_TASK_FAILED` / `3 OLD_TASK_STILL_RUNNING` / `4 EXTERNAL_PROBE_ZERO_FAILURE`（v1.sh） | spike/b `in-b1-v1b2.sh` B1 段 | 全量 | 单副本；prober 60s 窗口（与 spike 同参） |
| B2（随 V1 顺带） | `B2-ASSERT-1 OLD_TASK_ID_UNCHANGED_AFTER_REPLAY` / `2 NO_EXTRA_TASK_CREATED` | 同上 B2 段 | 全量 | — |
| V2 | `V2-ASSERT-1 DIGEST_CREATE_TASK_RUNNING_AND_LIVE` / `2 DIGEST_FORCE_TASK_RUNNING_AND_LIVE`（v2.sh，dind 内）；`V2-ASSERT-3 DOCKERD_LOG_ZERO_DIGEST_PULL` / `4 TAG_CONTROL_PULL_DETECTED`（run.sh 宿主侧，grep dind 容器日志 `manifests/sha256` / `manifests/v1`） | spike/a `e5-v2-digest.sh` | T1/T2/T3（T4 裸 sha256 探针未收录） | ①pull 只在 dockerd 日志可见（events 零行，spike/a #12），故断言在宿主侧；②T3 对照依赖出网可达 registry-1.docker.io（离线环境 ASSERT-4 会红=检测通道失效，如实暴露）；③专用全新 dind 保证日志无噪声 |
| V3 | `V3-ASSERT-1 ROUTING_ZERO_FAILURE` / `2 VIP_STABLE`（v3.sh） | spike/b `in-b3-v3v4.sh` V3 段 | 全量 | 100ms 采样；单副本；Traefik v3.5 合成路由 |
| V4 | `V4-ASSERT-1 ABRUPT_INFLIGHT_POST_KILL_SURFACES_502`（≥1）/ `2 GRACEFUL_INFLIGHT_POST_ZERO_FAILURE` / `3 KEEPALIVE_IDLE_POOL_ZERO_FAILURE` / `4 TRAEFIK_CONFIG_APPLIED_CLEAN`（v4.sh） | spike/b `in-b3d-v4post.sh`（A/C 相）+ `in-b3-v3v4.sh` serversTransport 段 | 最小子集 | 未收录：R1 空闲池干净退出对照组、R2 GET in-flight 三配置矩阵、`os.Exit` 陷阱正反证（属探针实现细节，回归价值在 A/C 两端点） |
| V6a | `V6A-ASSERT-1 UNPINNED_TASK_MIGRATED_TO_MGR` / `2 EMPTY_VOLUME_ACCIDENT` / `3 NEW_NODE_STAMP_MISSING` / `4 ORIGINAL_DATA_PRESERVED_ON_DEAD_NODE` / `5 PINNED_STAYS_PENDING_NO_NEWTASK` / `6 AUTO_REBIND_AFTER_RESTART` / `7 SAME_VOLUME_AFTER_ROUNDTRIP` / `8 STAMP_INTACT_AFTER_ROUNDTRIP`（v6.sh，双 dind） | spike/c `c3a-run.bat` / `c3b-run.bat`（+ in-waitsvc/in-stamp） | C3a+C3b 全量（kill 用宿主 SIGKILL） | ①三 dind 组网在 Actions runner 的稳定性未经验证（首跑 nightly 才知；按交付 §5 风险只放 nightly、失败开 issue）；②w2 重启用「kill 前 pidfile 预清理 + docker start」，noderestart.bat 的 rescue-dind 兜底未转译（预清理后 2s 级恢复，未观测到需要兜底） |
| V6b | `V6B-ASSERT-1 DRAIN_BLOCKS_APP` / `2 VOLUME_DATA_ALIVE_DURING_DRAIN` / `3 AUTO_REBIND_AFTER_ACTIVE` / `4 DATA_INTACT_AFTER_ROUNDTRIP`（v6.sh，drain/回岗） | spike/c `c4a-run.bat` | C4a 全量 | 未收录 C4b（node rm→永久 PENDING→人工 rebind→docker cp 抢救）：需第四个 dind join + 死容器 cp 编排，见下「未自动化项」 |
| V5/V5b | —（占位 job `v5-recovery-drill`，`if: false`） | spike/c `v5-up.bat` → `v5-backup.bat` → `v5-death-restore.bat`（+ `v5-sample.bat`） | **未自动化** | 依赖宿主侧「停止态 docker cp 冷备 → 同 IP 换 dind 容器 → dockerd 重启载 raft（+可选 force-new-cluster）→ 25 分钟 worker 连续性采样」编排，转译成本高。转译要点已在 spike/c README §5/§6（冷备 sha256 校验、同 IP 复用、join token 轮换注意）。启用时补 v5.sh（宿主编排）+ workflow job 去 `if: false` |
| V7 | — | — | **未自动化** | 引擎矩阵属 T2.24：双存储腿（containerd 默认/overlay2）与上一 minor 腿以 matrix 展开，run.sh 已预留 `DIND_IMAGE`/`DIND_EXTRA_ARGS` |

## CI 路径（`.github/workflows/nightly.yml`）

- `schedule` 每日一次 + `workflow_dispatch`；`concurrency` 组不互相取消
  （红即证据，不允许重试掩盖）；`ubuntu-latest`；引擎单腿
  `docker:29.8.1-dind`（矩阵腿占位注释，T2.24）。
- 五个 suite job（v1/v2/v3/v4/v6）各自独立 dind，失败隔离；每个 job 的
  步骤就是 `bash e2e/nightly/run.sh <suite>`——与本地完全同一条命令。
- 失败时 run.sh 自身 dump dind 日志尾部；workflow 另有 `if: failure()`
  dump 兜底（runner 级超时场景）与 `if: always()` 清理
  （容器 + `edgefleet-nightly-br` 网络）。
- v5-recovery-drill 为 `if: false` 占位 job，注明转译来源。

## 本地复跑（Windows Docker Desktop）

前置：Docker Desktop 运行中（宿主引擎可低于门禁；dind **镜像**固定
`docker:29.8.1-dind` 与 CI 一致）；Git Bash（仓库脚本为 POSIX sh + LF；
若工作区被 autocrlf 转成 CRLF，run.sh 会对送入 dind 的文本脚本自动去
CR，run.sh 自身需保持 LF 执行）；Go 在 PATH（v2 之外需要，用于编译
spike 探针）。

```bash
# 单项
bash e2e/nightly/run.sh v1
bash e2e/nightly/run.sh v2
# 全套
bash e2e/nightly/run.sh all
```

产物（探针二进制、dockerd 日志、V6 戳读回证据）全部落在 `mktemp -d`
临时目录，退出即弃，不入仓库。运行前后可用
`docker ps -a --filter name=edgefleet-nightly-` 核查无残留（run.sh 启动时
也会清扫同前缀幽灵容器，spike/a README #11 教训）。

## 已知限制

- **多节点腿的 runner 稳定性**：v6 双 dind（+1 牺牲 worker，共三容器）在本机
  Docker Desktop 实测通过；GitHub 托管 runner 上未验证，属交付 §5 已登记
  风险（只放 nightly、连续失败开 issue、必要时 self-hosted）。
- **观察窗类断言靠固定 sleep + 收敛轮询**：V6 的 w2 kill 断言按「节点 Down +
  活节点零 Running + 空NODE Pending 行」轮询收敛（上限 90s；本机实测 ~7s），
  drain 段为固定 20s 后断言（drain 是控制面动作，秒级到位，Spike C4a 实测
  ≤0.9s）；量级基准均为 Spike 实测（Down 判定 ~13.5s）。若上游行为漂移到
  超出窗口，断言红即为信号。注意：kill 后死节点的旧任务行会以
  `|w1|Running` 滞留数十秒（Spike C2 实测 ≤40s），任何「任意节点 Running」
  轮询都会被它瞬时误满足——断言必须按节点限定（本目录已按此实现）。
- **V2 的对照腿依赖外网**（见清单表局限行）。
- 引擎版本门禁由 `DIND_IMAGE` 固定；脚本内不出现版本号断言。
