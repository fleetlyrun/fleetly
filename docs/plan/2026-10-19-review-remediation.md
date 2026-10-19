# 评审修复排期（2026-10-19）

> 来源：`docs/reviews/2026-10-19-architecture-quality-acceptance.md`（15 条 H + 主题化 M + 机制建议）。
> 执行方式：`subagent-sequential-implement` —— 每阶段一个自足 prompt 派一个子代理实现；Lead 逐条取证验收（自己跑测试/读 diff）、通过后提交本地还原点 commit；未通过只重做本阶段。
> 约束：遵守 AGENTS.md（守卫/测试与功能同批、golden 同 commit、小步提交、用户可见文本英文/注释中文）；**只提交本地，不 push**；每阶段禁止重做/回退前序阶段产出。

## 阶段总表

| 阶段 | 主题 | 覆盖发现 | 验收门（每阶段） | 状态 |
|---|---|---|---|---|
| S1 | 工程基线：Windows 构建解封 | R10-M6（SIGWINCH） | `go build ./...` 双平台通过；`go test ./cmd/fleetly/...` 可运行；guards 绿 | ✅ |
| S2 | 安全边界：平台面门禁 + 身份写面归属 + 角色授予上限 | H1、H2（另含 R14-M2 的 init owner 修正、R14-M4 的 team 缺省退化） | 复现场景反转为 403；同队 admin 正常；apitest/guards 绿；golden 同批 | ✅ |
| S3 | 交付主链：安装路由面 + Browse 会话绑定 + 备份可靠性 + CLI 等待/Console 崩溃 | H12、H4、H5、H6、H14、H13 | 各修复单测/回归绿；install.sh guard 绿；console vitest 绿；build 双平台 | ✅ |
| S4 | 声明式与路由语义：restart 源/精确判定 + BG egress + 发布就绪门 | H15、R16-M1、H9、H7 | 新钉测绿；engine/assembly/traefik/providers 相关包绿；golden 同批 | ✅ |
| S5 | 双 Runtime 与守卫/完整性收尾 | H3、R12-M1、R05-M1、R05-M4、R09-M1 | k3s 规则+守卫绿；并发 audit/outbox 测试绿；CI 门禁就位；全量回归 | ✅ |

## 本轮不做（需设计决策/迁移，记录为后续 backlog）

- **H8 Metrics 租户过滤**：涉及 `capability.Metrics` 端口签名 + Console/CLI 查询构造，需设计（短期可先收归 platform admin，但会改变现有 member 观测动线）→ 单独立项。
- **H10 /v1/relay 节点个体凭证**：需 Enrollment/RBAC 双 Provider 设计 + 迁移（join token → 节点凭证）→ 单独立项。
- **H11 幂等键租户维度**：需迁移 00034 + 兼容策略 → 单独立项。
- **R02-M2/M5、R16-M2/M3/M5/M6 等收敛语义深水区**：与声明式模型 ADR 修订同批，避免代码先行造成语义漂移 → 随 ADR 修订批。
- **R03/R05/R06/R07 的其余 M/L**：进入常规技术债，不在本轮 P0/P1 射程。

## 每阶段记录模板

```md
### Sx 执行记录
- 子代理 prompt 要点：
- 改动文件：
- 验收证据（命令 + 结果）：
- 全局一致性检查：
- 还原点 commit：
- 残留/后续：
```

### S1 执行记录
- 子代理 prompt 要点：SIGWINCH Unix-only 按平台拆分（exec_resize_{unix,windows}.go），Linux 行为逐字节不变，禁止越界。
- 改动文件：`cmd/fleetly/cmd/verbs_exec.go`（改调 helper）、新增 `exec_resize_unix.go`（signal.Notify(SIGWINCH)）、新增 `exec_resize_windows.go`（no-op）。
- 验收证据（Lead 亲跑）：`gofmt -l cmd/fleetly/cmd` 空；`go build ./...` exit 0（Windows）；`GOOS=linux go build ./...` exit 0；`go test ./cmd/fleetly/...` → ok 4.7s（74 PASS）；`go test ./internal/guards/` → ok 16.3s。
- 全局一致性检查：仅 import 清理 + 两行函数名替换，resize 帧构造/信号 channel 语义未动；无其他 Windows-only 编译阻塞。
- 还原点 commit：见 git log（`fix(cli): exec TTY resize 按平台拆分...`）
- 残留/后续：Windows 终端 resize 不传播（明确不做）；js/plan9/wasip1 不在支持矩阵；CI Windows 编译门禁属 R09-L7（后续）。

### S2 执行记录
- 子代理 prompt 要点：平台管理员门 8 个 handler（EnrollNode/三节点动词/RestartComponent/RefreshTemplates/平台备份两动词）+ 身份写面归属门（SetUserPassword/DeleteUser）+ 角色授予特权上限（CreateUser/CreateToken/CreateInvitation/CreateRole）+ 首个 init 用户改 builtin-owner + R14-M4 缺省 team 修正。
- 改动文件：`fleetlygrpc/identityauthz.go`（新）、`authz.go`（callerIsPlatformAdmin/导出 RequirePlatformAdmin）、`contexts.go`、`templates.go`、`platform.go`、`systemgrpc/systemgrpc.go`、`identity.go`、`invitations.go`、`cmd/fleetly/cmd/init.go`、`init-whoami.golden`、两处 handler 级单测身份注入、`apitest/identity_authz_test.go`（新）。
- 验收证据（Lead 亲跑）：`gofmt -l` 空；`go build ./...` 双平台 exit 0；3 个新安全用例全 PASS（H1 九子例、H2 五子例、角色上限五子例）；`go test ./internal/testsupport/apitest/` ok 24.0s；interfaces/authn/identity 8 包 ok；guards ok 16.3s；cmd/fleetly ok 4.6s；`go vet` exit 0。
- 全局一致性检查：门在审计/副作用之前（拒绝零副作用）；平台管理员判定单源（callerIsPlatformAdmin）；角色上限复用 DotForm 展开语义（admin⇒write⇒read）；init-whoami golden 同批更新（唯一用户可见变化）。非平台管理员不能再转授 builtin-admin（其含 platform:admin）——有意收紧，待权限模型 ADR 恢复队内 admin 委派。
- 还原点 commit：见 git log（`fix(authz): S2 ...`）
- 残留/后续：ListUsers/GetUser 读面授权、Login TTL/限速、平台面 AST 守卫、Scope 词表第二维 ADR、ListNodes/ListNodeContainers 读门（R08-M1）、多队用户 Login 队选择语义。

### S3 执行记录
- 子代理 prompt 要点：install.sh 物化 proxy 端点 + guard；Browse ForwardAuth `?session=` 绑定；备份 pipe 读端生命周期 + ctx 感知等待；Platform Backup 单飞 + 唯一临时快照 rename + E_CONFLICT 映射；`--wait` 与 unary deadline 分离；Console 磁贴查询键拆分。
- 改动文件：install.sh、installenv_test.go（新）、browse.go、gateway_browse.go + 测试（新）、browse_api 测试、capability/ports.go 注释、backup.go + 测试、engine.go（哨兵/锁）、platformbackup.go + 测试、platform.go（映射）+ 测试（新）、dial.go + 测试、verbs_delivery.go、console project-overview.tsx + 测试。
- 验收证据（Lead 亲跑）：`gofmt -l` 空；双平台 build 0；engine 22.5s、assembly 0.4s、guards 17.2s、apitest 23.6s、cmd 4.6s 全 ok；vet 0；6 项新测试逐个 PASS；`sh -n install.sh` 0；`git diff --check` 0；console `pnpm test` 130/130。
- 全局一致性检查：S2 平台门保留（platform.go 只加错误映射）；H4 会话绑定为 `Auth.Address?session=` 且 cookie sid 比对先于 grant 校验；H5 成功路径字节不变；H6 单飞不误报失败事件；`git diff` 无范围外文件。
- 还原点 commit：见 git log（`fix(delivery): S3 ...`）
- 残留/后续：daemon 侧 endpoint 派生、备份前 `PRAGMA integrity_check`、install.sh 真机 systemd 验证、traefik forwardAuth query 的 e2e、快照 `.tmp` 启动清扫、rollback `--wait` 的 unary 无界（既有形态）。

### S4 执行记录
- 子代理 prompt 要点：restart 源改生效基线（纯函数可选 + golden 同批）；RestartSeqOnly 精确 proto 路径；蓝绿旧代 egress 同源；Route 发布就绪门（traefik Ready/端点 503/engine 启动门 + 基线重放开门）。
- 改动文件：verbs_structure.go + apps_restart 测试/golden、spec/diff.go + 测试、engine/{bluegreen,engine,construct,lifecycle,routepublish,bluegreen_test,managed_test}、traefik/{provider,config_test}、assembly/{proxyconfig,proxyconfig_test}。
- 验收证据（Lead 亲跑）：`gofmt -l` 空；双平台 build 0；spec 0.06s、engine 23.1s、traefik 0.04s、assembly 0.4s、guards 17.6s、cmd 4.6s 全 ok；vet 0；`git diff --check` 0；golden 变化经逐行核对（never-deployed 文案 + apps-restart-json to_revision r3→r2 属 H15 语义）。
- 全局一致性检查：`--runtime` 逃生面未动；routeReady 构造默认 true 保证手动驱动/测试零行为变化；只有 Start 的基线重放窗关闸；traefik Ready 失败不清位（LKG 继续）；端点对不实现 Ready 的旧 Provider 语义不变。
- 还原点 commit：见 git log（`fix(engine,cli): S4 ...`）
- 残留/后续：H7 真机 e2e（多 App 慢重放 + traefik 503 保 LKG）、publishRoutes skip 不 remember（由既有强制重放兜底）、H9 k3s e2e 断言、H15 的 ADR-0083 追记与 skills 文案。

### S5 执行记录
- 子代理 prompt 要点：k3s RBAC 补 configmaps/netpol update/pods deletecollection + 调用面↔RBAC AST 守卫 + e2e can-i；acceptanceguard 针随 ADR-0080 换形 + 防盲锚；audit 非事务 Append 串行化+有界重试；outbox TrimBefore 改 seq 前缀；CI 加 codegen 零漂移 job。
- 改动文件：`k3s/{rbac,rbac_test,rbac_surface_test}.go`、`e2e/dind-k3s.sh`、`guards/acceptanceguard_test.go`、`persistence/{audit,outbox}/repo{,_test}.go`、`.github/workflows/ci.yml`。
- 验收证据（Lead 亲跑）：`gofmt -l` 空；双平台 build 0；k3s 3.3s、audit 0.33s、outbox 0.24s、guards 17.4s、persistence 全包全 ok；vet 0；`git diff --check` 0；`sh -n e2e/dind-k3s.sh` 0；audit 并发 256 全成功、outbox 无中间洞（子代理实测复核）。
- 全局一致性检查：RBAC 只扩 `p.cli`（SA 身份）调用面，boot/管理 kubeconfig 面与 RbacV1/serviceaccounts 不扩；事务执行面不取锁（避免单连接死锁）；outbox 前缀裁剪保留 410 契约；CI job 与本地同命令。
- 还原点 commit：见 git log（`fix(k3s,persistence,guards,ci): S5 ...`）
- 残留/后续：R06-M1 ConfigMounts 仍归 backlog（H3 只消除 403，真机发布前应先落挂载，否则"显式 403 变静默无配置"）；审计跨进程仅靠重试；e2e/Actions 未本机运行。

## 终验（S1-S5 后，Lead 亲跑 2026-10-19；历史记录）

- `go build ./...`（Windows）exit 0；`GOOS=linux GOARCH=amd64 go build ./...` exit 0。
- `go test ./...`（root module）：除本机已知环境面 2 项外全绿——`keyring`（Windows ACL 不体现 0600/0700）与 `builders`（Docker daemon 不可达）；`cmd/fleetly` 自 S1 起在 Windows 可运行（35.9s）、engine 36.3s、guards 45.1s、apitest 45.8s、persistence 全包、k3s、traefik、interfaces 全绿。
- `go test ./genproto/... ./sdk/go/...` exit 0；`go vet ./...` exit 0；`gofmt -l .` 空；`go test ./internal/guards/` 45.1s ok。
- `cd console && pnpm test` 30 files / **130 tests passed**。
- 终验结论：**S1-S5 全部验收门通过**；12/15 条 H 已本地修复（未 push），H8/H10/H11 与收敛语义深水区列 backlog；e2e/Actions 未本机运行（Docker daemon 不可达），H3/H7/H9 真机触发需 staging/dind 台账。

## 还原点（本地 commit，未 push）

```
a5ae233f docs(plan): 评审修复排期 S1-S5
01bd214e fix(cli,guards): S1 Windows 构建解封
94a5267f fix(authz): S2 平台面门禁 + 身份写面归属 + 角色授予上限
eb852068 fix(delivery): S3 安装路由面/Browse 会话绑定/备份可靠性/wait deadline/Console 键
c925ba3b fix(engine,cli,spec,traefik): S4 restart 源=生效基线/精确 seq 判定/BG egress/Route 发布就绪门
cdecabe0 fix(k3s,persistence,guards,ci): S5 RBAC 调用面收敛/守卫换形/审计并发/outbox 前缀裁剪/CI codegen 门禁
d233c07d docs(plan,reviews): S1-S5 终验记录与修复状态归档
```

> 2026-10-19 已 push 到 `origin/main`（`35257f18..d233c07d`）。

---

## 第二轮 backlog 修复（S6-S10，已启动）

| 阶段 | 主题 | 覆盖发现 | 验收门 | 状态 |
|---|---|---|---|---|
| S6 | k3s ConfigMounts 挂载（H3 配套） | R06-M1 | translate/runtime ensure 新测试 + 引用名一致性；k3s/guards 绿 | ✅ |
| S7 | 幂等键调用方作用域 | H11（R14-H4） | 迁移 00034 + repo/enforcer 签名；跨 token/跨队同键不同体/同体不复用；apitest 绿 | ✅ |
| S8 | Metrics 租户域过滤（设计） | H8（R07-H2/R08-M2） | 设计文档 `docs/design/2026-10-19-security-backlog-design.md`（推荐 QueryScopedMetrics + raw 收平台管理员） | ✅ 设计 |
| S9 | H8 阶段 A 止血 | H8 | raw QueryMetrics 收平台管理员（403 先于 503）+ 响应 8MiB/1000 序列上限；member 图表失明（已裁决代价） | ✅ |
| S10 | H8 阶段 B scoped RPC + Console/CLI 切换 | H8 | **a** 契约+服务端 ✅（`e9654b8c`）；**b** Console scoped 图表/WhoAmI.platformAdmin/truncated 提示 + CLI `metrics series` + `console:gen/build` 同批 ✅ | ✅ |
| S11 | relay 节点个体凭证 | H10（R14-H3） | **a** 后端+兼容 ✅（`9fe85c4e`）；**b** 运维面：`nodes relay/revoke-relay` CLI + Console 节点行动作 + runbook + `node.left` 清理/`relay_replaced` 审计 + ADR 追记 ✅ | ✅ |
| S12 | 收敛语义深水区 + ADR 追记 | R02-M2/M5、R16-M2/M3/M5/M6、H15 ADR-0083 追记 | ADR 修订 + 判定表单源 + 静默面事实化；钉测翻向；**D3 已实施并经 staging 真机验证**（`ca199a82`） | ✅ |

> S6 先行原因：H3 已消除 k3s configmaps 403，若不落挂载，真机会从"显式 403 失败"变成"ConfigMap 建了但容器无配置"的静默失败。

> **第二轮收尾（2026-10-19）**：S6-S12 全部结项——S12 分三批落码：**b1** `2740a09d`（判定表接进生产 + `EnsureReport` 变更集 + `extra`/`replay` 真实信号 + `ensuredBy` 账本归属，已 push）；**b2** `23f4b0b1`（ADR-0081/0082/0083 修订 + CONTEXT 词条 + 守卫补强，已 push）；**D3** `ca199a82`（detect = 零运行时写：删 baseline 豁免 + 启动只重建期望账本 + gen 锚守卫 + 隔离 Warn + C1-C6 钉测；Lead 在 S12b2 后复核裁决执行），并已在 **staging 真机验证**（二进制 dc22485→ca199a82，detect 冷启动零写/路由可解析/drift 检出但不收敛/恢复 all 自动回卷；详见 `docs/runbooks/staging-fleetly.md` 2026-10-19 节）。第二轮 H 与目标 M 级发现（R02-M2/M5、R16-M2/M3/M5/M6）均已收敛，残余 M/L 见评审报告 §11。

### 第二轮裁决记录（2026-10-19，用户确认）
- H8：**先阶段 A 止血 + 随后阶段 B scoped RPC**；本批只做 **app 级**，project 级 scoped 查询与 `ListAlertRules`/`ListAlertStates` 的 Team 过滤单列小批。
- H10：**接受两步 enroll**（先 join，节点锚定后按节点领取 relay 脚本）；**rotate/重铸即时断开在连 relay**（在途 exec 收 relay_disconnected）。
- 设计文档：`docs/design/2026-10-19-security-backlog-design.md`（S8 归档，commit `5be1527b`）。

### S9 执行记录（H8 阶段 A）
- 改动：`fleetlygrpc/alerting.go`（QueryMetrics 首行 RequirePlatformAdmin，门先于 Metrics nil 的 503）、`providers/victoriametrics/provider.go`（响应体 8 MiB + 1000 序列上限，超限返回 `capability.MetricsQueryError` → E_INVALID_ARGUMENT）、`provider_test.go`（4 子例）、`apitest/metrics_authz_test.go`（新，3 非平台身份 403 + owner 非 403）。
- 验收证据（Lead 亲跑）：gofmt 空；双平台 build 0；victoriametrics 3.35s、fleetlygrpc 0.26s、apitest 25.8s、guards 18.0s；新测试逐个 PASS；vet 0；diff-check 0。
- 已知代价：member/非平台 admin 的图表与 CLI `metrics query` 现 403（已裁决；阶段 B 恢复）。告警评估不经查询面、不受影响。
- 残留：阶段 B `QueryScopedMetrics` + Console/CLI 切换（S10）；project 级与 alerts Team 过滤单列小批。

### S10a 执行记录（H8 阶段 B 契约+服务端）
- proto：`telemetry.proto` 新增 `QueryScopedMetrics`（GET `/v1/metrics/scoped`）+ 请求/响应（scope 恰一、metric 白名单 CEL）；`identity.proto` WhoAmIResponse 加 `platform_admin`；`mise run generate:all` 生成 6 个 genproto 文件（config.pb/wire_gen 零 diff），`generate:verify`/`buf lint`/`buf breaking` 均 0。
- capability：`ports.go` Metrics 加 `QueryScoped` + ScopedQuery/SeriesPage + 两错误哨兵；新 `metricsquery.go`（BuildScopedPromQL 固定模板：swarm 词汇臂 or k3s raw 臂；ClampScopedStep 24h/15s/1000 点/1000 序列上限）+ 10 形状 golden 测试。
- provider：抽 `queryRange`（8MiB 体上限/4xx 类型化/30s 超时类型化/序列 cap）；`QueryScoped` 截断 50 条 + truncated/total；raw 超限改哨兵。
- handler：`QueryScopedMetrics` 严格顺序（行级授权→503→动态参数→窗口/step→查询）；scoped VM 4xx→E_INTERNAL 带 cause；`WhoAmI.platform_admin` 映射；新 errcode E_METRICS_RESULT_TOO_LARGE(429)/E_METRICS_QUERY_TIMEOUT(504) + mapping + codes.golden。
- apitest：`NewWithMetrics` seam + 7 用例（本队成功/他队 403/404/8 项校验/截断/平台管理员跨队/模板拒绝 500/哨兵 429/504）全 PASS。
- 验收证据（Lead 亲跑）：buf lint 0；gofmt 空；双平台 build 0；capability/errcode/vm/fleetlygrpc/apitest(26.9s)/guards(19.1s)/cmd(5.1s) 全 ok；vet 0；diff-check 0；`mise run generate:verify`（提交后）0。
- 残留：S10b Console（useScopedMetricsSeries/whoami.platformAdmin/图表 truncated 提示）+ CLI `metrics series` + `console:gen && console:build` + golden；阶段 C k3s CPU% 真机；ADR 追记。

### S10b 执行记录（H8 阶段 B Console/CLI 切换）
- Console：`metric-presets` 改指标描述（去 PromQL builder）；`catalog.ts` 新增 `useScopedMetricsSeries`/raw 改名 `useRawMetricsSeries`/WhoAmI `platformAdmin`；`app-metrics`/`app-overview`/`_shell/metrics`/`database-metrics`/`databases` 切 scoped；Custom tab 仅平台管理员可见；`MetricChart` 截断提示；`console:gen` + `console:build` 同批（api 类型 + dist 82 项生成物）。
- CLI：新增 `metrics series`（scope 恰一/`--metric` 必填；人类逐点 `series=<labels> <time> <value>`、`--json` protojson）；`metrics query` synopsis 改平台管理员诊断；metrics 组注册同步。
- golden：改 2（组列表）/新 9（成功双形态/App/Database/usage 三类/服务端错误/offline 双形态）；demo 测试与参数校验单测同批。
- 验收证据（Lead 亲跑）：gofmt 空；build 0；cmd 5.4s、guards 20.0s；metrics golden 定向全 PASS；console `pnpm test` 30 files/134 tests PASS；`console:verify`（提交后）0。
- 残留：CLI golden 既有假时钟 flake（基线 worktree 亦偶发，非本批引入）；`console:gen` Windows shim 问题本次以本地转发器绕过（未入仓，建议单列修 gen.mjs 跨平台）；e2e 真机图表与 k3s CPU%（阶段 C）。

### S11a 执行记录（H10 阶段 1 后端+兼容面）
- 迁移 `00035_node_relay_credentials.sql`（platform_id PK/secret_sha256 UNIQUE/created_at；无外键/expires/revoked 列；forward_test 35）。
- domain：`fltrelay_` 材质（crypto/rand 32B base64url）+ sha256 hex + Repo 四方法；persistence UPSERT/单查/删除（读面无明文；hash-at-rest 测试）。
- capability：删 `ExecClusterToken`/`EnrollKit.RelayCommand`；新增 `RuntimeRelayMaterial` 面（FacesOf/Offered）；`NodeRelayOptions.Credential` 回调。
- providers：swarm `NodeRelayCommand` + 脚本改 `--relay-credential`（不含 SWMTKN）；k3s 无 relay material 面、Credential 回调；fleetlyd `--relay-credential`。
- engine：`fltint_` 内存内部 token + `ValidateRelayCredential`（内部/凭证行/存储故障分流）+ `RelayAttach` 双向绑定（PlatformID/CarrierID）+ `DropRelay/DropAllRelays`；删 `ExecValidateClusterToken`。
- gateway：`/v1/relay`、`/v1/platform/binary` 升级前凭证校验（401 bad_credential / 503 relay_unavailable），attach 错误 1013/1011/1008 分流，日志不落凭证。
- handler `EnrollNode` 三分支：初始 join-only / rotate（Runtime 轮换→事务删全部+审计→DropAllRelays）/ 定向重铸（Get→manager/空 carrier 拒→face 501→铸材质→脚本→事务 Put+审计→DropRelay）/ revoke（事务删+条件审计→DropRelay）；三处 LedgerOnly 理由注释；Detail 只含 carrier/prefix/reason。
- apitest 8 项 §6 验收锚 + engine 4 项 + repo/domain/provider/migration 测试全 PASS；e2e 脚本改两步流程（仅语法）。
- 验收证据（Lead 亲跑）：gofmt 空；双平台 build 0；node/persistence/migrations、swarm/k3s、engine(21.1s)/assembly/fleetlygrpc、apitest(28.2s)、guards(15.5s)、cmd/fleetly(5.5s)/fleetlyd 全 ok；vet 全仓 0；`buf breaking '.git#branch=origin/main'` 0；`sh -n e2e` 0；diff-check 0；`generate:verify`/`console:verify`（提交后）0。
- 残留（S11b）：`nodes relay/revoke-relay` CLI、Console 节点行动作/初始材料文案、runbook、`node.left` 删凭证+DropRelay+审计(reason=node_left)、`node.relay_replaced` 审计/warn、`E_RUNTIME_FACE_UNSUPPORTED` suggestion 补 relay material、e2e 真跑（无 Docker）。

### S11b 执行记录（H10 阶段 2 运维面/生命周期）
- engine：`node.left` 先删个体凭证→DropRelay→仅 deleted 审计 `reason=node_left`（独立于事件成败，事件失败不再 continue 且照常下线）；`RelayAttach` 活体顶替发 Warn + LedgerOnly `node.relay_replaced`（Detail version）；新增 `emitLedger` best-effort 取证面。
- CLI：`nodes relay --node`（敏感脚本 + 提示）与 `nodes revoke-relay --node`；组列表 golden 更新；新 golden 7 个（双形态/缺参/manager 拒绝）+ 参数校验。
- Console：`nodes-list.tsx` 新载体——初始 Enrollment 仅 join+提示；worker 行 Relay command/Revoke relay；rotate 文案；whoami.platformAdmin fail-closed 显隐；vitest 4 用例。
- 文档：runbook 升级序/新「节点 relay 凭证运维」节；ADR-0049 附录 C + ADR-0052/0053 Provider 差异追记；`E_RUNTIME_FACE_UNSUPPORTED` suggestion 补 relay material（codes.golden 同批）。
- e2e：rotate 恢复腿脚本（仅语法校验）；未真跑（无 Docker）。
- 验收证据（Lead 亲跑）：gofmt 空；双平台 build 0；engine 25.9s/apitest 31.2s/guards 21.3s/cmd 5.6s/fleetlyd 全 ok；NodeLeft 两测试 + RelayAttach 审计测试逐个 PASS；vet 全仓 0；diff-check 0；e2e 语法 0；console 138/138；`generate:verify`/`console:verify`（提交后）0。
- 残留/裁决点：`node.left` 抖动为单拍缺行即删凭证/断连（无宽限窗；runbook 已写处置，是否加宽限待后续观察）；e2e rotate 恢复腿与 staging 真机未跑（无 Docker）；checklist/评审报告状态留给 Lead 统一更新。

### S12 执行记录（收敛语义深水区 + ADR 追记）
- 子代理 prompt 要点：**b1**（已提交 `2740a09d`）判定表接进生产（8 判定行 + 信号/闸门两映射 + 两处真实调用）、`EnsureReport.Changed/Removed` 与审计按「fast 源 ∨ 真实变更」分流、`extra`/`replay` 真实信号、`ensuredBy` 账本归属；**b2**（S12b2，commit `23f4b0b1`）只做法源与守卫收尾——ADR-0081/0082/0083 修订、CONTEXT 词条、`writeclassguard_test.go` 两条断言、设计/plan/review 状态回写；**D3（detect 零写）Lead 裁决延后**，禁止改 detect/engine 行为。
- 改动文件（b1）：`internal/application/engine/{converge,drift,materialize,observ,observdomain,transition,teardown,options}.go` 与 `converge_test.go`/`fakeruntime_test.go`、`internal/domain/shared/capability/runtime.go`、swarm/k3s `runtime.go` + 内容等价/ensure 测试、`eventcode/events.go` + golden、`converge-verdicts.golden`（10→8 行）。
- 改动文件（b2 文档批）：`docs/adr/0081-write-class-author-class.md`、`docs/adr/0082-converge-by-default.md`、`docs/adr/0083-restart-seq.md`、`CONTEXT.md`（Declaration/Posture/Replay 三条）、`internal/guards/writeclassguard_test.go`、`docs/design/2026-10-19-converge-semantics.md`（状态 + §3.7 裁决记录 + §8 实施现状）、本文件、评审报告 §11。
- 验收证据（Lead 亲跑）：b1 见 `2740a09d` 与评审 §11；b2/终验：gofmt 空、双平台 build 0、guards 20.5s/engine 25.8s/cmd 全绿、vet 0、diff-check 0；终验 `go test ./...` 仅 2 项本机环境失败（keyring/builders），装配层 self-description golden 漏项由 Lead 终验捕获并在 `fc485f1d` 同步。
- 全局一致性检查：判定表 8 行与 golden 逐行一致；ADR 计数 `27 ACTION + 53 DECLARATION + 74 READ = 154` 与 `writeclassguard_test.go` 断言一致（S10a 的 `QueryScopedMetrics` 补入附录 A.14）；ADR-0082 行 10 机制改写为 `replay` 重放标记（撤销「缺席检测」旧表述）；ADR-0081 判据与 `SetUserPassword` 附录/边界案例三处同源；D3 行为零改动（`attemptConverge` baseline 豁免与 `options.go` 注释原样）。
- 还原点 commit：`402132fe`（设计稿）、`2740a09d`（S12b1 代码）、`23f4b0b1`（S12b2 文档/守卫）、`fc485f1d`（终验 self-description golden 同步）。
- 残留/后续：`--runtime` 校验改生效基线（P2 可选）；R16-M4（撤销 peer 同步 Ensure）、R16-L1/L2/L4/L6 等 M/L 残余见评审 §11。

### D3 执行记录（detect = 零运行时写 + staging 真机验证）
- 代码（commit `ca199a82`，12 文件）：`attemptConverge` 删 baseline 豁免（fast/slow/启动基线重放/peer 隔离四源同受）；`rebuildBaselines` detect 分支改走 `rebuildBaselineExpectations`（lockApp + driving 复查 + `expectedWorkloads` + `recordProjectedSpecs`，不 Ensure）；`recordProjectedSpecs` 写 `ensuredSpec`+`ensuredBy`、不写 gen 锚；gen 对照无锚即跳过（防 0 锚假信号）；`IsolateNetworkPeer`/`enforcePeerIsolation` 被拦落 Warn；CONTEXT/options/ADR-0082 §5 收口（验收锚 `[x]`）。
- 测试（C1-C6 全 PASS）：`TestPostureDetectWritesNothing`（替换旧测）、`...RebuildsExpectationsWithoutEnsure`、`...PublishesRoutesFromExpectationCache`、`...SpecDriftStillDetected`、`...GenDriftBlindDocumented`、`...IsolationSkipsWithWarn`（均含切回 all 反向断言）。
- Lead 门禁：gofmt 空、双平台 build 0、engine 22.9s、apitest 30.7s、guards 20.2s、cmd 6.2s 全绿、vet 0、diff-check 0。
- **staging 真机（fleetly-dev，2026-10-19）**：旧 dc22485 → 新 ca199a82，停服快照（data-root 109MB + 七卷 tar + 旧双二进制）后换装；migrations 33→35；16 容器 CreatedAt diff 零滚动；detect 冷启动后三探针 200、跨 60s 慢通道窗容器零变化、事件无 converge 堆积；`--replicas 2` 篡改 10s 内产出 `workload.drift_detected` 且保持 2/2 不收敛；恢复 all 后 20s 内自动回 1/1，审计出现 `workload.converge`（source=system + 基线 digest），事件无刷屏。回滚/留底路径见 runbook 2026-10-19 节。
- 残留/降级（成文 ADR-0082 §5）：detect 下 gen/stopped 失明、撤销 peer 不剥离（Warn，恢复 all 后下一拍剥离）；staging 无 peer 面，C5/C6 覆盖。

### S6 执行记录
- 改动：`k3s/translate.go`（sortedConfigMounts/configMountsWithMaterials/configVolumeName + toContainer SubPath 挂载 + podTemplate ConfigMap 卷 0444）、`translate_test.go`（三载体/缺材料/顺序稳定）、`runtime_ensure_test.go`（Ensure 引用名一致性 + SkipMaterials）。
- 验收证据（Lead 亲跑）：gofmt 空；双平台 build 0；k3s 3.3s（2 新测试 PASS）；guards 17.3s；vet 0；diff-check 0。
- 设计：卷名 `fleetly-config-<i>`（稳定序位次，避免净化同形/同名多 target 撞卷名）；SubPath="value" 单文件挂载；卷 DefaultMode 0444 对齐 swarm；材料缺失防御跳过、SkipMaterials 零挂载；canonical 逐字节稳定。
- 残留：`e2e:dind-k3s` 未覆盖 config materials（建议补断言）；SubPath 单文件在 ConfigMap 轮换时不热更新（需重调度/滚动，归后续载体版本化项）；R06-M7 孤建清扫仍 backlog。

### S7 执行记录
- 改动：migration `00034_idempotency_caller_scope.sql`（重建表 `(scope, idem_key)` 复合主键；存量行落空作用域不可达）、`forward_test.go`（链锚 34 + 检查点 33 + 存量处置测试）、domain `Record.Scope` + Repo 四方法加 scope、persistence repo SQL 带 scope、enforcer `callerScope`（token:<id> / user:<id> / anonymous）与测试（跨 Token/跨队不复用）。
- 验收证据（Lead 亲跑）：gofmt 空；双平台 build 0；idem 0.68s / persistence/idempotency 0.22s / migrations 1.78s / persistence 0.18s / guards 17.3s / apitest 26.9s 全 ok；新增 7 项测试逐个 PASS；vet 0；diff-check 0。
- 全局一致性检查：authn 在 idem 拦截器之前（生产 ctx 恒有身份）；ReceiveWebhook 为 PUBLIC 面，X-GitHub-Delivery 派生键落 anonymous 且响应不含凭证；存量空作用域行对全部调用方不可达、仅由 Sweep 过期清理。
- 残留：`entityid_guard` 豁免注释仍写"主键 = idem_key"（现为复合主键，守卫绿，随批更新注释）；迁移不可回滚（goose 只前滚惯例）。

---

## 第三轮 backlog 收口（S13-S19，2026-10-19）

台账（198 条 M/L 的裁决、逐条状态、最终统计与可复跑校验）：`docs/plan/2026-10-19-backlog-closure.md`；S13-S17 把 59 条 FIX-NOW 全部落码，S18 收口非 M/L 残项，S19 只改文档。

| 批次 | commit | 覆盖 | 门禁/证据（台账 §3.9 记录） |
|---|---|---|---|
| S13 | `1dd4171d` | R14-M1、R14-L1、R08-M2 | 身份读面最小化 / Login 双桶限速 / alerts Team 过滤；门禁全绿 |
| S14a | `5e5ab2fe` + `ff0b8681` | R03-M13、R07-M4、R07-M2、R14-M8、R07-L3、R07-L4 | 外部面/密钥材料；c1 回归修复后 cmd golden 复绿 |
| S14b | `42fb9f50` | R14-M5、R08-M3、R08-M8、R08-M9、R09-M8（+R14-M6 DUP） | 授权/事实/契约/端点认证；新增 factteam 守卫；多队无 token fail-fast |
| S15a | `ff8b8d18` | R02-M1、R02-L1、R02-M3、R02-M4、R02-M7、R13-L1、R16-L3 | 冷却签名三态/BG 重启代次/收口驱动权+维护锁/环 panic 可观测；engine `-race` 225s 绿 |
| S15b | `b2941cc3` | R16-M4、R16-L2（+R02-L7 DUP）、R13-M1、R03-M5/M6/M7/M8/L5 | peer 撤销异步/BG 退役代账本/Teardown 清账+LRU/compose 保真五连；engine `-race` 228s 绿 |
| S16a | `a543266b` | R06-M2、R06-M6、R06-M4、R06-M7、R06-M5 | k3s replicas/egress/logs、swarm config 清扫/exec 节拍；负例先红、门禁全绿 |
| S16b | `ce9a15c7` | R01-M1（+R01-H1†）、R08-M1、R03-M1/M2、R04-M1、R13-L3 | browse 五引擎方言/节点读面平台门/引擎卫生四连；engine `-race` 235s、console 139 全绿 |
| S17a | `8f5d41e7` | R12-M2/L1/L2/M3/M4/M7、R05-M8、R06-L1 | Runtime/loop/statefacade AST 判据、账本写点守卫、FakeRuntime 回执、e2e converged 断言；每项反向验证红→绿 |
| S17b | `c1e828d7` | R12-M5/L5、R09-L4/L5/L7、R15-L4、R16-L1/L4/L5、R10-M1/L1、R02-L5 | CI/verify 对账、写类守卫三盲点、install 0750、CLI delete JSON 契约、freeze 真钉测；负例先红→绿 |
| S18a | `07128fef` | project scoped metrics（非 M/L）、Login TTL（R14-M1 另一半） | 迁移 00036、`auth.login_token_ttl` 720h、raw 双臂、golden/console:gen 同批 |
| S18b | `4cfcc7fc` | 非 M/L：golden flake / console:gen shim / node.left 宽限窗 / browse flake / volumes golden | 两相假时钟、`gen.mjs` 跨平台、`NodeOptions.LeaveGrace` 30s、browse 根因；已 push |
| S19 | docs-only（未提交） | R15-M1（文档侧）+ ADR-0072/0082/0083 追记 + 台账 §3.10 + 报告 §11 + 本记录 | 无 go test；`git status --porcelain` 仅 docs/CONTEXT；台账 §3.10.4 校验脚本实跑全绿 |

### S19 执行记录（docs-only）
- 改动面：`CONTEXT.md`（Restore 词条）、`docs/adr/0005/0015/0072/0082/0083`、`docs/design/2026-09-30-architecture.md`、`docs/design/2026-09-30-domain-model.md`、`docs/design/2026-10-19-security-backlog-design.md`、`docs/design/2026-10-19-converge-semantics.md`（§9.1 S19 状态追记）、`docs/runbooks/staging-fleetly.md`、`docs/plan/2026-09-30-feature-checklist.md`（F2.3 条目）、`docs/plan/2026-10-19-backlog-closure.md`、`docs/reviews/2026-10-19-architecture-quality-acceptance.md`、本文件。
- 行为口径：v1 **无平台运行期只读闸门、无 `platform restore` 动词**；恢复 = Platform Backup 重放 + runbook 手工停服序；升级序末步为启动后健康核对（无「解除只读」）。实现闸门 + restore 动词登记为独立项（触发：F2.3 恢复链落地）。
- 验收：无代码/proto/golden/CI/脚本改动；台账 §3.10.4 的 PowerShell 校验实跑输出 `rows=199 unique=199`、`FIXED=17/DUP=9/FIX-NOW=60/ACCEPT=30/SEPARATE=83`、`fixed=76`、`total=198`，Compare-Object 全空。
- 未提交：按任务约束不 commit、不 push；由 Lead 验收后提交。

## 终验（S1-S19 收口，2026-10-19）

- **代码基线**：`HEAD = 4cfcc7fc`（S1-S18b 全部 push `origin/main`；`git log 35257f18..4cfcc7fc` 共 40 个提交）。S19 为 docs-only 工作区改动。
- **台账口径**：198 条 M/L 全部有裁决——FIXED 76（17 原 + 59 本轮）、DUP 9、ACCEPT 30、SEPARATE 83、FIX-NOW 0；外挂 `R01-H1†` 已修（不计入 198）。双向差集为空（`docs/plan/2026-10-19-backlog-closure.md` §3.10.4，实跑 `total=198`）。
- **文档一致性**：R15-M1 六份文档 + domain-model 已改 v1 诚实口径；`grep -rn "恢复期" docs/` 剩余命中均为历史审计记录、台账裁决原文或"数据库恢复期"无关语义（ADR-0055），无现行平台只读承诺；ADR-0005/ADR-0015 以 S19 修订注记保留历史正文（ADR 只追加/修订不删历史）；ADR-0072/0082/0083 追记已落。
- **门禁**：S13-S18b 各批由 Lead 在提交前验收（gofmt/双平台 build/guards/目标包 test/engine `-race`/console 等，见台账 §3.9）；S18b 已 push。S19 无 go test；自检 = `git status --porcelain` 仅 docs/CONTEXT + 台账 §3.10.4 脚本全绿。
- **未验证边界（诚实）**：S13-S18b 未上 staging（待 Lead S19 真机复核结果回填）；dind e2e（H7/H9、k3s config mount、relay 双节点）与 GitHub Actions 新 job 未真跑（本机无 Docker）；k3s CPU% 真机属 H8 阶段 C。
