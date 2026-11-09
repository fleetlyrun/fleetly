# 数据库 major 升级序 runbook（B7，ADR-0094 决策 5 / design §2.4）

> 正典：**新建新版本 + 恢复 → 强制新备份 → 删旧 + purge 旧卷 → 同名重建 + 恢复 → 报告 → 清理**。
> in-place 升级（pg_upgrade 等）明确否决。`fleetly databases upgrade` 向导（B7）是该序的编排投影；
> 本手工序是向导的**验金石**——向导六步与本文六步一一对应，任何步骤手工可做、可重放、可回退。
>
> CLI 本地执行律：全部操作经 `fleetly` CLI（`FLEETLY_ADDR`/`FLEETLY_TOKEN` 指向控制面）。

## 前置检查

- `fleetly engines list` 确认目标版本在册（非 Deprecated；Deprecated 版**只许**同版本灾备重建，不许跨入）。
- 源库 `fleetly databases get <DB>`：状态 `running`、记录当前 version（下文 `<OLD>`）。
- 恢复兼容轴（RestoreCompat，受理位执法）：同引擎同版本放行、**major 只升不降**、pgvector 复合 ID 分轴
  （`0.8-pg17 → 0.8-pg18` 升、任一轴降即拒）、跨引擎拒。旧备份行（engine_version 空）= 同引擎放行但
  「版本未记录，降级风险自负」——**优先触发新备份取有版本记录的源**。
- 断连窗披露：③ 删除到 ④ 恢复完成期间，Secret `database:<name>` 指向已拆 host——引用该库的 App 全程断连，
  时长 = ④ 的恢复时长（大库小时级）。低峰窗操作。

## 六步正典（每步：操作 → 验证点 → 回退点）

### ① 取恢复源 + 建中间库

```sh
fleetly databases backup --project <PRJ> <DB>          # 取新备份（跳过条件：已有 succeeded 且 engine_version 在册）
fleetly databases backups --project <PRJ> <DB>          # 等 succeeded，记下 <BK1>
fleetly databases verify <BK1>                          # digest 重算必须 ok
fleetly databases create --project <PRJ> --engine <ENG> --version <NEW> \
  --restore-from-backup <BK1> <NAME>-upgrade-<ts>
```

- **验证点**：create 受理过 RestoreCompat（降级在此拒）；中间库 `status=running` 且 `restore_state=succeeded`
  （`fleetly databases get`；`restore_from_backup` 保留为源历史，不再充当在途判据）；连入抽查数据行数。
- **回退点**：任一失败 = `fleetly databases delete <中间名>`——源库零触碰，升级未发生。

### ② 强制新备份（写入不丢窗的关闭动作）

```sh
fleetly databases backup --project <PRJ> <中间名>
# 等 succeeded → <BK2>
fleetly databases verify <BK2>                          # 不过 = 止步，绝不进 ③
```

- **验证点**：`<BK2>` succeeded 且 verify ok（**红队 A1**：① 的备份到 ③ 删除之间的写入否则静默丢失；
  ③ 的恢复源必须显式 = `<BK2>`）。
- **回退点**：verify 失败 = 修备份链（ObjectStore 可达性）后重触发；源库仍零触碰。

### ③ 删旧库 + purge 旧卷（断连窗开始；容量回收）

```sh
fleetly databases delete --project <PRJ> <DB>
fleetly volumes purge --project <PRJ> <DB> --preview     # 清单：卷名/载体键/pinned 节点/关联库披露 + 容量行
fleetly volumes purge --project <PRJ> <DB> --yes         # wide 档确认（数据不可恢复）
```

- **验证点**：`volumes list` 中卷行消失；运行侧载体确已删除（swarm：`docker volume ls | grep fleetly-vol-<卷行id>`
  为空；k3s：`kubectl get pvc -n <project-ns>` 无对应 claim）。**容量回收动机**：ULID 卷键（ADR-0097）已消除
  同名复用撞库——purge 的目的是回收磁盘，不是防撞。
- **回退点**：purge 前发现 `<BK2>` 有疑 = 停手（源库已删但 `<BK1>`/`<BK2>` 仍在，④ 可用任一恢复；
  数据未销毁）。purge 后 = 只剩对象存储备份（这正是 ② 强制 verify 的意义）。
- **断连窗披露**：此刻起 Secret `database:<NAME>` 指死 host，引用 App 断连至 ④ 完成。

### ④ 同名重建（恢复源显式 = ② 的 `<BK2>`）

```sh
fleetly databases create --project <PRJ> --engine <ENG> --version <NEW> \
  --restore-from-backup <BK2> <NAME>
```

- **验证点**：新行 `status=running`、`version=<NEW>`、`restore_state=succeeded`（`restore_from_backup` 保留为
  源历史）；连接串 host 已换代（`db-<新id>`）；连入抽查数据行数（对账 ① 的记录）。新行新卷（ULID 键）——旧 pgdata 不可能复用。
- **回退点**：恢复失败 = `restore_state=failed` + `restore_error` 留痕（终态，平台不再重试；半恢复态重试不幂等）→ 删重建，恢复源仍 `<BK2>`。

### ⑤ 验证与报告（断连窗结束）

- 引用方 App **重新部署**后取新 host（`secret_refs`/`database_refs` 值覆盖新 `db-<新id>`；不重部署 = 持旧值）。
- **验证点**：App 连通 + 业务读写正常；`fleetly databases get <NAME>` 全字段过目。
- **回退点**：业务异常 = App 回滚到旧 Revision 无济于事（旧 host 已死）——数据面回退 = 用 `<BK1>`（① 的
  旧版本备份）在 `<OLD>` 版本重建同库（降级方向 restore 对**新建同版本行**放行——灾备义）。

### ⑥ 清理中间库（容量回收）

```sh
fleetly databases delete --project <PRJ> <中间名>
fleetly volumes purge --project <PRJ> <中间名> --yes
```

- **验证点**：中间行/中间卷均不在；项目卷清单只剩在役卷。
- **回退点**：无（安全副本 = `<BK2>` 已 verify；中间卷本就冗余）。

## 向导对照（`fleetly databases upgrade`）

`fleetly databases upgrade --project <PRJ> <NAME> --version <NEW> [--preview | --yes]`
按同一六步编排：①–⑥ 逐步「先查目标态、达成即跳过」——**中断重跑从断点续**（③ 后重入须以名形态 +
`--project` 进，源行已 tombstone）；破坏性步 ③⑥ 的 purge 由向导现场重铸 wide 票兑换（服务端审计
proof=redeem）。无服务端编排行——**状态即平台行**（库行/备份行/卷行），向导不落新状态。

## staging 走查清单条目（随引入/翻 Default 批次，design §2.6 条 5）

- [ ] `databases upgrade --preview` 计划面：版本 diff + 恢复源 + purge 清单 + 断连窗披露齐全。
- [ ] 全程跑一遍向导（低峰窗；staging 库）；③ 后 Ctrl-C 中断 → 名形态重入 → 断点续跑成功。
- [ ] 引用 App 重部署后新 host 生效（`FLEETLY_DB_*` / secret_refs 面）。
- [ ] `volumes purge` 容量行 live 回报（swarm = docker volume inspect UsageData）；purge 后
  `docker volume ls` 对应载体消失。
