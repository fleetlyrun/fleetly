# AGENTS.md — fleetly 工程约定

对人与 AI Agent 同一生效。

- 词汇真源：根目录 `CONTEXT.md`（ADR-0007 词汇冻结：**更名即设计缺陷**，发现词条不合适走显式 ADR，不接受局部混用）。
- 结构真源：`docs/design/2026-09-30-architecture.md`（§2 分层树与依赖方向，§11 守卫清单）；分层形态以 ADR-0075（Clean 四环）为准。
- 决策真源：`docs/adr/`；进度真源：`docs/plan/2026-09-30-feature-checklist.md`。

## 语言约定

- **用户可见文本英文**（报错、日志、CLI 输出、API 文案）；**注释中文**。
- CLI 全命令双形态：人类默认 + `--json`（protojson snake_case），golden 双形态钉死。

## 工具链（mise 钉版，与 CI 同版本）

任务统一经 sh 执行：Linux 自带，Windows 需 Git Bash 的 sh 在 PATH。

```sh
mise install              # 安装 go/buf/protoc/protoc-gen-go/golangci-lint/node/pnpm
mise run generate:all     # 基线生成通道：插件钉装 .tmp-bin → buf generate（本地插件）→ config.pb.go → wire（GOWORK=off）
mise run generate:verify  # 基线通道再生成 + 零漂移断言（genproto/config/wire）
mise run test             # go test -race 三 module（根 + genproto + sdk/go）
mise run lint             # go vet/gofmt + golangci + buf lint + buf breaking（对 origin/main）
```

console 前端（F2.6/ADR-0044，node 24/pnpm 钉版入 mise）：`console:install`/`console:gen`（openapi-typescript 从 genproto swagger 再生成 src/api）/`console:build`（产物进 internal/interfaces/console/dist）/`console:verify`（再生成 + 再构建 + 零漂移，与 CI console job 同口径；dist 是提交进仓的生成物，改 proto/前端后必须 `console:gen && console:build` 同 commit）。

e2e dind 套件（CI 常态跑 upgrade/backup 两矩阵）：`e2e:dind`（单节点冒烟）/ `e2e:h2c`（受管 traefik Route）/ `e2e:twonode`（enroll + 双节点 + 卷钉住）/ `e2e:upgrade`（升级零扰动）/ `e2e:backup`（四引擎恢复演练 + 平台仓 roundtrip）。

## 生成物纪律

- `genproto/**`、`internal/config/config.pb.go`、`internal/assembly/wire_gen.go` 是生成物：**手改即错**；再生成走 mise 任务。
- proto 是唯一契约源：改 API 先改 `proto/`，再 `buf generate`；buf breaking 门禁在 CI 与 `lint:proto`。
- golden 再生成：`go test <包> -update`，与对应变更同 commit 提交。

## 分层与依赖方向（违反即守卫红，ADR-0075 四环 + 架构文档 §2）

```
domain      → （零外向；三红线：无 I/O、无 genproto、无 RPC 框架）
application → domain
infrastructure → domain（实现接口）
interfaces  → application + domain（读径；写必经 application——读写分离律）
assembly    → 全部（组合根）
```

- internal/ 四环 = `domain`（shared：spec/model/apperr/capability；identity；services：anchor/material/dbtemplate/dbbrowser/writes）、`application`（engine/authn/idem/governance/composeintake/apptemplate/schema）、`infrastructure`（persistence/providers/github/upload/keyring）、`interfaces`（grpc/admin/console）；assembly/config/buildinfo 组合根，guards/testsupport 元层（ADR-0075）。
- 编排器/基础设施 SDK 只准出现在 `internal/infrastructure/providers/**`（builders/swarm/traefik/zot/victorialogs/victoriametrics/localobjectstore）。
- providers 互不 import；除 cmd/assembly 外无人 import providers（经注册表间接装配）。
- persistence 按聚合分包，各聚合自己的 repo，禁止门面 `store.go`；Repo 接口随 P1b 上移 domain（ADR-0075 决策 3）。

## 守卫文化（架构由测试承载，`internal/guards`）

- 禁词扫描以 CONTEXT.md 的 _Avoid_ 表为源，覆盖全仓含 proto、测试夹具与 `skills/` 散文；例外条目带理由注释，不再命中即红（白名单双向保鲜）。禁词只执法 **fleetly 域内语义混用**——第三方专有词汇按本义使用不在禁列（如 GitHub 的 Organization 实体），走同一白名单机制带理由豁免（2026-10-13 用户裁定）。
- errcode/eventcode 注册表只增：新码带 Source 锚点 + suggestion，golden 与 usage 反扫同步更新。
- 新测试与功能同批落地，不事后补；门禁必跑全套，不可只跑触碰包。

## 提交纪律

小步提交（骨架/守卫/地基/部署链/账号/引导各自成 commit）；每完成一个 F 项在 `docs/plan/2026-09-30-feature-checklist.md` 标 `[x]` 并随该 commit 更新。用户可见行为变更的 commit 必须含对应 golden/守卫更新。

ADR 含可静态执法的承诺时，须同批开守卫任务（ADR-0001→irguard 为范式）；ADR 模板含验收锚小节（可测承诺逐条列为可勾选项）。

## 真机与 Agent 面

- staging 双节点集群是现役验证环境：操作前先读 `docs/runbooks/staging-fleetly.md`（升级序、flag-day、坑实录都在内）。
- `skills/` 是面向 Agent 的仓内技能（ADR-0031 首批），与 CLI/API 同能力面；散文中的示例命令同样被守卫执法。
