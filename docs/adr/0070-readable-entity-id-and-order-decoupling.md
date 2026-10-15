# ADR-0070: 可读实体 id（`<base>-<seq>`）与有序性解耦

日期：2026-10-12；状态：已采纳（同批落地：基础迁移原位修订 + entityid 装配单源 + 全链路切换；staging 重置重播种）。

## 背景

实体 id 一律 26 位大写 ULID，排查不可读：CLI/Console/日志/载体名/DNS/审计里出现的 `01JD0…` 无法与任何名字对照，人工排查要逐表反查。ULID 的隐性收益——时间有序——同时被硬依赖：15+ 处游标分页 `AND id < ? ORDER BY id DESC`（ADR-0026「游标 = ULID 创建序」）、停新保老排水锚（run ListDriving）、备份单飞 FIFO（OldestPending）、admission 在途顺序，全部把「创建序」搭在 id 字符串序上。id 形态与顺序语义耦合是设计债：动 id 形态必须连顺序机制一起动。

ULID 入选无决策记录（ADR-0021 仅「归档仓沿用」）；领域模型的硬承诺其实是「平台 ID 是归属锚 + 永不复用」（domain-model「ULID 或等价」措辞留了口子）。ULID 的存在理由是「无协调去重」——分布式各铸各的仍唯一；本平台 SQLite 单写者，事务本身就是铸造协调点，该价值归零。staging 重置重播种已拍板、外部消费者核证为零（ADR-0047 先例）——无兼容包袱，地基一次改对。

## 决策

1. **id 与顺序解耦，`seq` 是创建序唯一真源**：每张聚合表增 `seq INTEGER NOT NULL` + `UNIQUE(seq)`（事务内 MAX+1；SQLite 单写者安全，唯一索引使并发双铸 fail-closed；先例 outbox.seq/revisions.seq）。游标协议 `after_*_id` → `after_*_seq`（int64），全部 ListPage/排水锚/FIFO/admission 顺序切 seq 轴。**禁止 `ORDER BY id`**（守卫静态扫描兜底——seq 的十进制字典序陷阱：`web-9` > `web-1042`）。created_at 保留展示/粗过滤用途（TEXT RFC3339 秒精度是偏序，不承担排序）。**基础迁移 00001–00028 原位修订**（无增量回填迁移）——只接受全新库，与 staging 重置同口径；本地旧库作废重建。
2. **id = `<base>-<seq>`，seq 兼任身份后缀**：身份消歧（同名重建）、创建序、never-reuse 三机制合一——序号由单写者事务分配、含 tombstone 行取 MAX、构造性永不重发。终审对比（2026-10-12）：`web-<ULID>` 形态否决（26 位不透明尾巴永久进所有面、排序收益为零、后缀职责 ~20 bit 用 120 bit 承担）；`name+4 位随机` 否决（随机+查重+防复用三件套是 seq 之外的第二套机制，never-reuse 降级为运行时约定）；created_at 排序否决（秒精度偏序，同秒多行不可分辨）。id 铸成后不可变（全仓无 rename 面）；id 是不透明字符串，全仓禁止解析其内部结构（归属判定只查平台表）。
3. **逐实体方案**：

   | 类 | 实体 | base | 例 |
   |---|---|---|---|
   | 有名 | project/app/database/volume/network/secret/config/shared_variable/task/schedule/team/user/role/notification_channel/alert_rule | name | `web-42` |
   | 派生 | revision | `<app-id>-r<seq>`（per-App 修订号，UI 面语义沿用） | `web-42-r7` |
   | 派生 | deployment/build/run/backup/browse/network_peer/route | `<父id>-<seq>` | `web-42-88` |
   | 独立 | token/invitation/upload | `<kind>-<seq>` | `tok-15` |
   | 独立 | audit / node | `aud-<seq>` / `node-<seq>` | |

   （2026-10-15 P3-1 修订：exec session 出律——改 crypto 随机 `exec-<32hex>`，见下节「适用范围」；token/invitation/upload 不变。）

   task 有名用 name、无名系统 task 用 `task-<seq>`。铸造点收敛到行装配处：repo Create 为主（name/父 id/tx 同在手）；task 走 minttask（CONTEXT.md Task Mint 词条不动——TaskSpec 冻结先于行装配，minttask 事务内先取 seq 再注身份）；node 走 anchor 铸造；会话走各自装配点。
4. **name 是 id 的 base**：有名实体 name 校验收紧 dns 标签形态（`[a-z0-9]([a-z0-9-]*[a-z0-9])?`，≤63）——否则 `fleetly-<projectID>` 命名空间、`db-<id>` DNS 等派生名不安全。API 层执法，对齐 spec 内部名 regex 先例（processNameRe/SecretNamePattern）。
5. **单一格式，无任何 ULID 接受面**：spec.go 26 位大写正则（platformIDRe）替换为新格式校验；测试夹具/golden 掩码全量单一化；`oklog/ulid/v2` 出 go.mod，守卫禁复引。
6. **exec 中继帧变长会话字段**：`[1B kind][26B session][payload]` → `[1B kind][1B len][session][payload]`（修订 ADR-0049 定长记载）；manager/relay 同二进制、升级序同批换装。
7. **存量处置**：staging 重置重播种（先拆受管载体 + 清节点 `fleetly.*` 标记再重置平台库，否则 Drift 判旧载体孤儿）；不做数据改写工具。

## 适用范围（2026-10-15 P3-1 修订）

可读 id 律针对**持久化实体**——四要件：行有台账、跨面引用、人工排查诉求、seq 兼任创建序；缺一即出律。exec 会话不满足：它是进程内活体（注册表不落库），生命周期 ≤30min（硬 TTL 上界），ID 只在帧协议/票据/日志瞬时出现，无排查台账诉求。反向看，单调序号使会话 ID 可枚举——`exec-1`、`exec-2`…让跨队会话 ID 探测零成本，是已封堵的 S1 案卷暴露面（attach 的 Team 比对）的放大器，纯负资产。**修订：exec 会话 ID 出律，改 crypto 随机（`exec-<32hex>`，CreateSession 铸造）**；中继帧协议（决策 6 的 1B 长度前缀变长形态）本就承载任意会话 ID，零改动。

## Consequences

- 排查面全链可读：载体名/DNS/日志/审计/对象键自带名字与创建排名；口头传达、肉眼比对、剪贴板核对全部可念可记。
- 后缀即创建排名，id 自我注解（`web-42` = 第 42 个 App）；序号泄露创建活动量——内部运维平台接受（GitHub issue 号同例）。
- 跨表同号（apps 的 `web-42` 与 databases 的 `pg-42`）：PK 按表分立，audit/事件/URL 全部带 kind 或路径上下文，无歧义面。
- 游标/顺序语义不依赖任何 id 性质；未来再动 id 形态是纯铸造层决策。
- 取代关系：ADR-0026 游标惯例（「游标 = ULID 创建序」）由本 ADR 取代；ADR-0021 ulid 依赖行废止；ADR-0019「spec 体 26 字符 ULID 字面量」、ADR-0049 定长帧、ADR-0023/0029「新 ULID」措辞由本 ADR 修订（承诺不变，形态词更新）。
- 破坏性变更集中一批：proto 游标字段改名改型（buf breaking 对 origin/main 红一次，commit 上 main 后自愈）；CLI/Console id 形态变化（golden 全刷、夹具全转）。
- restore-to-T 场景：世界整体回滚后重新占有旧序号，语义自洽（不是复用——回滚世界合法重置）。

## 验收锚

- [x] `internal/model/entityid` 包测：Join/Valid/ValidName/派生确定性。
- [x] state 层 seq 轴：游标分页/排水方向/FIFO/admission 顺序测试全绿；UNIQUE(seq) 双铸 fail-closed 测试。
- [x] 守卫：state 层零 `ORDER BY id`；全仓零 `oklog/ulid` import；golden/夹具零 ULID 字面量。
- [x] exec relay 帧：变长编解码单测 + exec e2e 腿全绿。
- [x] golden 全量重刷零漂移；generate:verify/lint 全绿（buf breaking 对 origin/main 红一次随 commit 自愈；console:verify 同——提交前态）。
- [ ] staging 重置重播种后全面走查 PASS（runbook ADR-0070 节承载操作序；上站后勾销）。
