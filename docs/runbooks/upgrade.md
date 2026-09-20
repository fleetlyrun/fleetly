# Runbook：平台升级（升级双轨）

适用：fleetly v0.1（单节点）。设计依据：architecture §4.2 横切硬指标
（平台自升级原子化，2026-09-17 审核裁决的双轨口径）。

## 0. 双轨口径（不得混淆）

| | **fleetlyd 轨**（本文件 §1，`deploy/upgrade.sh`） | **Engine/主机轨**（本文件 §3，人工 + 冷备） |
| --- | --- | --- |
| 升级对象 | fleetlyd 控制面二进制 | Docker Engine / 宿主 OS / 内核 |
| 备份语义 | **热备**：升级序列内自动 `pre_upgrade` 快照（verified 门） | **冷备**：停应用维护窗口内，先备份（状态快照 + Engine 冷备）再动手 |
| 应用影响 | **零**：不停 Engine、应用不停（Swarm service 与 Traefik 独立于 daemon 存活——daemon 停止期间应用路由继续服务，dind E2E 已实证 probe 零失败） | **有停机**：维护窗口内应用停机，有状态应用如实告知停机 |
| 失败语义 | 自动回退 `fleetlyd.previous`，再验证，仍失败停在最诚实状态打诊断 | 按 Engine 官方回滚 + 备份重放，人工执行 |
| 禁止 | 禁止 `--force-recreate` 式升级（对应用服务零操作——脚本不触碰任何 Swarm service） | 禁止在无冷备的情况下动 Engine |

一句话：**fleetlyd 升级随时可做、不需要窗口；Engine/主机升级必须计划
维护窗口 + 冷备先行。** 用 fleetlyd 轨的脚本去动 Engine/主机 = 违规混用。

---

## 1. fleetlyd 轨：`deploy/upgrade.sh`（编排式原子升级）

### 1.1 前置

```sh
# root、daemon 在跑（脚本自检）；API token（pre_upgrade 备份用）
export FLEETLY_TOKEN=<token>      # bootstrap token: <数据根>/bootstrap-token（缺省 /var/lib/fleetly/bootstrap-token，0600、不进日志；首登后删除）
```

### 1.2 执行（三种获取形态）

```sh
# 最新 stable（缺省；stable 渠道只接受带签名版本）
curl -fsSL https://fleetly.dev/upgrade.sh | sudo sh -

# 钉定版本
sudo sh upgrade.sh --version v0.1.1

# 离线/开发形态（本地两份不同版本串的 fleetlyd+fleetly）
sudo sh upgrade.sh --bin-dir /tmp/new-bin
```

可选参数：

- `--probe-url <url>` + `--probe-host <host>`：升级前后对该入口采样
  （期望连续 3 次 200；Host 头用于 Traefik 路由）；
- `--api-addr` / `--http-addr`：daemon 地址（缺省回环 8421/8420）；
- `--allow-nightly`：显式接受无签名 nightly 目标（红色警告降级）；
- `--skip-signature-verify`：跳过 cosign（仅调试）；
- `--skip-backup`：跳过升级前快照（**破坏原子升级保证**，红色警告——
  只在密钥/台账确认不可用的极端场景使用）；
- `--auto-restore`：回退时状态库 schema 高于回退二进制上限，从本运行的
  verified pre_upgrade 快照自动恢复状态库再拉起旧件（缺省 die + 三步
  人肉指引——见 §4.2）。

### 1.3 脚本做了什么（八步序列）

1. **预下载**新二进制并校验（checksums sha256 必验 + 签名链；先下后停，
   daemon 全程在线，下载失败零影响）；
2. **升级前热备快照**：`fleetly backups create --kind pre_upgrade`（同步），
   `verify_status` 必须 `verified`，否则**在停 daemon 之前中止**；
3. **应用健康基线**：`apps list` 的 derived_state 集合 + 可选 probe 采样；
4. `systemctl stop fleetlyd`（无 systemd 环境走 pid 文件）——**应用不停**；
5. **原子换二进制**：同目录 rename 链，旧件存 `fleetlyd.previous`；
6. start + liveness 门（manual 形态下进程早死会快速失败，不耗满预算）；
7. **升级后验证**：REST ping 版本 = 目标版本 + derived_state 与基线一致 +
   probe 持续 200；
8. **失败自动回退**：任一步失败 → 停 → `fleetlyd.previous` 归位 →
   start → 再验证 → 成功则报告「ROLLED BACK（平台健康、升级未应用）」，
   仍失败则停在**最诚实状态**（RED 报告 + systemctl/journal 诊断转储），
   绝不硬编绿色。

### 1.4 升级后

```sh
fleetly system status 2>/dev/null || curl -s http://127.0.0.1:8420/v1/system/status
FLEETLY_TOKEN=<tok> fleetly backups list    # 升级后首启的 daily 备份（verified）已入台账
```

回退件 `/opt/fleetly/bin/fleetlyd.previous` 在升级成功后可保留一份周期
（建议保留到下一次成功升级）。

---

## 2. Engine 升级回归（升级前必读）

Docker 29.x 有破坏式变更史（引擎门禁下限 29.8.1 即此缘故）。Engine/主机
轨动手前核对：

- [ ] 目标 Engine 版本 ≥ 29.8.1（平台门禁）；
- [ ] iptables(legacy) 后端保持（nftables-only 不支持 Swarm 节点）；
- [ ] 「上一版本 → 新版本」的升级 E2E（`deploy/run-upgrade-test.sh` 场景）
      在等价环境通过。

---

## 3. Engine/主机轨：冷备 + 维护窗口（人工）

> 本轨**有停机**。窗口选择请如实告知用户/团队；有状态应用（带卷）停机
> 是本轨的预期语义，不是事故。

1. **窗口前**：`fleetly backups create --kind pre_upgrade` 确认 verified；
   主密钥离线副本在位（见 backup-restore.md §2）。
2. **停应用**：`docker service scale <svc>=0`（或整 stack down）；
   确认 `fleetly apps list` 全部 `down` 且入口无流量。
3. **冷备**（控制面 + Engine 运行态）：
   ```sh
   systemctl stop fleetlyd docker
   tar -czf /root/cold-$(date +%F).tgz \
     /var/lib/fleetly /var/lib/docker/swarm
   # /var/lib/docker/swarm 含 raft 与 autolock key——介质加密、异机存放
   systemctl start docker fleetlyd
   ```
4. **执行 Engine/OS 升级**（官方流程）。
5. **回填验证**：Engine 起来后按 backup-restore.md §4/§5 核对状态库；
   `docker node ls` 确认 Swarm 成员身份健在（`--force-new-cluster` 仅在
   raft 判死时使用——后果与处置见 state-model §2.7 恢复顺序）。
6. **恢复应用**：逐服务 `scale` 回目标副本；观察窗内盯
   `fleetly apps list`（running）与入口探针。

两轨各自操作、各自记录；**fleetlyd 升级永远不触发冷备、Engine 升级永远
不能用 §1 脚本代替**。

---

## 4. 带 schema 迁移的升级（回退 = 二进制 + 快照，缺一不可）

迁移体系只加法（SQL 迁移内嵌二进制，启动即应用；无 down migration），
「回滚 = 恢复快照」是架构契约（state-model §2.8）。这决定了 fleetlyd 轨
升级在 schema 迁移面前的完整语义：

### 4.1 守卫语义

- 新 fleetlyd 首次启动即把状态库迁移到新 schema（`goose_db_version` 可查）；
- 此后**旧 fleetlyd 对该库拒绝启动**：state.Open 检测库版本 > 本二进制
  已知最大迁移版本即 fail-fast，错误信息形如
  `数据库来自更新版本（schema N > 本二进制 M）；回退二进制前须按快照恢复
  状态库，见 docs/runbooks/backup-restore.md`；
- 守卫的意图：旧二进制读不懂新 schema，静默运行是数据损坏的温床——宁可
  拒启，不给「回退只换二进制」的错配留绿色假象。

### 4.2 回退序列（upgrade.sh 失败自动回退时的完整口径）

`upgrade.sh` 的回退段是 **schema 感知**的（F5/S20 整改后口径——start 前
探测、显式裁决，不再依赖「拉起后报错」）：

1. 换二进制（`fleetlyd.previous` 归位）后、start 之前，脚本以
   `fleetlyd schema-version` 探测状态库 schema 版本（db）与回退二进制的
   支持上限（max）并比对；
2. db > max（新 daemon 已迁移 schema）时**默认 die**——保持 fleetlyd
   停止状态，给三步人肉指引：① 保持停机（此刻已停，别 start）；
   ② 核验并恢复本次升级的 pre_upgrade 快照（对 manifest 校 sha256 与
   主密钥指纹、清掉 `-wal/-shm` 残留后覆盖状态库，见 backup-restore.md
   §4/§5）；③ 再以旧二进制拉起（或重跑 upgrade.sh）；
3. `--auto-restore` 是第②步的显式授权自动形态：脚本代做「校验快照
   （verified + sha256 + 主密钥指纹，任一不过即 die）→ 现库旁存
   `<db>.pre-schema-restore.<时间戳>`（只增不销毁）→ 快照入库 → 复核
   schema ≤ 旧件上限 → 拉起旧件」，回退报告标记 `auto-restore: yes`；
4. 探测失败（回退二进制早于 `schema-version` 子命令）不阻断：warn 放行，
   最终防线仍是旧 daemon 启动时的高版本守卫（§4.1）；
5. 升级成功（未回退）则无需任何动作——新 schema 与新二进制配套。

操作口径一句话：**升级窗口内「二进制版本」与「schema 版本」必须同进同
退；快照是 schema 的回退载体，`fleetlyd.previous` 只是代码的；二者错配
时脚本宁可 die，不替操作员默判。**

### 4.3 与冷备轨的边界

- fleetlyd 轨（§1）：schema 迁移由热备快照兜底——pre_upgrade 快照即旧
  schema 的完整副本，回退 = 恢复它；全程应用不停。
- Engine/主机轨（§3）：冷备同样覆盖状态库（tar 内含 `/var/lib/fleetly`），
  Engine 升级若伴随 fleetlyd 大版本跨越，回退口径与本节相同。
- 两轨都不要在**无 verified 快照**的情况下跨 schema 版本升级/回退；
  `--skip-backup` 跳过的不只是备份，是 schema 的退路。
