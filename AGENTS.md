# AGENTS.md — fleetly 工程约定

对人与 AI Agent 同一生效。词汇真源是根目录 `CONTEXT.md`（ADR-0007 词汇冻结：**更名即设计缺陷**，发现词条不合适走显式 ADR，不接受局部混用）。结构真源：`docs/design/2026-09-30-architecture.md`（§2 分层树与依赖方向）。

## 语言约定

- **用户可见文本英文**（报错、日志、CLI 输出、API 文案）；**注释中文**。
- CLI 全命令双形态：人类默认 + `--json`（protojson snake_case），golden 双形态钉死。

## 工具链（mise 钉版，与 CI 同版本）

```sh
mise install            # 安装 go/buf/protoc/protoc-gen-go/golangci-lint
mise run generate:all   # buf generate + config.pb.go + wire（wire 任务自带 GOWORK=off）
mise run test           # go test -race 三 module
mise run lint           # go vet/gofmt + golangci + buf lint
```

## 生成物纪律

- `genproto/**` 与 `*.pb.go`、`wire_gen.go` 是生成物：**手改即错**；再生成走 mise 任务。
- proto 是唯一契约源：改 API 先改 `proto/`，再 `buf generate`；buf breaking 门禁在 CI。
- golden 再生成：`go test <包> -update`，与对应变更同 commit 提交。

## 守卫文化（架构由测试承载，架构文档 §11）

- import 守卫：编排器/基础设施 SDK 只准出现在 `internal/providers/**`；`model/spec` 是叶子（不 import 任何 internal 包）；providers 互不 import，除 cmd 外无人 import providers。
- errcode/eventcode 注册表只增：新码带 Source 锚点 + suggestion，golden 与 usage 反扫同步更新。
- 禁词扫描以 CONTEXT.md 的 _Avoid_ 表为源；例外条目带理由注释，不再命中即红（白名单双向保鲜）。
- 新测试与功能同批落地，不事后补。

## 提交纪律

小步提交（骨架/守卫/地基/部署链/账号/引导各自成 commit）；每完成一个 F 项在 `docs/plan/2026-09-30-feature-checklist.md` 标 `[x]` 并随该 commit 更新。用户可见行为变更的 commit 必须含对应 golden/守卫更新。

ADR 含可静态执法的承诺时，须同批开守卫任务（ADR-0001→irguard 为范式）；ADR 模板含验收锚小节（可测承诺逐条列为可勾选项）。
