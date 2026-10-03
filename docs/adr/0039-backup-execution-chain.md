# ADR-0039: 备份执行链——工具容器、ObjectStore 承载数据面、restic 承载平台面

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-04 | CONTEXT.md Backup/Platform Backup/ObjectStore 词条、ADR-0014（材料纪律）、ADR-0015（升级序前置）、ADR-0019（控制面节点单机假设合法形态）、ADR-0020（备份默认+恢复演练验收）、ADR-0021（钉版）、ADR-0025（Run/一次性语义）、ADR-0029 决策 7/8（备份口径真源）、ADR-0032（railpack 钉版先例）、F2.8（ObjectStore 耦合面）、提案 P2（注入守卫挂账） |

## 背景

ADR-0029 决策 7 把备份执行链（定时触发 + 产物写 ObjectStore + 保留窗滚动 +
恢复演练）落在 F2.2；ADR-0020 把恢复演练钉为验收必过项。功能清单 F2.2 的
三个命名材料（restic、外置 S3 目标、保留策略）需要安放；`dbtemplate.
BackupCommand` 空槽（ADR-0029 决策 2 预留）与 `providers/localobjectstore`
（注册在册、装配未接线）同时到期兑现。P2 挂账（shellguard 头注释）：备份
命令落地时须同批落解析器级注入断言。

## 决策

1. **双轨分立（词汇即架构）**：CONTEXT.md 早已把 **Backup**（数据面）与
   **Platform Backup**（控制面导出）立为分立词条，承载面随词汇分立：
   - **Database Backup = 引擎原生逻辑 dump → 原始对象进 ObjectStore 端口**
     （键 `backups/<projectID>/<databaseID>/<ts>`，sha256 回执 = verify 锚，
     ADR-0029 决策 7 原样兑现）。CONTEXT.md "ObjectStore 承载 Backup" 钉死
     此轨；**restic 不进数据库轨**——restic 仓是不透明块存储，破坏键契约、
     digest 校验锚与 List/Delete 保留滚动（localobjectstore 为此而生）。
   - **Platform Backup = restic 快照**（SQLite 一致快照 + keys/ + config +
     backups/ + uploads/），本地仓恒在 + 外置 S3 仓可配。restic 的去重/
     加密/校验/保留（forget）与文件级控制面数据天然合身。**外置 S3 仓捎带
     backups/ 目录离机**：F2.8 ObjectStore s3 Provider 落地前，配置
     platform_backup.s3 即达成数据库备份对象离机。
2. **执行载体 = 控制面 daemon 一次性工具容器**（修订 ADR-0029 决策 7 的
   "one-shot Run" 措辞；可见性由 backups 行承载——Build 行同款先例：daemon
   执行、行驱动、零 Run 实体）：artifact 流（dump stdout → `Put` 流式铸
   digest；恢复 `Get` → restore stdin）需要控制面经纪人，swarm Run 无
   artifact 通道、Runtime 端口无 exec 面（F3.2 RuntimeExec 是用户面另案）。
   控制面节点单机假设 = ADR-0019 构建同款显式合法形态（P3 审计已登记类目）。
   多节点可达性成立：工具容器附着项目 overlay 网即达 `db-<id>`（任意节点）。
3. **capability.RuntimeUtility 新可选子面**（Runtime 第 6 子面，FacesOf/
   Offered 同批扩；Builder 同款装配语义——非 Kind 注册、cmd 直连 Deps）：
   `RunUtility(ctx, req)`：镜像/argv/env/材料文件（bind `/run/secrets/` 同
   swarm 语义）/平台网络附件/**平台卷 ID 引用**（卷载体名解析是 Provider
   私有公式，故子面住在 Runtime Provider 上而非独立 provider 包）/stdio
   流/超时。实现 = providers/swarm（daemon 容器，标签 `fleetly.utility=
   true`，容器名 `fleetly-utility-<ulid>`，结束即删）。**实现子面在 swarm
   Provider 而执行面是 daemon 容器**：manager 的 daemon 就是控制面 docker
   （构建链同源），attachable overlay 允许 daemon 容器入网解析 swarm DNS。
4. **项目网络 Attachable=true**（create 面，ensureNetworks）；存量非
   attachable 网络不改不炸——备份执行器附着失败报精确错误（文本带重建
   序指引），runbook 记 flag-day 操作序。新生环境（e2e/staging 重装后）无
   此问题。
5. **dbtemplate 槽启用——接口演进为 dump/restore 渲染对**（内部接口、零
   wire 面；原 `BackupCommand() string` 槽退役，"届时只填实现"兑现为填
   实现时按需定形）：每引擎渲染 argv 数组 + 材料文件 + 恢复形态：
   - postgres / pgvector：`pg_dump --format=custom`（PGPASSFILE 文件面）；
     恢复 = `pg_restore` 流式（custom 格式支持非寻位 stdin，整档缓冲）。
   - mysql：`mysqldump --single-transaction --routines --triggers --events`
     （`--defaults-extra-file` INI 文件面）；恢复 = `mysql` 客户端 stdin 流。
   - mongo：`mongodump --archive --gzip`（`--config` YAML 文件面）；恢复 =
     `mongorestore --archive --gzip` 流式。
   - redis：`redis-cli --rdb -` 流式 RDB（REDISCLI_AUTH env 是材料纪律唯一
     登记例外——redis-cli 无文件面；一次性平台容器内 env）；恢复 = **预置
   卷**（RDB 仅启动时装载：恢复路径 = 平台卷行预建钉住 manager → 工具容
   器预置 dump.rdb 进卷 → 库首启装载；工具容器恒在 manager，锚定一致）。
   密码一律平台解密后经文件下发（redis 例外见上）；argv 渲染纯数组、零
   shell 拼串、零 SQL 拼接（args 数组文化延续，shellguard 射程内不变）。
6. **状态面（00019）**：`backups` 表（id/project/database/engine/object_
   key/size/digest/status/error/started/finished）+ `databases` 增
   `last_backup_at`（调度锚，行存续独立于备份行清理）+ `restore_from_
   backup`/`restore_error`（恢复挂起语义：在场且库 running → 环内执行恢复
   一次；成功清位 + `database.restored` 事件，失败清挂起 + 落 error——
   半恢复态重试不可幂等，诚实留给用户重建）。对象键时间戳 = 无冒号紧凑
   UTC 形态（Windows 控制面纪律，localobjectstore 注释既有口径）。
7. **调度与保留**：backup 新收敛环（现第 8 环；单写者文化）：到期库
   （`now - last_backup_at ≥ interval`，缺省 24h，行级可调）逐个执行；
   retention 滚动（`List` ModTime 排序 → `Delete` + 行同步删，缺省窗 7d
   行级可调——ADR-0029 决策 7 口径）；Platform Backup 全局节拍（缺省
   24h）同环驱动。备份执行带界（缺省 15m 硬上限，选项可配）。
8. **verify 裁决**：`VerifyBackup` = `Get` 全量重算 sha256 比对 Put 回执
   （静态完整性：腐损/截断可检出）；引擎级结构校验（pg_restore --list 类）
   不做——dump 完整性由工具退出码（执行器只记成功退出）+ 行级 digest 双锚
   承担，结构校验留真实需求出现再议。**恢复演练 = 真恢复**（试恢复到
   临时 Database 实例 + 数据断言），ADR-0020 验收必过锚，dind e2e 四引擎
   形态全覆盖（pg/mysql/mongo 流式 + redis 预置卷）。
9. **Platform Backup = restic**：钉版 **0.19.1**（2026-10 现行稳定，装配期
   经 node2 实证；install.sh 下载静态资产，railpack 先例 + 钉版一致性守卫
   同款）。仓库密码首用铸造落 `keys/platform-backup.key`（0600）**排除在备
   份集外**（仓密进仓自锁，循环依赖——runbook 提示离机保管）。备份集 =
   SQLite `VACUUM INTO` 一致快照（排除 live state.db/-wal/-shm）+ keys/ +
   config + backups/ + uploads/；排除 platform-backups/ 自身与临时目录。
   保留 = `restic forget --keep-within 7d --prune`（双仓同策略）；校验 =
   每次成功后 `restic check`。外置仓 `server.platform_backup.s3`
   {endpoint, bucket, prefix, access_key_id, secret_access_key}（restic
   原生 S3 backend = F2.2 的"外置 S3 目标"）。恢复到临时 DataRoot 起临时
   fleetlyd 的全量演练 = F2.3 升级序（失败回滚路径）的一部分，本批验收锚
   = 本地仓 roundtrip（backup → check → snapshots 可列举）。restic 二进制
   缺席 = Platform Backup 停用 + doctor 诚实标注（railpack"失败不阻断"
   先例），数据库轨不受影响。
10. **API/守卫/CLI**：DatabasesService +`TriggerBackup`/`ListBackups`（
    after_backup_id + limit，ADR-0026 惯例）/`VerifyBackup`；
    `CreateDatabaseRequest` +`restore_from_backup`（同 Project 恢复到新
    名，engine 恒取源行——跨引擎恢复拒绝）。SystemService
    +`TriggerPlatformBackup`/`ListPlatformBackups`（快照列举 = restic
    snapshots 直读，零状态行）。事件 +5：`database.backup_succeeded/
    backup_failed/restored` + `platform.backup_succeeded/backup_failed`。
    errcode 倾向复用（E_NOT_FOUND/E_INVALID_ARGUMENT）；ObjectStore/Utility
    未装配或网络不可附着的精确失败缺码时 +1 带锚。幂等：两个 Trigger 进
    idem 面；冻结分类：Trigger*Backup = **exempt**（保护性操作，冻结期
    备份不停摆——停止族同理由），restore 经 CreateDatabase 既有 frozen 面。
    CLI：`databases backup|backups|verify` + `databases create
    --restore-from-backup` + `platform backup|backups`（组动词进 groups
    golden），全部双形态。
11. **P2 挂账落法（解析器级注入断言）**：落地形态 = 渲染面注入家族四件
    （guards injection 面）：① 备份/恢复 argv 渲染——敌意库名/密码/host
    注入后 argv 元素形状精确断言（数组构造性无 shell，测试钉死渲染器不做
    拼接/分裂）；② mysql defaults-extra-file INI 生成——**ini 解析器断言
    单 [client] 节**（密码可含换行——Secret 值用户可覆写；控制字符密码
    精确拒绝，"单语句校验"的结构化文本等价物）；③ mongo config YAML 经
    yaml.Marshal 结构化生成（构造性安全 + 行为钉死）；④ mongosh init
    脚本（F2.1 既有面）注入回归补课。原文预期的"平台生成 SQL"面在落地
    形态中不存在（mysqldump 产物是数据不是平台生成 SQL；库/用户初始化走
    镜像 env 面）——shellguard 头注释挂账行更新为实录，提案 P2 勾账带
    偏差注。
12. **F2.8 耦合（本批钉形、F2.8 实现）**：`server.object_store`
    {provider local|s3, s3 {endpoint, region, bucket, prefix, path_style,
    access_key_id, secret_access_key}}——s3 在册即数据库 dump 对象离机。
    "同机备份非灾备"告警解除条件 = object_store.s3 或 platform_backup.s3
    任一在场；告警面本批 = doctor/Describe Notes（F2.5 接告警通道后升级）。

## 后果

- swarm Provider 首次直跑非 service 容器（工具容器执行面）；Runtime 端口
  +1 可选子面（未实现 = 备份链降级：环跳过 + API 精确失败，Inspector
  降级文化同款）。
- ADR-0029 决策 7 的 "one-shot Run" 措辞由本 ADR 修订（执行载体 = 工具
  容器 + backups 行）；键契约/保留口径/恢复到新库语义不变。
- 存量 staging 项目网络需一次 flag-day 重建才可附着（runbook 操作序随批）。
- e2e 新脚本 `dind-backup.sh`（四引擎演练）；种子/断言经 docker exec
  容器内本地信任面（unix socket 免密），不触 Secret 回显纪律。
- Platform Backup 交付即解锁 F2.3 升级序前置（TriggerPlatformBackup 就是
  升级脚本要调的动词）。

## 验收锚

- [ ] 五引擎槽填实（argv + 材料面 + redis 预置卷特例）+ 注入家族四件
- [ ] RuntimeUtility 子面：daemon 工具容器执行（网络附着/材料 bind/平台卷/
      stdio/超时/容器清扫），FacesOf/Offered 扩面
- [ ] 项目网络 attachable + 存量网精确错误路径
- [ ] backups 表 + 调度环：24h/7d 滚动（ModTime 排序）+ 手动 Trigger +
      verify digest 重算 + 行/对象同删
- [ ] 恢复：restore_from_backup 环内恢复（三引擎流式 + redis 预置卷）+
      database.restored 事件 + 失败清挂起落 error
- [ ] e2e dind-backup.sh 四引擎演练全绿（ADR-0020 必过锚）
- [ ] Platform Backup：restic 0.19.1 钉版守卫 + 本地仓 roundtrip +
      forget 保留 + 密钥排除 + S3 仓可配 + 缺席降级
- [ ] API/守卫/CLI 全喂食：idem/freeze/grpc/gateway/authz + errcode/
      eventcode 同批 + CLI 双形态 golden + groups 清单
- [ ] shellguard 挂账行实录更新 + 提案 P2 勾账（偏差注）
