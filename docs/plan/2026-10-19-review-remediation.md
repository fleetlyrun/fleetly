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
| S4 | 声明式与路由语义：restart 源/精确判定 + BG egress + 发布就绪门 | H15、R16-M1、H9、H7 | 新钉测绿；engine/assembly/traefik/providers 相关包绿；golden 同批 | ☐ |
| S5 | 双 Runtime 与守卫/完整性收尾 | H3、R12-M1、R05-M1、R05-M4、R09-M1 | k3s 规则+守卫绿；并发 audit/outbox 测试绿；CI 门禁就位；全量回归 | ☐ |

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

## 终验（全部阶段后）

- `go build ./...`（Windows）+ `GOOS=linux go build ./...`
- `go test ./...` 三 module（剔除本机已知环境面：keyring Windows ACL、builders Docker daemon）
- `go test ./internal/guards/` + `go vet ./...` + `gofmt -l`
- `cd console && pnpm test`
- 逐条对照本文件阶段门 + 更新 `docs/reviews/2026-10-19-architecture-quality-acceptance.md` 修复状态
