# fleetly 交接文档：F2.8 ObjectStore S3 Provider（第三方 Agent 用，自包含）

> **生命周期**：任务交接档——F2.8 落地（checklist 勾账）后可删。
> 本文档自包含：不依赖任何会话记忆。阅读顺序 = 先读 §1 真源文件，再回来按 §5 实施。
> 仓库：D:/Codes/qiulin/fleetly（git 仓库，main 分支直接开发——无 PR 流程，小步提交直推）。

---

## 1. 项目一句话 + 必读真源（按序）

fleetly 是一个部署平台（对标 dokploy/coolify 定位、小微团队目标用户）：Go 单体控制面（fleetlyd）+ CLI（fleetly）+ proto 单源 API；Runtime 抽象（当前 swarm 实现，未来 k3s）；受管自宿组件（traefik/zot/VictoriaLogs/VictoriaMetrics/cAdvisor）由平台自己部署。

动手前**必读**（都在仓内）：
1. `AGENTS.md` — 工程约定入口（语言/生成物/守卫/提交纪律）
2. `CONTEXT.md` — 词汇真源（词汇冻结：更名即设计缺陷；新词条须入册）
3. `docs/adr/0020-backup-by-default-restore-drill.md` — 备份默认 + ObjectStore 端口起源
4. `docs/adr/0039-backup-execution-chain.md` — **ObjectStore 消费面真源**（备份执行链）
5. `internal/capability/ports.go` 的 `ObjectStore` 接口 + 键契约注释
6. `internal/providers/localobjectstore/` — ObjectStore 第一实现（范式参考）
7. 功能清单：`docs/plan/2026-09-30-feature-checklist.md`（F2.8 条目）
8. 架构总纲：`docs/design/2026-09-30-architecture.md`（§2 分层树/§11 守卫）

## 2. 硬约束（违反 = 返工）

- **语言**：用户可见文本（报错/日志/CLI 输出/API 文案）英文；**注释中文**。
- **proto 先行**：改 API 先改 `proto/`，再 `mise run generate:proto:local`（remote buf 通道常断，直接用 local）；config 改动后 `mise run generate:config`。`genproto/**`、`*.pb.go`、`wire_gen.go` 是生成物，**手改即错**。
- **golden 双形态**：每个 CLI 动词有人类 + `--json` 两份 golden（`cmd/fleetly/cmd/testdata/golden/`）；行为变更须同 commit 再生成（`go test <包> -update`）。注意 **protojson 省略零值布尔**——断言别 grep `"field":false`（实证坑）。
- **守卫门禁全套必跑**：`mise run test`（三 module -race）+ `mise run lint`（vet/gofmt/golangci/buf lint/buf breaking）。禁词扫描扫全仓含测试夹具——新增 CONTEXT.md 的 `_Avoid_` 词条必须同步在 `internal/guards/wording_test.go` 分诊（banned 正则或 skipped 带理由），否则红。
- **新测试与功能同批**，不事后补。**小步提交**（地基/实现/文档各自成 commit）；每完成一个 F 项在 checklist 标 `[x]` 随 commit。
- **词汇冻结**：CONTEXT.md 是真源。已裁决勿重开：不内置 DNS（解析器归 Runtime）、canary 不做、alias 通道 N2 不动、不做 MCP。
- **Provider 纪律**：providers 只准经注册表（`capability.RegisterFactory`）装配，除 cmd 外无人 import providers，providers 互不 import（守卫反扫）。

## 3. 工具链与工作流

```sh
mise install            # 装 go/buf/protoc/golangci-lint（钉版与 CI 同）
mise run generate:all   # buf + config.pb.go + wire
mise run test           # go test -race 三 module
mise run lint           # 全套门禁
```

- **CI**：GitHub Actions，六个 job（Backend + 五个 dind e2e）。观察用：
  ```sh
  set HTTPS_PROXY=http://127.0.0.1:10801&& set HTTP_PROXY=http://127.0.0.1:10801&& gh run list --limit 3
  ```
  （本机代理是 10801；git push 走 ssh 不受影响。）失败诊断：`gh run view --job <id> --log-failed`。
- **本地跑不了 dind e2e**（Windows/WSL bash 域坑）——**CI 是唯一 e2e 验证面**。写完推 CI 看结果。
- **编辑工具坑**：对同一文件的多次工具编辑有陈旧快照回退问题——机械批量改动用 python 脚本读写文件更稳（本仓多次实证）。
- Windows shell：cmd 不吃 heredoc/`$()`；`;` 是参数分隔符；远程复杂操作一律写脚本→scp→`sh`（`sh -n` 预检：`docker run --rm -v <dir>:/h alpine sh -n /h/x.sh`）。

## 4. 当前系统状态（2026-10-04 收官时；动工前以 git log 为准）

- F2.4/F2.5（日志/指标两批）已收官，代码尾 7232da4 的 CI run 37207849798 六 job 全绿。**main 会被并行会话推进（文档批等）——照例先 `git status` + `git log --oneline -5` 核实。**
- **staging**（双节点 swarm：manager `root@fleetly-dev.deeploop.net` + worker `root@143.198.234.68`）：现役 7232da4-f25final。受管域五件：traefik/zot/victorialogs/victoriametrics 各 1/1 + **cadvisor 2/2（全局服务，每节点一 task）**。端口表在 `docs/runbooks/staging-fleetly.md`（含 8080 无认证的 VPC-only 边界）。
- 近两批实录在 runbook 10-04 记录·三/四；细节读 ADR-0040/0041。
- **受管 Generation 语义（勿破坏）**：per-受管域独立计数 + 从 `InspectWorkloads` 观测播种续接（重启零滚）。新增受管 Provider 不会滚其他域；**全局形态 Workload 必须声明 `Replicas:1`**（InspectWorkloads 对全局服务报缺省 1，声明 0 = 每拍假 drift）。
- 无 systemd 环境的 daemon env 在 `/etc/fleetlyd.env`（install.sh 写；重启助手读——勿再手拼 env）。

## 5. 任务：F2.8 ObjectStore S3 Provider

### 5.1 现状盘点（已探明，勿重复探）

- **端口已在位**：`capability.ObjectStore`（Put/Get/Stat/List/Delete），键契约注释在 ports.go（正斜杠分层、禁穿越、adapter 是钳制执法者）。哨兵 `ErrObjectNotFound`。
- **第一实现**：`internal/providers/localobjectstore`（DataRoot/backups/ 本地目录，零配置在册）。它的 safeKey/原子写/sha256 回执是端口契约的参考实现。
- **消费方已接好**：ADR-0039 备份执行链——数据库 dump 经 ObjectStore 端口写产物（engine 的 backup 执行器）；`assembly` 里 ObjectStore 经 `capability.Build(ctx, KindObjectStore, "")` 装配（localObjectstore 工厂自注册）。**新 S3 Provider 注册进工厂表后，备份轨自动可用它**——前提是装配层有选择机制（当前 Build 取唯一在册实现；多家在册后需要 config 选择，这是设计点①）。
- **config 五元组已存在**：`internal/config/config.proto` 的 `PlatformBackupS3`（endpoint/bucket/prefix/access_key_id/secret_access_key）——目前只喂 restic 外置仓（platform_backup.s3，ADR-0039）。访问器 `PlatformBackupS3()`（endpoint/bucket 空 = nil）。
- **e2e 在位**：`e2e/dind-backup.sh`（四引擎备份演练 + 平台仓 roundtrip，CI e2e-backup job）。
- **「未配异地持续告警」已由 F2.5 承载**：engine 内置规则 platform-offsite-backup（s3 未配 24h → firing；`internal/engine/metrics.go` 的 evaluateSystemRules）+ doctor 两检查。**F2.8 配好 s3 后该规则自动消警**（归位路径已写好，无需改）。

### 5.2 设计决策点（先 ADR 后代码）

1. **config 块形态**：复用 `platform_backup.s3` 五元组 vs 新独立 `object_store` 块（可多目标？）。倾向：v1 复用五元组（备份是唯一消费方；restic 仓与 ObjectStore 指向同一桶时天然一致）——但要 ADR 明说。
2. **RustFS opt-in 自宿**（checklist 原文有）：受管自宿范式成熟（zot/VL/VM 三批真源，ADR-0040/0041；关键件：FacesOf 子面协商/材料字节稳定/密码绝不进 argv/`SkipMaterials` 退出面/带卷自动钉控制面节点）。评估 v1 是否只做外置 S3、RustFS 挂账——建议只做外置（用户可先接现有 MinIO/云 S3），RustFS 走后续批（挂账理由：又一受管组件全生命周期成本）。
3. **凭证落点**：secret_access_key 是凭证材料。两个仓内先例：hook secret（age 信封入 DB，ADR-0014）vs registry/logging 凭证（keys/ 明文 json 0o600）。config 里的五元组本身已是明文形态（restic 在用）——v1 沿用 config 明文 + ADR 记边界（config 文件权限 0600），或升级信封（改动面大，涉及 config 解密层——不建议本批）。
4. **S3 客户端选型**：go.mod 目前无 AWS SDK。选项：minio-go（轻、S3 兼容生态标准）/ aws-sdk-go-v2（重）。倾向 minio-go，ADR 记理由。注意架构 §10 依赖克制文化。
5. **装配选择机制**：localObjectstore 恒在册（本地备份目标开箱即用，ADR-0020）——S3 在册后 `Build(KindObjectStore, "")` 取哪个？需要 config 驱动的选择（如 object_store.driver = local|s3）或 name 传参。这是本批真正的装配层改动。

### 5.3 实施要点

- 键契约执法：S3 adapter 侧 safeKey 同款钳制（端口不信任调用方——穿越是对象存储第一攻击面）。
- Put 流式 sha256 回执、Stat/List 元数据、Delete 幂等——对着 localObjectstore 的行为语义写（备份 verify 锚依赖回执 digest）。
- 覆盖现有 localObjectstore 测试形态：键穿越拒绝、原子性、幂等删、digest 正确性。
- e2e：dind 里起 minio（单容器）做假 S3 端点，跑 dind-backup.sh 同款演练对 S3 目标——或至少 apitest 级 fake。CI 网络可拉镜像（VM/cadvisor 都在拉）。
- **验收锚建议**：①配置 s3 后 `databases backup` 产物落 S3 且 verify 绿；②内置规则消警（alerts list 里 platform-offsite-backup → ok）+ doctor 消警；③未配置时行为与现状零差（升级零扰动）；④e2e S3 演练进 CI。
- ADR 编号续接：下一个是 **0042**（模板看 docs/adr/ 近期件；含"验收锚"小节——可测承诺逐条勾选）。

### 5.4 收官流程

1. 全套门禁绿（§3）。
2. checklist F2.8 标 [x]（带实录注记，样式仿 F2.4/F2.5 条目）。
3. runbook 回写（staging 换装实录 + 新端口/边界）。
4. push → CI 六 job 绿。
5. （可选，若有 staging 访问权）换装验证：**必须先读 `docs/runbooks/staging-fleetly.md` 的"平台升级操作序"**——前置 platform backup + 手工卷 tar，再换二进制；换装二进制要带版本 `-ldflags "-X main.version=<commit>-<tag>"`。若无访问权：CI 绿 + 本地门禁即为交付标准，staging 换装留给仓库所有者。

## 6. 已裁决勿重开（防止"好心"翻案）

- 不内置平台 DNS；canary 不做（P5 词条）；alias 通道 N2 不动；不做 MCP。
- 数据面停止 stop-first + 60s grace（勿改 start-first）。
- 受管域凭证密码绝不进 argv（swarm service argv 集群可 inspect）。
- 平台查询构造执法行级隔离（VL/VM 单租户是裁决，不是缺陷）。
- 日志检索双径（无 text=实时路径字节级保持）；指标采集 = engine 集中 scrape（非 vmalert/Alertmanager）。
- goose 只前滚，无 Down。

## 7. 挂账清单（存在但勿强捆进本批）

networks 重建动词（pre-F2.2 项目网不 attachable）；1680 匿名卷清扫；cadvisor 8080 多租户前收窄；host 端口全局服务滚动序（旧 task 占位→`docker service rm`+reconcile 愈）；QuerySeries 多序列（Console 批）；docker 日志轮转卫生批。

## 8. 队列（F2.8 之后）

F2.9 两级变量（ADR-0027 收口）→ F2.6 最小 Console（React+Vite 三页）→ F2.7 dbtemplate 目录化 → N3（P5 蓝绿+P15+P16 别名定稿）→ N4（k3s Provider）。

---

*敏感信息说明：本文档不含 token/密码。staging 地址与端口表本身在仓内 runbook（非秘密）。*
