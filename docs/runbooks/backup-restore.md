# Runbook：控制面状态备份与恢复（L1/L2）

适用：fleetly v0.1（单节点）。设计依据：state-model §2.7（备份、等序原则
与恢复）、architecture §4.2（平台状态备份基线 + 状态诚实契约）。

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
3. 抽一份最新备份做完整性核对：

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
     `verified` 备份（先按 §5 做一次离线快照抄底现状）；
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

# ⑤ 启动 + 验证清单（§6）
systemctl start fleetlyd
```

**L2（仅 DB 恢复到新集群）**：新机装平台 → 上述 ②–④ 回填 → 应用按
`apps`/`domains` 台账人工重部署 → 显式处理绑定差异（放置绑定随 DB 回来，
节点 ID 不匹配的走 `rebind`）。恢复期**禁止自动收敛**（等序原则：DB 旧于
raft 时，自动收敛会静默回滚部署）；恢复后先只读观察、比对
`fleetly apps list` 与底座实况的差异清单，人工处理后再恢复正常操作。

## 5. 验证清单（恢复完成判定）

```sh
systemctl status fleetlyd --no-pager                    # active (running)
curl -s http://127.0.0.1:8420/healthz/readiness         # 各组件 ok
FLEETLY_TOKEN=<tok> fleetly apps list                   # 应用清单与恢复预期一致
FLEETLY_TOKEN=<tok> fleetly backups list                # 台账可读（含恢复前最后一条）
docker service ls                                       # 应用服务未被改动（fleetly-ingress 在列）
```

附加核对：新 daemon 首启会立即产生一条 `daily` 备份（verified）——这条
出现 = 备份链在恢复后的库上重新闭环。

## 6. 边界（如实告知）

- 单节点 v0.1 整机磁盘丢失 = 应用与数据同时丢失，控制面 DR 不覆盖；
  备份应**异机存放**（当前版本备份目录在本机，异机上传随 v0.2 S3 目标）。
- daemon 在备份写入中途被杀死（崩溃/强杀）可能留下没有台账行的半成品
  目录（`backups/<id>/` 无 manifest）——它不是备份，恢复时永远以台账行
  为准；半成品目录可手动删除。
- 恢复演练（L1）首次执行预算 ≤10 分钟；记录见
  [drill-l1-2026-09-18.md](drill-l1-2026-09-18.md)。
