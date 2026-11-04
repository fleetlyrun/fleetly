# ADR-0092：不透明实体 id（ULID 铸造单源）与 name 边界寻址

日期：2026-11-03 立法（提案与逐项裁决记录见 `docs/proposals/2026-11-03-opaque-entity-id-and-name-addressing.md` §0/§5；2026-11-04 补充需求修订——id 界面可见、与 name 同等重要且均可复制）｜ 状态：实施中（A1 铸造面 40b1b65d、A2 载体名 c3f2a413、A3a Resolve 五 RPC 94db9b4b、A3b CLI 换装进行中；staging flag-day 随 A4。占号复核：0091 已被 platform-logs 批占用——并行会话编号复核纪律再次实证）

## 背景

ADR-0070（2026-10-12）把实体 id 定为名基可读形 `<name>-<seq>`，动机是排查面可读。一个月实战暴露四组张力：①id 内嵌 name 使「用户定位键」与「内部身份」耦在同一字符串——id 在一切输出面都是主位，name 退化为展示属性；②`<base>-<seq>` 形态诱惑解析（preview 的 processOfID 即因此长出一个潜伏 bug）；③seq 后缀让裸 name 永远不是合法键，CLI/文档被迫全程携带 `torchwood-pg-1` 形态；④名基 id 与每表 seq 双轨并存，解释成本长存。同时 ADR-0070 决策 1（顺序语义入每表 `seq` 列、游标走 seq、禁 `ORDER BY id`）恰好为换形留好了缝：id 形态与顺序/分页/游标完全解耦。

用户提案（2026-11-03）：id 换全局唯一且有序的不透明形态（不对用户可见、只用于内部定位）；name 接棒用户可见定位键（project name team 内唯一、app/db name 项目内唯一，CLI `--project/--app` 传 name）；载体名 `<process>-<id>` 便于定位。评估四轮裁决后定案，含两项修订：id 用户界面**可见**且与 name 同等重要（2026-11-04）；ULID 无 kind 前缀（二裁）。

## 决策

1. **id = 26 位小写 ULID（`Mint(now)` 单源）**：`internal/domain/shared/model/entityid` 唯一铸造入口；时间经参数入域（ADR-0087 决策 2 纯度判据——域不持墙钟），熵 crypto/rand。否决 `<kind>-<ULID>`（类型在载体域前缀/事件 aggregate/审计锚/REST 路径皆有承载，id 自述类型属冗余编码且违不透明本义）；否决全局计数器（restore-to-T/快照回滚会重置计数器撞 id，ULID 时间基免疫——ADR-0070 的恢复重置承诺无碰撞面延续）。`Valid` 双形收：新形裸 ULID / 派生形 / 旧名基形（快照恢复共存面合法，铸造不再产出）。`Join`/`Audit` 退役；oklog/ulid v2 重引为直接依赖，import 单点收口本包。
2. **派生律不变**：revision/deployment/build/run/backup/browse/network_peer/route 保持 `<父id>-<kind字母><seq>`（Derive/Revision 原样——kind 字母是同父跨表兄弟消歧，非实体类型自述）；蓝绿 `-g<gen>`（ADR-0048）、Task/Run/Browse/受管域载体名公式不变。**seq 列与游标协议原样**（ADR-0070 决策 1 全保留）——换形零触碰排序/分页/游标面。
3. **name 是用户定位键，边界解析**：统一律「name 在边界解析，id 在内核与存储」。三边界：CLI 三形态（id | 裸 name〔--project 兄弟旗标或 team 作用域〕| `project/name` 复合）；服务端 Resolve 五 RPC 单源（ResolveProject team 作用域——token 全员绑 team 天然零歧义，ADR-0028 team 轴唯一不翻案；ResolveApp/ResolveDatabase/ResolveTask/ResolveSchedule 项目内名，project 段收 id|name）；spec 跨项目引用 intake 期解析成 id 冻结进 IR（`project:<name>/<net>` 受理形）。id|name 判据 = 26 位 crockford 定长（`entityid.LooksMinted`——name 可无连字符，不能拿连字符当判别）。否决案：双字段塞每个 GetXxx（面乘积爆炸）。
4. **project name 维持 team 轴唯一**（ADR-0028 不翻案；§5.4 team 前缀逃生口不做——token 单 team 绑定下无场景，token 多 team 化时联动重评）。
5. **载体名双段律**（swarm/k3s 归一公式）：App workload = `fleetly-<appName>-<proc>-<appID>`（name 段在前 `docker service ls` 同 app 聚簇、id 段收尾锚定；id 全局唯一后 swarm 公式的 team/project 消歧段整段退役）；Database = `fleetly-db-<name>-<id>`；蓝绿 `-g<gen>`/Task/Browse 不变；DNS 发现面 name 基不动（`<proc>`/`<proc>.<appName>`/`db-<id>` env 注入——id 不进用户书写面：重建即换 id，id 基 DNS 等于每次重建引用全断）。**实例级命名归编排器**（swarm task `<service>.<slot>.<taskid>`、k3s pod rs+随机后缀）——载体名只承担 workload 唯一。metricsquery k3s pod 臂双端夹持正则（`fleetly-<name>…<id>`），swarm 臂 label 基不变。`NamespaceRef` 增 `AppName/DatabaseName` 承载可读段。
6. **id 不退场（2026-11-04 补充需求）**：id 界面可见、与 name 同等重要且均可复制——CLI 列表 NAME/ID 双列、get 双行、`--json` 原样；console 列表/详情 id+name 双展示双复制钮（databases 先例推广），路由保持 id 基（URL 是定位器非身份展示面；name 基引入 team 消解 IA 连锁，§5.1 裁）；events `aggregate_id`/审计资源锚/备份对象键仍是 id 面，人类输出留 id 与之自洽。
7. **staging flag-day 重置**：不写数据迁移（验证环境先例充分，外部消费者为零）；旧形 id 在恢复共存面合法（Valid 双形）。

## 后果

- 用户定位动线全面 name 化：`fleetly databases get pg --project shop`；id 从「必须携带的键」退为「可复制的稳定锚」——同名重建即换 id（身份消歧由 ULID 承担，never-reuse 从构造性变为熵保证）。
- `docker service ls`/`docker ps` 一眼定位（`fleetly-shop-web-01jbx…`），两 provider 载体公式归一消除分立解释；内网发现零变化（name 基 DNS 不动，重建不断链）。
- 解析债清偿两笔随批：preview `processOfID`（全仓唯一 id 解析点，查表化）；taskview `-r<seq>` 字典序坑（`r10<r9` 双位数倒序，ADR-0070 时代遗留，换行 Seq 排序）。
- golden 确定性方案：`statetest.New` 注入计数熵 + repo FakeClock（固定起点）⇒ `01kdvdna00000000000000000N` 形全程可复现；`SwapForTest` 调用面守卫收口（生产换源 = golden 假确定性即红）。
- 债与后手：console 路由 name 基（独立小批可换，name 不可变无陈旧链接风险）；token 多 team 化时 team 前缀复合形态重评；exec 目标 `<app-id>/<process>` 的 app 段 name 化未随本批。
- 守卫/测试连坐（同批）：entityid_guard 四件（ORDER BY id 禁令承接/ulid import 单点收口/SwapForTest 调用面收口/seq 迁移不变量）+ entityid 单测（形态/唯一/双形 Valid）+ apitest `TestResolveByNameFaces` + CLI name 形态 golden + 326 golden 重钉。

## 验收锚

- [x] A1：entityid Mint/Valid/LooksMinted 单测绿；守卫四件绿；43 铸造点零 Join/Audit 残留（编译面承载）；preview processOfID 查表化；taskview Seq 排序；326 CLI golden 重钉三连绿。
- [x] A2：swarm/k3s 公式测试绿（`fleetly-<name>…<id>` 双段钉死）；metricsquery 双端夹持正则测试绿；apitest Remove/Ensure ns 断言含 name 段。
- [x] A3a：buf breaking 纯增量过；apitest TestResolveByNameFaces（本队命中/id 短路/复合双形态/他队 404/task+schedule 面）；console:gen 随 swagger 再生成。
- [ ] A3b：CLI 三形态旗标全量换装 + reuse.go 退役 + name 形态 golden ≥2 + skills 三份示例换装。
- [ ] A4：P4 用户面（CLI 双列/get 双行 + console 全实体 id+name 双展示双复制钮）+ staging flag-day 重置上站 + runbook 记录。
