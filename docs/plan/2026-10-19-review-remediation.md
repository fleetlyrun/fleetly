# 评审修复排期（2026-10-19）

> 来源：`docs/reviews/2026-10-19-architecture-quality-acceptance.md`（15 条 H + 主题化 M + 机制建议）。
> 执行方式：`subagent-sequential-implement` —— 每阶段一个自足 prompt 派一个子代理实现；Lead 逐条取证验收（自己跑测试/读 diff）、通过后提交本地还原点 commit；未通过只重做本阶段。
> 约束：遵守 AGENTS.md（守卫/测试与功能同批、golden 同 commit、小步提交、用户可见文本英文/注释中文）；**只提交本地，不 push**；每阶段禁止重做/回退前序阶段产出。

## 阶段总表

| 阶段 | 主题 | 覆盖发现 | 验收门（每阶段） | 状态 |
|---|---|---|---|---|
| S1 | 工程基线：Windows 构建解封 | R10-M6（SIGWINCH） | `go build ./...` 双平台通过；`go test ./cmd/fleetly/...` 可运行；guards 绿 | ✅ |
| S2 | 安全边界：平台面门禁 + 身份写面归属 + 角色授予上限 | H1、H2（另含 R14-M2 的 init owner 修正、R14-M4 的 team 缺省退化） | 复现场景反转为 403；同队 admin 正常；apitest/guards 绿；golden 同批 | ☐ |
| S3 | 交付主链：安装路由面 + Browse 会话绑定 + 备份可靠性 + CLI 等待/Console 崩溃 | H12、H4、H5、H6、H14、H13 | 各修复单测/回归绿；install.sh guard 绿；console vitest 绿；build 双平台 | ☐ |
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

## 终验（全部阶段后）

- `go build ./...`（Windows）+ `GOOS=linux go build ./...`
- `go test ./...` 三 module（剔除本机已知环境面：keyring Windows ACL、builders Docker daemon）
- `go test ./internal/guards/` + `go vet ./...` + `gofmt -l`
- `cd console && pnpm test`
- 逐条对照本文件阶段门 + 更新 `docs/reviews/2026-10-19-architecture-quality-acceptance.md` 修复状态
