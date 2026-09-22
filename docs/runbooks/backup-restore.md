# Runbook：控制面状态备份与恢复（L1/L2）

适用：fleetly v0.1（单节点）。设计依据：state-model §2.7（备份、等序原则
与恢复）、architecture §4.2（平台状态备份基线 + 状态诚实契约）、
object-storage 专项 §2.3/§5（E3 远端上传轨与恢复）。

---

## 0. 备份是什么、在哪、何时发生

- **内容**：控制面 SQLite 状态库（权威态：apps/revisions/deployments/
  env_vars 密文/domains/placements/volumes/tokens/审计/事件/**备份台账**）
  的一致性快照，`VACUUM INTO` 单语句产出（WAL 下与读写并行安全）。
- **布局**：`/var/lib/fleetly/backups/<ULID>/` 下
  - `fleetly.db` —— 快照本体（独立单文件 SQLite）；
  - `manifest.json` —— 元数据：id/kind/created_at/platform_version/
    schema_version/size_bytes/**sha256**/verify_status/**key_fingerprint**
    /tables（关键表行数抽查结果）。
- **触发**：
  - `daily` —— daemon 每日一拍（启动即一拍）；
  - `post_deploy` —— 每次部署成功后（异步，不阻塞部署）；
  - `pre_upgrade` —— `deploy/upgrade.sh` 升级序列第 ② 步；
  - `manual` —— `fleetly backups create`（同步，等回读校验结论）。
- **诚实契约**：每次快照立即回读校验（重新打开 + `PRAGMA integrity_check`
  + 关键表行数抽查 + 迁移版本核对），结论落台账 `state_backups.verify_status`
  （`verified` / `failed`）。**verify 失败 = 红色告警三件套**：
  ① 台账 `failed` 行 + 审计 `backup.failed (result=error)`；
  ② `fleetly system status` 的 `state.backup` 组件 `ok=false`（错误原文可
  行动）；
  ③ fleetlyd 日志 ERROR。
  不存在「绿色成功但实际没备份」的路径。
- **保留**：`backup.keep`（缺省 7 份）。超限删最旧：台账删行 + 审计
  `backup.pruned` + 目录清除（台账只描述真实存在的备份）。

## 1. 巡检（每周一分钟）

```sh
FLEETLY_ADDR=127.0.0.1:8421 FLEETLY_TOKEN=<token> fleetly backups list
```

判定：

1. 最近一行 `verified`，且 `created_at` 在 24h 内（daily 拍子正常）；
2. 无 `failed` 行持续存在（出现即按 §3 处理——**不要无视红色**）；
3. `s3.mode ≠ unset` 时：最近一行 `upload_status` 为 `ok`（`failed` 行 =
   上传轨红，`backup.upload_failed` 事件 + 系统状态 `state.backup` 组件
   同步变红——本地 verify 结论不受影响，处置见 §3 与 §5）；
4. 抽一份最新备份做完整性核对：

```sh
BK=/var/lib/fleetly/backups/<id>
sha256sum "$BK/fleetly.db"        # 与 manifest.json 的 sha256 一致
grep -E 'schema_version|verify_status' "$BK/manifest.json"
```

## 2. 主密钥的独立保管（硬要求）

- 主密钥文件：`/var/lib/fleetly/fleetly.key`（`secrets.key_path`）。
  **密钥绝不进备份目录**——manifest 只记录它的 sha256 指纹
  （`key_fingerprint`）。把密钥配置进备份目录会被 daemon 拒绝启动备份面。
- 备份 = 数据 + 元数据；**恢复能力 = 备份 + 密钥**。密钥丢了，
  平台 env 密文全部不可解（架构 §2.3 已知边界）。
- 因此：把密钥文件**另行离线保管一份**（口令保险箱 / 密钥管理介质，
  与备份盘不同介质），并记录指纹便于核对：

```sh
sha256sum /var/lib/fleetly/fleetly.key   # 与最新备份 manifest 的 key_fingerprint 一致
```

- 建议节奏：每次轮换/首次安装后离线保管一次；巡检时核对指纹未变。

## 3. 备份失败（红色告警）处置

1. 看原因：`fleetly backups list`（failed 行带 error 原文）+
   `journalctl -u fleetlyd -n 100 | grep backup`。
2. 常见原因与处置：
   - 磁盘满 → 清理 `/var/lib/fleetly` 膨胀项（构建缓存/旧日志）后
     `fleetly backups create` 重试；
   - `integrity_check` 报错 → 主库疑似损坏，**立即**按 §4 恢复到最近
     `verified` 备份（先按 §4 步骤 ③ 做一次离线快照抄底现状）；
   - ledger/审计写失败 → 数据库连接/权限问题，修好后再触发。
3. 修复后手动触发并确认 verified：
   `FLEETLY_TOKEN=<tok> fleetly backups create` → 输出含 `verify=verified`。

## 4. 恢复（L1：同机 raft 无损，只回填 SQLite）

**顺序固定**（state-model §2.7）。全程应用不受影响（Swarm service 与
Traefik 独立于 daemon），但恢复期间控制面只读不可用。

```sh
# ① 停 daemon（应用继续服务）
systemctl stop fleetlyd

# ② 校验备份集（校验和 + 密钥指纹；不匹配 → 拒绝半恢复，停下排查）
BK=/var/lib/fleetly/backups/<id>
sha256sum "$BK/fleetly.db"                 # == manifest.json .sha256
sha256sum /var/lib/fleetly/fleetly.key     # == manifest.json .key_fingerprint
#   指纹不一致 = 密钥被换过/拿错备份：停下来，不要继续（E_BACKUP_KEY_MISSING 语义）。

# ③ 抄底现状（恢复失败可退回；绝不覆盖唯一可用副本）
cp /var/lib/fleetly/fleetly.db /var/lib/fleetly/fleetly.db.pre-restore-$(date +%s)

# ④ 回填（含 -wal/-shm 一并移除——快照是独立单文件库，旧 WAL 不可复用）
rm -f /var/lib/fleetly/fleetly.db-wal /var/lib/fleetly/fleetly.db-shm
cp "$BK/fleetly.db" /var/lib/fleetly/fleetly.db
chown root:root /var/lib/fleetly/fleetly.db && chmod 0600 /var/lib/fleetly/fleetly.db

# ⑤ 启动 + 验证清单（§7）
systemctl start fleetlyd
```

本地恢复完成后，若 s3.mode ≠ unset，继续 §5.3 的备份链闭环核对
（口令在恢复后的库里，链路自证）。

**L2（仅 DB 恢复到新集群）**：新机装平台 → 上述 ②–④ 回填 → 应用按
`apps`/`domains` 台账人工重部署 → 显式处理绑定差异（放置绑定随 DB 回来，
节点 ID 不匹配的走 `rebind`）。恢复期**禁止自动收敛**（等序原则：DB 旧于
raft 时，自动收敛会静默回滚部署）；恢复后先只读观察、比对
`fleetly apps list` 与底座实况的差异清单，人工处理后再恢复正常操作。

## 5. 从 S3 恢复（E3-3 上传轨：远端快照取回）

适用场景：本地备份目录丢失/损坏（误删、磁盘局部故障），但 `fleetly.key`
与一份**含 S3 配置的库**仍可复原。每份远端快照的内容 = 一个备份目录
（`fleetly.db` + `manifest.json`，与 §0 布局一致）——取回后按 §4 同样
核对（sha256 / key 指纹）再回填。

### 5.0 恢复顺序固定：先 key 与库，再口令，再远端

- repo 位置（两种 mode 同一路径形态）：
  `s3:<endpoint_url>/<bucket>/statebackups`。external 模式 endpoint/bucket
  取 `fleetly s3 show`；rustfs（托管）模式为
  `s3:http://rustfs:9000/fleetly/statebackups`（仅平台内网可达）。
- **repo 口令不在任何配置文件里**：上传轨首次上传时惰性生成（32B 随机
  hex），以 envelope 密文（标准 age 形态）存于库内 `platform_settings`
  键 `s3.restic_password`——只有 `fleetly.key` 能解密（无 CLI/API 读面，
  密钥纪律如此）。
- 因此顺序必须为：① 找回 `fleetly.key`（§2 的离线托管件）→ ② 按 §4 恢复
  任一**晚于 s3 配置**的库副本（口令密文随之回来）→ ③ 从恢复后的库解出
  口令（§5.1）→ ④ restic 取回快照（§5.2）→ ⑤ 按 §4 回填并按 §5.3 核对
  备份链闭环。

### 5.1 取回 repo 口令（在恢复后的库上）

```sh
# ① 从恢复后的库导出口令密文（age 密文 TEXT 形态）
sqlite3 /var/lib/fleetly/fleetly.db \
  "SELECT value FROM platform_settings WHERE key = 's3.restic_password'" \
  > /tmp/restic_password.age

# ② 用主密钥解密（标准 age；用 age CLI 或等价实现）
age -d -i /var/lib/fleetly/fleetly.key -o /tmp/restic_password.txt /tmp/restic_password.age
export RESTIC_PASSWORD=$(cat /tmp/restic_password.txt)

# ③ 立即收尾：口令不落 history/磁盘
rm -f /tmp/restic_password.age /tmp/restic_password.txt
```

### 5.2 restic 取回快照（钉版镜像，与上传轨同版）

```sh
RESTIC_IMAGE=restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510

# rustfs 模式：repo 目标与网络挂接（endpoint 是网络 alias，须挂内部网）
export RESTIC_REPOSITORY=s3:http://rustfs:9000/fleetly/statebackups
NET_ARGS="--network fleetly-rustfs-net"
# external 模式：repo 目标按 s3 show，出网即可（注释掉上一行 NET_ARGS）
# export RESTIC_REPOSITORY=s3:https://s3.example.test/fleetly-backups/statebackups

# 列出远端快照（自建/托管端点均显式 path-style，与上传轨 -o 参数一致）
docker run --rm $NET_ARGS \
  -e RESTIC_REPOSITORY -e RESTIC_PASSWORD \
  "$RESTIC_IMAGE" -o s3.bucket-lookup=path snapshots --json

# 取回最新快照（或以 snapshots 输出里的短 id 指定）
mkdir -p /var/lib/fleetly/restore
docker run --rm $NET_ARGS \
  -e RESTIC_REPOSITORY -e RESTIC_PASSWORD \
  -v /var/lib/fleetly/restore:/restore \
  "$RESTIC_IMAGE" -o s3.bucket-lookup=path restore latest --target /restore
```

取回目录内即 `<id>/fleetly.db` + `<id>/manifest.json`——按 §4 步骤 ②③④
核对与回填（sha256、key 指纹、抄底现状、`-wal/-shm` 清理）。回填的库中
`s3.restic_password` 密文与现用口令一致（同一 repo），上传轨闭环不受影响。

### 5.3 恢复后备份链闭环核对点

```sh
systemctl start fleetlyd
FLEETLY_TOKEN=<tok> fleetly s3 status          # mode/endpoint/部署态符合预期
FLEETLY_TOKEN=<tok> fleetly backups create     # verify=verified 且 upload_status=ok
FLEETLY_TOKEN=<tok> fleetly backups list       # 台账两列结论如实（§1 判定）
```

- 新 `manual` 行 `verified` **且** `upload_status=ok` = 恢复后的库 → 上传
  轨 → 远端 repo 全链重新闭环（口令解密、凭证、网络、桶权限全部自证）；
- `upload_status=failed` 且错误含 `restic` 字样 = repo 侧问题（口令/凭证/
  网络），按 `fleetlyd` 日志（secret 已擦除）归因后重试。

### 5.4 诚实口径（防误删 ≠ 灾备）

- **本机 RustFS（s3.mode=rustfs）= 便捷层**：防误删、防单文件损坏。主机
  整体损毁时 key、库与 RustFS 数据**一同丢失**——repo 口令不可解，远端份
  同样不可用。它不是灾备，Console 的 S3 卡也常驻同口径标注。
- **external 端点 = 灾备向**：主机损毁后可恢复的前提是两件离线托管件同时
  存在——`fleetly.key`（§2）+ 任一含 `s3.restic_password` 密文的库副本
  （如最近一次异地抄送的 `/var/lib/fleetly/fleetly.db` 离线拷贝）。二者缺
  一，远端 repo 在密码学上不可达（口令不重建、不旁路）。
- 跨节点互备（库副本的自动化异地托管）挂账 v0.2（object-storage §6）；
  兑现前，灾备 = 上面的两件离线托管件 + external 端点，由操作者纪律保证。

## 6. 库备份与恢复（E4 managed-databases，W4-S5/S6）

适用：托管库实例（`db_instances`）的数据备份/恢复。与 §0-§5 的**控制面
状态备份**是两套独立机制（独立表 `db_backups`、独立 repo 命名空间
`db/<instance>/`、独立恢复语义）——词形相近，处置路径互不通用。设计依据：
managed-databases §2.6（备份/恢复适配器）、§2.5（轮换）。

### 6.0 机制速览

- **内容**：逻辑备份（PG = `pg_dump -Fc`；Redis = RDB 流式导出），由
  一次性 Swarm job 执行（dbtools 镜像，digest 钉定；挂实例数据卷、钉绑定
  节点），入平台 restic repo（与状态备份同一 repo 基础设施、
  `db/<instance>/` 独立命名空间）。
- **触发**：`daily`（per 实例计划，缺省每日 03:00 UTC、保留 7 份）、
  `manual`（`fleetly databases backup <name>` 或 Console 备份卡
  「Back up now」）、`pre_upgrade`（升级备份门内部类别）。
- **诚实契约**：每次备份立即回读校验（restic 读回 + 引擎级头部校验），
  结论落 `db_backups.verify_status`（`verified`/`failed`）。**failed 行 =
  备份不可信**——恢复目标选择永远跳过 failed 行；无 verified 备份的实例
  不可升级（备份门如实拒绝）。在途备份无台账行——受理响应只是 accepted，
  结论看台账与 `db.backup_*` 事件。
- **前置态**：备份与恢复都要求实例 ready/degraded（paused/failed 如实
  409）。s3.mode=unset 时备份受理诚实拒绝（E_S3_NOT_CONFIGURED）——先在
  System → Storage 配置目标。

### 6.1 巡检与手动备份

```sh
FLEETLY_ADDR=127.0.0.1:8421 FLEETLY_TOKEN=<tok> fleetly databases backups <name> --json
# 最新行 verify_status=verified 且 created_at 在计划窗口内；failed 行出现
# 即按事件 db.backup_failed 的 error 摘要归因（凭据材料零出现）。
```

手动触发：`fleetly databases backup <name>`（异步受理）→ 轮询台账至
verified。Console：库详情页 Backups 卡。

**首次启用前置**：库备份与控制面状态备份共享同一 restic repo——repo
口令由控制面上传轨**首次上传时**惰性生成。全新平台先做一次
`fleetly backups create`（结论 upload ok = 口令在位 + 目标可写），再做
库备份；否则库备份受理后的异步链会以
「restic repository password is not provisioned yet」诚实失败（事件面
可行动文本指到此步）。

### 6.2 恢复（原地重放，破坏性两段式）

`fleetly databases restore <name> --snapshot <id> --confirm <name>`
（Console：备份卡逐行 Restore → 输入实例名确认）。流程 = 实例 scale 0 →
job 挂卷只读改写重放 → 重部署 → 健康门 → ready。恢复期间引用方 app
**连不上是诚实暴露**（pg 断连错误即产品）；分钟级。

**凭据边界（务必知晓）**：恢复重放的是**备份时刻的库内密码**。若备份后
做过 `databases rotate`，恢复完成后的库内密码与平台权威态密文错位——
`fleetly databases show <name>` 的 `password_fingerprint` 是比对锚（它
是平台侧现值的指纹）。处置：恢复后**再 rotate 一次**（两段式 confirm），
引用 app 自动重部署后全链对齐。未做过轮换的实例无此收尾。

### 6.3 恢复中断（critical）人工收尾

恢复中断 = 实例保持停止 + `db.restore_failed`（critical 口径）+ last_error
带现场。人工步骤：

1. `fleetly databases retry <name>` 先尝试重收敛（多数瞬态可恢复——job
   重跑是幂等重放）；
2. retry 无法收敛（卷上数据半成品）→ 换目标快照再 restore 一次（较新或
   较旧的 verified 行均可——原地重放会覆盖半成品）；
3. 仍失败 → 按删除/重建路径处理（`databases delete` 默认**保留卷**转
   orphaned，人工确认无需取证后 `--delete-volumes` 或手动清卷），并以最
   近 verified 备份重建后重放。

### 6.4 诚实口径（同节点 RustFS ≠ 灾备）

`s3.mode=rustfs` 时库备份与状态备份同一约束：**同节点 RustFS = 便捷层
（防误删/单文件损坏），不是灾备**——主机整体损毁时实例数据卷与备份一同
丢失。灾备向配置 = external S3 端点（§5.4 同口径；Console 备份卡与 S3
设置卡常驻同文案标注）。跨节点 DR：建新库 + 手动重放 + rebind 的 runbook
组合（managed-databases §2.6「恢复」行），不做一键跨实例恢复。

## 7. 验证清单（恢复完成判定）

```sh
systemctl status fleetlyd --no-pager                    # active (running)
curl -s http://127.0.0.1:8420/healthz/readiness         # 各组件 ok
FLEETLY_TOKEN=<tok> fleetly apps list                   # 应用清单与恢复预期一致
FLEETLY_TOKEN=<tok> fleetly backups list                # 台账可读（含恢复前最后一条）
docker service ls                                       # 应用服务未被改动（fleetly-ingress 在列）
```

附加核对：新 daemon 首启会立即产生一条 `daily` 备份（verified）——这条
出现 = 备份链在恢复后的库上重新闭环。

## 8. 边界（如实告知）

- 单节点 v0.1 整机磁盘丢失 = 应用与数据同时丢失，控制面 DR 不覆盖；
  远端上传轨（E3-3）缓解单机磁盘故障，但**主机整体损毁的恢复边界见
  §5.4 诚实口径**——本机 RustFS 不改变该边界。
- daemon 在备份写入中途被杀死（崩溃/强杀）可能留下没有台账行的半成品
  目录（`backups/<id>/` 无 manifest）——它不是备份，恢复时永远以台账行
  为准；半成品目录可手动删除。
- 恢复演练（L1）首次执行预算 ≤10 分钟；记录见
  [drill-l1-2026-09-18.md](drill-l1-2026-09-18.md)。
