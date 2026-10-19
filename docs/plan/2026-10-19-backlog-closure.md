# 残余 backlog 收口台账（2026-10-19）

> **状态**：本轮收口中——台账已交付；**S13（身份读面/Login 限速/alerts Team 过滤）已验收提交**；S14-S19 按 §3 执行。设计稿阶段：design-only 已过；后续由各批执行子代理更新本文件状态行。
> **日期**：2026-10-19。
> **基线**：`HEAD = 2ada1446`（S1-S12 + D3 已 push `origin/main`）；**另有 S13 工作区改动未提交**（`git status --porcelain` 显示 5 个修改文件 + 4 个新增文件，见 §3.1 注）。
> **来源**：17 份归档报告（`docs/reviews/2026-10-19-architecture-acceptance/R01…R16`，R08 两份）、汇总报告 §4/§5/§11、`docs/plan/2026-10-19-review-remediation.md`、`docs/design/2026-10-19-security-backlog-design.md`、`docs/design/2026-10-19-converge-semantics.md`、ADR-0081/0082/0083、`docs/runbooks/staging-fleetly.md` 2026-10-19 节。
> **用途**：S13-S17 修复批的任务来源与最终报告 §4 的事实来源。逐条只给"裁决 + 动作"，不复述报告证据；证据回链报告原文。
> **读者**：Lead（裁决争议项）、S13-S17 子代理（照 §3 执行）、最终报告作者（照 §4 写风险）。

## 0. 分类定义与使用说明

### 0.1 裁决枚举（每条必选其一，**裁决针对"本轮动作"，不改变严重度**）

| 裁决 | 定义 | 必带信息 | 台账标记 |
|---|---|---|---|
| `FIXED` | 已由 S1-S12 / D3 落码并 push | **commit 或阶段**（来自 plan §各阶段记录 / 报告 §11 / 代码可核） | 只写来源，不写方案 |
| `DUP` | 与台账内另一条同源（同文件/同修复面/同一根因） | **合并到哪个 ID** | 不单独排期、不重复计数 |
| `FIX-NOW` | 本轮（S13-S17）可由子代理安全修复 | 批次编号 + 文件面 + 行为 + 测试锚 + 成本/风险 | 见 §3 分组 |
| `ACCEPT` | 接受现状，不修 | **具体理由**（现状可接受 / 设计取舍 / 上游不可控 / 成本远超收益）+ **触发重开条件** | 含"随触随修"子类：下次触碰该文件时必须同 commit 修正 |
| `SEPARATE` | 需独立立项/设计，本轮不修 | 为什么不能本轮修 + 前置依赖 + 建议 owner + 触发 | 进 §4 风险清单候选 |

### 0.2 三条硬规则

1. **零遗漏**：报告里每个 `R\d\d-[ML]\d+` ID 在本台账 §2 有且仅有一行（`DUP` 也占一行，指向主条目）。
2. **FIXED 必须有来源**：只认 plan 执行记录 / 报告 §11 / commit / 代码现状四者之一；"H 修复顺带"不算来源，除非该 M/L 条目本身被点名或被同一 commit 的 diff 覆盖（逐条核过）。
3. **FIX-NOW 必须可执行**：文件面 + 行为 + 测试形态三要素齐全；给不出测试形态的不许进 FIX-NOW，降级 SEPARATE。

### 0.3 计数口径（重要：与任务书给的 181 有差异，见 §1.2）

- **原始发现**：216 条 = H 18 + M 112 + L 86（`grep` 实测，见 §1.4）。
- **任务书口径**：181 条 = M 104 + L 77。该口径**漏计两处**：R03 的 5 条 L（R03-L1..L5）与 R14 的全部 M/L（8M+4L=12）。
- **本台账口径**：按"零遗漏"硬规则登记 **198 条 M/L**（M 112 + L 86），另将报告标 H 但经 Lead 校准降 M 的 `R01-H1` 作为**外挂行**（†，不计入 198）登记，因为它是仍未修的活缺陷、且汇总报告 §4.2 已按 M 引用。
- **去重后**：9 条 `DUP` → 唯一待处置面 189 条（198-9）；其中 `FIXED` 17、`FIX-NOW` 59、`ACCEPT` 30、`SEPARATE` 83、`DUP` 9。
- **不在本台账范围**：汇总报告 §3 的 15 条 H 已全部修复（§11），本台账不重复登记；唯一例外是 `R01-H1`（报告标 H、Lead 校准降 M、仍未修），按 † 外挂行处理。

## 1. 口径与统计

### 1.1 逐报告 M/L 计数（任务书口径 vs 台账实测）

| 报告 | 模块 | 任务书 M/L | 实测 M/L | 差异 | 本台账行数（含 †） |
|---|---|---|---|---|---|
| R01 | domain 层与端口契约 | 3/2 | 3/2 | — | 5 + 外挂 1 |
| R02 | engine 部署/收敛/状态机 | 7/8 | 7/8 | — | 15 |
| R03 | engine 构建/材料/intake | 13/0 | 13/**5** | **+5 L** | 18 |
| R04 | engine 数据/自动化/观测/备份 | 5/3 | 5/3 | — | 8 |
| R05 | 持久化与状态层 | 8/6 | 8/6 | — | 14 |
| R06 | Runtime Providers（swarm+k3s） | 8/4 | 8/4 | — | 12 |
| R07 | 服务类 Providers 与外部适配 | 5/7 | 5/7 | — | 12 |
| R08 | 接口层与 proto 契约（interfaces） | 11/8 | 11/8 | — | 19 |
| R08 | H1/H2 独立复现（repro） | 0/0 | 0/0 | — | 0 |
| R09 | 组合根/配置/安装/CI | 8/7 | 8/7 | — | 15 |
| R10 | CLI/SDK/Agent 面 | 9/4 | 9/4 | — | 13 |
| R11 | Console 前端 | 8/7 | 8/7 | — | 15 |
| R12 | 守卫/测试设施/e2e | 7/7 | 7/7 | — | 14 |
| R13 | 横切：集成/事务/并发 | 3/3 | 3/3 | — | 6 |
| R14 | 横切：安全/授权/密钥 | **未列** | **8/4** | **+12** | 12 |
| R15 | 横切：验收声明/历史挂账 | 3/5 | 3/5 | — | 8 |
| R16 | 核心声明式模型专项 | 6/6 | 6/6 | — | 12 |
| **合计** | | **104/77=181** | **112/86=198** | **+17** | **199** |

### 1.2 181 vs 198 对账（为什么不按 181 收口）

任务书给的 181 与报告原文的 198 差 17 条，来源可逐一指认，**不是统计噪声**：

| 漏计组 | 条目 | 条数 | 判定 |
|---|---|---|---|
| R03 的 L | `R03-L1..L5`（构建错误未脱敏/属主大小写/方言文档漂移/死注释/空探针载荷） | 5 | 报告 §4 明确编号存在，必须登记 |
| R14 的 M | `R14-M1..M8`（Login 爆破/init owner/节点三动词/team 缺省/freeze 绕过/Edge 端点/下载校验/KEK 轮换） | 8 | 报告用 `####` 四级标题 + 列表，任务书按 `###` 提取时整组漏掉 |
| R14 的 L | `R14-L1..L4`（identity 读面/token 比较/argv 入事件/restic 密码换行） | 4 | 同上（以 `- **R14-Lx｜**` 列表形式出现） |

> 结论：**181 是提取口径的漏计，不是去重口径**。汇总报告 §4 自述"去重后约 105 条 M"、§5"约 75 条 L"，与 198 的差异也来自主题归并（同一主题下多条合并叙述）。本台账选择"先零遗漏登记、再用 DUP 去重"，因此以 198 为工作口径。若 Lead 坚持 181，需要显式裁定"R03-L1..L5 与 R14 的 12 条不修也不登记"——本台账不建议，因为 R14 里含 4 条已修（M2/M3/M4/L2），漏登记会让"已修清单"不完整。

### 1.3 裁决分类计数（按报告 × 裁决）

| 报告 | FIXED | DUP | FIX-NOW | ACCEPT | SEPARATE | 小计 |
|---|---|---|---|---|---|---|
| R01 | 0 | 1 | 1 | 0 | 3 | 5 |
| R02 | 2 | 2 | 6 | 2 | 3 | 15 |
| R03 | 0 | 0 | 8 | 4 | 6 | 18 |
| R04 | 0 | 0 | 1 | 1 | 6 | 8 |
| R05 | 2 | 1 | 1 | 3 | 7 | 14 |
| R06 | 1 | 0 | 6 | 2 | 3 | 12 |
| R07 | 0 | 1 | 3 | 2 | 6 | 12 |
| R08 | 0 | 1 | 5 | 2 | 11 | 19 |
| R09 | 1 | 1 | 3 | 4 | 6 | 15 |
| R10 | 1 | 0 | 2 | 3 | 7 | 13 |
| R11 | 0 | 0 | 0 | 2 | 13 | 15 |
| R12 | 1 | 0 | 8 | 0 | 5 | 14 |
| R13 | 0 | 0 | 3 | 0 | 3 | 6 |
| R14 | 4 | 2 | 5 | 0 | 1 | 12 |
| R15 | 0 | 0 | 1 | 4 | 3 | 8 |
| R16 | 5 | 0 | 6 | 1 | 0 | 12 |
| **合计** | **17** | **9** | **59** | **30** | **83** | **198** |

> 外挂行 `R01-H1†` 记 `FIX-NOW`（S16-c6），不计入上表。

### 1.4 计数校验（可复跑）

```powershell
# ① 报告侧 M/L 计数（应为 112 / 86，合计 198）
$dir="docs\reviews\2026-10-19-architecture-acceptance"
$ids = Get-ChildItem $dir -Filter "R*.md" | ForEach-Object {
  [regex]::Matches((Get-Content $_.FullName -Raw), '(R\d{2}-[HML]\d+)(?=｜)') | ForEach-Object { $_.Groups[1].Value }
} | Sort-Object -Unique
"total=$($ids.Count) M=$(($ids | ? {$_ -match '-M\d+$'}).Count) L=$(($ids | ? {$_ -match '-L\d+$'}).Count)"
# 期望：total=216 M=112 L=86（含 H 18）

# ② 台账侧（§2 表格第一列；应 198 个唯一 ID + 1 个 † 外挂）
$ledger = "docs\plan\2026-10-19-backlog-closure.md"
$rows = Select-String -Path $ledger -Pattern '^\| (R\d{2}-[HML]\d+)' | ForEach-Object { $_.Matches[0].Groups[1].Value }
"rows=$($rows.Count) unique=$(($rows | Sort-Object -Unique).Count)"
# 期望：rows=199 unique=199（含 R01-H1† 的去重后仍 199 个 ID 串）

# ③ 双向差集（两个都应为空）
$reportML = $ids | ? { $_ -match '-[ML]\d+$' }
$ledgerML = $rows | ? { $_ -match '-[ML]\d+$' }
Compare-Object $reportML $ledgerML
# 期望：空（ledgerML 不含 † 外挂行；报告侧 H 行也不进入 $reportML）
```

### 1.5 模块列缩写

`domain`＝R01；`engine-deploy`＝R02；`engine-build`＝R03；`engine-data`＝R04；`persistence`＝R05；`providers-runtime`＝R06；`providers-services`＝R07；`interfaces-api`＝R08；`assembly`＝R09；`cli`＝R10；`console`＝R11；`guards`＝R12；`integration`＝R13；`security`＝R14；`acceptance`＝R15；`declarative`＝R16。

## 2. 逐条台账

> 列说明：`ID`＝报告原文 ID（`†`＝外挂行，不计入 198）；`标题`≤40 字（压缩自报告标题，语义不变）；`裁决`按 §0.1；`依据/动作` 只写台账需要的部分——`FIXED` 给来源，`DUP` 给主条目，`FIX-NOW` 给批次+文件面+测试锚，`ACCEPT` 给理由+触发，`SEPARATE` 给原因+前置+owner+触发。

### 2.1 R01 domain 层与端口契约（3M/2L + 外挂 1）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R01-H1† | redis browse 全链不可用且 API 假成功 | M（校准） | domain | FIX-NOW | S16-c6：`dbbrowser` redis 分支不要求 user（或铸占位 user）；`dbtemplate.ConnURL→Render` 五引擎往返表测（免 Docker）；成本 S |
| R01-M1 | Mongoku 连接串丢 DB/authSource | M | domain | FIX-NOW | S16-c6 同批：`mongoku.go` 用 `Conn.DB` 补库段或显式 `authSource`；连接串钉测；成本 S |
| R01-M2 | ValidateJob 缺校验，注释承诺 fail-closed | M | domain | SEPARATE | 抽公共校验会改变既有 spec 接受面，需与 R01-M3、双 Runtime 翻译同批；owner engine-spec；触发：下次 spec/proto 批次或首个 job 配置错配事故 |
| R01-M3 | IR 值域缺口（resources/副本/探针） | M | domain | SEPARATE | 副本/资源上界需 governance 配额模型（per-Project）+ 双 Provider 翻译同日落地；owner engine-spec+governance；触发：多租户配额需求或资源事故 |
| R01-L1 | FacesOf 14 面只断言 11 面 | L | domain | DUP | 并入 R06-L1（同一 `faces_test.go`，FacesOf 面断言同批补） |
| R01-L2 | 注册表守卫把注释当 usage；GRPC 码名无校验 | L | domain | SEPARATE | 守卫改 AST 判据属守卫加固批（与 R12-L3/L4 同源）；owner guards；触发：守卫元审计批 |

### 2.2 R02 engine 部署/收敛/状态机（7M/8L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R02-M1 | 冷却解除径②在无签名源上恒真 | M | engine-deploy | FIX-NOW | S15-c1：`attemptConverge` 增"携带签名"形参 + `failSig` 三态比较；钉测"冷却中 `IsolateNetworkPeer` 不释放冷却"；成本 S |
| R02-M2 | 裁决表装饰函数，golden 不会变红 | M | engine-deploy | FIXED | S12 `2740a09d`（判定表接进生产）+ `23f4b0b1`（ADR-0082 锚修订） |
| R02-M3 | 动作面重启按 unscoped workloadID 定位 | M | engine-deploy | FIX-NOW | S15-c2：按最新 succeeded Generation 解析代次化载体；swarm/k3s 各补 BG 重启命中用例；成本 S |
| R02-M4 | 删除收口对 failed 待回滚窗失明 | M | engine-deploy | FIX-NOW | S15-c3：Teardown/Delete 预检统一"驱动权"谓词（`DrivingByApp`）；钉测 failed 待回滚窗拒绝收口；成本 S |
| R02-M5 | Cancel 遗留回卷无审计无事件 | M | engine-deploy | FIXED | S12 `2740a09d`：慢通道按真实变更发审计+事件；ADR 行 10 机制更正 |
| R02-M6 | 声明式重启对构建源触发全量重建 | M | engine-deploy | SEPARATE | 需构建缓存键/基线 digest 复用设计 + ADR-0083 追补；owner engine-build；触发：首个构建源 App 重启失败或镜像漂移 |
| R02-M7 | 环 panic 终止无消费面，写面静默停摆 | M | engine-deploy | FIX-NOW | S15-c4：`Loop.Done()` 接入 supervisor + health/doctor 行；钉测环 panic 后可见；成本 S |
| R02-L1 | 已收敛签名漂移消失后不清零 | L | engine-deploy | FIX-NOW | S15-c1 同批：信号集为空时清 `resolved[appID]`；钉测同形态复发走快通道；成本 S |
| R02-L2 | 事件 payload `Kind` 恒空 | L | engine-deploy | ACCEPT | 审计 AfterFP 已可区分 restart/rollback，补列需迁移+事件契约变更，成本远超收益；触发：事件消费者需按 kind 分流 |
| R02-L3 | 翻案后三处陈旧注释/行锚 | L | engine-deploy | ACCEPT | 纯注释漂移、无行为影响；随触随修（改 `observ.go`/`networkrebuild.go`/ADR-0082 时同 commit 修） |
| R02-L4 | 收敛事实走非事务 Emit，两行可单边落库 | L | engine-deploy | SEPARATE | 需"事务外事实发射契约"基座（汇总 §6.2-S3），跨 kernel/engine；owner kernel；触发：事实双写缺口事故或 ADR-0080 修订 |
| R02-L5 | 冻结闸门 fail-closed 分支无真钉测 | L | engine-deploy | FIX-NOW | S17-c8：构造 Project 不可解析/注入 Freezes repo 错误，断言跳过本拍、不落签名、下拍重试；成本 S |
| R02-L6 | 「账本只由 Ensure 写」只有注释 | L | engine-deploy | DUP | 并入 R12-M3（同一承诺缺守卫，守卫落在 guards 批） |
| R02-L7 | 冷却记账不随 App 收口清理 | L | engine-deploy | DUP | 并入 R16-L2（同一 App 收口清理面，一并清 9 表） |
| R02-L8 | egress 事实读失败 fail-open | L | engine-deploy | SEPARATE | 需口径裁决（读失败=跳过本拍 vs 空集代偿），且跨 R04 主审文件；owner converge；触发：ADR-0052 修订或首次 egress 隔离缺口 |

### 2.3 R03 engine 构建/材料/intake（13M/5L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R03-M1 | 构建输入登记永不删且常驻明文凭证 | M | engine-build | FIX-NOW | S16-c8：终态/defer 删 `build.inputs` 条目、失败回滚同删；断言终态后 map 无残留；成本 S |
| R03-M2 | git 上下文以"目录存在"判完成 | M | engine-build | FIX-NOW | S16-c8：clone 到 `.cloning` + 成功原子 rename（与 upload 同纪律）；中断用例断言不采半截目录；成本 S |
| R03-M3 | `contexts/<rev>` 无保留/清扫策略 | M | engine-build | SEPARATE | 需容量模型 + 清扫安全条件（无引用校验）；owner engine-build；触发：数据根容量阈值或首次写满 |
| R03-M4 | Build 终态对 Revision 粘滞 | M | engine-build | SEPARATE | 重铸语义/API 重试通道需裁决（行不删、seq 递增）；owner engine-build；触发：用户取消后重部署撞墙 |
| R03-M5 | compose `$$` 二次转义 | M | engine-build | FIX-NOW | S15-c8：只对奇数个连续 `$` 加倍；overlay 用例；成本 S |
| R03-M6 | `replicas: 0` 静默归一为 1 | M | engine-build | FIX-NOW | S15-c8：不支持缩零则受理期拒收并给英文文案；overlay 用例；成本 S |
| R03-M7 | label job secrets 长语法丢 target/uid/gid | M | engine-build | FIX-NOW | S15-c8：label 方言 job 复用 `serviceSecrets`/长语法执法；正反用例；成本 S |
| R03-M8 | external `name` 覆盖只对 volumes/secrets 生效 | M | engine-build | FIX-NOW | S15-c8：networks/configs 解析声明 `Name`；用例；成本 S |
| R03-M9 | `cache_from` 有契约无消费方 | M | engine-build | SEPARATE | 二选一（接 BuildKit / 删契约字段）需 IR 契约裁决；owner engine-build+proto；触发：IR 契约批次 |
| R03-M10 | schema 把 int64 声明为 integer | M | engine-build | SEPARATE | 改输出类型会改 Agent 自描述契约 + golden；owner schema；触发：Agent 面契约批 |
| R03-M11 | git 批 compose 不冻结 `source_text` | M | engine-build | SEPARATE | 涉及 webhook 摄取透传 + 存量回填口径；owner intake；触发：compose 读面需求 |
| R03-M12 | 删变量后 material push 仍用冻结值 | M | engine-build | SEPARATE | 需"变量来源账本"改造冻结链（ADR-0072 决策 6 重写）；owner deployspec；触发：变量删除误用事故 |
| R03-M13 | registry tags 仓名只做前缀校验 | M | engine-build | FIX-NOW | S14-c1：repository 收敛为平台仓名白名单（禁 `..`/`?`/`#`）；engine 层正反用例；成本 S |
| R03-L1 | 构建终态错误文本未脱敏 | L | engine-build | ACCEPT | 未发现 Secret 入 error 的路径（脱敏主出口在日志）；随触随修（终态错误过脱敏表）；触发：上游错误现实例 |
| R03-L2 | GitHub 属主匹配大小写敏感 | L | engine-build | ACCEPT | 仅可用性（落匿名 clone + 提示误导）；随触随修（owner 双侧归一）；触发：下次改 `gitmaterial.go` |
| R03-L3 | 方言文档仍把 configs 列为未开放 | L | engine-build | ACCEPT | 文档滞后、代码已支持；随触随修（改 `compose-dialect.md` 时补 configs/external）；触发：文档批 |
| R03-L4 | apptemplate 注释引用已删函数+死分支 | L | engine-build | ACCEPT | 注释误导、分支不可达；随触随修；触发：下次改 `apptemplate.go` |
| R03-L5 | 空探针载荷 `["CMD"]` 被受理 | L | engine-build | FIX-NOW | S15-c8：CMD/CMD-SHELL 载荷为空即拒；overlay 用例；成本 S |

### 2.4 R04 engine 数据/自动化/观测/备份（5M/3L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R04-M1 | Owner Lease 首次布防缺失 | M | engine-data | FIX-NOW | S16-c8：resident + owner 铸造即写 `LeaseDeadline`；不续期→宽限排空用例；成本 S |
| R04-M2 | 日志游标越过丢批 + flush 竞态 | M | engine-data | SEPARATE | 需游标回退/流重启语义 + 并发 flush 测试；owner engine-data；触发：日志丢批事故或可观测性批 |
| R04-M3 | Task/指标缓存与缓冲区无回收 | M | engine-data | SEPARATE | 需容量模型（metrics lastCPU/backup stderr/locks）；owner engine-data；触发：长跑内存曲线或 OOM |
| R04-M4 | Exec attach 与 finish 交错留孤儿 | M | engine-data | SEPARATE | 需显式附件状态机（idle/attaching/attached/closing）；owner engine-data+runtime；触发：节点侧孤儿 exec 事故 |
| R04-M5 | 备份/恢复最后回执写失败无收尾 | M | engine-data | SEPARATE | 与备份链完整性批同做（行态清扫 + 退避重试）；owner engine-data；触发：备份批（与 R05-L6/R07-L2 同） |
| R04-L1 | Schedule 时区/DST 无测试钉住 | L | engine-data | ACCEPT | DST 语义由 tzdata 保证、当前无跨时区客户；触发：首个 DST 地区客户或调度投诉 |
| R04-L2 | browse cookie 在 TLS=auto 无 Secure | L | engine-data | SEPARATE | 需 TLS 模式贯通到 cookie 写入点；owner browse；触发：TLS=auto 生产部署或 browse 面下次触碰 |
| R04-L3 | node.left 发射失败仍 available=true | L | engine-data | SEPARATE | 需生命周期口径（先落库后发射/重试标记）；owner nodes；触发：节点抖动处置批 |

### 2.5 R05 持久化与状态层（8M/6L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R05-M1 | 非事务 audit.Append 并发撞 UNIQUE 丢行 | M | persistence | FIXED | S5 `cdecabe0`：Append 串行化 + 有界重试 |
| R05-M2 | 硬删行破坏 seq/id 永不重发 | M | persistence | SEPARATE | 需影子序列/墓碑化迁移（ADR-0070 修订）；owner persistence；触发：事件重发事故或 ADR-0070 修订 |
| R05-M3 | 迁移 00016 对存量冲突无清数/前置校验 | M | persistence | SEPARATE | 需前置校验 + 前进测试矩阵（含最早库有数据）；owner persistence；触发：升级前滚演练或存量升级失败 |
| R05-M4 | outbox 按 created_at 裁剪产生中间洞 | M | persistence | FIXED | S5 `cdecabe0`：改 seq 前缀裁剪 |
| R05-M5 | SQLite 连接池=1，长扫描串行化全局 | M | persistence | SEPARATE | 池>1 需事务/单写者模型压测（WAL 读并发收益）；owner persistence；触发：规模压测或 p99 尾延迟 |
| R05-M6 | 关键查询缺索引/全表扫描 | M | persistence | SEPARATE | 索引批依赖 M5 结论 + 慢查询观测；owner persistence；触发：同 R05-M5 |
| R05-M7 | 审计读面无游标，只能读最近 ≤1000 行 | M | persistence | DUP | 并入 R08-M6（同一审计/List 分页契约，proto+CLI+Console 同批） |
| R05-M8 | statefacade 表清单停在 00005（19/38） | M | persistence | FIX-NOW | S17-c1：从 `migrations/*.sql` 解析 `CREATE TABLE` 动态生成扫描面；新增表即纳入；成本 S |
| R05-L1 | `browse.Delete` 死分支（ErrNoRows） | L | persistence | ACCEPT | 无行为影响（幂等成功靠不查 RowsAffected）；随触随修；触发：下次改 browse repo |
| R05-L2 | 重复删除语义跨聚合 200/404 分裂 | L | persistence | SEPARATE | 需 API 裁决（统一幂等成功或统一 404）+ 跨聚合一致性测试；owner api-contract；触发：SDK/CLI 重试语义批 |
| R05-L3 | 状态词表硬编码在 SQL，无守卫咬合 | L | persistence | SEPARATE | 需 `ActiveStateStrings()` 单源 + 迁移索引谓词守卫；owner persistence+state；触发：新增 run/deployment 状态 |
| R05-L4 | Transit 列白名单静默丢未列入字段 | L | persistence | ACCEPT | 当前列清单与 mut 面一致，风险在未来加字段；随触随修（文档列白名单）；触发：新增 Transit 可写字段 |
| R05-L5 | 三个 Upsert 注释称"tombstone 复活" | L | persistence | ACCEPT | 注释漂移、实现为同名新建；随触随修；触发：下次改三处 Upsert |
| R05-L6 | `DueForPrune` 无状态谓词，可裁在途行 | L | persistence | SEPARATE | 随备份链完整性批（SQL 加 `status NOT IN (...)`）；owner persistence；触发：备份批（与 R04-M5 同） |

### 2.6 R06 Runtime Providers（swarm+k3s）（8M/4L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R06-M1 | k3s ConfigMounts 静默丢弃，ConfigMap 孤建 | M | providers-runtime | FIXED | S6 `b37a61c1`：ConfigMap 卷 + SubPath 单文件挂载（0444） |
| R06-M2 | k3s 副本数硬编码 1 致假 drift/假收敛 | M | providers-runtime | FIX-NOW | S16-c1：读 Deployment `Spec.Replicas`；把 `replicas=1 恰好通过` 改 `replicas=3` 断言；成本 S |
| R06-M3 | k3s 逐 pod 事件不聚合，degraded 被覆盖 | M | providers-runtime | SEPARATE | 需与 swarm `aggregateWorkloadEvents` 同源 + 多 pod 测试；owner k3s；触发：k3s 多副本 App 上线 |
| R06-M4 | k3s 日志帧时间/节点/until 不等价 | M | providers-runtime | FIX-NOW | S16-c3：`Timestamps: true` 解析 RFC3339 + 按 pod 标记还原平台节点 ID + `Until` 明示；logs 单测；成本 S |
| R06-M5 | swarm exec stdin EOF 后无退避忙轮询 | M | providers-runtime | FIX-NOW | S16-c5：`stdinDone` 置 nil 恢复 150ms 节拍 + 可注入节拍测试；成本 S |
| R06-M6 | k3s egress deny netpol 仍 create-only | M | providers-runtime | FIX-NOW | S16-c2：`reconcileEgressNetpol` 走 `putNetpol` 单源；形状漂移测试；成本 S |
| R06-M7 | swarm Config 载体无清扫面 | M | providers-runtime | FIX-NOW | S16-c4：`RuntimeHygiene` 扩 config 面（managed 标签+前缀+现役引用集+宽限窗）；`orphan_test` 扩用例；成本 S |
| R06-M8 | 中继写队列满静默丢控制帧 | M | providers-runtime | SEPARATE | 需控制帧/数据帧分队列与背压协议；owner runtime-relay；触发：exec 控制帧丢失事故 |
| R06-L1 | FacesOf 不含 RuntimeRestart，断言清单缺项 | L | providers-runtime | FIX-NOW | S17-c1：`ProviderFaces` 增 `Restart`，断言 Exec/Restart；engine 两处改走 FacesOf；成本 S |
| R06-L2 | swarm `mapEvent` 把 node 事件全映射 NodeJoined | L | providers-runtime | ACCEPT | leave 由 `DescribeCluster` 对账兜底，仅事件命名失真；随触随修；触发：出现 node leave 事件消费者 |
| R06-L3 | ADR-0052 载体映射表两处漂移 | L | providers-runtime | ACCEPT | 文档-实现漂移（liveness 有意不做/ConfigFiles 已落地）；随触随修；触发：下次改 ADR-0052 或载体映射 |
| R06-L4 | k3s Addresses 不校验载体存在 | L | providers-runtime | SEPARATE | 需按实际 Service 列表交集出端点（与 swarm 同构）；owner k3s；触发：路由发布到空端点事故 |

### 2.7 R07 服务类 Providers 与外部适配（5M/7L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R07-M1 | VL follow 零锚点算出 ~292 年偏移 | M | providers-services | SEPARATE | 与 R07-L1 同属 VL 查询面批（锚点 + 4xx 类型化 + LogsQL 转义）；owner providers-logging；触发：排障检索回放事故 |
| R07-M2 | traefik 名字可生成 `@`；大写 host 永不命中 | M | providers-services | FIX-NOW | S14-c3：`routeKey` 白名单折叠 + `validateDynamicConfig` 名字字符集 + host 小写归一；golden 同批；成本 S |
| R07-M3 | zot 凭证并发首铸竞速 | M | providers-services | SEPARATE | 需 singleflight/唯一 tmp + 回读校验；owner providers-registry；触发：并发首建 401 或首次多 App 同建 |
| R07-M4 | GitHub 出站无重定向约束/超时（盲 SSRF） | M | providers-services | FIX-NOW | S14-c2：`NewClient` 注入 `CheckRedirect` + `Timeout`，`DialContext` 校验解析 IP；重定向/挂起用例；成本 S-M |
| R07-M5 | Health 语义分裂（traefik 恒 true） | M | providers-services | SEPARATE | 需 liveness/readiness 语义进 Provider 契约 + status 降级矩阵；owner providers；触发：降级矩阵批 |
| R07-L1 | VL 上游 4xx 未类型化（落 500） | L | providers-services | SEPARATE | 随 R07-M1 同批（错误分层对齐 VM）；owner providers-logging；触发同 R07-M1（排障检索回放事故） |
| R07-L2 | localobjectstore 固定 `.tmp` + 未 fsync 目录 | L | providers-services | SEPARATE | 对象写原子性随备份链完整性批；owner localobjectstore；触发：掉电丢对象/并发同键写 |
| R07-L3 | keyring 首启 KEK 写非原子/无权限复核 | L | providers-services | FIX-NOW | S14-c4：tmp+rename+fsync+权限复核；崩溃窗用例；成本 S |
| R07-L4 | keyring/railpack 测试在 Windows 恒红 | L | providers-services | ACCEPT | Windows 非支持平台，R09-L7 显式化后按平台 skip；触发：Windows 支持立项 |
| R07-L5 | upload blob GC refcount TOCTOU | L | providers-services | SEPARATE | 需对象生命周期设计（dedup 与清扫互斥）；owner upload；触发：dedup 后部署失败事故 |
| R07-L6 | traefik 共享令牌进受管 argv | L | providers-services | ACCEPT | 已知例外，宿主 root 面即信任边界；触发：引入低权运维或多租宿主时须重评 |
| R07-L7 | zot 内容代理直插 URL path | L | providers-services | DUP | 并入 R03-M13（同一仓名归属/路径段校验面） |

### 2.8 R08 接口层与 proto 契约（11M/8L；repro 报告 0 条 M/L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R08-M1 | 节点读面对 member 开放且无 Team 过滤 | M | interfaces-api | FIX-NOW | S16-c7：`ListNodes`/`ListNodeContainers` 平台门或按 callerTeam 行过滤；成员/跨队负例；成本 S |
| R08-M2 | 观测/告警读面全平台开放（残余 alerts） | M | interfaces-api | FIX-NOW | S13-c3：`ListAlertRules`/`ListAlertStates` Team 过滤；指标半边已由 S9/S10 收口；**工作区进行中**；成本 S |
| R08-M3 | 复合 handler 直调服务方法绕过 scope | M | interfaces-api | FIX-NOW | S14-c6：内层动作显式 `HasScope`（或注解取能力并集）+ 自定义角色授权测试；成本 S-M |
| R08-M4 | PutSecret 无值上限（32MiB 收包） | M | interfaces-api | SEPARATE | 需载体上限核对 + per-Project 配额（acceptance quota 族）；owner interfaces+governance；触发：配额批 |
| R08-M5 | 85/153 RPC 无必填注解，同族 400/404 分裂 | M | interfaces-api | SEPARATE | 契约大扫 + 守卫（寻址字段 required 或登记豁免）；owner api-contract；触发：proto 契约批 |
| R08-M6 | 审计无游标 + identity/alerting 无分页 | M | interfaces-api | SEPARATE | 需 proto only-add + repo 游标 + CLI/Console 同批；owner api-contract；触发：分页批（含 R05-M7/R11-L7） |
| R08-M7 | 流式/票据面无并发/时长/数量上限 | M | interfaces-api | SEPARATE | 需限流基座扩展（per-token/team 信号量 + 单流时长 + ticket 容量）；owner interfaces；触发：DoS 演练或流式批 |
| R08-M8 | `factOf` 归属解析失败吞成平台级可见 | M | interfaces-api | FIX-NOW | S14-c7：解析失败返回 error 整单拒绝（fail-closed）+ 事实 TeamID 为空须显式理由守卫；成本 S |
| R08-M9 | 公开 GetStatus 无认证且透传内部信息 | M | interfaces-api | FIX-NOW | S14-c8：拆公开最小态与鉴权详情 + 探活 TTL 缓存；匿名面负例；成本 S-M |
| R08-M10 | 原生入口先读 body 后验凭证，ReadTimeout=0 | M | interfaces-api | SEPARATE | 读预算需网关层统一（MaxBytesReader/超时/反代上限）；owner gateway；触发：暴露面加固批 |
| R08-M11 | 同步长动词与 30s unary 预算错配 | M | interfaces-api | SEPARATE | 需长动词豁免或异步契约（触发+Wait）；owner api-contract；触发：备份触发 DEADLINE_EXCEEDED |
| R08-L1 | 删除型动词 scope 阶梯不一致 | L | interfaces-api | SEPARATE | 属权限模型第二维（H1 根因，ADR 未立）；owner authz ADR；触发：权限模型 ADR |
| R08-L2 | Cipher 关态三处直接解引用 | L | interfaces-api | SEPARATE | 接口层健壮性小批（统一 nil 前置检查/单点 helper）；owner interfaces；触发：S18 小批 |
| R08-L3 | 分页 limit 绕过 `listLimit()` | L | interfaces-api | SEPARATE | 同上小批（三处改 `listLimit`）；owner interfaces；触发：S18 小批 |
| R08-L4 | SSE 注释称支持 Last-Event-ID，实际不读 | L | interfaces-api | SEPARATE | 随流式批（与 R08-M7/R11-M4 同）；owner interfaces；触发：流式批或 SSE 重连投诉 |
| R08-L5 | registry.proto 缺默认错误响应 | L | interfaces-api | ACCEPT | proto 卫生、运行无影响；随触随修（改 registry.proto 时补 swagger option）；触发：proto 批次 |
| R08-L6 | 非内容哈希文件也发 immutable | L | interfaces-api | DUP | 并入 R11-L3（同一静态资源缓存策略，console/embed 两侧同改） |
| R08-L7 | >72 字节口令走 bcrypt 错误 → 500 | L | interfaces-api | ACCEPT | 无数据/安全后果；随触随修（长度约束与 R08-M5 CEL 同批）；触发：契约批 |
| R08-L8 | InstantiateTemplate 跨队复用 Route 并回显 id | L | interfaces-api | SEPARATE | 需模板实例化语义裁决（跨队 host 冲突应 409）；owner templates；触发：模板多队使用 |

### 2.9 R09 组合根/配置/安装/CI（8M/7L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R09-M1 | CI 无生成物零漂移门禁 | M | assembly | FIXED | S5 `cdecabe0`：CI 增 Codegen job（`mise run generate:verify` 零漂移） |
| R09-M2 | install.sh 无升级语义 | M | assembly | SEPARATE | 需升级分支（备份旧二进制/restart/代次健康）+ 真机验证；owner assembly-release；触发：首次原地升级事故 |
| R09-M3 | 下载物无校验和/签名，安装脚本管道执行 | M | assembly | SEPARATE | 需发布流水线产出 SHA256SUMS/cosign；owner release；**触发：v1 发版前（供应链阻断项）** |
| R09-M4 | `data.root` 配置面三分裂 | M | assembly | SEPARATE | 需 Provider 工厂 data-root 通道（与 registry/logging 同款）；owner assembly/config；触发：数据根分裂事故 |
| R09-M5 | 关停预算被框架 5s 截断 | M | assembly | DUP | 并入 R13-M2（同一 `StopTimeout=5s` 与排水预算） |
| R09-M6 | janitor Stop 不 join 在途 sweep | M | assembly | SEPARATE | 随关停序批（`cancel(); <-loop.Done()`）；owner assembly；触发：与 R13-M2 同批 |
| R09-M7 | 无单实例约束，同数据根可双 daemon | M | assembly | SEPARATE | 需数据根 flock + 运维语义（升级/备份共存）；owner assembly；触发：双 daemon 双写事故 |
| R09-M8 | proxy 端点默认全接口 + 无认证 | M | assembly | SEPARATE | 需暴露面默认安全设计（ADR-0036 前置项）；owner assembly；触发：多租户启用前 |
| R09-L1 | 8.6MB Linux ELF 提交在仓根 | L | assembly | ACCEPT | 历史产物、不影响运行；触发：仓库体积治理批 |
| R09-L2 | config-example 只覆盖 3/11 配置面 | L | assembly | ACCEPT | 模板滞后；随触随修（补顶层段 + 覆盖守卫）；触发：配置模板批 |
| R09-L3 | 组合根注释与现状不符 | L | assembly | ACCEPT | 注释漂移；随触随修；触发：下次改装配 |
| R09-L4 | `wire` 跑 `go mod tidy` 但零漂移不覆盖 go.mod | L | assembly | FIX-NOW | S17-c5：断言范围含 `go.mod`/`go.sum` 或拆 `tidy` 独立任务；成本 S |
| R09-L5 | install.sh 预建数据根 0755 压过 0750 | L | assembly | FIX-NOW | S17-c7：`install -d -m 0750` + 幂等 `chmod`；installenv guard 断言；成本 S |
| R09-L6 | buf.yaml 一次性更名豁免已过期 | L | assembly | ACCEPT | 无行为影响；随触随修（删 ignore）；触发：buf 配置批次 |
| R09-L7 | CI 无跨平台编译门禁 | L | assembly | FIX-NOW | S17-c7：CI 加 `GOOS=windows GOARCH=amd64 go build ./cmd/... ./internal/...`；成本 S |

### 2.10 R10 CLI/SDK/Agent 面（9M/4L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R10-M1 | 两个 delete 成功路径 `--json` 为空 | M | cli | FIX-NOW | S17-c8：成功回 `{"deleted":id}`（或合法 `{}`）+ golden 断言 `json.Valid` 非空；成本 S |
| R10-M2 | 18 条 usage 旗标排在位置参数后 | M | cli | SEPARATE | 需逐条改 + 守卫把 usage 喂真 parser；owner cli；触发：CLI 契约批 |
| R10-M3 | sslip 缺省 host 不剥 `grpc(s)://` | M | cli | SEPARATE | 需 scheme 解析单点 + quickstart 带 scheme 用例；owner cli；触发：CLI 契约批 |
| R10-M4 | download-backup 无流 deadline 且失败留半文件 | M | cli | SEPARATE | 需 `.part`+rename+noDeadline+大对象测试；owner cli；触发：大备份恢复验收 |
| R10-M5 | 连接参数面（凭据 addr 只写不读）不一致 | M | cli | SEPARATE | 需 flag>env>凭据 单源裁决 + 兼容；owner cli；触发：CLI 契约批 |
| R10-M6 | Windows 上 `cmd/fleetly` 不可编译 | M | cli | FIXED | S1 `01bd214e`：SIGWINCH 按平台拆分（`exec_resize_{unix,windows}.go`） |
| R10-M7 | exec 上行泵两 goroutine 并发 Send | M | cli | SEPARATE | 需单发送者重构（channel 合并）+ 假 stream `-race`；owner cli；触发：流契约报错/exec 异常 |
| R10-M8 | skills database-provision 与 CLI 实质漂移 | M | cli | SEPARATE | 需技能重写 + 能力存在性守卫（动词清单反扫）；owner skills/cli；触发：技能批 |
| R10-M9 | SDK `FromError` 用直接断言非 errors.As | M | cli | SEPARATE | 随 CLI/SDK 契约小批（与 M2/M5 同）；owner sdk；触发：SDK 用户报告包装错误不可投影 |
| R10-L1 | 双形态 golden 守卫只查文件名且只递归一层 | L | cli | FIX-NOW | S17-c8：递归嵌套组 + 成功 `-json` 断言非空 json.Valid；成本 S |
| R10-L2 | `tasks scale --concurrency` 缺省 0 会排空池 | L | cli | ACCEPT | 误用需显式漏旗标；随触随修（`fs.Visit` 判定）；触发：下次改 tasks scale |
| R10-L3 | 根命令 `--json` 输出人类帮助 | L | cli | ACCEPT | 根非契约面（组才是）；随触随修；触发：根契约批 |
| R10-L4 | `events list` synopsis 过期 | L | cli | ACCEPT | 文案滞后；随触随修；触发：下次改 events 动词 |

### 2.11 R11 Console 前端（8M/7L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R11-M1 | 部署状态词表与控制面不一致 | M | console | SEPARATE | 需跨语言守卫（Go 常量↔TS 词表）+ 终态集单源；owner console/contract；触发：Console 状态批（含 M2/M8） |
| R11-M2 | quickstart 在 cancelled 终态永不 resolve | M | console | SEPARATE | 随 R11-M1（终态集单源 + 流收口兜底 resolve）；owner console；触发同 R11-M1（状态词表批） |
| R11-M3 | 断流重连×3 + 轮询兜底是死代码 | M | console | SEPARATE | 需流状态机（终态收流 vs 传输收口）；owner console；触发：Console 流式批（含 M4） |
| R11-M4 | 事件重连用旧游标致重复回放 | M | console | SEPARATE | 随流式批（与 R08-L4/R08-M7 同）；owner console；触发：流式批或事件页重复帧投诉 |
| R11-M5 | 反模式守卫扫描面 ≠ 声称的全量 src | M | console | SEPARATE | 需单次 walk + 例外表 + 文件数下限；owner console-guards；触发：Console 守卫批 |
| R11-M6 | 首屏无条件预载 xterm + 全站无压缩 | M | console | SEPARATE | 需路由级动态 import + 产物断言 + gzip；owner console/perf；触发：前端性能批 |
| R11-M7 | New token 默认角色=admin（最小权限反转） | M | console | SEPARATE | 改默认不预选/禁提交（3 行）；owner console；触发：Console 最小权限批（S18，与 R11-L4 同批） |
| R11-M8 | 蓝绿双代窗卡片忽略"活跃态"半边 | M | console | SEPARATE | 随 R11-M1（判式补 releasing/observing）；owner console；触发同 R11-M1 |
| R11-L1 | 冻结 Spec 50 路扇出且 staleTime=0 | L | console | SEPARATE | 需 `staleTime`/按需 `enabled`；owner console/perf；触发：前端性能批 |
| R11-L2 | src/api 只用于实体形状，信封手写 as T | L | console | SEPARATE | 需调用点改用生成 schema 类型（成本 M）；owner console/contract；触发：Console 契约批 |
| R11-L3 | 未哈希 public 资产也发 365d immutable | L | console | SEPARATE | 与 R08-L6 同改（仅 `assets/` immutable）；owner console/static；触发：静态资源批 |
| R11-L4 | 凭据存 localStorage + 全站无 CSP | L | console | SEPARATE | 需 CSP/安全头（S）+ 会话形态（M）两段；owner console/security；触发：XSS 演练或安全头批 |
| R11-L5 | 巨型单文件承载多域面板 | L | console | ACCEPT | 纯重构债、无行为影响；触发：下次改对应面板时按域拆 |
| R11-L6 | 5 个已声明依赖零引用 | L | console | ACCEPT | 仅体积/维护噪音；随触随修（pnpm 清理）；触发：依赖升级批 |
| R11-L7 | `useRevisions` 固定 limit=200 升序 | L | console | SEPARATE | 需降序/游标接口配合（与 R08-M6 分页批同）；owner console/contract；触发：分页批 |

### 2.12 R12 守卫/测试设施/e2e（7M/7L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R12-M1 | acceptanceguard 针失配，检测器零断言 | M | guards | FIXED | S5 `cdecabe0`：针换形（`s.Tx.Do(`）+ 防盲锚 |
| R12-M2 | Runtime 覆盖反扫用裸子串零判别力 | M | guards | FIX-NOW | S17-c1：AST 判据（`SelectorExpr.Sel`）+ 正向锚；成本 S |
| R12-M3 | 账本"只由 Ensure 写"无守卫 | M | guards | FIX-NOW | S17-c2：新增 `ensuredledger_test.go`（`ensuredGen/ensuredSpec` 写点白名单）；成本 S |
| R12-M4 | e2e 零 `workload.converged` 正向断言 | M | guards | FIX-NOW | S17-c3：`dind-smoke.sh` 删任务→≤90s converged + 无 converge-failed 断言；成本 S |
| R12-M5 | verify 类门禁不在 CI/对账射程 | M | guards | FIX-NOW | S17-c5：verify 类任务纳入 CI/对账，或显式 `reconciled:false`+理由；成本 S |
| R12-M6 | 词汇冻结机器执行力约 1/3 | M | guards | SEPARATE | 需人工评审流程载体（skippedTokens 加评审日期/载体，到期即红）；owner guards；触发：ADR-0007 守卫批 |
| R12-M7 | apitest 假 Runtime 恒空 EnsureReport | M | guards | FIX-NOW | S17-c4：FakeRuntime 可配 Gens/rollout stalled + 跨夹具一致性断言；成本 S |
| R12-L1 | 三红线"无 I/O"不拦裸 `net` | L | guards | FIX-NOW | S17-c1：禁列抽共享表（`net`/`io/ioutil`/`syscall`，`net/url` 白名单）；成本 S |
| R12-L2 | loopguard 只认字面 ticker 且无正向锚 | L | guards | FIX-NOW | S17-c1：AST 判据（Ticker/Tick/After in for+select）+ `loop.go` 正向锚；成本 S |
| R12-L3 | Source 锚不校验函数限定符 | L | guards | SEPARATE | 随守卫加固批（与 R01-L2 同批：AST 校验 FuncDecl/TypeSpec）；owner guards；触发：守卫加固批 |
| R12-L4 | errmapping 守卫单针，兜底评审不存在 | L | guards | SEPARATE | 需 AST/errorlint 判据；owner guards；触发：守卫加固批 |
| R12-L5 | e2e↔CI 对账阈值余量过大且不校验脚本存在 | L | guards | FIX-NOW | S17-c5：阈值提到实测 12/12 + `os.Stat` 脚本存在性断言；成本 S |
| R12-L6 | 蓝绿"零 5xx"把 4xx 当健康 | L | guards | SEPARATE | 需探针改 2xx 白名单 + 诊断分布；owner e2e；触发：e2e 断言批 |
| R12-L7 | smoke 告警链全链 SKIP 仍退出 0 | L | guards | SEPARATE | 需机器可读跳过标记 + nightly/特权 job 承担；owner e2e/ci；触发：告警链自动承载批 |

### 2.13 R13 横切：集成/事务/并发（3M/3L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R13-M1 | 期望账本无退场面，BG 退役代永久残留 | M | integration | FIX-NOW | S15-c6：BG 收口对 `appOwner(appID)` 做本代保留集差集 `forget`（或只消费最新代）；两代+peer 钉测；成本 S |
| R13-M2 | 排水预算实为框架 StopTimeout=5s | M | integration | SEPARATE | 需显式 `WithStopTimeout` + `engineService.Stop` 自带 deadline（与 R09-M5/M6 同批）；owner assembly/engine；触发：关停/升级演练 |
| R13-M3 | 外部副作用无补偿原语（改密窗口） | M | integration | SEPARATE | 需两阶段凭证/恢复通道设计；owner engine/database；触发：凭证轮换丢失事故或 ADR 追补 |
| R13-L1 | 两个载体写动词缺 maintenanceMu 读锁 | L | integration | FIX-NOW | S15-c3：`TeardownApp`/`SweepTerminalTaskCarriers` 补读锁 + guards 反扫；成本 S |
| R13-L2 | 幂等 24h 重放回执失败退化为 90s | L | integration | SEPARATE | 需回执与业务同事务/不可重跑动词清单；owner idem；触发：幂等批 |
| R13-L3 | Database 状态推进用 call-gen 而非 wire-gen | L | integration | FIX-NOW | S16-c8：改传 `wireGen`（一行）+ call≠wire 钉测；成本 S |

### 2.14 R14 横切：安全/授权/密钥（8M/4L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R14-M1 | Login 是 PUBLIC 且不在限速面 | M | security | FIX-NOW | S13-c1：per-name+per-IP 固定窗（`loginlimit.go`）；**工作区已落，待提交/验收**；成本 S |
| R14-M2 | init 后可能无 builtin-owner 身份 | M | security | FIXED | S2 `94a5267f`：首个 init 用户改 builtin-owner |
| R14-M3 | 节点三动词只按 nodes:write 门禁 | M | security | FIXED | S2 `94a5267f`：`nodeAdmin` 首行 `RequirePlatformAdmin`（代码核：门先于子面协商/审计） |
| R14-M4 | 非 default Team 省略 team_id 一律 403 | M | security | FIXED | S2 `94a5267f`：三处预填删除，统一 caller-team 解析 |
| R14-M5 | freeze 可被省略 team_id 绕过 | M | security | FIX-NOW | S14-c5：`FreezeGuard` 对 `ref==""` 回退调用方 Team 解析；跨队绕过负例；成本 S |
| R14-M6 | Edge 端点缺省无认证+通配绑定 | M | security | DUP | 并入 R09-M8（同一 proxy/edge 暴露面默认安全项） |
| R14-M7 | install 下载物零校验（root 安装运行） | M | security | DUP | 并入 R09-M3（同一下载物供应链项） |
| R14-M8 | KEK 轮换窗缺失静默铸新并丢弃退役 | M | security | FIX-NOW | S14-c4：`HasActive()==false && len(retired)>0` 视为硬错误；用例；成本 S |
| R14-L1 | identity 目录读面全平台可见 | L | security | FIX-NOW | S13-c2：`ListUsers/GetUser/ListRoles/ListTeams` 本队成员+平台 owner 过滤；**工作区进行中**；成本 S |
| R14-L2 | relay/join token 用 `==` 逐个比较 | L | security | FIXED | S11a `9fe85c4e`：join token 退出 relay 鉴权；凭证比对走 `subtle.ConstantTimeCompare`（`engine/exec.go:201`） |
| R14-L3 | exec 事件载荷含完整 argv | L | security | SEPARATE | 需事件载荷脱敏/只进审计 Detail；owner interfaces；触发：事件面敏感载荷批 |
| R14-L4 | 平台备份仓密文件对换行敏感 | L | security | FIX-NOW | S14-c4：`TrimSpace` + 非 hex 即硬错；用例；成本 S |

### 2.15 R15 横切：验收声明/历史挂账（3M/5L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R15-M1 | "恢复期平台只读/解除只读"无实现 | M | acceptance | SEPARATE | 二选一：实现平台态闸门+`platform restore` 动词，或同批修订六份文档；**需产品裁决**；owner product/ADR；触发：灾备叙事对外承诺前 |
| R15-M2 | G1-G24 执法手册仓内不可达 | M | acceptance | SEPARATE | 需补档原始条目（条目名+要求+守卫+反推假设）；owner docs/audit；触发：审计链补档或下次收敛计划 |
| R15-M3 | ADR-0015 N-1 兼容零实现零挂账 | M | acceptance | SEPARATE | 二选一：实现版本协商头+超差 errcode，或成文"开发期无兼容负担"；owner product/ADR；触发：首个跨版本 CLI 连生产 |
| R15-L1 | 契约/注册表注释仍写收敛 opt-in | L | acceptance | ACCEPT | 翻案后文档漂移；随触随修（同 commit 修三处）；触发：下次改 ADR-0005/0022 下游 |
| R15-L2 | 架构 §4 IR 草图与 proto 现形不符 | L | acceptance | ACCEPT | 文档漂移、无运行影响；随触随修；触发：下次改 architecture §4 |
| R15-L3 | README 状态节严重过期 | L | acceptance | ACCEPT | 文档滞后；随触随修；触发：下次 README 变更 |
| R15-L4 | proto 变更与 console:gen 未同 commit（8 例） | L | acceptance | FIX-NOW | S17-c5：CI/hook 检查"proto/swagger 变更 ⇒ console/src/api 同 commit"；成本 S |
| R15-L5 | F2.8 勾选文本含明确不做项 | L | acceptance | ACCEPT | 半落地打勾；随触随修（条目文本回改）；触发：下次动 checklist |

### 2.16 R16 核心声明式模型专项（6M/6L）

| ID | 标题（≤40字） | 严重度 | 模块 | 裁决 | 依据/动作 |
|---|---|---|---|---|---|
| R16-M1 | RestartSeqOnly 用路径后缀判定 | M | declarative | FIXED | S4 `c925ba3b`：改精确 proto 路径判定 |
| R16-M2 | detect 语义双向错位 | M | declarative | FIXED | S12 `2740a09d`（事实分流/信号）+ D3 `ca199a82`（detect 零写）；残余失明按 ADR-0082 §5 成文降级 |
| R16-M3 | 账本被未成功物化写脏（Cancel 后失明） | M | declarative | FIXED | S12 `2740a09d`：`EnsureReport.Changed/Removed` + `ensuredBy` 账本归属 |
| R16-M4 | 撤销 peer 在 handler 内同步 Ensure | M | declarative | FIX-NOW | S15-c5：改"标记 + Kick driftScan"，请求不等 Ensure；请求路径写面钉测；成本 S |
| R16-M5 | ADR-0081 DECLARATION 定义自相矛盾 | M | declarative | FIXED | S12 `23f4b0b1`：两子类判据 + 域级收敛面表 + 154 计数 |
| R16-M6 | 慢通道/基线成功收敛完全静默 | M | declarative | FIXED | S12 `2740a09d`：审计按「fast 源 ∨ 真实变更」分流，事件 fast-only |
| R16-L1 | 翻案只登记一处，ADR 本体仍 Accepted | L | declarative | FIX-NOW | S17-c6：ADR-0005/0022 加被推翻注记 + 下游设计翻案行 + `opt-in` 禁词；成本 S |
| R16-L2 | 收敛 9 张 per-App 表随 App 删除不清 | L | declarative | FIX-NOW | S15-c7：`TeardownApp` 末尾 `forget(appID)`（含 R02-L7 冷却记账）+ `projMemo` 上限；成本 S |
| R16-L3 | 缺 Inspector 时缺省收敛静默停摆 | L | declarative | FIX-NOW | S15-c4：装配期 fail-fast/明确 Warn + doctor「收敛能力」行；成本 S |
| R16-L4 | 写类守卫三盲点（互斥/READ/FrozenVerbs） | L | declarative | FIX-NOW | S17-c6：补三条断言（互斥、READ 显式集合、FrozenVerbs 34/8）；成本 S |
| R16-L5 | 六处测试"通过但承诺未兑现" | L | declarative | FIX-NOW | S17-c8：补四类负向测试 + 三处断言加强（载体移除/审计存在/fail-closed 打点）；成本 M |
| R16-L6 | Author Class 三铁律零可执行载体 | L | declarative | ACCEPT | ADR 已声明推迟，非违约；触发：autoscale 立项时同批落字段注册表 + 静态守卫 |

### 2.17 台账自检（写完后跑）

- [x] 每个报告 ID 在 §2 出现且仅出现一次（`§1.4` ③ 双向差集为空）。
- [x] `FIXED` 17 条全部有 commit/阶段来源（附录 A 汇总）。
- [x] `DUP` 9 条全部指向存在的主条目（R06-L1、R12-M3、R16-L2、R08-M6、R03-M13、R11-L3、R13-M2、R09-M8、R09-M3）。
- [x] `FIX-NOW` 59 条全部带批次编号，批次明细见 §3。
- [x] `ACCEPT` 30 条全部带"理由 + 触发"（"随触随修"者给出触碰面）。
- [x] `SEPARATE` 83 条全部带"不能本轮修原因 + owner + 触发"。

## 3. 修复批建议（S13-S17）

### 3.0 批次原则

- **项 = 一个可独立 commit 的工单**；同文件面的 2-5 个同源 ID 合并成一项（避免碎片 commit），合并关系在明细表显式列出。每批 3-8 项。
- **文件面尽量不重叠**：同一批内按文件/包分组；跨批若共用文件（如 `converge.go` 在 S15-c1/c7 两处）由同一子代理顺序做，或拆成两段 commit。
- **每批一个验收门**，门内命令与 CI 同口径（`mise run lint:go` / `go test -race` / `mise run generate:verify` / `console:verify`）。
- 所有 FIX-NOW 项的测试形态遵循既有文化：**负例先红**（复现/攻击链用例）→ 修复后转绿；golden 变更必须逐行核对并在 commit message 说明语义变化。

### 3.1 批次总表

| 批次 | 主题 | 项数 | 覆盖 ID | 与当前计划关系 |
|---|---|---|---|---|
| S13 | 身份与读面最小化 | 3 | 3（R14-M1、R14-L1、R08-M2） | **一致（已派，工作区进行中）**；只补"提交前核对点"（§3.2） |
| S14 | 安全边界二批：外部面/密钥/契约 | 8 | 10 | 原 S14 的 **project 级 scoped 指标属 H8 阶段 C（非台账 M/L 项）**，可并跑但不要混 commit；若"S14 TTL"指 R08-M7，本台账判 `SEPARATE`，不塞进 S14 |
| S15 | 收敛与引擎残余 | 8 | 15 +1 DUP | **一致**（收敛残余），补 compose 受理保真五连 |
| S16 | Runtime 一致性与适配面 | 8 | 11 + † 1（共 12 行） | **一致**（R06-M7/R08-M1 保持）；k3s CPU% 属 H8 阶段 C，非台账项，仍随 S16 |
| S17 | 守卫与测试基建 | 8 | 20 | **一致**（测试基建）；新增 R09-L5/L7（工程基线）与 R10-M1/L1（CLI 契约 golden）填满批次 |

> **S13 工作区现状（2026-10-19 17:39 采样）**：`git status --porcelain` = 6 修改 + 5 新增源文件（另有本台账临时文件 4 个，已完成使命、收尾时删除）：`identity/membership/repo.go`、`persistence/membership/repo.go`、`fleetlygrpc/{alerting,identity,identityauthz,services}.go` + `loginlimit{,_test}.go`、`apitest/{alerting_team_filter,identity_read_authz,login_ratelimit}_test.go`。台账确认与 S13 三项范围一致（登录限速 / identity 读面 / alerts Team 过滤）；**提交前 Lead 需核**：① membership repo 改动是否仅服务读面过滤；② 限速常量与窗口是否符合 §1 风险边界（不跨副本、重启清零）；③ `git diff --stat proto/ genproto/` 应为空。

### 3.2 S13 明细（3 项）

| 项 | 覆盖 ID | 文件面 | 行为 | 测试锚 | 成本 |
|---|---|---|---|---|---|
| S13-c1 | R14-M1 | `fleetlygrpc/loginlimit.go`（新）、`services.go`（Login 前占位） | per-name + per-IP 固定窗；失败计数、成功清零；桶上限+overflow 桶 | `login_ratelimit_test.go`（双维度超限、成功清零、桶上限） | S |
| S13-c2 | R14-L1 | `identity.go`/`identityauthz.go`、`membership` repo | 目录读面按"本队成员 + 平台 owner"过滤 | `identity_read_authz_test.go`（跨队不可见、平台 owner 可见） | S |
| S13-c3 | R08-M2 | `alerting.go` | `ListAlertRules/States` 非平台管理员按 Team 过滤 | `alerting_team_filter_test.go`（他队规则不可见） | S |

**验收门（S13）**：`gofmt -l` 空；双平台 `go build ./...` 0；`go test ./internal/interfaces/grpc/fleetlygrpc/... ./internal/testsupport/apitest/... ./internal/domain/identity/... ./internal/infrastructure/persistence/membership/...` 全绿；`go test ./internal/guards/` 绿；`go vet ./...` 0；proto/genproto 零 diff。

### 3.3 S14 明细（8 项 / 10 ID）

| 项 | 覆盖 ID | 文件面 | 行为 | 测试锚 | 成本/风险 |
|---|---|---|---|---|---|
| S14-c1 | R03-M13 | `engine/registrycontent.go` | repository 收敛为平台仓名白名单（`^<lower(projectID)>/[a-z0-9._-]+$`，拒 `..`/`?`/`#`/`%`） | 注入 `../`/`%2e%2e`/`?` 的负例；正常仓名正例 | S / 低（合法仓名不受影响） |
| S14-c2 | R07-M4 | `providers/github` 客户端构造 | 注入 `CheckRedirect`（跨 host 拒绝）+ 总超时；`DialContext` 校验解析后 IP 非私网 | 302 到他 host 拒绝；挂起上游超时；DNS rebinding 用例（可注入 resolver） | S-M / 中（需假 HTTP 服务器） |
| S14-c3 | R07-M2 | `providers/traefik`（`routeKey`/`validateDynamicConfig`）、路由受理 host 归一 | 名字恒 DNS 安全（非 `[a-z0-9-]` 折叠）；预检拒 `@`；host 统一小写 | golden 路由集；`evil@x` path 用例拒绝；大写 host 归一后命中 | S / 中（存量大写 host 行不迁移，仅在受理面归一） |
| S14-c4 | R14-M8 + R07-L3 + R14-L4 | `infrastructure/keyring`、`engine/platformbackup.go` | KEK 写 tmp+rename+fsync+权限复核；active 缺失且 retired 存在→硬错误；restic 密码 `TrimSpace`+非 hex 硬错 | 崩溃窗（tmp 残留）用例、retired 非空用例、密码带换行用例 | S / 低（失败即 fail loud） |
| S14-c5 | R14-M5 | `freezeguard`/`governance` 调用点 | 目标=调用方 Team 的创建型动词，`ref==""` 回退 caller team 解析 | 冻结队省略 team_id 建 Project → 拒绝 | S / 中（需确认其他动词语义不变） |
| S14-c6 | R08-M3 | `fleetlygrpc/templates.go` 等复合 handler | 内层动作显式 `HasScope`（或注解改能力并集） | 自定义角色（仅 templates:write）建 App/Secret → 403 | S-M / 中（授权面变更需全量 apitest） |
| S14-c7 | R08-M8 | `fleetlygrpc/acceptance.go` `factOf` | 归属解析失败返回 error，整单拒绝（fail-closed）；`TeamID==""` 事实须显式理由 | Project 行被删/解析失败用例；factdual 守卫加"空 Team 需理由" | S / 低 |
| S14-c8 | R08-M9 | GetStatus handler + 探活缓存 | 公开面只回 `{healthy,version}`；components 详情需鉴权；探活结果 TTL 缓存 | 匿名访问仅最小态；鉴权后含详情；缓存命中不重复探活 | S-M / 低（契约 only-add，不改 proto 字段语义） |

**验收门（S14）**：每项负例先红后绿；`go test ./internal/infrastructure/... ./internal/interfaces/... ./internal/application/...` 绿；`go test ./internal/guards/` 绿；`go vet ./...` 0；proto/genproto 零 diff（S14 全部在 Go 侧，无契约变更）；traefik golden 逐行核对。

### 3.4 S15 明细（8 项 / 15 FN + 1 DUP）

| 项 | 覆盖 ID | 文件面 | 行为 | 测试锚 | 成本/风险 |
|---|---|---|---|---|---|
| S15-c1 | R02-M1、R02-L1 | `engine/converge.go` | `attemptConverge` 增"携带签名"形参；`failSig` 三态；信号集空时清 `resolved` | 冷却中触发 `IsolateNetworkPeer` 不释放冷却；同形态复发走快通道 | S / 低 |
| S15-c2 | R02-M3 | `engine/processrestart.go`、`swarm/restart.go`、`k3s/restart.go` | 按最新 succeeded Generation 解析代次化载体（两种 ID 形态都试） | BG App 重启命中恰一个代次化载体（双 Provider） | S / 低 |
| S15-c3 | R02-M4、R13-L1 | `engine/teardown.go`、`fleetlygrpc/structure.go`、`guards` | 收口预检改驱动权谓词；两处写动词补 `lockMaintenance`；guards 反扫 | failed 待回滚窗拒绝收口；锁面守卫反扫红灯 | S / 中（锁面需 -race） |
| S15-c4 | R02-M7、R16-L3 | `engine/loop.go`/`lifecycle.go`、`engine/doctor.go`、assembly 装配 | `Loop.Done()` 接 supervisor 并暴露 `ring=<name> dead`；Inspector 缺席 fail-fast/明确 Warn + doctor 行 | 注入环 panic 后 health/doctor 可见；Inspector 缺席启动用例 | S / 低 |
| S15-c5 | R16-M4 | `fleetlygrpc/networks.go`、`engine/peers.go` | 撤销 peer 改"标记 + Kick driftScan"，请求不等 Ensure | 撤销请求返回不等 Ensure（注入慢 Ensure）；下一拍完成剥离 | S / 中（首拍窗口语义需在 ADR-0082 注记） |
| S15-c6 | R13-M1 | `engine/bluegreen.go`、`engine/observ*.go` | BG 收口按本代保留集对 owner 做差集 `forget`（或只消费最新代） | BG 两代 + peer 后 `crossProjectRefs` 不含退役代；30s 拍无重收敛 | S / 中 |
| S15-c7 | R16-L2（+R02-L7 DUP） | `engine/converge.go`（book）、`engine/teardown.go` | `TeardownApp` 末尾 `converge.forget(appID)`（9 表）；`projMemo` LRU 上限 | 删除 App 后 9 表无 key；converge-failed 告警不残留 | S / 低（锁身份先取后删） |
| S15-c8 | R03-M5、R03-M6、R03-M7、R03-M8、R03-L5 | `application/composeintake/intake.go` + 测试 | `$$` 只对奇数连续 `$` 加倍；`replicas:0` 拒收；label job 长语法执法；networks/configs external name；空探针载荷拒收 | 五个 overlay/表测：`$$` 保真、0 副本拒收文案、uid/gid 保真、external name 生效、`["CMD"]` 拒收 | S / 中（同文件多处改，需逐项 commit 或单 commit 分节注释） |

**验收门（S15）**：`go test -race ./internal/application/engine/...` 绿；新增钉测逐条 PASS；`converge-verdicts.golden` 不变（若变必须有 ADR-0082 追记）；`go test ./internal/testsupport/apitest/` 绿；无迁移；`gofmt`/`vet`/双平台 build 0。

### 3.5 S16 明细（8 项 / 11 ID + 附录外挂 † 1 条）

| 项 | 覆盖 ID | 文件面 | 行为 | 测试锚 | 成本/风险 |
|---|---|---|---|---|---|
| S16-c1 | R06-M2 | `k3s/runtime.go`（InspectWorkloads） | 读 Deployment `Spec.Replicas`（DaemonSet 语义另定） | `replicas=3` 断言替代 `EqualValues(1)`；重启后零假 drift | S / 低 |
| S16-c2 | R06-M6 | `k3s/network.go` | `reconcileEgressNetpol` 改走 `putNetpol` 单源 | 形状漂移（旧 policy 残留）用例 | S / 低 |
| S16-c3 | R06-M4 | `k3s/logs.go` | `Timestamps: true` + 解析 RFC3339 + 还原平台节点 ID + `Until` 显式不支持/客户端过滤 | logs 单测（时间戳/节点/窗口） | S / 低 |
| S16-c4 | R06-M7 | `swarm/hygiene`（或 secret 清扫扩展） | config 载体清扫：managed 标签+前缀+现役引用集+出生宽限 | `orphan_test.go` 扩 config 用例；三重防误删 | S / 中（误删风险需灰度高） |
| S16-c5 | R06-M5 | `swarm/exec.go` | `stdinDone` 置 nil 恢复 150ms 节拍；节拍可注入 | 忙轮询用例（记录 ExecInspect 间隔） | S / 低 |
| S16-c6 | R01-H1†、R01-M1 | `domain/services/dbbrowser`（render/mongoku）、`dbtemplate` 测试 | redis 分支不要求 user；mongoku 用 `Conn.DB` 补库段/authSource | 五引擎 `ConnURL→browseConnFromURL→Render` 往返表测（免 Docker） | S / 低（不依赖真机） |
| S16-c7 | R08-M1 | `fleetlygrpc/contexts.go` | `ListNodes`/`ListNodeContainers` 平台门，或非平台按 callerTeam 行过滤 | member 跨队负例；平台管理员正例；relay 状态不泄漏 | S / 中（读面变更影响 Console 节点页，需 whoami.platformAdmin fail-closed 显隐） |
| S16-c8 | R03-M1、R03-M2、R04-M1、R13-L3 | `engine/buildsource.go`、`engine/minttask.go`、`engine/database.go` | inputs 登记终态删除；clone `.cloning`+原子 rename；resident+owner 铸 `LeaseDeadline`；Database 状态传 `wireGen` | 终态后 map 空；中断 clone 不采半截；不续期→宽限排空；call≠wire 钉测 | S / 低 |

**验收门（S16）**：`go test ./internal/infrastructure/providers/... ./internal/domain/services/... ./internal/application/engine/...` 绿（builders 环境项除外）；k3s/swarm ensure 一致性测试绿；browse 表测绿；节点读面负例绿；无需 Docker（除既有环境项）；双平台 build 0。

### 3.6 S17 明细（8 项 / 20 ID）

| 项 | 覆盖 ID | 文件面 | 行为 | 测试锚 | 成本/风险 |
|---|---|---|---|---|---|
| S17-c1 | R12-M2、R12-L2、R05-M8、R12-L1、R06-L1 | `internal/guards/*` | Runtime 覆盖反扫改 AST；loopguard AST+Ticker/Tick/After+正向锚；statefacade 表清单从 migrations 解析；三红线禁列共享表；`ProviderFaces.Restart` + 断言 | 每条守卫做"删断言即红"反验；`faces_test` 14 面 | S / 低 |
| S17-c2 | R12-M3 | `guards/ensuredledger_test.go`（新） | `ensuredGen/ensuredSpec` 写点白名单（仅 record*/forget*） | 越界写点红灯；正向锚计数 | S / 低 |
| S17-c3 | R12-M4 | `e2e/dind-smoke.sh` | 删任务/`service scale` → ≤90s `workload.converged` + 期间无 converge-failed | e2e 单腿真跑（CI dind） | S-M / 中（需 dind 环境） |
| S17-c4 | R12-M7 | `testsupport/apitest` FakeRuntime | `Ensure` 可配 `Gens`（等价重下发回旧 gen）；`SetRolloutStalled`；跨夹具一致性断言 | 内容等价记账可达；收敛判定用例 | S / 低 |
| S17-c5 | R12-M5、R12-L5、R09-L4、R15-L4 | `.github/workflows/ci.yml`、`guards/e2eciaccount_test.go`、mise 任务 | verify 类任务入 CI 或显式登记；对账阈值 12/12 + 脚本存在性；go.mod 纳入零漂移；proto⇒console 同 commit 检查 | CI 本地同命令；反验（删 verify 步骤应红） | S / 低 |
| S17-c6 | R16-L4、R16-L1 | `guards/writeclassguard_test.go`、ADR-0005/0022、两份下游设计 | 互斥断言、READ 显式集合、FrozenVerbs 34/8；ADR 被推翻注记 + `opt-in` 禁词 | 三条断言；wording 扫全仓 | S / 低 |
| S17-c7 | R09-L7、R09-L5 | `ci.yml`、`install.sh`、`guards/installenv_test.go` | Windows 交叉编译门；数据根 `install -d -m 0750` + 幂等 chmod | CI job 一次跑通；installenv 断言 | S / 低 |
| S17-c8 | R10-M1、R10-L1、R02-L5、R16-L5 | `cmd/fleetly` golden、`engine/converge_test.go`、`engine` 相关测试 | delete 成功回 `{"deleted":id}` 或 `{}`；golden 递归+`json.Valid` 非空；freeze fail-closed 真钉测；四类负向测试+三处断言加强 | 双形态 golden；失败注入用例；`-race` | M / 中（golden 批量更新需逐行） |

**验收门（S17）**：每个新守卫/断言做反向验证（删掉应红）；`go test ./internal/guards/` 绿；`go test ./cmd/fleetly/...` 绿（既有假时钟 flake 单跑复验）；CI 新增 job 与本地同命令；`mise run generate:verify`/`console:verify` 0（若 S17 触发生成物）。

### 3.7 与当前计划的冲突与裁决

| 计划项 | 台账裁决 | 说明 |
|---|---|---|
| S13（已派：S2 读面 + Login 限速 + alerts Team 过滤） | **一致** | 台账三项完全对齐；不追加，避免与进行中工作区冲突 |
| S14（project metrics + TTL） | **调整** | project 级 scoped 指标是 H8 阶段 C（H8 已由 S9/S10 收口 app 级），不属 M/L 台账；TTL 若指流式/票据上限（R08-M7）判 `SEPARATE`（需限流基座）。S14 改为安全边界二批；project metrics 顺延 S18 或与 S14 并跑（不混 commit） |
| S15（收敛残余） | **一致** | 台账补入 compose 受理保真五连（同批不冲突：文件面独立） |
| S16（R06-M7/R08-M1/k3s CPU%） | **一致** | R06-M7→c4、R08-M1→c7；k3s CPU% 属 H8 阶段 C 非台账项，仍随 S16 |
| S17（测试基建） | **一致** | 台账把"工程基线"（R09-L5/L7）与"CLI 契约 golden"（R10-M1/L1）并入，理由：同属"门禁/契约断言"，避免再开 S18 只为 4 条 L |

> **口径冲突**：若 Lead 坚持 181 口径，则 §1.2 的 17 条会掉出台账；其中 R14-M2/M3/M4/L2 是已修项（掉出会让"已修清单"缺 4 条）、R14-M1/L1 正在进行（S13），因此**建议不接受 181**，以 198 为准并把 181 作为"提取口径"记录在案。

### 3.8 非 M/L 的机制残余（不占批次，供最终报告"残余"节引用）

以下项在报告里不是 M/L 编号（属阶段残留或 H8 阶段 C），不占 S13-S17 批次，但收口时必须点名，否则最终报告会漏：

| 项 | 来源 | 状态 | 建议 |
|---|---|---|---|
| H8 阶段 C：k3s CPU% 量纲/真机 | 报告 §11 残余、S10a 残留 | 未做 | 随 S16 真机项；需 k3s 指标面先有 CPU 语义 |
| project 级 scoped 指标 | S10 裁决"单列小批" | 未做 | S18 候选；app 级已由 S10 收口 |
| 平台面 AST 守卫（scope 词表第二维） | S2 残留 | 未做 | 与 R08-L1/权限模型 ADR 同批 |
| `cmd/fleetly` golden 假时钟 flake | 报告 §11、S10b 残留 | 基线既有 | 单列修夹具（假时钟漂移），不与功能批混 |
| `console:gen` Windows shim | S10b 残留 | 本地转发器绕过 | 单列修 `gen.mjs` 跨平台 |
| e2e/Actions 真跑台账（H7/H9、k3s config mount、relay 双节点） | 报告 §11 修复边界 | 未跑（无 Docker） | dind/staging 台账批；不阻塞 S13-S17 代码批 |
| `node.left` 抖动宽限窗 | S11b 残留/裁决点 | 观察中 | 先按 runbook 处置；触发：节点抖动误删凭证 |

### 3.9 执行状态（滚动更新）

| 批次 | 状态 | commit | 备注 |
|---|---|---|---|
| S13 | ✅ | `1dd4171d` | 身份读面最小化/Login 双桶限速/alerts Team 过滤；门禁全绿 |
| S14a | ✅ | `5e5ab2fe` + 回归修复 `ff0b8681` | c1-c4 外部面/密钥材料；c1 首版项目段白名单漏 entityid 连字符，Lead 门禁发现并修复（cmd golden 复绿） |
| S14b | ✅ | 本提交 | c5-c9 授权/事实/契约/端点认证；新增 factteam 守卫；c9 多队无 token 启动 fail-fast（部署注意见下） |
| S15a | ✅ | 本提交 | 收敛/引擎 c1-c4：冷却签名三态、BG 重启按 succeeded gen、收口驱动权+维护锁守卫、环 panic/Inspector 可观测；engine `-race` 225s 绿 |
| S15b | ✅ | 本提交 | 收敛/引擎 c5-c8：peer 撤销异步剥离、BG 退役代账本差集、Teardown 清 converge 记账+projMemo LRU、compose 保真五连；engine `-race` 228s 绿 |
| S16a | ✅ | 本提交 | Runtime 一致性 c1-c5：k3s 副本数/egress 收敛/日志帧（R06-M2/M6/M4）、swarm config 载体清扫/exec 节拍（R06-M7/M5）；负例先红、门禁全绿 |
| S16b | ✅ | `ce9a15c7` | Runtime/适配面 c6-c8：browse 五引擎方言往返（R01-H1†/M1）、节点读面平台门+Console fail-closed（R08-M1）、引擎卫生四连（R03-M1/M2、R04-M1、R13-L3）；engine `-race` 235s、console 139 全绿 |
| S17a | ✅ | `8f5d41e7` | 守卫 c1-c4：Runtime/loop/statefacade 判据 AST 化、三红线共享禁列表、账本写点守卫、FakeRuntime 回执可配、e2e converged 断言；每项反向验证红→绿 |
| S17b | ✅ | 本提交 | 守卫 c5-c8：CI/verify 对账（hermetic job、Windows 交叉编译门、go.mod 零漂移、proto⇒console 同 commit 守卫）、写类守卫三盲点（READ 显式 74 项/FrozenVerbs 34+8/三桶互斥）、install 数据根 0750、CLI delete `--json` 契约 + 防空 golden、freeze 查询故障真钉测；负例先红→绿 |
| S18 | 待派 | — | 非 M/L 残项 |
| S19 | 待派 | — | 文档修订/真机复核/终验 |

> **c9 部署注意（S14b 遗留）**：既有**多队**部署在换装到含 c9 的二进制前，必须先设置 `FLEETLY_SERVER_PROXY_CONFIG_AUTH_TOKEN`（或把 `server.proxy_config.addr` 绑回环），否则启动 fail-fast；单队部署（含当前 staging）零扰动。换装 staging 前先核团队数。

## 4. 剩余风险清单（供最终报告 §4 更新，取 ACCEPT/SEPARATE 中影响发布/安全最高者）

> 选取标准：① 安全/数据完整性；② 发布或升级链阻断；③ 事故放大器（控制面自己坏了）；④ 对外承诺与实现不符。按"若不修，最坏后果 × 触发概率"排序。

| # | 风险 | 来源条目 | 现状为何不修 | 最坏后果 | 建议动作 / 触发 |
|---|---|---|---|---|---|
| 1 | **安装链供应链无校验**：fleetly/railpack/restic 与 docker 安装脚本零签名零校验，root 安装 root 运行 | R09-M3（+R14-M7 DUP） | 需发布流水线（SHA256SUMS/cosign）配合，非代码批可解 | 上游/镜像被替换即 root 级沦陷；无审计链 | **v1 发版前阻断项**：先出 `SHA256SUMS` + install 校验（缺清单即 die），cosign 可后补 |
| 2 | **"恢复期平台只读/解除只读"是六份文档的现行承诺，全仓无闸门、无 `platform restore` 动词** | R15-M1 | 需产品裁决（实现闸门 vs 改文档），且涉及 ADR-0005/0015/architecture 多处 | 灾备演练时写面仍可写：恢复中新建资源会被恢复覆盖/账本错乱；对外承诺不成立 | 二选一并同批改文；触发：首次真实灾备演练或对外文档发布前 |
| 3 | **关停预算 5s 截断在途 Ensure/构建/备份**，随后 DB/runtime 仍被 cleanup 关闭 | R13-M2（+R09-M5/M6 DUP/同批） | 需显式 `WithStopTimeout` + 引擎排水 ctx + janitor join，属装配/引擎交叉 | 升级/重启时在途写被腰斩：载体半下发、备份半写、审计缺尾 | 与 S15 收敛批解耦，单列"关停序"小批；触发：首次升级窗口内出现半态资源 |
| 4 | **SQLite 单连接 + 关键查询缺索引**：长扫描把 API/事件流/收敛环串行化 | R05-M5、R05-M6 | 池>1 需事务/单写者模型压测，索引需慢查询观测支撑 | 规模上来后 p99 尖刺、流式超时、收敛节拍漂移（staging 小规模未暴露） | 先加慢查询/耗时观测 + 高价值索引（审计/outbox/runs/builds/backups）；触发：压测或 p99 告警 |
| 5 | **破坏性外部动作无补偿原语**：数据库改密成功而 Secret 写失败 = 凭证永久丢失（rotate 自述的 Browse 恢复路径依赖同一份被覆盖 Secret） | R13-M3 | 需两阶段凭证/恢复通道设计，跨 engine+database+keyring | 一次失败即不可逆凭证丢失，只能重建数据库账号 | 设计"待生效凭证"两阶段；触发：首次 rotate 失败或客户化数据库 |
| 6 | **契约面不可预测**：85/153 RPC 无必填注解（同族缺 id 有 400 有 404）+ `PutSecret` 无大小上限（32MiB 收包） | R08-M5、R08-M4 | 契约大扫需 proto/CLI/Console 同批 + 配额模型 | Agent/SDK 按错误码分支失效；member 可灌 32MiB secret 无配额（跨租户资源滥用） | 先补寻址字段 required（寻址一族）+ secret 上限；触发：SDK/Agent 错误处理事故 |
| 7 | **审计只能读最近 ≤1000 行 + 流式/票据无上限** | R08-M6（+R05-M7 DUP）、R08-M7 | 需 proto only-add + repo 游标 + CLI/Console 同批；限流需基座扩展 | 永续账本不可回读（合规/取证缺口）；follow 流与票据可被单租户打满 | 审计游标优先（只增字段）；触发：取证需求或流式 DoS |
| 8 | **无单实例约束 + Edge/proxy 端点默认无认证** | R09-M7、R09-M8（+R14-M6 DUP） | 需 flock 运维语义与暴露面默认安全设计（ADR-0036 前置项） | 同数据根双 daemon 双写 SQLite（WAL 接受双写）；端点被同网段任意进程伪造 traefik 配置 | 数据根 flock + 端点 `auth_token` 必填（teams>1 拒绝启动）；触发：多租户启用前 |
| 9 | **Console 凭据存 localStorage 且全站无 CSP/安全头** | R11-L4 | 需 CSP（S）+ 会话形态改造（M），归 Console 安全批 | 任一 XSS 即全量 token 可取（叠加默认 admin token 习惯） | 先落 CSP/nosniff/frame-ancestors（S）；触发：安全头批或 XSS 演练 |
| 10 | **exec 会话可靠性双洞**：relay 写队列满静默丢控制帧；attach×finish 竞态留节点侧孤儿 | R06-M8、R04-M4 | 分别需帧队列/背压协议与附件状态机，均为 M 级重构 | exec 会话静默断开/幽灵进程占用节点资源，排障无信号 | 单列"exec 可靠性"批；触发：首次孤儿进程或 relay 断连投诉 |

> **观察项（不进 top10 但需在最终报告"残余"提一句）**：R05-M2（硬删聚合 seq 可重发，ADR-0070 承诺被破坏）、R02-M6（构建源 App"重启"触发全量重建）、R16-L6（Author Class 三铁律零执法，已声明推迟）、R09-M2（install 无升级语义）。

## 5. 待 Lead 裁决的争议项（5 条）

**Lead 裁决（2026-10-19）**：口径采用 **198 条**（181 为提取口径，漏计 R03-L1..L5 与 R14 全部 12 条；零遗漏优先）。5 项争议裁决如下：

| # | 争议 | 选项 A | 选项 B | 台账倾向 |
|---|---|---|---|---|
| 1 | R15-M1 平台只读/restore | 实现最小平台态闸门 + `platform restore`（新错误码 + skill） | 同批修订六份文档，删掉"只读/解除只读"承诺 | **B 先做（S）**：v1 无 restore 动词时，闸门没有触发面；文档必须先诚实。实现留 ADR |
| 2 | R16-M2 残余（detect 下 Cancel 遗留不回卷、撤销 peer 不剥离） | 接受为 detect 的语义（ADR-0082 §5 已写）并补 CONTEXT Posture 一句 | 让 detect 下"回卷/剥离"作为唯一例外放行 | **A**：与 D3 裁决（detect=零写）一致；放行会重新引入"detect 也写"的歧义 |
| 3 | R03-M12 变量冻结不回收 | 接受为设计边界（material 冻结体不回溯变量删除），ADR-0072 决策 6 改文 | 实现"变量来源账本"（store 层键重算） | **A**（本轮）：B 是 M 级改造；但需在 ADR 写明"删除变量对 hook 推送不生效" |
| 4 | R09-M8/R14-M6 Edge/proxy 端点认证 | v1 必做（teams>1 且无 auth_token 拒绝启动） | 留作多租户启用前置项（当前单租户部署可接受） | **A**：ADR-0036 已自述为前置项，且成本 S；不做等于把"多租户"承诺悬空 |
| 5 | R02-M6 构建源 App 的"重启" | 接受"重启=新 Revision→重建"（构建缓存命中时成本低） | 实现基线 digest 复用，保证"重启零构建" | **B**（目标态）但本轮不做：需 ADR-0083 追补 + Build 复用语义；先接受 A 并写清边界 |

**裁决落地归属**：① R15-M1 → **S19 文档修订**（删/改六份"只读/restore"承诺，留 ADR 说明）；② R16-M2 残余 → **ACCEPT**（ADR-0082 §5 已成文，S19 只在报告/词条补一句）；③ R03-M12 → **ACCEPT + S19 ADR-0072 修订**（写明"删除变量不回溯冻结体"边界）；④ R09-M8/R14-M6 → **S14-c9 新增**（v1 必做：受管 Edge/proxy 端点默认认证）；⑤ R02-M6 → **ACCEPT + S19 ADR-0083 追补边界**（目标态 B 列后续独立项）。

## 附录 A：FIXED 证据索引（17 条）

| ID | 阶段/commit | 证据形态 |
|---|---|---|
| `R02-M2` | S12 `2740a09d` + `23f4b0b1` | 判定表 8 行接进生产 + golden 8 行；ADR-0082 锚修订（plan S12 记录 / 报告 §11） |
| `R02-M5` | S12 `2740a09d` | 慢通道按"fast 源 ∨ 真实变更"发审计+事件；ADR 行 10 机制更正 |
| `R05-M1` | S5 `cdecabe0` | audit.Append 串行化+有界重试；并发 256 用例（plan S5） |
| `R05-M4` | S5 `cdecabe0` | outbox 前缀裁剪；无中间洞测试（plan S5） |
| `R06-M1` | S6 `b37a61c1` | k3s ConfigMap 卷+SubPath；引用名一致性测试（plan S6） |
| `R09-M1` | S5 `cdecabe0` | CI Codegen job；`mise run generate:verify`（`.github/workflows/ci.yml:57`） |
| `R10-M6` | S1 `01bd214e` | `exec_resize_{unix,windows}.go` 拆分；双平台 build 0（plan S1） |
| `R12-M1` | S5 `cdecabe0` | acceptanceguard 针改 `s.Tx.Do(` + 防盲锚（plan S5） |
| `R14-M2` | S2 `94a5267f` | init 首用户改 builtin-owner（plan S2 / 报告 §11） |
| `R14-M3` | S2 `94a5267f` | **代码核**：`fleetlygrpc` `nodeAdmin` 首行 `RequirePlatformAdmin`（门先于子面协商/审计） |
| `R14-M4` | S2 `94a5267f` | 三处删预填、统一 caller-team 解析（plan S2） |
| `R14-L2` | S11a `9fe85c4e` | **代码核**：join token 退出 relay 鉴权（`engine/exec.go` 拒 join token）；凭证比对 `subtle.ConstantTimeCompare`（`exec.go:201`） |
| `R16-M1` | S4 `c925ba3b` | RestartSeqOnly 改精确 proto 路径；env 键 `restart_seq` 回归测（plan S4） |
| `R16-M2` | S12 `2740a09d` + D3 `ca199a82` | 信号/闸门分流 + detect 零写（C1-C6 钉测）+ ADR-0082 §5 成文降级 |
| `R16-M3` | S12 `2740a09d` | `EnsureReport.Changed/Removed` + `ensuredBy` 账本归属（plan S12） |
| `R16-M5` | S12 `23f4b0b1` | ADR-0081 两子类判据 + 域级收敛面表 + 154 计数（plan S12b2） |
| `R16-M6` | S12 `2740a09d` | 审计与事件解耦（事件 fast-only）（plan S12b1） |

## 附录 B：边界与未验证项（诚实清单）

1. **未跑任何 e2e/真机**：本台账的判断全部来自报告原文 + 代码静态核实 + plan/§11 记录；dind/staging 触发面（H7/H9、k3s config mount、relay 双节点、R06-M2/C3 真机）仍未验证。
2. **S13 未提交**：§3.1 列的工作区改动是并行子代理的进行中产物，本台账只做范围确认，不作为 FIXED 证据；若 S13 提交内容与范围不符，R14-M1/R14-L1/R08-M2 应退回 `FIX-NOW` 待验。
3. **代码核过 vs 记录核过**：17 条 FIXED 中 2 条（R14-M3、R14-L2）是本次代码直核；其余 15 条依据 plan 执行记录与报告 §11（两者互相独立），未逐 commit 读 diff。
4. **ACCEPT 判断的时效**：所有"随触随修"条目依赖"下次触碰同文件同 commit 修正"的纪律，本台账未加守卫；若 Lead 要机制化，应把 §2 的 ACCEPT 行按文件面汇总成一份 `fix-on-touch` 清单（可作为 S17-c1 的附带产出）。
5. **未覆盖的上游语义**：MongoDB authSource 缺省、traefik `@` 名字拒绝、zot ACL 兜底、KEK age 解析失败路径等依赖上游行为，报告已标"待复核"的条目（R01-M1、R07-M2、R07-L7 等）在本台账按"代码事实 + 上游常识"裁决，真机可能调整严重度但不改裁决方向。
6. **本文件不是修复承诺**：`FIX-NOW` 的批次与项数是建议，Lead 可按风险重排；唯一硬约束是"每条 ID 必须留在台账里，裁决可改、不可丢"。
