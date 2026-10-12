# ADR-0070: 可读实体 id（name+4 位后缀）与有序性解耦

日期：2026-10-12；状态：已采纳（同批落地：基础迁移原位修订 + entityid 铸造单源 + 全链路切换；staging 重置重播种）。

## 背景

实体 id 一律 26 位大写 ULID，排查不可读：CLI/Console/日志/载体名/DNS/审计里出现的 `01JD0…` 无法与任何名字对照，人工排查要逐表反查。ULID 的隐性收益——时间有序——同时被硬依赖：15+ 处游标分页 `AND id < ? ORDER BY id DESC`（ADR-0026「游标 = ULID 创建序」）、停新保老排水锚（run ListDriving）、备份单飞 FIFO（OldestPending）、admission 在途顺序，全部把「创建序」搭在 id 字符串序上。id 形态与顺序语义耦合是设计债：动 id 形态必须连顺序机制一起动。

ULID 入选无决策记录（ADR-0021 仅「归档仓沿用」）；领域模型的硬承诺其实是「平台 ID 是归属锚 + 永不复用」（domain-model「ULID 或等价」措辞留了口子）。staging 重置重播种已拍板、外部消费者核证为零（ADR-0047 先例）——无兼容包袱，地基一次改对。

## 决策

1. **id 与顺序解耦，`seq` 是创建序唯一真源**：每张聚合表增 `seq INTEGER NOT NULL`（插入事务内 MAX(seq)+1；SQLite 单写者安全；先例 outbox.seq/revisions.seq）。id 不再承载任何顺序语义：游标协议 `after_*_id` → `after_*_seq`（int64），全部 ListPage/排水锚/FIFO/admission 顺序切 seq 轴，(scope,id) 游标索引换 (scope,seq)。**基础迁移 00001–00028 原位修订**（无增量回填迁移）——只接受全新库，与 staging 重置同口径；本地旧库作废重建。
2. **id = `<base>-<4 位>`**：suffix 小写 Crockford base32（去 i/l/o/u，32 符号，32^4≈105 万/base）；`internal/model/entityid` 铸造单源——`Mint(base, exists)` 事务内查重（含 tombstone，never-reuse 承诺 ADR-0023/0029 保持），`Derive` 确定性派生。铸造点收敛到行装配处：repo Create 为主（name/父 id/tx 同在手）；task 走 minttask（CONTEXT.md Task Mint 词条不动）；node 走 anchor 铸造；会话走各自装配点。
3. **逐实体方案**：

   | 类 | 实体 | base | 例 |
   |---|---|---|---|
   | 有名 | project/app/database/volume/network/secret/config/shared_variable/task/schedule/team/user/role/notification_channel/alert_rule | name | `web-m2x8` |
   | 派生 | revision | `<app-id>-r<seq>`（确定性） | `web-m2x8-r42` |
   | 派生 | deployment/build/run/backup | `<父id>-<4位>` | `web-m2x8-9f3k` |
   | 独立 | token/invitation/upload/browse/exec session | `<kind>-<4位>` | `tok-9f3k` |
   | 独立 | audit / node | `aud-<seq>`（确定性）/ `node-<4位>` | |

   task 有名用 name、无名系统 task 用 `task-<4位>`。id 铸成后不可变（全仓无 rename 面）；id 是不透明字符串，平台不解析（既有归属锚原则）。
4. **name 是 id 的 base**：有名实体 name 校验收紧 dns-label 形态（`[a-z0-9]([a-z0-9-]*[a-z0-9])?`，≤63）——否则 `fleetly-<projectID>` namespace、`db-<id>` DNS、载体名等派生名不安全。API 层执法，对齐 spec 内部名 regex 先例（processNameRe/SecretNamePattern）。
5. **单一格式，无任何 ULID 接受面**：spec.go 26 位大写正则（platformIDRe）替换为新格式校验；测试夹具/golden 掩码全量单一化；`oklog/ulid/v2` 出 go.mod，守卫禁复引。
6. **exec 中继帧变长会话字段**：`[1B kind][26B session][payload]` → `[1B kind][1B len][session][payload]`（修订 ADR-0049 定长记载）；manager/relay 同二进制、升级序同批换装。
7. **存量处置**：staging 重置重播种（先拆受管载体 + 清节点 `fleetly.*` label 再重置平台库，否则 Drift 判旧载体孤儿）；不做数据改写工具。

## Consequences

- 排查面全链可读：载体名/DNS/日志/审计/对象键自带名字上下文；runbook「swarm 标签大写 ULID grep 恒空」真机坑消解。
- 游标/顺序语义不依赖任何 id 性质；未来再动 id 形态是纯铸造层决策。
- 取代关系：ADR-0026 游标惯例（「游标 = ULID 创建序」）由本 ADR 取代；ADR-0021 ulid 依赖行废止；ADR-0019「spec 体 26 字符 ULID 字面量」、ADR-0049 定长帧、ADR-0023/0029「新 ULID」措辞由本 ADR 修订（承诺不变，形态词更新）。
- 破坏性变更集中一批：proto 游标字段改名改型（buf breaking 对 origin/main 红一次，commit 上 main 后自愈）；CLI/Console id 形态变化（golden 全刷、夹具全转）。
- 4 位后缀防撞空间 105 万/base，同名重建铸造循环有界重试；确定性派生（revision/audit）无随机面。

## 验收锚

- [ ] `internal/model/entityid` 包测：字符集/长度/Mint 查重重试/tombstone 命中/Derive 确定性。
- [ ] state 层 seq 轴：游标分页/排水方向/FIFO/admission 顺序测试全绿；变异验证=铸造不递增 seq → 游标序断言红。
- [ ] 单一格式：apitest/golden/夹具零 ULID 字面量（守卫静态扫描）；`oklog/ulid` 全仓零 import（守卫）。
- [ ] exec relay 帧：变长编解码单测 + exec e2e 腿全绿。
- [ ] golden 全量重刷零漂移；generate:verify/lint/console:verify 全绿。
- [ ] staging 重置重播种后全面走查 PASS。
