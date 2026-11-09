# fleetly 不透明实体 id 与 name 寻址提案（2026-11-03）

> 状态：**设计全项裁决完毕（2026-11-03）+ 补充需求修订（2026-11-04：id 界面可见、与 name 同等重要且均可复制）**——主轴三项（§0 D1–D3）+ 开放问题五项（§5 裁决记录）全部有裁；本文件为完整设计载体与实施批次真源，实施批复后各批开 **ADR-0092** 并携守卫任务（占号复核 2026-11-04：0091 已被 platform-logs 批并行占用——并行会话编号复核纪律再次实证）。
> 核心问题：实体 id 是否从 ADR-0070 的名基可读形态（`<name>-<seq>`）换成不透明形态；name 是否接棒用户可见定位键；载体名如何跟着改形。
> 结论先行：**换，且地基已留好缝**——ADR-0070 决策 1（顺序语义入每表 `seq` 列）使 id 形态可独立更换，游标协议/分页/排序面**零触碰**。统一设计律一句话：**name 在边界（CLI/API/spec 受理）解析，id 在内核与存储**。
> 词汇遵守 CONTEXT.md：本提案零新词条（id/name 语义不变，变的只是 id 形态与寻址动线）；`resolve` 作 RPC 名与既有 `Resolve` 词汇无冲突（受理面既有用途在案）。
> 关联：ADR-0070（决策 2 名基可读、决策 5 无 ULID 接受面被本提案**取代**；决策 1 seq 顺序单源、决策 3 派生律、决策 4 name dns 标签形态**保留**）、ADR-0028（project name team 轴唯一**维持**，新增 token team 作用域解析缝）、ADR-0007（词汇冻结）、ADR-0048（蓝绿 `-g<gen>` 后缀保留）、ADR-0062（Task 铸造单源 rowmint 保留）、ADR-0021/0026（被 0070 取代的旧游标/ulid 决策，本提案再改写其中 ulid 部分）。

## 0. 提案总表

| # | 提案 | 重量 | 裁决 |
|---|---|---|---|
| D1 | id 形态 = 26 位小写 ULID，全局唯一+时间有序（**二裁**：初裁 `<kind>-<ULID>` 的 kind 前缀否决——类型在消费位皆有承载，id 自述类型属冗余编码且违「不透明」本义；**2026-11-04 补充需求**：id 用户界面**可见**、与 name 同等重要且均可复制——定位键换 name，id 不退场） | 决策 | **✅ 已裁（2026-11-03，2026-11-04 修订）** |
| D2 | project name 唯一性维持 team 轴（ADR-0028 不翻案），按名寻址经 token team 作用域解析 | 决策 | **✅ 已裁（2026-11-03）** |
| D3 | 换形范围 = 全部有名实体一次换（entityid 单源不劈叉）；派生实体保持父链律 | 决策 | **✅ 已裁（2026-11-03）** |
| P1 | entityid 单源重铸（`Join`→`Mint()` 无参）+ 全铸造点换装 + 守卫三件改版 | 中 | 待实施批复 |
| P2 | name 寻址面：Resolve 五 RPC（structure 三枚+automation 两枚，team/项目作用域）+ CLI 旗标三形态语义 + `reuse.go` 客户端扫 List 退役 | 中大 | 待实施批复 |
| P3 | 载体名改形：swarm/k3s workload 归一 `fleetly-<app>-<proc>-<appID>` + DB 载体对称加 name 段 `fleetly-db-<name>-<dbID>` + metricsquery workload/db 臂同源（**DNS 发现面维持 name 基不变；vol/task/run/namespace 公式零改动**） | 中 | 待实施批复 |
| P4 | 用户面：CLI id/name 双列并列展示、console 列表/详情 id+name 双展示双复制钮（databases `id.slice+CopyButton` 先例推广全实体）、skills 示例换 name 形态 | 小 | 待实施批复 |
| P5 | ADR-0092（验收锚+守卫任务）+ staging flag-day 重置 + runbook | 小 | 待实施批复 |

## 1. 现状核查（2026-11-03 四路全仓调查）

| 面 | 现状 | 佐证 |
|---|---|---|
| 铸造单源 | `internal/domain/shared/model/entityid` 四 helper：`Join(base,seq)`/`Derive(parent,kind,seq)`/`Revision`/`Audit`；43 个铸造点全走它，无散装构造器 | `entityid.go:40-69`；各 `persistence/<agg>/repo.go` Create |
| 顺序真源 | 每表 `seq INTEGER NOT NULL` + `UNIQUE(seq)`，`seq_counters` 单调计数器（`persistence.NextSeq`，00043）；游标协议 `after_*_seq`；守卫禁 `ORDER BY id`、禁 `oklog/ulid` import | `state.go`；`guards/entityid_guard_test.go` |
| 寻址现状 | 全链（CLI 旗标/位置参、gRPC `GetXxxRequest{id}`、REST 路径 `{id}`、console 路由 `$appId`）**只用完整 id**；按名仅 create 查重内部用 + CLI intake 客户端扫 List（`reuse.go` 自认丑） | `structure.proto:503-1003`；`cmd/fleetly/cmd/reuse.go:3-8` |
| name 唯一性 | project：team 轴（`00010_team_axis.sql`，ADR-0028）；app/db：项目内（唯一索引已就位，name 点查有索引支撑）；**token 全员绑 team**（密码会话同），authn interceptor 将 TeamID 放入上下文 | `idx_apps_project_name`/`idx_databases_project_name`；`authn/interceptor.go:54,171`；`project/repo.go` 已有 `GetByNameInTeam`（无生产调用方） |
| 载体名 | swarm：`fleetly-<team>-<project>-<appID>-<process>`（63 截断+sha256 兜底）；k3s：namespace `fleetly-<projectID>` + 载体 `fleetly-<appID>-<proc>`（无 team/project 段——**分立公式互不一致**）；容器本体名 swarm 不可控（task 自动命名），平台只控 service 名与 hostname | `swarm/translate.go:78-102`；`k3s/translate.go:99-136` |
| 解析债 | 生产代码解析 id 内部结构**仅一处**：`fleetlygrpc/preview.go:615 processOfID`（`LastIndex("-")` 取进程名，注释已过时；进程名含连字符即错） | `preview.go:613-620` |
| spec 引用 | 跨项目网络挂靠 `project:<project-id>/<network-name>`——**用户手写**，是 id 唯一的用户书写面 | `domain/shared/spec/spec.go:609-641` |

## 2. 目标形态

### 2.1 id 语法（D1，二裁去 kind 前缀）

```
<26 位小写 Crockford base32（ULID）>               有名/独立实体：01jbx8w2v4nq7r3t5y9a6d8hkc
<父id>-<kind字母><seq>                             派生实体（律不变）：01jbx…-d3 / -r7 / -b2
exec-<32hex> / flt_… / sync-<16hex>                出律家族不变
```

- 全局唯一（时间+随机，计数器重置免疫）、时间有序（ULID 内嵌）；DNS 安全（crockford 小写字符集全落 RFC1123 标签）。
- **kind 前缀否决理由**（2026-11-03 二裁）：实体类型在一切消费位皆有承载——载体名域前缀（`db-`/`fleetly-db-`/`fleetly-vol-`/`task-`/`run-`）、事件 `aggregate` 字段、审计资源锚（`database/<id>`）、REST 路径、metrics label——id 自述类型是冗余编码，且违「id 不透明」本义（自述类型=半透明）；否决后 id 更短（26 vs 30+），kind 词表（维护+守卫钉死+Mint 签名带参）整块消失，载体名公式除 workload 外零改动（§3 P3 缩水）。
- `entityid.Valid` 正则**需改**（本体从两段名基形变单段 26 位）：`^[0-9a-hjkmnp-tv-z]{26}(-[a-z][a-z0-9]*[0-9]+)?$` 并纳旧名基形（快照恢复共存面，§4），守卫钉死。
- `Mint()` 无参（各聚合 repo 本身即类型承载点，铸造面不需要 kind 实参）；`Derive`/`Revision` 的 kind 字母（d/b/r/s/p/rt）**保留**——那是同父跨表兄弟消歧，非实体类型自述，与本裁决正交。
- 有名/独立实体全量（project/app/database/volume/network/secret/config/shared_variable/app_variable/task/schedule/team/user/role/alert_rule/token/invitation/upload/githubapp/freeze/node/audit/membership）一律 `Mint()` 铸 ULID——原 `<kind>-<seq>` 族（tok-15 等）尾段一并换 ULID，单源不留两段 seq 形。

### 2.2 name 寻址模型（D2+P2）

统一律：**name 在边界解析，id 在内核与存储**。三处边界：

1. **CLI**：`--project` / `--app` / `--database` 及位置参数收三形态：
   - 完整 id（`app-01jbx…`）——脚本从 `--json` 取值的动线保留；
   - 裸 name——`--project` team 作用域内解析（token 绑 team，天然零歧义）；`--app`/`--database` 裸 name 需同命令存在 `--project` 兄弟旗标；
   - 复合 `project/name`——无 `--project` 兄弟旗标时的形态（先例：exec 目标 `<app-id>/<process>`、spec `project:...`）。
   - 形态判定：整值恰为 26 位 crockford（无连字符）→ id 优先，否则 name（name 恰为合法 ULID 形属病态边角，id 优先序写入文档）。
2. **API**：新增 Resolve 五 RPC（服务端单源解析，替代 `reuse.go` 客户端游标扫 List；structure 面三枚 + automation 面两枚——task/schedule 随 A3 带上，§5.3 裁）：
   ```
   ResolveProject(name) → Project            // token team 作用域内唯一，歧义/不存在 fail-closed
   ResolveApp(project, name) → App           // project 收 id|name
   ResolveDatabase(project, name) → Database
   ResolveTask(project, name) → Task         // name 项目内唯一（含终态行）
   ResolveSchedule(project, name) → Schedule
   ```
   否决案（双字段塞进每个 GetXxx）：每个 handler/protovalidate/golden 各带一支 id-or-name 分叉，面乘积爆炸；Resolve 收敛在五点。
3. **spec 受理**：跨项目网络挂靠 `project:<project-id>/<net>` → `project:<project-name>/<net>`，**intake 期解析成 id 冻结进 Spec IR**——运行时/已存 revision 仍纯 id 面零变化；解析作用域=声明方 app 所属 team（与 token 边界一致）。

### 2.3 不变式（本提案不碰的面）

- `seq` 列与游标协议原样（ADR-0070 决策 1 全保留）；`restore-to-T`/平台恢复重置序号仍合法——ULID 免疫重置撞号（这正是**否决全局整数计数器**的理由：计数器随快照回滚后新铸 id 撞旧 id）。
- 派生实体父链律（`Derive`/`Revision`）原样；蓝绿 `-g<gen>` 后缀原样（ADR-0048）。
- name 不可变（全仓无 rename 面，保持）；name dns 标签形态约束保持（ADR-0070 决策 4——`<process>.<appName>` alias 与 Task 名仍靠它）。
- 「id 不透明、全仓禁解析内部结构」约定保持且强化——现有唯一违例 `preview.go:615 processOfID` 随 P1 清偿（改为查表）。
- JSON/API 响应 id 字段**保留**（协议完整性 + 脚本稳定锚），只是不再是 UI/人类输出的主位。

## 3. 改动面清单（P1–P4）

### P1 铸造面

- `entityid`：`Join` 退役 → `Mint() string` 无参（单点引入 `oklog/ulid`，守卫改「仅 entityid 可 import」+ 铸造输出形态钉死）；`Derive`/`Revision` 不动，`Audit` 尾段 seq→ULID 一并换。
- 铸造点换装：`persistence/*` 各 repo Create（31 处）+ `minttask.go`/`admission.go`/`task.go`/`buildsource.go` 保持单源调用（rowmintguard 语义不变）。
- 清偿：`preview.go:615` 查表化；测试夹具/注释 ULID 残留三处（`delete_app_test.go:98` 等）顺带清。
- 夹具：apitest 夹具 id（`web-1` 形）→ Mint 形；persistence repo_test 手工铸 FK 夹具 id 换形。

### P2 寻址面

- proto：structure 面 Resolve 三 RPC（含 HTTP 注解 `/v1/resolve/...`）+ 服务端实现（`GetByNameInTeam` 上移为生产路径；app/db `GetByName` 已在）；buf breaking 兼容（纯增量）。
- CLI：旗标三形态解析 helper（单点）+ 全旗标语义声明换装（`--project` 描述 "project id or name" 等）；`reuse.go` 三 find 函数退役改走 Resolve；golden 双形态全刷。
- skills：三份 SKILL.md 示例命令换 name 形态（散文同被守卫执法，同批改）。

### P3 载体名（用户「container name 方便定位」诉求的落点）

事实澄清：swarm 模式容器本体名平台不可控（swarm task 自动命名），平台可控面 = **service 名 + hostname**（已钉为 service 名）+ k3s pod 名。改形公式（2026-11-03 三裁补 app name 段、五裁 DB 对称加 name 段；drift guard 与 `metricsquery.go` 读侧同源同批改——workload 臂与 db 臂公式均加 name 段，namespace 臂不变）：

| 对象 | 现 | 新 |
|---|---|---|
| swarm workload | `fleetly-<team>-<project>-<appID>-<proc>` | `fleetly-<appName>-<proc>-<appID>`（name 可读段在前——`docker service ls` 排序同 app 聚簇；id 锚段收尾；id 全局唯一后 team/project 消歧段**整段可删**——k3s 本就没带，两 provider 公式就此归一） |
| k3s workload | `fleetly-<appID>-<proc>` | 同上 |
| 蓝绿 | `…-g<gen>` | 不变 |
| DB 载体 | `fleetly-db-<dbID>` | `fleetly-db-<name>-<dbID>`（§5.5 裁：对称加 name 段，与 app workload 同法） |
| DB DNS alias / 连接串 host / 注入 env | `db-<dbID>` / `FLEETLY_DB_*` | **不变**（发现/注入面不吞载体公式） |
| Task/Run DNS alias | `task-<taskID>` / `run-<runID>` | **不变** |
| volume 载体 | `fleetly-vol-<volID>`（swarm+k3s PVC） | **不变** |
| k3s namespace | `fleetly-<projectID>` | **不变** |
| App↔App alias | `<proc>` + `<proc>.<appName>` | **不变**（见下 DNS 两面律） |

**DNS 名如何表示（两面律）**：

- **发现面（用户书写，name 基，不动）**：app↔app 走既有双别名 `<process>` / `<process>.<appName>`（ADR-0048 决策 3——后者即 app+process 的 DNS 表示）；app→db 走 `db-<dbID>`（平台注入 env，非用户手写，重建后重注入自洽）。**id 不进发现面**——spec/env 是 name 书写面且必须跨重建稳定：重建即换 id，id 基 DNS 等于每次重建引用全断。
- **锚定面（机器/排障，id 基）**：载体名本身即排障 DNS 句柄——swarm service 名天然注册 overlay DNS（traefik 兜底解析走它），hostname 跟随 service 名；k3s 侧发现走 Service 载体（`sanitize(addressing)`，name 基不变），pod 名非 DNS 面，id 段纯承担定位。用户书写面永不引用载体名。

落位示例：swarm service / 容器 hostname = `fleetly-shop-web-01jbx8w2v4nq7r3t5y9a6d8hkc`，DB 载体 = `fleetly-shop-pg-01jbx…`，`docker service ls`/`docker ps` 一眼定位（app/db+process 可读、id 可回查）；内网发现仍 `web` / `web.shop` / `db-<dbID>`。

- 63 截断+sha256 分支保留为安全网（app/proc 双 name 段+id 后总长更易触顶；截断丢可读段但 id 锚+hash 保唯一，provider 按 label 匹配不受影响）。
- **实例级命名归编排器**（2026-11-03 四裁钉案）：载体名只承担 workload 唯一，不承担实例唯一——swarm 每 task 容器自动命名 `<service>.<slot>.<taskid>`、k3s pod `<deployment>-<rs 哈希>-<随机后缀>`，多实例共享载体名是设计形态（VIP/Service 聚合在载体层）；尾段维持 `appID`（proc 段区分同 app 多进程、appID 段区分跨项目同名 app）。可选叠加：swarm Hostname 模板 `{{.Service.Name}}-{{.Task.Slot}}`（现钉 `{{.Service.Name}}`，多实例 hostname 分实例），随 A2 顺带裁。
- e2e：`dind-bluegreen.sh:211` 公式复制点同批改；其余 e2e 从 JSON 提 id 整串回传，形态无关安全。

### P4 用户面（2026-11-04 补充需求：id 界面可见，与 name 同等重要且均可复制）

- CLI 人类输出：list 表格 NAME/ID 双列并列（各可取用）；get 详情 name/id 双行；create 回执 `created app web (id 01jbx…)`；golden 双形态全刷。
- console：路由保持 id 基（§5.1 裁）；列表/详情 id+name 双展示**双复制钮**——databases 列表现有 `id.slice(0,10)`+CopyButton 先例推广全实体（apps 列表补 id 列，与 databases 同形）；name 侧复制钮同批补齐；hero/面包屑维持 name-first 展示，详情区给完整 id。
- skills/golden/守卫例命令同批（AGENTS 纪律：用户可见行为变更与 golden 同 commit）。

## 4. 批次与 ADR

| 批 | 内容 | 依赖 |
|---|---|---|
| A1 | P1 铸造面 + 守卫改版（entityid_guard/新 Mint 形态钉死） | — |
| A2 | P3 workload+DB 载体名两侧（swarm/k3s，公式加 name 段）+ metricsquery workload/db 臂同源 + drift/e2e 公式点（`dind-bluegreen.sh:211` 同步；vol/task/run/namespace 公式零改动） | A1 |
| A3 | P2 proto（Resolve 五 RPC）+服务端+CLI 旗标三形态+reuse 退役+golden 双形态+skills（含 task-pool 按名示例） | A1 |
| A4 | P4 用户面收尾（id/name 双展示双复制）+ ADR-0092（验收锚/守卫任务）+ staging flag-day 重置 + runbook | A1–A3 |

staging 迁移：**flag-day 重置**（验证环境先例充分，无外部消费者；不写数据迁移）。恢复场景注记：旧形态 id 快照恢复到新版共存合法（id 是不透明 TEXT，存储/读取无形态假设；`Valid` 正则双形态兼容）。

## 5. 开放问题裁决记录（2026-11-03 全项有裁）

1. **console 路由键**：✅ **维持 id 基**——URL/REST 是定位器非身份展示面（展示面已 name-first）；换 name 要连动 HTTP 注解/gateway/console 全路由，且 project team 轴唯一引入 IA 连锁（跨 team 同名需 URL 加 team 段或依赖会话上下文）。将来独立小批可换（name 不可变，无陈旧链接风险）。
2. **CLI 人类输出 id 保留度**：✅ **保留末列/详情行**（2026-11-03）→ **2026-11-04 补充需求升格：id 与 name 同等重要**——CLI 列表双列并列、get 详情双行，均可直接复制；console 列表/详情 id+name 双展示+复制钮（databases 已有先例推广全实体）；events `aggregate_id`/审计资源锚/备份对象键仍是 id 面，人类输出留 id 与之自洽，「输出抓 id 喂脚本」动线不断；`--json` 不变，golden 双形态照刷。
3. **task/schedule 按名寻址**：✅ **随 A3 带上**——ResolveTask/ResolveSchedule（automation 面，name 项目内唯一已就位），CLI 旗标三形态同步，task-pool skill 示例换装。
4. **`--project` team 前缀逃生口**：✅ **不做**（YAGNI）——token 单 team 绑定下无此场景；登记为 token 多 team 化时的联动项。
5. **DB 载体名对称项**：✅ **带上**——`fleetly-db-<name>-<dbID>`（DNS alias/连接串/注入 env `db-<dbID>` 面不动），translate 公式与 metricsquery db 臂同批改。
