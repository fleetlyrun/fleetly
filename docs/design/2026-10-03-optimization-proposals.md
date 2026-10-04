# 优化方案提案（2026-10-03）

来源：`docs/research/2026-10-03-competitor-architecture-deep-dive.md`（六竞品架构深潜）§4/§6/§7 的行动项展开。本文是**提案集**——每案给设计、取舍、验收锚与开放问题。**2026-10-03 已全案裁决（§0.1 裁决记录）；实施时按需各自开 ADR 并携带守卫任务（AGENTS.md 纪律）。**

词汇遵守 CONTEXT.md：`rollout`/`deploy(名词单用)` 在 Avoid 表——本文滚动部署一律写"滚动（rolling）"；蓝绿不引入 slot/canary 词汇（canary 列入 Avoid 候选），复用既有 **Generation** 词条表达"双 Generation 并存窗"。

## 0. 提案总表

| # | 提案 | 重量 | 建议批次 | 裁决（2026-10-03） |
|---|---|---|---|---|
| P1 | 升级矩阵集成测试 | 测试设施 | 升级数据安全修复批 | **采纳**（旧版取 git tag 构建） |
| P2 | 执行型注入守卫 | 守卫 | 随 P1 | **采纳** |
| P3 | 单机假设审计 | 审查任务 | N2 开工前一次性 | **采纳** |
| P4 | 守卫体积执法红线 | 守卫 | N2 | **采纳，阈值 1200** |
| P5 | 部署策略面：rolling 默认 + blue-green 可选 | **大** | N3 | **采纳，N3 伴随；四问见 §P5 裁决** |
| P6 | Token 语义补强 | 中 | N2 | **T1/T2 采纳；T3 否决**；空交集 403 带原因 |
| P7 | digest 确定性纪律 | 小 | N2 | **采纳** |
| P8 | schemaVersion check-strategy 三细节 | 小 | 首 spec 演进时 | **记录**，v1→v2 时启用 |
| P9 | Route 发布前校验 | 中 | N2 | **采纳**（traefik 校验能力真机核对为前置） |
| P10 | 准入响应显式化 | 小 | N2 | **采纳** |
| P11 | 错误只打一次 + 进程级脱敏 | 中 | N2b | **采纳，随 Logging Provider 批** |
| P12 | 构建工具链容器化 | 大 | 触发条件定义 | **记录不排期** |
| P13 | Runtime 抽象收缩纪律 | 纪律条款 | N4 | **挂账至 N4 ADR 验收锚** |
| P14 | 事件/审计/授权同源（tsuru 模式） | 大 | 观察项 | **维持 ADR-0037**，重开触发器在册 |
| P15 | engine 观测域垂直切 | 中 | N3 | **并入 N3 与 P5 同批**（避免二次动土） |
| P16 | 集群 DNS 不内置（命名声明归平台、解析器归 Runtime） | 裁决 | 双别名增补挂 P5 | **不做**（维持领域模型 §7）；双别名消歧挂账 P5 同批 |

### 0.1 裁决记录（2026-10-03，依批次次序）

1. **升级修复批**：P1 采纳——旧版二进制取 git tag checkout 构建（零外部依赖）；脚本同时是数据面 stop-first 修复批的验收锚（红→绿记录进 runbook）。P2 采纳——四类生成面真执行用例 + meta 红测试，dbtemplate 落地时补第五组、槽位先占。
2. **N2 开工前**：P3 采纳——审查文档入仓，发现逐条三选一处置，双节点 e2e 补断言。
3. **N2**：P4 采纳、阈值 1200（现仓最大正常文件 628 行留一倍余量，低于竞品事故水位一个量级；1000 太紧徒增白名单噪音）。P6：T1 实时收窄 + T2 fail-safe 门（含 scope 声明表守卫反扫）采纳；**T3 三级 ability 否决**——Secret"值永不回显只回指纹"已把敏感轴内化进实体模型，不存在读明文面；Token 空交集报 **403 带原因**（Agent 可判定"找管理员"而非"重新认证"）。P7 采纳（两条性质测试随首个消费点落）。P9 采纳（traefik 校验能力真机核对为实施前置，不可用降级 schema 级并诚实记录边界）。P10 采纳（四 outcome golden，幂等命中与 admission 去重字段分立）。
4. **N2b**：P11 采纳，随 Logging Provider 批；占位锚=build log 中 Secret 值零出现。
5. **N3**：P5 采纳，**N3 伴随不提前**（N2 重心是安全缺口；Console 部署详情页是价值最大化面；例外口：若升级修复批发现滚动语义与 stop-first 修复存在必须蓝绿机制才解的交互，允许拉子件提前）；**Process 级字段**；**stable 别名双代窗=轮询双代（方案①）**，接流量走 Edge 精确切换，诚实边界入文档；**代次名 `{proc}.g{gen}`、stable 名为主引用面**，代次名只服务 Edge 后端解析与调试。本裁决 N3 开工 ADR 时终审（deletion test 复审）。P15 并入 N3 与 P5 同批——resolveBackend/期望缓存是共同动土面，分两次拆是二次动土；当前锁竞争不是小微规模痛点。
6. **挂账**：P8 记录（首次 v1→v2 启用）；P12 记录不排期（三触发条件在册）；P13 挂账 N4 ADR；P14 维持 ADR-0037（两重开触发器在册）。
7. **即席（2026-10-03，DNS 议题）**：P16——**不内置集群 DNS 服务**，维持"平台只生成命名声明（Addressing/alias 公式），解析器归 Runtime（swarm 节点本地 DNS）"；同网裸名撞名以**双别名**（`进程名` + `进程名.应用名`）消歧，挂 P5 同批定稿。六竞品深调未出现推翻"自建 DNS 不做"（领域模型 §7）的硬理由。

---

## P1 升级矩阵集成测试

**动机**：ADR-0015 验收="升级期间用户 Workload 零重启、路由零中断"，当前只有真机 staging 手工验证；staging pg WAL 事故（start-first+10s grace 硬杀）证明升级链是最脆的面。dokploy 把这件事做成了 CI 常态（`.github/workflows/upgrade-integration-test.yml`：旧稳定版→新版配对矩阵，验证升级期间 PG/Mongo/双 web/静态站存活）。

**设计**：
- 新 e2e 脚本 `e2e/dind-upgrade.sh`：dind 内装**上一个发布版**二进制（版本来源：git tag 或构建产物缓存，需 CI 侧配对逻辑）→ 部署代表性 Workload 集（1 个无状态 web+h2c Route、1 个受管 postgres Database、1 个 resident Task）→ 触发 `fleetlyd upgrade` 到**当前构建**→ 断言三件：
  1. 升级期间 web 的 Route 请求零失败（探针容器持续 curl，容许有限的 in-flight 优雅退出计数，断言 0 个 5xx/连接拒绝——以 ADR-0015 口径为准）；
  2. Database 容器无重启且数据存活（写入标记行→升级→读回）；
  3. Task Run 不被平台重启（`docker ps` 计数稳定 / Run 停止原因为空）。
- CI：`ci.yml` 增 job（与现有三个 dind job 并列），矩阵起点先只做 `latest-tag → HEAD` 单配对，多版本矩阵待发布节奏成型后扩。
- 与升级数据安全修复批（数据面 stop-first+宽 grace）的关系：本脚本是该修复批的**验收锚**——修复批落地前后各跑一次，修复前预期红（复现 WAL 风险面）、修复后绿。

**取舍**：dind 升级测试的版本配对需要"旧版二进制"获取机制（build 行内容寻址复用的坑：直接 checkout 旧 tag 构建，不依赖缓存）。矩阵全量（N-2、N-3）暂缓——发布节奏未成型，先钉最新前一版。

**验收锚**：
- [x] `e2e/dind-upgrade.sh` + `mise run e2e:upgrade` + CI job `e2e-upgrade` 落地〔2026-10-03：旧版=HEAD~1 的 worktree 构建（仓尚无 tag，连续验证"上一版→本版"；tag 通道随发布节奏切换，ci.yml fetch-depth:2 为前提）；本地结构验证走通安装/身份链/三件负载部署（本机 Windows Docker Desktop 当日病灶——docker cp 大文件进 dind 产生"可见不可开"的坏 inode，完整断言链未本地跑完，**CI 首跑为验证真源**）〕
- [ ] CI `e2e-upgrade` 首绿（含 database 负载——受管 DB 在 dind 属首跑面，首轮若 pending 复现则取 CI 日志诊断：本机首跑曾卡 `pending` 超时，现场被环境病灶污染未取证，疑点=离线 dind 受管 postgres 环境面 vs 升级路径，待 CI 干净环境区分）
- [x] runbook `staging-fleetly.md` 升级章引用本脚本为前置检查〔2026-10-03〕
- 注：数据面 stop-first 修复本体已由同日修复批先行落地并真机验收（`3b3dd85` + runbook `9674340`），本脚本锚语义调整为**回归锚**（每次 push 验证"上一版→本版"升级零扰动）。

**开放问题**：旧版二进制取 git tag 直接构建，还是引发布产物 CDN？
**→ 裁决（2026-10-03）**：git tag checkout 构建——零外部依赖，不绑发布节奏。
**→ 落地实录（2026-10-03）**：仓尚无任何 git tag，先行形态=HEAD~1 worktree 构建（比 tag 配对更连续：每 commit 验证一次升路）；首个 tag 发布后切 latest-tag→HEAD。

---

## P2 执行型注入守卫

**动机**：dokploy 的注入测试把生成的命令放进真 `/bin/sh` 执行恶意 payload、用标记文件断言未触发（`__test__/compose/compose-command-injection.test.ts`），比字符串断言/golden 硬一个量级。fleetly 凡生成 shell/SQL/配置文本的面，目前只有 golden 钉形状，没有"真执行"验证。

**设计**：
- `internal/guards` 新增 `injection_test.go`（或按域分散到各包的测试），对四类生成面各立一组用例，payload 统一形态：字段值含 `; touch /tmp/pwned`、`` `touch /tmp/pwned` ``、`$(touch ...)`、`&& touch ...`：
  1. **Enrollment join 材料**（swarm join 命令串）：守卫只验证生成物进脚本文件+scp+sh 的链路（记忆教训：cmd→ssh→sh 三层引号必炸，正是注入面）；
  2. **goose 迁移 SQL**：N2 备份链将生成 SQL（dbtemplate），payload 注入 Database 名/用户名字段，断言生成的 SQL 解析不产生第二个语句（用 SQL parser 断言单语句，而非真跑）；
  3. **traefik 配置**：Route host/path 字段注入，断言生成的 TOML/YAML 解析后不含越界键；
  4. **用户镜像 tag / build args**（Build 面）：注入后走 docker CLI 时无参数逃逸（tag 校验已有？核对 `internal/spec` 归一化校验的覆盖，补缺口）。
- 执行环境：需要真 shell 的用 `testscript`（go 老牌 .txtar 驱动，rogpeppe/go-internal）或直接 `exec.Command("sh", "-c", generated)`，断言标记文件不存在。

**取舍**：真执行测试比 golden 慢，放 guards 单独的 `TestInjection*` 前缀，不拖累常规测试节拍。SQL 面用解析器断言（不真连库）。

**验收锚**：
- [x] 注入守卫落地（2026-10-03 实录，形态按 fleetly 实际修正）：① **shellguard**（`internal/guards/shellguard_test.go`，AST 静态红线：exec.Command* 禁 shell 解释器与裸 `-c`——把"args 数组、永不 shell 拼串"的架构承诺钉成 CI 红线，零豁免全绿）；② **validateImageRef**（`internal/spec`：Source 与进程 image 双面接入，逃逸字符族=空白/控制字符/反引号，语法面诚实归 daemon；payload 家族单测 `TestValidateImageRefInjection`）；③ 核对确认既有防线：git clone（`--` 终结选项解析 + 禁 ext/file 传输，安全批遗产）与 traefik 规则内插（`ValidateRouteHost/Path` 双面白名单）已在位——原案"四类真执行用例"中的 traefik/git 两面无需重造
- [x] dbtemplate 的 BackupCommand 落地（N2，F2.2）时同批补 SQL 解析器级断言（单语句校验，不走 shell）——shellguard 头注释已挂账〔2026-10-04 实录：落地形态为纯 argv 渲染对 + 结构化材料文件（ADR-0039 决策 5/11），零 shell、零平台生成 SQL——原文预期的 SQL 生成面不存在（mysqldump 产物是数据；库/用户初始化走镜像 env 面）。解析器级断言以渲染面注入家族落地：`internal/engine/dbtemplate/dbtemplate_test.go` TestInjectionBackupRenders（敌意密码三面全拒 / 敌意 host argv 形状不变性 / mysql defaults INI 解析器恰一节 [client] 断言 = "单语句校验"的结构化文本等价物 / mongo config JSON 往返 / redis conf 行级钉）；shellguard 头注释挂账行同步实录化〕
- [ ] meta 红测试（守卫自验证）顺延：shellguard 当前零豁免条目，首条豁免出现时再立（届时有真实红样例）

**开放问题**：无实质分歧。

---

## P3 单机假设审计

**动机**：zane 名义用 Swarm、事实单机——健康检查读"本 daemon 所在节点容器"的 DNS（`main_activities.py:1629-1647`）、端口探测 busybox 绑 `0.0.0.0`、volume 测量跑本机 alpine `du`。这类假设不炸单机、专炸多节点。fleetly 双节点 e2e 已存在，但没有系统性的"假设清单"。

**设计**：一次性审查任务（产出文档 `docs/reviews/2026-10-xx-single-node-assumption-audit.md`），列出全部隐含"控制面所在节点"假设的代码路径：
- 已知显式裁决：构建恒在控制面节点（ADR-0019，合法，不列违例）；
- 审查面：StreamLogs 走 swarm API 还是本机 docker.sock（盘点：logs.go 走 daemon h2c——daemon 在控制面节点，swarm API 转发多节点？核对）、InspectWorkloads 的 drift 对照、ExecProbe（swarm service exec 是集群面，OK）、zot 镜像预拉、孤儿清扫的遍历面、Upload 流式 tar 的落盘位置。
- 每个发现的处置三选一：①多节点安全（补双节点 e2e 断言）；②隐式假设（开 issue 进 N2 修）；③显式单机裁决（文档记录边界，如 ADR-0019 同类）。

**验收锚**：
- [x] 审查文档入仓（2026-10-03：`docs/reviews/2026-10-03-single-node-assumption-audit.md`，发现 A~G 逐面处置）
- [x] 发现中的违例清零或全部有显式裁决挂账〔唯一真缺陷=发现 A（StreamLogs 容器发现走 manager 本节点 ContainerList，远端容器日志静默缺失——zane 同款病灶），修复归宿 **F2.4 日志管线重做**（采集面天然多节点），F2.4 前诚实边界="日志流仅覆盖 manager 节点容器"〕
- [x] 双节点 e2e（`dind-two-node.sh`）按发现清单补断言〔尾部新增"日志流覆盖 manager 侧副本"现状下限断言，F2.4 落地后升级为两节点全覆盖〕

---

## P4 守卫体积执法红线

**动机**：六家竞品的 God module 全部从功能正确的代码长出来（coolify 5806 行部署 job、zane 3700 行 models、porter 1750 行 manifest）。fleetly 现有 27 个守卫全部执法**方向**（import/词汇/单源），无一执法**聚集**。盘点线索：`Engine` 结构体 20+ repo 依赖、9 个域子结构、3 把锁；`spec/normalize.go` 628 行。

**设计**：
- `internal/guards` 新增 `sizeguard_test.go`：非生成物 `.go` 文件 >1200 行即红（阈值待裁，见开放问题）；例外白名单条目带理由注释，不再命中即红（白名单双向保鲜惯例）。
- 附带**结构体依赖宽度**检查的讨论项：单结构体构造参数/字段聚合 `providers`+`state` 越过某阈值提示？——此项执法成本高（误报面大），建议只做文件级，结构体级靠 review。

**取舍**：体积红线是粗尺，会有"合理大文件"例外（迁移 SQL、golden 夹具——golden 在 testdata 不扫；生成物已排除）。粗尺的价值是防 5806 行级的事故，不是防 1300 行的边界。

**验收锚**：
- [x] sizeguard 入 CI（`internal/guards/sizeguard_test.go`，阈值 1200，范围 internal/cmd/e2e 非生成物），现仓全绿零豁免〔2026-10-03；现仓最大正常文件 spec/normalize.go 628 行〕
- [x] `normalize.go`（628 行）评估：低于红线近一倍，不拆不豁免（首批白名单为空）

**开放问题**：阈值 1200 还是 1000？
**→ 裁决（2026-10-03）**：1200。现仓最大正常文件 628 行，留一倍余量；1000 会在未来 spec 演进时立刻制造白名单噪音。

---

## P5 部署策略面：rolling 默认 + blue-green 可选（主讨论案）

**动机**：
1. 行业证据：零停机后进者被罚是公认痛点（DX 报告 §2 高频抱怨）——滚动部署中**新副本未就绪就替换旧副本**，启动慢的负载（JVM、冷缓存）出现容量谷。zane 的解法（每个部署独立载体 + slot 别名 + Caddy upstream 切换）实现了"先验后切"与"回滚零重建"。
2. 自家证据：staging 升级事故证明"滚动替换对启动慢/有数据面倾向的负载是危险默认"（数据面已另裁 stop-first+宽 grace，本提案不碰 Database 轨）。
3. fleetly 现状：Deployment 的 releasing = 单代 Ensure + L1 健康门；失败回滚 = 完整 Replay（重建载体，慢）。蓝绿的回滚=切回，代价近零。

**核心设计——Runtime 契约不变，蓝绿是 engine 的编排变体**：

fleetly 的 Runtime 契约是"Ensure(ns, 期望 Workload 集, gen) 幂等收敛"。蓝绿不需要改契约：**期望集阶段性包含两代**即可。

- **Spec 面**：`AppSpec.processes[].strategy`，值 `rolling`（默认）| `blue-green`。新词条候选进 CONTEXT.md：**Deployment Strategy**（Process 级部署切换策略）；`canary` 列入 Avoid（显式不做，T1 后再议）。
- **载体命名**：blue-green 模式下 Workload 载体名带代次成分（Provider 私有公式扩展，如 swarm `fleetly-...-web-g42`）；rolling 维持现名。平台 ID 标记锚定归属不受影响（唯一性本就以平台 ID 兜底，架构 §5）。
- **执行序列**（Deployment 状态机内新增 releasing 的变体路径，状态枚举不变）：
  1. releasing(双代窗)：Ensure 期望集 = 旧代 ∪ 新代（两代并存，期望集语义天然不移除任何一方）；新代带代次化网络别名（`{proc}.g{gen}`，Task per-Run DNS 的同构先例）。
  2. L1 健康门：新代全部就绪。失败 → 直接 Ensure 期望集 = 仅旧代（新代载体被移除，旧代从未被触碰）→ failed。**这步就是"回滚零重建"**：不需要 Replay，因为旧代还在跑。
  3. 切换：Route 后端解析从旧代别名切到新代别名（Edge 发布行集指纹机制复用；`resolveBackend` 消费代次化地址——盘点确认该面已是独立解析点）。
  4. observing（L3 观察窗）：窗内失败 → Route 切回旧代（仍在双代窗）→ 移除新代 → rolling-back 终态路径复用。
  5. 收尾：观察窗过 → Ensure 期望集 = 仅新代（旧代载体移除）→ succeeded；stable 进程别名（ADR-0034 `{app}.{proc}`）在此刻随旧代退役、新代继承。
- **跨进程引用的诚实边界**：双代窗内，跨进程 DNS 引用（app A worker → app B `web`）打到哪一代？swarm 别名轮询会双代分摊。裁决候选：①stable 别名双代窗内轮询双代（与滚动部署的共存窗语义一致，不算倒退，文档明示）；②blue-green Process 的跨进程引用也走代次名（引用方需感知代次——复杂，否决倾向）。**建议①**：接流量走 Edge（精确切换），进程间调用接受短暂双代（本来就是无状态调用语义）。
- **与既有机制的交互**：
  - supersede 抢占：新 Deployment 收口在途 Generation 的既有语义（ADR-0016/领域模型 §4）扩展为"收口在途双代"——旧 Deployment 的双代窗内被抢占时，其新代由抢占者移除，其旧代照常由抢占者序列接管；
  - 配额（ADR-0017）：双代窗内 Workload 计数双代都计（诚实，不豁免）；
  - firstBootJobs：在新代 L1 之前执行（与现序列一致，无交互变化）；
  - Drift/Generation：两代 = 两个已下发 Generation 并存，Drift 对照锚按代次分别记录（Watch 流已有滚动窗口 gen 归因——盘点确认）；
  - **Database 轨明确不适用**：有状态负载双代 = 数据分叉，维持 stop-first+宽 grace 修复批裁决。
- **资源账**：双代窗内副本数翻倍。默认 rolling 不变，blue-green 是用户显式选择；文档明示资源代价。

**取舍与替代方案**：
- 替代 A：只做滚动参数调优（`max_unavailable=0` 语义）——swarm 滚动可配先落后切，但回滚仍是 Replay 重建、且旧代不可保活观察。不解决"回滚零重建"与观察窗切回。
- 替代 B：Runtime 契约加 BlueGreen 子面——违反窄面原则（编排策略是 engine 职责，不是 Runtime 语义）；本方案证明不需要。
- deletion test：砍掉后 rolling+健康门在多数场景够用；blue-green 的独占价值 = ①切流量前新代全量就绪（容量谷消失）②观察窗内切回零代价③staging 类"启动慢负载"安全默认可选。竞品证据 + 自家事故支持保留设计，**但排期可以不急**（见开放问题）。

**验收锚**：
- [ ] blue-green 部署：Route 探针在切换步零 5xx；旧代载体在观察窗内始终存活
- [ ] 新代 L1 失败：旧代零扰动（载体 ID 不变、零重启），Deployment 终态 failed，无 Replay 发生
- [ ] 观察窗内手动切回：Route 指回旧代，请求恢复，旧代载体未重建
- [ ] supersede 在双代窗内抢占：无双代残留（孤儿清扫零新增）
- [ ] 双节点拓扑下蓝绿两代可分节点调度（Placement 约束对两代一致）
- [ ] golden：strategy 字段归一化双形态；CONTEXT.md 词条 + Avoid（canary）同批更新

**开放问题**（讨论焦点）：
1. **排期**：N2 后、N3 Console 伴随（部署详情页"当前代/上一代"叙事是 Console 高价值面），还是提前进 N2？
2. Process 级 vs App 级字段。
3. stable 别名双代窗语义采纳①还是②。
4. 代次名格式 `{proc}.g{gen}`：gen 是单调整数，名字随部署漂移——跨进程引用方看到名字变化是否可接受。

**→ 裁决（2026-10-03）**：
1. **N3 伴随，不提前进 N2**——N2 重心是数据安全与备份（安全缺口优先），蓝绿是体验增益；例外口：若 N2 升级修复批发现滚动语义与 stop-first 修复存在必须蓝绿机制才解的交互，允许拉子件提前。N3 开工 ADR 时终审（deletion test 复审）。
2. **Process 级**——投影单位一致；web 蓝绿 + worker 滚动的混合是真实需求。
3. **方案①（轮询双代）**——无状态跨进程调用本就接受共存窗（与滚动语义一致，非倒退），接流量走 Edge 精确切换；诚实边界入文档。
4. **`{proc}.g{gen}`，stable 名为主引用面**——代次名只服务 Edge 后端解析与调试，不鼓励用户直接引用。

---

## P6 Token 语义补强

**动机**：zane 三维修权中两项是 fleetly 尚未显式承诺的语义（token 永不超过 creator、实时收窄；fail-safe scope 门），coolify 的三级 ability 提供了敏感面粒度讨论的锚点。ADR-0035（行级授权）已落地团队/项目两级绑定，本提案是其收口补全。

**设计（T1 实时收窄）**：
- 语义：Token 每次请求的有效授权 = min(creator 当前授权, Token 声明 Scope/绑定)。creator 被移出 Team / Role 降级 / Project 绑定被删 → Token 权限**立即**收窄（下一次请求生效），无需吊销重发。
- 实现落点：`internal/authn` 拦截器的授权求值已是每次请求执行（ADR-0035 面上），核对当前 Token 绑定判定是否已在请求时与 creator membership 求交；若 Token 绑定是独立快照（创建时固化），改为求交语义 + 审计"收窄生效"事件。
- creator 被删除：Token 立即全体失效（终态），进审计。

**设计（T2 fail-safe scope 门）**：
- 新写面 RPC 忘声明 scope → 一律拒绝（默认关闭而非默认开放）。zane 形态：view 忘写 `required_scopes` 则 token 全拒——服务端强制声明，杜绝"新 RPC 上线时忘了挂授权检查"。
- fleetly 落点：写面动词的 scope 声明表（authn 侧已有 per-RPC 授权映射）+ **守卫反扫**：proto 写面 RPC 未在 scope 声明表落网即红（与 idem 覆盖反扫、errcode usage 反扫同构的注册表只增纪律）。

**讨论项（T3 三级 ability，预期否决）**：coolify 的 `read/write/write:sensitive` 对应的敏感可见面，在 fleetly 被 Secret 模型结构性消除——**值永不回显、只回指纹**，不存在"读明文"API 面。Console 的 Secret 列表可见性走既有行级授权（ADR-0035）。**建议：不引入三级 ability**，理由=敏感轴已内化在实体模型而非权限粒度。

**验收锚**：
- [x] creator 降权后 Token 立即失去对应面（apitest：WhoAmI 面收窄 + 写面 403 + 审计事件；ADR-0038，2026-10-04）
- [x] creator 被删 → Token 全失效（FK 结构性盖住 + resolve 兜底分支 reason=creator_deleted；ADR-0038）
- [x] scope 声明表 + 守卫反扫入 CI（新增写面 RPC 未声明即红）（TestScopeDeclarationsMatchVerbs：方向误标/未知动词/豁免保鲜三面把守；ADR-0038）
- [x] 语义写入 ADR-0035 增补节或新 ADR（ADR-0038 独立成篇）

**开放问题**：Token 的"绑定 Project 集"与 creator 当前可访问 Project 集求交后为空时，Token 是报 403 还是 401 语义？
**→ 裁决（2026-10-03）**：403 带原因——Agent 可判定"找管理员"而非"重新认证"。T3 同日否决（§0.1）。

---

## P7 digest 确定性纪律

**动机**：porter 的参数 digest 实现注释明确踩过坑：仅按 Name 排序会因 `sort.Slice` 不稳定跨调用漂移；正解是投影结构体**全字段排序** + 算法前缀 + 免重解析用于漂移检测（`pkg/storage/run.go:282-320`）。

**设计**（实现纪律，随用随落）：
- fleetly 三处 digest 消费点统一纪律：① AppSpec/Revision 冻结指纹（归一化后的 canonical 形态取 digest）；② Drift 对照锚（ADR-0022）；③ N2 dbtemplate 镜像 digest 钉定（DT-9——此项是 registry digest 透传，不经我们计算，只核对传递完整性）。
- 纪律条款：digest 输入必须经 canonical 化（字段排序确定、map 遍历序不进入输入）；跨调用可比（同输入同输出是**测试钉住**的承诺，不是假设）；输出带算法前缀（`sha256:`）。
- 守卫：canonical 化函数的单测包含"重复调用 1000 次 digest 稳定"+"map 序打乱输入 digest 不变"两条性质测试。

**验收锚**：
- [x] 性质测试入仓（稳定性 + 输入序无关性）（2026-10-04 F2.1 批：managedFingerprint/materialsFingerprint + schema CanonicalJSON——1000 次稳定 + map 构造序 200 轮无关）
- [x] N2 备份链/模板链实施时本纪律进对应 ADR 验收锚（ADR-0036 N2 兑现节引用；F2.2 备份批随批落）

---

## P8 schemaVersion check-strategy 三细节

**动机**：porter 用四年实战换来的三条（`pkg/schema/check-strategy.go`、`migrations/init_cache.go`）：① warnOnly 二元组——版本可读但不等于默认版 → 警告不阻断；② **区间共存**——资源级版本声明支持区间（"1.0.1 || 1.1.0"），日常演进比全局单一版本号便宜；③ 检查结果缓存——每次调用都做 schema 校验太贵，必须按连接缓存。

**设计**：fleetly 当前 schemaVersion 策略（ADR-0002：可读旧版+提示，拒绝跳代）本质是 ①+②的简化形。补两条：
- 落地时机：**首次真正的 spec 演进**（schemaVersion 从 v1 → v2 的第一个变更）时启用，现在只记录纪律；
- 届时形态：读入侧校验返回 `(warn, err)` 二元组；校验结果按（文件/记录身份）缓存在读入路径，不重复解析。

**验收锚**：随首个 spec 演进 ADR 携带（现在无实施项）。

---

## P9 Route 发布前校验

**动机**：caprover 的 nginx 管线"生成→校验→激活→失败回滚"是 Edge 面正确形态（`LoadBalancerManager.ts:104-241`）。fleetly 的 traefik HTTP provider 形态下控制面是真源、天然免疫配置丢失，但**发布一个会让 traefik 拒载的 Route**（畸形规则、冲突 host）目前要等 traefik poll 失败才发现。

**设计**：
- Route 发布步（managed reconciler 的 Route 发布通道）增加前置校验：完整 Edge 配置快照（含新增 Route）经 traefik 校验面验证（traefik 配置 dry-run/validate 能力核对：`--configfile` 校验模式或 API 面的校验端点；不可用则退化为 schema 级校验 + 冲突检测）。
- 校验失败：Route 发布失败并明示（Edge 降级矩阵既有口径："Route 变更失败并明示"），存量路由不受影响。
- 发布后确认：traefik 实际加载确认（poll 间隔 5s 内的回读或事件），失败回滚该次发布。

**验收锚**：
- [x] 发布畸形 Route → 拒绝并给精确原因（不是等 traefik 静默拒载）〔2026-10-04：受理位白名单（安全批 P0 已建，host/path 单真源 `capability.ValidateRouteHost/Path`）+ 新增**发布前 schema 级预检** `validateDynamicConfig`——JSON 严格回读（未知字段=形态漂移）/router.service 引用闭合/规则精确落生成语法（Host/PathPrefix/HostSNI + 反引号定界）/servers URL scheme 合面；预检红=整快照拒绝且快照不换（单测钉死）；畸形 host 的 e2e 断言进 dind-h2c-route.sh（拒绝文案锚 "must be a DNS hostname"）〕
- [x] 校验失败不影响存量路由（e2e 断言）〔2026-10-04：e2e 断言拒绝尝试后 HTTP/h2c 双存量路由继续服务；**traefik 侧行为真机实证**（见开放问题结论）——坏快照整份拒载 + last-known-good 继续匹配转发（502 来自死后端、路由仍在），控制面预检把同一语义提前到发布位〕
- [x] 降级矩阵行更新（Edge 配置发布行的行为细化）〔2026-10-04：架构 §8 Edge 行带真机核对实录 + 预检语义 + 发布后确认未落地的边界注〕

**开放问题**：traefik 校验能力的真机核对（v3 API 是否暴露 config validate）——若不可用，schema 级校验的覆盖边界要诚实记录。
**→ 裁决（2026-10-03）**：真机核对为实施前置动作（非裁决项）；不可用则降级 schema 级校验，边界诚实记录进降级矩阵行。
**→ 真机核对实录（2026-10-04，本机 Docker Desktop + traefik:v3.5=3.5.6 实跑，钉版 v3.5.4 同系）**：①子命令面仅 `healthcheck`/`version`——无 config validate/check 子命令（traefik#2077 长期开口）；②API 面全只读遥测（/api/http/routers、/api/rawdata 等），POST /api/validate=404；③坏动态配置行为=**整文件原子拒载**（同文件好路由一并不加载）+ **last-known-good 继续服务**（502 实证路由仍匹配转发）+ **拒载零日志**（docker logs 恒空——日志面不可依赖，API 面差异是唯一观测通道）；④顺带：file provider 目录模式不收 .json 扩展（.toml/.yml/.yaml）——fleetly 走 HTTP provider（内容 JSON）不受影响。**结论：降级轨道生效**；发布后加载确认（traefik 只读 API 回读）未随批落地——API 面暴露是安全权衡（集群内可达即事实开放，无原生 token 认证），随 Console/证书观测批（IssueCertificate 的"经 traefik API 读证书状态"同批）一并裁决。

---

## P10 准入响应显式化

**动机**：coolify 准入返回结构化结果（`queue_full/skipped/queued` + 既有 deployment 引用），UI/API/webhook 三入口行为一致；调用方（尤其 Agent）可判定"去重命中/排队第几位"。fleetly ADR-0016 已有全部语义，差**响应形态**的显式化。

**设计**：
- 创建型部署 RPC 的受理响应统一携带：`admission: { outcome: deduplicated | queued | merged(latest-wins) | superseded, position: int, existing_deployment: ref }`。
- CLI `--json` golden 同批更新；`deployments create` 人类形态打印"已并入队列第 N 位/与既有部署去重（commit 相同）"。
- 幂等键命中（idem 层）与 admission 去重（engine 层）在响应里可区分——两套机制容易混淆，字段显式分立。

**验收锚**：
- [x] 四种 outcome 各一条 golden〔2026-10-04：CLI golden 流收官步 deploy-supersede（superseded）/deploy-commit-dedup（--json 轮 deduplicated + existing_deployment 引用）+ 既有步形态刷新（queued/merged 携 position；golden 流中段 h.Drive 后在途恒前方——position 2 是诚实形态）。deploy/rollback --json 改渲染响应本体（`{deployment, admission}`，uploads put 同款先例）；webhook accepted 响应同批携带〕
- [x] Agent 场景测试：同 commit 重发 → 拿到既有引用而非新部署〔2026-10-04：apitest TestDeployCommitDedupAdmission（Deploy RPC 面）+ webhook d-1b 轮（engine 层 commit 去重 → admission=deduplicated + existing；幂等层重放轮逐字节等于首次响应，不标 deduplicated——两机制形态天然分立，落地实录与裁决一致）〕

---

## P11 错误只打一次 + 进程级脱敏（N2b 随 Logging Provider）

**动机**：porter 两件纪律——① `log.Error()` 只进 span 不打印，最终错误由 main 统一输出一次（`traceLogger.go:67-70`），从根上消灭错误重复打印；② `CensoredWriter` 包装进程全部 stdout/stderr，敏感值实时替换（`portcontext/context.go:438-474`）。

**设计**（N2b Logging Provider 批的输出纪律，提前记录）：
- 构建日志流（builder 面向用户的 build log）与未来的 exec 输出：敏感值过滤在**出口单点**实现（不散布在各产生点）——Secret 值注入构建 env 时，同步把值登记进该次构建的脱敏表，出口统一替换为指纹短形态。
- 错误链：一处错误在 CLI 输出只出现一次（错误信封 errcode + suggestion 已有；核对长链路错误的重复打印面）。

**验收锚**：随 N2b ADR；占位一条：[x] build log 中 Secret 值零出现（注入已知 Secret 后全量断言）。〔2026-10-04 随 ADR-0040 落地：出口单点 = builder.Build writer 链（redactWriter→环形缓冲+VL ingest），脱敏表 = PushCred.Secret + SecretFiles 值 → `secret:<fp8>` 指纹短形态；单测锚 TestBuildLogRedactionSecretZeroOccurrence + staging 真机（推送凭证零出现）双绿；错误只打一次核对 = CLI renderErrorFor 单点已立、流式动词纯上抛无双打面，核对通过无需修〕

---

## P12 构建工具链容器化（记录不排期）

coolify helper 容器模式（构建工具封容器、控制面只发指令）：价值=升级工具链不动控制面、规避 musl/glibc。fleetly 当前内嵌 buildkit + 钉版 railpack 的单二进制红利真实存在。**触发条件**（满足其一开始）：① 多节点构建缓存诉求（独立构建机）；② 工具链体积/安全更新频率压迫单二进制；③ BuildKit 版本与 daemon 解耦需求。届时 Builder Provider 形态：daemon 内嵌（现状）→ 构建容器（helper 模式）平滑切换，Capability 端口不变。

---

## P13 Runtime 抽象收缩纪律（N4 时复核）

tsuru 教训：docker→K8s 迁移完成后抽象未收缩，17 能力接口 + 注册表永续背负，终局只剩一个实现。**纪律条款**：N4（k3s Provider 试点）兑现 Runtime 抽象的战略价值（ADR-0001 终审场景）后，复审收缩：可选子面未被两 Provider 同时实现的，降级或删除；注册表保留（成本近零），接口面收缩。挂账至 N4 ADR 验收锚。

---

## P14 事件/审计/授权同源（维持 ADR-0037，重开触发器）

ADR-0037（2026-10-03）刚裁决事件/审计声明面维持现状。tsuru 证据（Kind=PermissionScheme，授权/审计/行级可见性同源一棵树，`event/event.go:752-760,294-320`）不构成即时推翻理由——fleetly 单 Team 规模下行级事件可见性需求未出现。**重开触发器**（任一满足）：① Console 批需要事件流按 Team/Project 行级过滤；② 跨团队共享 Project 成为真实需求。触发时再评估"事件 Kind 与 Scope 注册表同源"的形态。

---

## P15 engine 观测域垂直切（结构裁量）

盘点线索：`observDomain` 是所有域的写热点（Engine 结构体汇聚面之一）。候选 5 域拆分已完成机械批，下一步方向是**观测域垂直切**：`observ.go` 的归属解析/期望缓存（`appWorkloadExpectations`、`resolveBackend`）从 Engine 汇聚结构中独立成域内组件，消除"所有域都写同一结构"的锁竞争面。与 P4 同批裁量：若体积红线先触发 observ.go 拆分，本项顺带完成。**注意**：盘点口径"观测缓存不参与决策"在 `resolveBackend` 处有例外（Route 后端解析消费内存缓存）——若 P5 采纳，蓝绿的代次解析恰好要求该面显式化，两项有联动。

---

## P16 集群 DNS 不内置（2026-10-03 即席裁决）

**问题**：是否内置平台 DNS 服务（CoreDNS 类），做集群内的服务名解析。

**裁决：不内置。**平台拥有**命名声明**（Addressing/alias 公式，随投影进 Provider），**解析器归 Runtime**（swarm 节点本地 DNS 127.0.0.11，无集中式单点）。这是领域模型 §7"自建 DNS 不做"的维持——六竞品深调（架构报告 §3/§5）未出现推翻它的硬理由，且六家无一自持集群 DNS（zane 用 swarm alias + slot 别名、caprover 全局一网 + 服务名、dokploy/coolify 全是 docker 网络原生）。

**现状基线**（裁决依据）：
- App 进程别名 = 进程名（ADR-0034，compose 服务名互访语义，staging 真机闭环）；Task 双级 DNS（池级 + per-Run）同机制（ADR-0025 决策 6）；
- P5 蓝绿的全部 DNS 需求（stable 别名双代 RR、代次名 `{proc}.g{gen}`）同样落在 alias 生成面，不需要解析器自持。

**内置 DNS 的五个候选动机逐个驳**：

| 动机 | 评估 |
|---|---|
| 跨 App/跨 Project 撞名消歧 | 真问题，零成本解法见下（双别名） |
| 解析行为控制权（双代轮询/池级负载） | swarm 同 alias 多载体 DNS RR 即所需语义（ADR-0034 决策 3 诚实标注 + 真机实证）；当前无 swarm RR 达不到的需求 |
| Runtime 无关 | 论点错位：平台承诺的是命名约定（Addressing 进投影，alias 公式 Provider 私有），换 k3s 时翻译为 k8s Service 名 + 自带 CoreDNS；自持解析器反而把 Runtime 实现细节变成平台负担 |
| 集群外用服务名访问 | 非 fleetly 场景：外部流量走 Route/Edge（公网域名+TLS），东西向名只在集群内有意义 |
| 诊断观测 | `fleetly net` 类 CLI 诊断命令可覆盖，不需要 server |

**结构性反对理由**：
1. **故障面**：Edge 挂 = 外部流量断（事故等级单列）；平台 DNS 挂 = **集群内一切服务发现断**。用节点本地、无单点的 swarm 内置 DNS 换平台自持组件（哪怕多副本）是可靠性倒退，违背"升级永不弄坏你的东西"的信任主线。
2. **重量**：又一个受管组件全生命周期（部署/升级序/观测），加所有 Workload 改 `--dns` 接入的 ndots/search domain/转发链坑面（Docker29 坑清单级）。
3. **竞品零先例**（上述六家）。

**吸收的真问题——双别名消歧（挂 P5 同批）**：同 Project 两个 App 各有 `web` 进程共享项目网（或跨 Project 挂靠同网）→ 裸名 DNS RR 混流（ADR-0034 决策 3 已诚实标注）。解法：别名生成升级为双值——`进程名`（裸名，保 compose 单栈兼容；单 App 内进程名唯一故归一化路径永不撞名）+ `进程名.应用名`（全名，多 App 共网消歧）。纯投影/alias 通道变化，零解析器。**定稿时机与 P5 同批**（代次名 `{proc}.g{gen}` 与全名 `{proc}.{app}` 同属别名词汇面，一次定稿避免两次动 alias 通道）；提前触发器 = 第一个真实撞名场景出现。

**验收锚**：
- [ ] 双别名随 P5 ADR 定稿并携带（projection 断言 + 同网撞名消歧 e2e）
- [ ] 本裁决维持项：词汇不扩（不新增 DNS server 类词条）；本节即"不做"的记录真源（维持类裁决不开新 ADR，同 P14 处理）

**重开触发器**（任一满足再议内置 DNS）：
1. swarm RR 语义在 P5 双代窗真机验证中被证明不可控（如 VIP 行为与预期不符且无绕法）；
2. 出现"集群外实体必须用集群内服务名"的真实场景（如多集群联邦）；
3. N4 换 k3s 时命名约定翻译出现 k8s Service 语义覆盖不了的缺口。

---

## 附：裁决后行动映射

| 裁决 | 随批守卫任务（AGENTS.md 纪律） |
|---|---|
| P1 采纳 | 无新守卫（e2e 即验收） |
| P2 采纳 | 注入守卫本身即守卫任务 |
| P4 采纳 | sizeguard + 白名单双向保鲜 |
| P5 采纳 | strategy 归一化 golden + CONTEXT.md 词条/Avoid 同批 + 双代收口守卫（孤儿清扫断言）；**P16 双别名随本批：projection 断言 + 同网撞名消歧 e2e** |
| P6 采纳 | scope 声明表反扫守卫 |
| P9 采纳 | Edge 降级矩阵 golden 更新 |
| P10 采纳 | admission outcome golden 四形态 |
