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

## 终验（全部阶段后，Lead 亲跑 2026-10-19）

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
| S8 | Metrics 租户域过滤（设计→实现） | H8（R07-H2/R08-M2） | 设计：`docs/design/2026-10-19-security-backlog-design.md`（推荐 QueryScopedMetrics + raw 收平台管理员）；实现待裁决 | 🟡 设计完成 |
| S9 | relay 节点个体凭证 | H10（R14-H3） | 设计：同上文档（推荐按节点个体 relay 凭证 + 两步 enroll）；实现待裁决 | 🟡 设计完成 |
| S10 | 收敛语义深水区 + ADR 追记 | R02-M2/M5、R16-M2/M3/M5/M6、H15 ADR-0083 追记 | ADR 修订 + 判定表单源 + 静默面事实化；钉测翻向 | ☐ |

> S6 先行原因：H3 已消除 k3s configmaps 403，若不落挂载，真机会从"显式 403 失败"变成"ConfigMap 建了但容器无配置"的静默失败。

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
