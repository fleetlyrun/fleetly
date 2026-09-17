# Spike A — 构建与镜像验证 FINDINGS

状态：**已完成**（2026-09-17，两段执行：前序代理完成 E0/E1 与 E2a 首步后超时；续作代理完成 E2a–E5 全部剩余实验并复核前序成果）。

环境基线：本机 Docker Desktop 29.7.2（宿主）+ 特权 `docker:29.8.1-dind`（spike/a 独立 module，Go 1.26.6）。所有依赖 Engine ≥29.8 行为的实验在 dind 内执行；buildkitd 钉 `moby/buildkit:v0.32.2`，registry 钉 `registry:2.8.3`。Railpack 以 **Go 库**形式钉 `github.com/railwayapp/railpack v0.39.0`（spike/a/go.mod）。

设计依据：`docs/design/2026-09-17-architecture.md` §4.1 Spike A 行（验收真源）、`docs/research/2026-09-17-competitive-landscape.md` §5、`docs/research/2026-09-17-swarm-substrate-assessment.md` §6 V2。

---

## 0. 实验矩阵总览

| # | 假设 | 方法 | 结果 | 结论 | 设计影响 | 执行者 |
|---|------|------|------|------|----------|--------|
| E1 | Railpack 钉版本后 plan JSON 逐字节可复现；不钉则漂移 | dind 内 `spikerailpack prepare` 双跑 + byte-compare + 无钉对照 | ✅ 钉版本双跑逐字节一致；unpinned 出现 node 22.17.0→22.23.2 漂移且 secrets 数组消失 | 钉版本 + plan 归档机制成立，漂移风险真实存在 | 每次构建前生成并归档 plan JSON；版本由平台注入而非信任应用仓 | 前序完成，续作逐字节复核 ✅ |
| E2a | 本地层缓存显著加速；railpack 对 secret 变化正确失效/恢复 | 同输入双构建计时；tokA→tokA→tokB→tokA 四连构建；plan 层 secret 对比 | ✅ 85.09s→4.17s（全 CACHED，~20x）；tokB 使 secret 相关层重执行（3.11s）、tokA 回退全 CACHED（1.59s）；plan 字节与 secret 值无关且不含 secret | 本地层缓存与 secrets-hash 失效（正面+反面）全部成立 | 缓存语义可直接依赖 railpack 默认；平台无需自研 hash 戳 | 前序中断于 A1，续作完成全部 ✅ |
| E2b | registry cache 可作为跨 buildkitd 实例的缓存层；导入后重建显著加速 | `--cache-to type=registry,mode=max` 导出→`buildctl prune --all` 清空本地（du=0B 验证）→无 cache-from 冷对照→有 cache-from 导入 | ✅ 导出后 registry catalog 出现 `spike-a-cache`；冷对照 889.47s vs 导入 8s（全 CACHED，~111x） | registry cache 往返成立；跨节点/重建 buildkitd 场景缓存可迁移 | v0.2 多节点可共享 registry cache；buildkitd 可随时重建不丢缓存能力 | 续作（前序脚本有 prune bug，已修复后重跑） |
| E2c | 纯 BuildKit 层面存在「secret 变化不失效」事故；hash 戳可缓解 | `--mount=type=secret` 事故 Dockerfile vs `ARG NPM_TOKEN_HASH` 缓解 Dockerfile，buildctl 直驱 | ✅ 事故复现：换 token 后全 CACHED，产物指纹仍是旧 token（镜像 config sha256 完全相同）；缓解成立：hash 变→重执行、指纹随新 token 变化 | railpack 的 secrets-hash 是对真实 BuildKit 缺陷的必要补偿；Dockerfile 兜底路径必须平台强制注入 hash 戳 | Dockerfile 一等路径需平台侧注入 `sha256(secret)` ARG 的构建约定 | 续作 |
| E3 | 构建期凭证不进最终镜像（history/Env/文件系统/plan 四个面） | ARG 反模式对照 + 四镜像 history/Env 检查 + 文件系统定向 grep + plan grep | ✅ ARG 值确认进 history（反模式成立）；secret 挂载构建的四镜像全部 CLEAN；`/run/secrets` 不存在于最终镜像；归档 plan 不含 secret 值 | `--mount=type=secret`（railpack step secrets）凭证零残留；平台必须禁止用户 ARG 传 secret | 应用模型需显式声明 secrets（进 hash 失效集），禁止环境变量直通 ARG | 续作（E3.4 全盘 grep 卡死已改定向扫描） |
| E4 | buildkitd 在硬 cgroup 限额（1GiB/1.5CPU）内可完成构建；rootless 可行性 | 重建 buildkitd 带 `--memory 1g --cpus 1.5` 冷构建×2 + 热重建；rootless 变体探针 | ✅ 限额生效、574.17s 冷构建完成、峰值 601.5MiB/1GiB、无 OOM；rootless 变体在内核 6.18 可启动可构建（外层部署形态未验证，见 §5.2） | v0.1 采用 privileged buildkitd + cgroup 硬限额；rootless 列为外层形态后续评估 | buildkitd 必须带内存/CPU 限额交付；构建失败第一来源是外网（apt 镜像源抖动）而非资源 | 续作 |
| E5 | 单节点 swarm 下 service 以 `名称@sha256:<imageID>` 创建零 pull 尝试；`--force` 更新正常 | dind 内 swarm init→digest 引用创建→events/dockerd 日志取证→`--force` 更新→tag 引用对照→裸 sha256 探针 | ✅ T1/T2/T4 零 pull 且任务 Running、探活成功；T3 tag 对照有真实 pull 尝试（dockerd 日志 2 行）失败后回退本地仍运行 | D9「v0.1 起本地 digest 引用免 registry」在 29.8.1 实测成立；多节点需镜像分发（create 提示原文） | v0.1 无 registry 架构确认；发布语义按 digest 引用设计；本地部署镜像禁用 buildx provenance | 续作 |

通用结论标注：✅=实测证据支持；⚠️=有证据但有局限（局限明示）。

---

## 1. E1 Railpack 钉版本 + plan 可复现（前序成果，续作复核）

**方法**：`scripts/in-e1-prepare.sh` 在 dind 内运行 `spikerailpack prepare`（驱动 = 上游 railpack CLI 命令经 Go 库重组，`cmd/spikerailpack/main.go`）。

**原始输出**（`artifacts/logs/in-e1.log`）：

```
===== E1.6 byte-compare =====
NODE_PLAN_REPRODUCIBLE sha256=49bc9ebb20c3242c7f9a0ca3188bdf96cb8ef00a45ee62aeb45766f6747f10b7
GO_PLAN_REPRODUCIBLE sha256=8a44e8a74d8fec8a59a01310ca77cdd0f1608d9f7007b4fe7c65bbf5f6eba6ff
UNPINNED_DIFFERS (see diff below)
--- /work/plans/node.plan.json
+++ /work/plans/node.plan.unpinned.json
-        "generated-mise-toml": "[tools]\n  node = \"22.17.0\"\n ..."
+        "generated-mise-toml": "[tools]\n  node = \"22.23.2\"\n ..."
-  "secrets": [
-    "RAILPACK_NODE_VERSION"
-  ],
```

**续作复核**（2026-09-17，宿主对 `artifacts/plans/` 独立重算 sha256）：

```
49bc9ebb20c3242c7f9a0ca3188bdf96cb8ef00a45ee62aeb45766f6747f10b7  node.plan.json
49bc9ebb20c3242c7f9a0ca3188bdf96cb8ef00a45ee62aeb45766f6747f10b7  node.plan.repro.json
8a44e8a74d8fec8a59a01310ca77cdd0f1608d9f7007b4fe7c65bbf5f6eba6ff  go.plan.json
8a44e8a74d8fec8a59a01310ca77cdd0f1608d9f7007b4fe7c65bbf5f6eba6ff  go.plan.repro.json
93ee14c390da7904d8b5bc6f1681008ce1c13e9a4161ad07527cea74b79c7f83  node.plan.unpinned.json
```

pinned 与 repro 两两逐字节一致，hash 与 in-e1.log 声明一致。

**附带证据（D6）**：`spikerailpack` 本身就是把 `railwayapp/railpack v0.39.0` 作为库导入后重组上游 CLI 命令（Build/Prepare/Plan/Schema/Info），能编译、能出 plan、能构建——「Railpack 是可复用 Go 库」在该版本成立。plan 中还可见 railpack 生成的 mise 配置自带 `minimum_release_age = "14d"` 与 `paranoid = true`（供应链友好默认，见 `artifacts/plans/node.plan.json` generated-mise-toml 字段）。

**设计影响**：架构 §4.1「从源码到可运行镜像可复现（钉版本 + plan JSON 归档）」——通过。平台必须自己注入 `RAILPACK_NODE_VERSION`/`RAILPACK_GO_VERSION` 等版本 pin（unpinned 时 railpack/mise 会静默解析到最新版，两天内就出现 22.17.0→22.23.2 漂移）。

**重跑**：`spike\a\scripts\e1-prepare-plans.bat`（需先 `e0-up.bat`）。

---

## 2. E2a 本地层缓存 + railpack secrets-hash 失效（续作完成；前序中断于首个冷构建）

**方法**：`scripts/in-e2a-build-cache-local.sh`。同输入双构建计时；随后 tokA→tokA→tokB→tokA 四连构建观察 CACHED 集合变化；plan 层面对比不同 secret 是否改变 plan 字节。

**原始输出**（`artifacts/logs/in-e2a.log`，节选）：

```
===== BUILD-A1 node first build (cold baseline) =====
│ Successfully built image in 85.09s │
===== BUILD-A2 node rebuild same input (expect CACHED + shorter) =====
#6 CACHED … #26 CACHED（20 行全 CACHED）
│ Successfully built image in 4.17s  │
===== BUILD-B go =====
│ Successfully built image in 14.55s │
```

secrets-hash 四连（tag=sec）：

| 构建 | token | 耗时 | CACHED 集合 |
|------|-------|------|-------------|
| sec-1 | tok-a | 1.96s | #6–#26 全 CACHED |
| sec-2 | tok-a | 1.87s | 全 CACHED |
| sec-3 | **tok-b** | **3.11s** | **#3,#9–#11,#17–#26 CACHED；#4–#8,#12–#16 缺席 = 重执行** |
| sec-4 | tok-a（回退） | 1.59s | 全 CACHED |

tokB 构建时 secret 相关层（install/build 段）失效重执行，无关层（系统包、mise 安装）保持命中；换回 tokA 后全部恢复命中——失效范围精确限定在声明了 secret 的层。

plan 层面（`in-e2a.log` 尾部）：

```
PLAN_WITH_DIFFERENT_SECRETS_IDENTICAL (hash is computed at BUILD time, not archived)
SECRET_VALUE_NOT_IN_PLAN
```

**可运行性证明**（`artifacts/logs/in-e2a-runnable.log`，续作补证）：

```
node container ip=172.18.0.2  go container ip=172.18.0.3
node / -> spike-a-node-app OK stamp=unset
go / -> spike-a-go-app OK stamp=unset
--- why first attempt failed (runtime image has no wget):
NO_WGET
```

> 坑：railpack 运行时镜像无 wget 也无可用的 shell 工具链，`docker exec <ctr> wget` 不可行；需从外部按容器 IP 探测。另 Engine 29.8.1 的 `docker inspect` 已移除 `.NetworkSettings.IPAddress` 顶层字段，须用 `{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}`。

**结论**：架构 §4.1「二次构建显著加速且 env 变化正确失效」——通过（railpack 侧）。

**重跑**：`spike\a\scripts\e2a-build-cache-local.bat`；可运行性补证 `sh scripts/in-e2a-runnable-fix.sh`（dind 内）。

---

## 3. E2b registry cache 往返（续作，修复前序脚本 bug 后重跑）

**方法**：`scripts/in-e2b-registry-cache.sh`。`--cache-to type=registry,ref=spike-a-registry:5000/spike-a-cache:node,mode=max` 导出；`buildctl prune --all` 清空本地（**必须无 `--force`**，见坑 #1）；`buildctl du` 验证 0B；无 `--cache-from` 冷对照；prune 后带 `--cache-from type=registry` 重建。

**关键原始输出**（`artifacts/logs/in-e2b.log`，2026-09-17 重跑）：

```
===== E2b.3 registry catalog after export =====
{"repositories":["spike-a-cache"]}
===== E2b.4 wipe local buildkit cache entirely =====  (prune 列出 2.96GB 记录)
--- buildctl du after prune (expect no records):
Reclaimable:	0B
Total:		0B
===== E2b.5 control: rebuild WITHOUT --cache-from after prune (cold) =====
start 16:11:28
│ Successfully built image in 889.47s │
end 16:26:18
===== E2b.6 prune again, rebuild WITH --cache-from =====  (du 再次 0B)
start 16:26:20
#7 CACHED … #26 CACHED（20 行全 CACHED）
end 16:26:28
```

**结论**：本地缓存清空（du=0B 双重验证）后，无 cache-from 冷对照 889.47s，带 registry cache-from 导入 8s 且全 CACHED——缓存确实来自 registry 往返，加速 ~111x。架构 §4.1「缓存三情形」之 registry cache——通过。

**注**：889s 冷对照显著慢于 E2a 的 85s 冷基线，差异纯由外网带宽波动造成（buildkitd 网络计数器从 ~0 拉到 2.02GB）——这本身是有价值的数据点：冷构建成本被外网主导且方差极大，缓存的价值远超常规估计。

**重跑**：`spike\a\scripts\e2b-cache-registry.bat`（含修复后的 prune 语法）。

---

## 4. E2c raw BuildKit：secrets 事故与缓解（续作）

**方法**：`scripts/in-e2c-raw.sh` + `scripts/raw-ctx/Dockerfile.{accident,hash}`，buildctl 直驱钉版 buildkitd。

**事故复现**（`artifacts/logs/in-e2c.log`）：

```
--- vA (token A, 冷):
manifest inside vA: fingerprint:144a0d011fad        # = sha256("tok-a-123456") 前 12 位
#9 exporting manifest sha256:2177fd355dd8… 
--- vB (token B, 同 Dockerfile):
#6 CACHED #7 CACHED #8 CACHED                        # secret 挂载不在 RUN 缓存键内
manifest inside vB (built with TOKEN B): fingerprint:144a0d011fad   # 仍是 token A 的指纹 = 陈旧
#9 exporting manifest sha256:2177fd355dd8…           # 与 vA 完全相同的镜像
```

换 secret 后构建零失效，产物内容仍是旧 secret 的结果——Dokploy PR #4557 事故在纯 BuildKit 层面复现（强证据：vA/vB 镜像 config sha256 逐字节相同）。

**缓解**（hash 戳进 ARG，`Dockerfile.hash`）：

```
HASH_A=144a0d011fad8b86 HASH_B=7b1536a7ad62ae10
--- hA (hashA+tokenA): fingerprint:144a0d011fad；config sha256 c160ee17…
--- hA2 (同输入):     全 CACHED；config sha256 c160ee17…（不变）
--- hB (hashB+tokenB): 重执行（config sha256 变为 25cf68dd…）
manifest inside hB: fingerprint:7b1536a7ad62        # token B 的指纹 = 正确新产物
```

**结论**：secrets-hash 失效的反面（不变→命中）与正面（变化→失效）在 raw BuildKit 与 railpack（E2a）两侧都成立。architecture D6 的「Dockerfile 兜底」路径需要平台约定：凡用户声明的 secret，平台向 Dockerfile 构建注入 `sha256(secret)` ARG 作为缓存戳，否则兜底路径携带同类事故。

---

## 5. E4 构建隔离与资源限额（续作）

### 5.1 特权 buildkitd + 硬 cgroup 限额

**方法**：`scripts/in-e4-limits.sh`。销毁原 buildkitd，以 `--memory 1g --memory-swap 1g --cpus 1.5 --privileged` 重建，注入 `scripts/buildkitd.toml`，跑冷构建×2（每次前 `buildctl prune --all`）+ 热重建。

**原始输出**（`artifacts/logs/in-e4.log`）：

```
===== E4.2 ceiling as recorded by the engine =====
Memory=1073741824 NanoCpus=1500000000 Privileged=true
===== E4.3 cold build #1 under limits =====
#9 ERROR: process "sh -c apt-get update && apt-get install -y libatomic1" did not complete successfully: exit code: 100
spike-a-buildkitd: cpu=0.17% mem=61.63MiB / 1GiB
OOMKilled=false Status=running
===== E4.4 cold build #2 under limits (repeatable) =====
│ Successfully built image in 574.17s      │
spike-a-buildkitd: cpu=0.01% mem=601.5MiB / 1GiB
OOMKilled=false Status=running
===== E4.5 warm rebuild under limits =====
#6 CACHED … #13 CACHED（8 行）
end 16:48:12（约 3s）
```

**判读**：
- 限额真实生效且构建可完成：冷构建 574.17s 完成，构建刚结束采样内存 **601.5MiB / 1GiB**，`OOMKilled=false`。架构 §4.1「构建在 CPU/内存限额内」——通过（最小证据：1GiB 内存天花板下完成同规模构建；峰值约为天花板 59%）。
- E4.3 的 apt exit 100 是 **debian 镜像源网络抖动**，与限额无关：失败时内存仅 61.63MiB（远未触顶）、非 OOM；相同步骤在 E2a/E2b 的多次构建均成功，E4.4 重跑即过。它从反面给出一个数据点：构建失败的第一大来源是外网，而不是资源。

**局限（如实声明）**：
- 内存是「构建结束后采样 + OOM 标志」，不是构建过程的逐秒曲线；1.5 CPU 限额由 cgroup（NanoCpus）声明、engine 侧确认，但未记录构建中的实时 CPU 曲线。
- 「缓存跨重启存活」未直接演示（E4.4→E4.5 之间无重启；buildkitd 未挂数据卷时容器重建即丢缓存——跨实例缓存应走 E2b 的 registry cache，这正好构成互补）。
- dind 本身是特权容器，这里的 cgroup 限额是 dind 内层的二重限额；宿主侧还要叠加平台层限额（超出 spike 范围）。

### 5.2 rootless vs 特权选型

**证据**（`artifacts/logs/e4-rootless-report.log`，dind 内嵌套探针）：

```
=== kernel / userns baseline ===
Linux … 6.18.33.2-microsoft-standard-WSL2 … x86_64 Linux
no userns sysctl
=== pull + start rootless buildkitd ===
pull ok
run exit: 0
=== tiny scratch build through the rootless worker ===
build exit: 0
#5 exporting config sha256:47fb8d62e25f…
#5 DONE 0.2s
```

moby/buildkit:v0.32.2-rootless（`--oci-worker-no-process-sandbox`）在 WSL2 内核 6.18 上可启动并完成 scratch 构建。**局限（如实声明）**：该探针运行在**特权 dind 内部**——rootless buildkitd 的 userns/fuse-overlayfs 依赖在特权 dind 里天然满足，因此这只证明「rootless 变体在该内核上能跑」，**不能**证明「宿主 Docker rootless 部署形态可用」（那需要在非特权宿主环境直测，超出本 spike 条件）。

**推理与选型建议**（最小证据 + 推理）：
1. edgefleet 目标用户是「会 Docker、不愿学 k8s」的自托管人群（架构 §1.2），v0.1 平台本身就以 root 运行 Docker 操作（swarm 管理）；构建发生在平台已控的 Docker 内。**特权 buildkitd + 硬限额**在这个威胁模型下已覆盖「构建压死宿主」（E4.1 实测）的主要风险。
2. rootless buildkitd 的额外收益（无特权攻击面）与额外成本（需内核 ≥5.11 + fuse-overlayfs + userns 配置，竞品调研 §5.5）不成比例：Coolify/Dokploy 等同类全部默认特权 buildkitd，未见 rootless 生产先例。
3. 综合：**v0.1 选特权 buildkitd + cgroup 硬限额（实测通过）；rootless 有「变体能跑」的最小证据（内核 6.18），列为 v0.2 前的外层部署形态评估项**。

**重跑**：`spike\a\scripts\e4-limits.bat`、`spike\a\scripts\e4-rootless-run.bat`。

---

## 6. E5 V2 本地 digest 免 pull（续作；29.8.1-dind 全新环境实测）

**方法**：`scripts/e5-v2-digest.sh`（宿主编排 `e5-run.bat`，自建全新特权 dind）。dind 内 `swarm init` → buildx 构建本地镜像（零 registry 参与）→ T1 `service create` 以 `名称@sha256:<imageID>` 引用 → T2 `service update --force` → T3 tag 引用对照 → T4 裸 `sha256:<imageID>` 探针。取证三层：task 状态、`docker events` 窗口、dockerd 全程日志 pull 行 grep。

**原始输出**（`artifacts/logs/e5-report.log` + `e5-dockerd-full.log`）：

```
image id:        sha256:42cfdf83c16148cec670de439cddf8468e174b14ce11648ad1302210909cc6a6
--- standalone sanity run: running / alive-from-standalone

T1: spike-a-svc  replicated  1/1  spike-a/app@sha256:42cfdf83…
    task: Running；exec 探活 alive-from-digest-task（exit 0）
    events[t1]: 11 条，pull 相关 0；dockerd pull 行 0

T2: service update --force exit 0 → 新任务 Running（旧任务 Shutdown）
    exec 探活 alive-after-force；events[t2] pull 相关 0；dockerd pull 行 0

T3 (对照): service create spike-a/app:v1 → 任务仍 Running（本地回退）
    dockerfd 全程仅有的 2 条 pull 行都来自这个 tag 任务：
    level=info msg="fetch failed" error="pull access denied …" method=HEAD
      url="https://registry-1.docker.io/v2/spike-a/app/manifests/v1"
    level=error msg="pulling image failed" error="pull access denied for
      spike-a/app …" module=node/agent/taskmanager
    （pull 尝试不出现在 docker events 里，只在 dockerd 日志）

T4: service create sha256:42cfdf83…（裸 image-ID，无名称成分）
    create exit 0；任务 Running；pull 相关 0
```

**结论**：架构 §4.1/D9/V2——**29.8.1 单节点 swarm 下，`app@sha256:<本地 digest>` 创建与 `--force` 重放均零 pull 尝试、任务正常运行；tag 引用则每次创建都真实发起 pull（失败后回退本地仍可运行）**。裸 `sha256:<id>`（无名称成分）也可用。tag 回退「失败可继续运行」的行为同时得到复现（对照成立）。

**附带发现（写入 §7 #9/#10）**：
- create 时 daemon 打印提示：`image … could not be accessed on a registry to record its digest. Each node will access … independently`——单节点无害；**多节点 v0.2 必须有镜像分发（zot），否则各节点各显神通**。
- buildx 默认产出带 provenance 证明的 manifest list，本地 digest= list digest；首轮实验中 swarm 任务解析该 list digest 后容器以 `exec /busybox: no such file or directory` 崩溃循环。构建本地部署镜像必须 `--provenance=false --sbom=false`（或 `docker import`），并先做 standalone 自检再交给 swarm。
- dind 内 busybox 无 `httpd`/`wget` applet（alpine 拆在 busybox-extras）；alpine 的 `/bin/busybox` 为动态链接，FROM scratch 必须一并 COPY `/lib/ld-musl-x86_64.so.1`。

**执行过程如实记录**：E5 共跑 4 轮。第 1 轮（attestation list + 动态 busybox 双坑叠加）任务崩溃循环；第 2 轮修复部分仍败（httpd applet 缺失）；第 3 轮在宿主侧终止后，**dind 内脚本成为幽灵进程继续执行并执行了 `swarm leave`/`rmi` 清理，污染第 4 轮**——最终以全新 dind 完整跑通第 4 轮取证。教训（§7 #11）：宿主侧 kill 不会终止 dind 内已启动进程；重跑前必须 `ps` 清场或重建 dind。

**重跑**：`spike\a\scripts\e5-run.bat`（自建独立 dind，~4 分钟；结束自动拆除）。

**wait_task 计时伪影说明**：报告中三处 `T* task state: NOT-RUNNING (deadline)` 为轮询 45s 截止的伪影（dind 内 swarm 调度约 50s 才到 Running）；紧随其后的 `service ps` 原文均显示任务 `Running`，且 exec 探活成功——以 service ps 与 exec 证据为准。

---

## 7. 意外发现（坑与数据点）

1. **buildkit v0.32.2 的 `buildctl prune` 没有 `--force` 旗标**：前序 E2b 脚本写 `buildctl prune --all --force`，两次 prune 全部静默失败（脚本未检查 prune 退出码），“冷对照”实际吃了本地缓存。续作重跑前用 `buildctl du`（prune 后 = 0B）把“缓存确实被清空”变成显式验证步骤。教训：凡“清空类”操作必须跟一个“空态断言”。
2. **railpack 运行时镜像无 wget、无可用 shell 工具**：可运行性探测不能 `docker exec` 进容器，需外部按容器 IP 探测（或应用自带 healthcheck 端点）。对平台设计的影响：健康检查必须走 TCP/HTTP 探针，不能假设镜像内有任何工具。
3. **`grep -r /` 在容器内挂死**：即使 12MB 的 alpine rootfs，`grep -r` 扫 `/` 会因 `/proc`（kcore 等）挂起 >2min 无输出。文件系统取证必须扫真实目录白名单（/app /work /etc /usr /var /opt /root /home /tmp）。
4. **Engine 29.8.1 的 inspect 模板变化**：`.NetworkSettings.IPAddress` 顶层字段已移除，须遍历 `.NetworkSettings.Networks`。
5. **`docker cp` 宿主→特权 dind 静默丢文件**（前序已记录于 e0-up.bat 头注，续作全程沿用 exec+stdin/bind-mount 方案未再踩）；`.bat` 里 `docker exec -i < file` 送达 0 字节；`type file | docker exec -i` 在 MB 级丢字节。**bind mount（`-v`）逐字节保真**（sha256 验证），是唯一可靠通道。
6. **冷构建成本被外网带宽主导且方差极大**：同一种冷构建（node fixture）85s（E2a，快网络）vs 889s（E2b，慢网络，~700KB/s）。缓存体系（本地层 + registry cache）的价值比“加速几十秒”高一个量级，建议平台把「buildkitd 缓存卷持久化 + registry cache 导出」当作默认能力而非优化项。
7. **railpack 生成的 mise 配置自带供应链保守默认**：`minimum_release_age = "14d"`、`paranoid = true`（见 plan JSON generated-mise-toml）。这缓解了「mise 解析最新版」的一部分风险，但不消除漂移（14 天后仍会漂），版本 pin 仍必须由平台注入。
8. **railpack 对构建上下文的假定**：plan 中 build step `secrets: ["*"]`——railpack 把全部环境变量作为 BuildKit secrets 传给构建步骤，平台的 secrets 数组（声明式）是唯一准入面。
9. **buildx 默认 provenance 证明与 swarm 本地 digest 引用不兼容**：`docker build`（buildx）默认产出含 attestation 的 manifest list，`docker image inspect .Id` 返回 list digest；swarm 任务以该 digest 启动时容器 `exec /busybox: no such file or directory` 崩溃循环。本地部署镜像构建必须 `--provenance=false --sbom=false`。对平台的意义：edgefleet 构建管线如果要走 buildx，必须显式关闭 provenance，或部署/运行两侧都感知 manifest list。
10. **alpine 的 busybox 是动态链接且无 httpd/wget applet**：`/bin/busybox` 为 ELF ET_DYN，FROM scratch 镜像必须连同 `/lib/ld-musl-x86_64.so.1` 一起 COPY；`httpd`/`wget` 在 busybox-extras 包里，`docker:29.8.1-dind` 自带 busybox 没有。写最小验证镜像时连续踩中这两点。
11. **宿主侧 kill 不终止 dind 内进程（幽灵进程污染）**：E5 第 3 轮在宿主 `TaskStop` 杀掉 `docker exec` 后，dind 内 `sh /tmp/e5v2.sh` 继续跑到收尾段，执行了 `swarm leave --force` 与 `docker rmi`，把第 4 轮测试环境拆了（症状：`This node is not a swarm manager`、events 里出现不属于本轮的 image delete）。重跑类实验前必须 `ps aux | grep` 清场，或干脆重建 dind。
12. **镜像排除于 docker events 之外**：T3 的 pull 尝试在 `docker events` 中完全不可见（0 条），只在 dockerd 日志。以「无 pull 尝试」为验收断言时，取证必须查 dockerd 日志而非 events——e5-run.bat 的 E5.4 步骤因此保留。

---

## 8. 复现指引

前置：本机 Docker Desktop；仓库根目录。所有 .bat 在 cmd 下运行。

```bat
:: 全新环境（约 2 分钟：交叉编译驱动 + 起 dind + 起内部 buildkitd/registry）
spike\a\scripts\e0-up.bat

:: E1 plan 可复现（~1 分钟）
spike\a\scripts\e1-prepare-plans.bat

:: E2a 本地缓存 + secrets-hash（~2 分钟，冷构建视外网 1–15 分钟）
spike\a\scripts\e2a-build-cache-local.bat

:: E2b registry cache 往返（视外网 3–20 分钟）
spike\a\scripts\e2b-cache-registry.bat

:: E2c raw BuildKit secrets 事故+缓解（~1 分钟）
spike\a\scripts\e2c-raw-buildkit.bat

:: E3 凭证不进镜像（~2 分钟；依赖 E2a/E2c 产物镜像在 dind 内存在）
spike\a\scripts\e3-secret-leak.bat

:: E4 资源限额（冷构建×2 + 热重建，视外网 5–40 分钟）
spike\a\scripts\e4-limits.bat

:: E4 rootless 探针（自建独立 dind，~3 分钟）
spike\a\scripts\e4-rootless-run.bat

:: E5 V2 digest 免 pull（自建独立 dind，~5 分钟）
spike\a\scripts\e5-run.bat

:: 清理
spike\a\scripts\e0-down.bat
```

主仓回归（围栏验证）：`go build ./... && go test ./... ./sdk/go/... -race`——2026-09-17 续作实测全绿。

产物索引：
- 计划归档：`spike/a/artifacts/plans/*.json`（node/go × pinned+repro、unpinned 对照）
- 日志：`spike/a/artifacts/logs/`（in-e1 / in-e2a / in-e2a-runnable / in-e2b / in-e2c / in-e3 / in-e3-fsfix / in-e4 / e4-rootless-report / e5-report / e5-dockerd-full）
- 脚本：`spike/a/scripts/`（宿主编排 .bat + dind 内实验 in-*.sh + raw-ctx 取证用 Dockerfile）
