# R03 engine 构建/材料/intake 评审报告

> 评审者：r03-engine-build｜基线：`D:\Codes\qiulin\fleetly` main @ `35257f18`（2026-10-19）
> 只读评审：未改动仓库任何文件；行为验证经 `go test -overlay` 把夹具注入仓外文件
> （`D:\Codes\qiulin\fleetly-review-2026-10-19\_tmp_r03_*`，不落仓库），下文凡标
> 「实测」的结论均来自这些 overlay 用例（见 §1.3 复验命令）。

## 1. 覆盖清单

### 1.1 完整读过（生产文件）
- `internal/application/composeintake/intake.go`（1167 行，全文）
- `internal/application/apptemplate/apptemplate.go`、`render.go`、`catalog.go`、`catalog/*.yaml`（全文）
- `internal/application/schema/schema.go`、`reflect.go`、`proto.go`、`specs.go`（全文）
- `internal/application/transact/transact.go`（全文）
- `internal/application/engine/`：`buildsource.go`、`builder.go`、`buildlog.go`、`materials.go`、
  `gitmaterial.go`、`render.go`、`registrycontent.go`、`console.go`、`deployspec.go`（全文）
- 为核对冻结链/调用方补读：`engine/lifecycle.go`、`projection.go`（1-230）、`transition.go`（330-397）、
  `predeploy.go`（150-310）、`loop.go`、`construct.go`（相关段）
- 调用方：`interfaces/grpc/fleetlygrpc/delivery.go`（1-280）、`webhook.go`（100-375）、
  `templates.go`（全文）、`githubapps.go`（200-500）、`registry.go`（全文）
- 相邻实现/契约：`infrastructure/github/client.go`、`persistence/githubapp/repo.go`、
  `infrastructure/providers/zot/content.go`、`infrastructure/upload/extract.go`、
  `domain/delivery/revision/revision.go`、`domain/delivery/build/build.go`、
  `domain/shared/spec/{merge,variables}.go`、`domain/shared/capability/{ports,faces}.go`（相关段）、
  `persistence/build/repo.go`、`persistence/migrations/{00030_github_apps,00031_revision_source_text}.sql`
- 契约/文档对照：`proto/fleetly/spec/v1/spec.proto`（BuildSpec/JobSpec/ProcessSpec 段）、
  `docs/adr/0072`、`docs/adr/0074`、`docs/adr/0050`、`docs/reference/compose-dialect.md`、
  `docs/runbooks/staging-fleetly.md`（构建失败语义段）

### 1.2 抽样读过（测试与 golden）
- 全读：`composeintake/intake_test.go`、`apptemplate/apptemplate_test.go`、`schema/schema_test.go`、
  `engine/{builder_test,buildroute_test,buildsource_test,materials_test,logging_test,crashrecovery_test,deployspec_test,gitmaterial_test,render_test,console_test,registrycontent_test}.go`
- golden/schema 面：`cmd/fleetly/cmd/testdata/golden/apps-spec-json.golden`、`explain-json.golden`、
  `internal/assembly/schema_gate_test.go`（读）、`apitest/rest_deployread_test.go`（source_text 锚）
- compose-go 上游语义核对（module cache v2.15.0）：`template/template.go`、`types/command.go`、
  `types/types.go`、`loader/loader.go`、`loader/validate.go`

### 1.3 实测（只读命令）
- `go test ./internal/application/{composeintake,apptemplate,schema,transact}/` → 全绿
- `go test ./internal/application/engine/` → ok（24s）
- overlay 行为验证（`-run TestR03`，全 PASS，日志即证据）：
  `go test -overlay=...\_tmp_r03_intake_overlay.json ./internal/application/composeintake/ -run TestR03 -v`
  `go test -overlay=...\_tmp_r03_engine_overlay.json ./internal/application/engine/ -run TestR03 -v`
  覆盖：command `$$`、replicas:0、network/config `name` 覆盖、job secret target、healthcheck `[CMD]`、
  build inputs 常驻、cache_from 丢弃、registry tags 路径注入、删除变量后的 material 合并、git 上下文完成判据。

### 1.4 未覆盖/无法覆盖
- 构建执行面（BuildKit/push/railpack/static 实现）属 R07；本报告只评 engine 侧编排与端口映射。
- e2e dind 套件（Docker daemon 本机不可达，PROFILE §1.1）——未跑，不声称真机复验。
- Console/CLI 对 compose/模板/schema 的消费面（R10/R11）仅按调用点核对。
- 历史挂账真实性抽样归 R15；ADR 声明的系统性核实由 R15 负责，本报告只报与 R03 代码直接冲突处。

## 2. 模块画像与边界

R03 是"声明 → 冻结 → 材料 → 构建"链的 engine/application 半边：

- **受理与归一化**：`composeintake.FromYAML` 是 compose 受理唯一解析器（delivery Deploy、webhook git 批、
  模板 dryRender 三处消费），把 compose-go 官方解析产物执法为 `specv1.AppSpec` 并做叶子校验。
- **冻结咽喉**：`engine.SubmitSpec`（`deployspec.go`）单点承载"所有权合并（MergeHookOwned 双向）→
  三级变量合成（Shared < 源 env < AppVariable）→ protojson 内容寻址冻结（复用零写）→ admission"。
  webhook push / refresh / Deploy 三入口共用（ADR-0072 追记、ADR-0074 追记）。
- **材料面**：`resolveMaterials`（Secret 值 age 解封 → 文件材料；`registry:<host>` 凭证；`config_mounts`
  按名取当前版本）；`gitmaterial.go` 解析 Project→Global→`git:<host>` Secret 三级材料并铸 installation token。
- **构建链**：`driveBuilding`（Revision 级一次构建/产物 digest 复用）→ `buildStep/executeBuild`
  （queued→building→终态 CAS 四件一拍、并发上限、15m 超时、停机回 queued、日志脱敏单出口）→
  上下文准备（git 检出 / upload 解包）→ `builders` 端口按 Builder 名路由。
- **App 模板**：`apptemplate` 叶子（解析 fail-closed / 三型变量渲染 / secret 值永不进 Spec / external
  声明铸位 / 目录 digest + 全量预校验），实例化编排在 api 层复用既有 Deploy 链。
- **schema/explain**：`schema` 包以 proto 反射生成 IR JSON Schema、以 Go 反射生成事件 payload schema，
  注册表 + golden 漂移门（`internal/assembly/schema_gate_test.go`）。
- **事务**：`transact` 是 ADR-0080 显式 UoW：Stores（全聚合 tx 绑定）+ `Do` 单入口，Binder 由组合根注入。

依赖方向与 ADR-0075 一致：composeintake → {compose-go, domain/spec}；engine → domain + capability；
schema/transact 叶子（schema 只依赖 genproto+stdlib）。与架构文档的偏差主要在三处（均已列入发现）：
compose 方言文档与代码漂移（L3）、`source_text` 承诺在 git 批 compose 路径未覆盖（M11）、
ADR-0072 决策 6 的"删除 store 键回落 intake 值"在 material 方向不成立（M12）。

## 3. 优点（带证据）

### 3.1 官方插值的 fail-closed 预检包得很准（ADR-0074 决策 5）
`intake.go:364-395`：在官方 `Substitute` 槽外层用 `bracedHoleRe/shortHoleRe` 预扫，未消解裸孔直接
拒绝（官方缺省是"警告+空串"），并用 `escaped()` 的奇偶 `$` 计数正确放行 `$${VAR}`。对 `${VAR:-default}`
（带修饰符、自带回退）不误拦，语义边界与 compose-spec 对齐。

### 3.2 反射白名单把"上游新增字段默认拒"落成机制
`intake.go:649-681` 在 typed 产物上按 yaml tag 反射执法，白名单只增；顶层闭集在
`LoadModelWithContext` 与 `ModelToProject` 两阶段之间执法（`intake.go:136-152`，因为 `x-` 键会被挪进
`#extensions`）。嵌套块（deploy/healthcheck/port/network/volume/secret/job）各自单列扩展通道。
测试 `TestFromYAMLRejections` 40+ 条否定矩阵逐条钉住"静默丢即静默失败"的验收锚。

### 3.3 GitHub App 材料面是"私钥只写不读"的构造级执法
`persistence/githubapp/repo.go:86-99,176-180`：读面 SQL 投影 `NULL` 掉 `private_key_ciphertext`（不是
应用层过滤）；`githubapps.go:388-396` 投影只回 fingerprint；`github/client.go:114-152` installation token
缓存键 = EntityID+KeyFingerprint（轮换即失效），token/私钥不进错误文本；`buildsource.go:397-432` clone
argv 双防线（`protocol.ext/file.allow=never` + `--` 终结）并有针缝负例测试（`buildsource_test.go:12-54`）。

### 3.4 构建日志脱敏是单出口 + 有序替换 + 持久化/非持久化同纪律
`buildlog.go:198-212`：`buildLogWriter` 是 builder 的唯一 writer 实参，脱敏（值→`secret:<sha256前8>`）
先于环形缓冲与 VL ingest；`newRedactTable` 按值长降序替换（`buildlog.go:159-172`）；
`logging_test.go:102-165` 同时断言 live 缓冲与 VL 批次零原文、`logging_test.go:167-193` 断言 Logging
面缺席时脱敏仍在。

### 3.5 构建崩溃/孤儿恢复是按形态分类钉死的
`builder.go:80-102`（无登记 queued 行：归属活跃则等重建、归属终态则一跳 cancelled，禁 queued↔building
振荡）、`builder.go:122-172`（无输入即终态 cancelled、未知 builder 精确失败）、`lifecycle.go:97-115`
（building 行启动重置回 queued）、`lifecycle.go:122-141`（取消 → wg 有界排水）；`crashrecovery_test.go`
三类形态（在途重启/取消后孤儿/硬杀遗留）逐一有界断言。

### 3.6 冻结咽喉的内容寻址复用确为"零写零事实"
`deployspec.go:194-235`：`FindByDigest` 命中即 `errContentReused` 回滚（不落新 Revision、不落审计），
`deployspec_test.go:50-66` 断言同 Spec 两次受理单行 Revision + 单条 `revision.create` 审计；
digest 序列化参数（protojson `EmitUnpopulated:false + UseProtoNames:true`）在文件头声明为跨面铁律。

## 4. 发现清单

> 未发现 H 级：最接近 H 的两条是 R03-M13（多租户查询面，取决于 zot 上游路径归一，标注待复核）
> 与 R03-M2（崩溃/超时后可构建出错误源码的静默错误产物）。全部 M 均给出校准理由。
> L 级单独列表（§4.14-§4.19）。

### R03-M1｜构建输入登记表 `e.build.inputs` 永不删除——无界增长且常驻推送凭证明文
- 维度：安全 / 可靠性
- 严重度：M（进程内无界 map + 凭证常驻内存；单进程长期运行会持续增长，凭证生命周期与进程等长）
- 位置：`internal/application/engine/buildsource.go:106-116`、`:155-162`；读点 `builder.go:133-136`；
  初始化 `construct.go:81`（全仓无任何 `delete(e.build.inputs, ...)`，已 grep 确认）
- 证据：
  ```go
  // buildsource.go:111-115（登记；此后无回收点）
  buildID := entityid.Derive(d.AppID, "b", bseq)
  input.BuildID = buildID
  e.build.inputsMu.Lock()
  e.build.inputs[buildID] = input
  e.build.inputsMu.Unlock()
  ```
  实测（overlay `TestR03BuildInputRetainedAfterSuccess`）：Build 行落 `succeeded` 后
  `e.build.inputs[b.ID]` 仍在，且条目携带 `PushCred.Secret`：
  `terminal build input retained in memory: true (map size 1, entry carries PushCred.Secret)`。
- 影响：每次构建（git/upload 源）向 map 写入一条含 `ContextDir`、`PushCred`（per-Project registry
  密码明文）、未来 `SecretFiles` 的请求体，永不释放；10k 次构建即 10k 条凭证明文常驻堆。tx 提交失败时
  登记也已写入（登记在 `e.tx.Do` 内、行提交前），失败路径同样泄漏。无观测面（无 size 指标）。
- 建议：终态迁移时（或 `executeBuild` defer）删除登记；tx 失败回滚时删除；`builds.inputs` 增加
  上限/年龄断言。成本 S。建议附带一条"terminal 后登记必须清空"的白盒测试（当前 crashrecovery
  只断言行为，未断言内存）。
- 状态：已亲验（代码 + overlay 实测）

### R03-M2｜git 构建上下文以"目录存在"判完成——clone 超时/被杀留下的半截目录被当作有效源码
- 维度：可靠性 / 正确性（构建产物来源完整性）
- 严重度：M（触发需 clone 中断，但后果是"静默构建错误源码"且重试无法自愈；校准不升 H 因需特定中断窗口）
- 位置：`internal/application/engine/buildsource.go:188-201`（存在即跳过 clone）、`:353-375`（clone 直写最终目录 + 5m 超时）
- 证据：
  ```go
  // buildsource.go:189-200
  dir := filepath.Join(e.opts.Data.DataRoot, "contexts", d.ToRevision)
  if _, err := os.Stat(dir); os.IsNotExist(err) {
      cloneURL, secrets, rerr := e.prepareCloneURL(ctx, projectID, origin.Git.GetRepo())
      ...
      if cerr := e.cloneSource(ctx, cloneURL, origin.Git.GetRef(), dir, secrets); cerr != nil {
  ```
  实测（overlay `TestR03GitContextTrustsExistingDirWithoutMarker`）：预置一个"半截"目录（非 git 仓库、
  无任何完成标记）后 `prepareBuildInput` 直接返回成功、不触发 clone。对比 upload 面用
  `tmp + 原子 rename`（`buildsource.go:326-346`）保证"目录存在=完整"。
- 影响：`cloneSource` 用 `context.WithTimeout(ctx, 5*time.Minute)` 包裹 `exec.CommandContext`
  （`buildsource.go:353-355`），超时/进程退出时 Go 以 SIGKILL 杀 git——git 的退出清理不会执行，目标目录
  残留半截 `.git` 与部分 worktree；同 Revision 下一次部署（或重启后的 `driveBuilding`）看到目录存在即
  跳过 clone，用残缺树构建。若 Dockerfile 恰好已落盘，构建可"成功"出错误产物；若缺文件则构建失败，
  而此时 Revision 已被终态 Build 粘滞（M4），无法靠重试恢复。checkout 失败（`buildsource.go:367-373`）
  同样留下默认分支工作树。
- 建议：与 upload 同纪律——clone 到 `contexts/<rev>.cloning` + 成功后原子 rename（或写 `.ready` 标记，
  存在标记才跳过）；失败路径 RemoveAll；上下文目录加"必须是 git 仓库且 ref 匹配"校验。成本 M。
- 状态：已亲验（代码路径 + 完成判据实测；"SIGKILL 不清理"为 git/Go 标准行为，未重新真机复现）

### R03-M3｜`contexts/<revision>` 无任何保留/清扫策略——数据根随构建次数无界增长
- 维度：可靠性（容量）/ 可观测性
- 严重度：M（长期运行磁盘无界增长；数据根与 SQLite/blobs 同盘，写满会击穿控制面；校准：当前体量小、短期不爆）
- 位置：写点 `buildsource.go:189`（git）、`:317-321`（upload）；全仓 grep 仅这些写点与测试引用；
  retention janitor 的清扫清单 `internal/assembly/provides.go:741-780`（idempotency/outbox/upload
  tmp/upload 行/受管 Secret/卷/peer grant/Task 载体——**无 contexts**）
- 证据：
  ```go
  // buildsource.go:305-308 / :317
  // extractUploadContext ... 原子 rename 到 contexts/<revision>（幂等：目标已存在跳过）
  dir := filepath.Join(e.opts.Data.DataRoot, "contexts", revisionID)
  ```
  ```go
  // provides.go:752-757：janitor 每拍清扫面（无构建上下文）
  s.sweepUploads(ctx)
  if n, err := s.blobs.SweepOrphanTmp(uploadTmpAge); err != nil { ... }
  ```
- 影响：Revision 行不可变、不删（`build/build.go:30` "构建记录永不删"；revision 无 UPDATE/DELETE 路径），
  每个曾构建的 Revision 永久占一份完整源码树（git 全量 clone + upload 解包）。长期运行的 git 应用
  每次新 commit 部署即多一份仓库副本，且无指标/告警/额度。清理与回滚/重放语义有张力（重放旧
  Revision 需要上下文），必须显式策略而不能简单按龄删。
- 建议：设计保留策略（如"仅保留最新成功部署链 + 最近 N 个 Revision 的上下文，清扫前校验无
  queued/building Build 引用"），挂到 retention janitor 新面；同时加数据根用量指标/doctor 项。
  成本 M-L。机制层建议：engine 暴露 `SweepBuildContexts`（与 `SweepTerminalTaskCarriers` 同构）。
- 状态：已亲验（写点 + 清扫面清单 + 无消费方 grep）

### R03-M4｜Build 终态对 Revision 粘滞：取消/超时/失败后同一 Revision 永久不可再部署
- 维度：可靠性 / 可维护性
- 严重度：M（可复现的用户流程死角：cancel 后重部署会继承 cancelled；无 API 重试通道；校准：failed 形态
  已被 runbook 记为"Revision 一次性语义"，但 cancelled/expired 的继承是孤儿回收副作用，未见记载）
- 位置：`internal/application/engine/buildsource.go:72-92`（终态即 failDeployment）、
  `internal/infrastructure/persistence/build/repo.go:79-80`（"历史行应只有一条"）；孤儿回收制造
  cancelled 行：`builder.go:96-101`；超时制造 expired：`builder.go:196-201`
- 证据：
  ```go
  // buildsource.go:76-91
  for _, b := range existing {
      switch b.State {
      case build.StateSucceeded:
          return e.transitAndReload(...)   // 复用
      case build.StateQueued, build.StateBuilding:
          ...
      default:
          // 终态失败（failed/cancelled/expired）→ 部署失败。
          return e.failDeployment(ctx, d, fmt.Sprintf("build %s: %s (%s)", b.ID, b.State, b.Error))
      }
  }
  ```
  runbook 已记录同类语义（`docs/runbooks/staging-fleetly.md:220`："失败 Build 行按 Revision 一次性语义
  使同内容重试直接继承失败——需换内容铸新 Revision 才会真重推"）。
- 影响：用户取消一个卡在 building 的部署后，其 queued Build 行会被 `buildPickable` 判孤儿并落
  cancelled；随后对**同一 Revision**（内容寻址复用，改 cli flags 重发不会产生新 Revision）再次部署，
  `driveBuilding` 命中 cancelled 行直接失败，文案为 `build <id>: cancelled (...)`——用户既无法重试也
  得不到"改内容"的处置提示。构建因瞬时故障失败/超时同理（runbook 只把 failed 形态当已知边界，未覆盖
  cancelled/expired，也无 CLI/Console 指引）。
- 建议：a) 短路径：终态为 cancelled/expired 时允许在同一 Revision 上重新铸 Build 行（行不删、seq 递增，
  复用语义改为"最新 succeeded 或存在活跃行才复用"）；b) 或至少 failDeployment 文案带处置提示
  （"re-deploy with a changed spec to mint a new revision / cancel then retry..."）。成本 S-M。
- 状态：已亲验（代码路径；cancelled 继承未写行为测试）

### R03-M5｜compose `command` 的 `$$`（compose 标准转义）被二次转义，迁移文件会静默执行错误命令
- 维度：功能（保真）
- 严重度：M（迁移面文件静默行为变化；与同一方言文档宣告的 `$$` 转义语义冲突；校准：仅影响 command 面）
- 位置：`internal/application/composeintake/intake.go:229-278`（`escapeCommandDollars`）、
  `docs/reference/compose-dialect.md:50-55`（插值律）；测试盲区 `intake_test.go:491-551`
- 证据：
  ```go
  // intake.go:242-253（对所有 $ 无差别加倍，含用户已写的 compose 转义 $$）
  switch t := v.(type) {
  case string:
      return strings.ReplaceAll(t, "$", "$$")
  case []any:
  ```
  compose-go 的 `template.Substitute` 对 `$$` 输出**单个** `$`（module cache `template/template.go`,
  `DefaultReplacementAppliedFunc` escaped 组），故 `$`→`$$` 预处理对**单个** `$` 是"还原原文"，
  但对用户已写的 `$$` 是"再逃一次"。
  实测（overlay `TestR03CommandDollarEscapeObserved`）：
  `command: ["sh","-c","echo $$PATH"]` → IR `[]string{"sh","-c","echo $$PATH"}`（预期 `$PATH`）；
  `command: ["sh","-c","echo $PATH"]` → IR `$PATH`（透传，符合设计）。
- 影响：把 Docker Compose 文件直接迁到 fleetly（dokploy 模板仓、git 批 compose、模板库的主要输入形态）
  时，command 里按 compose 规范写 `$$VAR` 的脚本会以 `$$VAR` 进 IR；经 shell 解释时 `$$` 是 PID，
  结果为 `PIDVAR`——静默错误行为，无受理错误。healthcheck CMD-SHELL 未被预逃逸（`$` 会走 fail-closed
  拒绝），两面口径不一致。既有测试只覆盖"不转义 `$`"，未覆盖 `$$`。
- 建议：预逃逸改为"只对奇数个连续 `$` 加倍"（或先把 `$$` 占位、加倍余下 `$`、还原占位），确保
  compose 转义语义保真；补 `$$`/`$${VAR}`/`$${VAR:-x}` command 正例。成本 S-M。
- 状态：已亲验（overlay 实测）

### R03-M6｜`deploy.replicas: 0` 被静默归一为 1（声明的期望态被丢弃）
- 维度：功能（保真/静默补默认值）
- 严重度：M（"声明 0 副本"是合法 compose 值，平台静默按 1 副本运行；校准：无错误、无日志、无审计）
- 位置：`internal/application/composeintake/intake.go:599-605`（只处理 >0）；默认值
  `intake.go:541`（`p := &specv1.ProcessSpec{Name: name, Replicas: 1}`）
- 证据：
  ```go
  // intake.go:603-605
  if svc.Deploy.Replicas != nil && *svc.Deploy.Replicas > 0 {
      p.Replicas = int64(*svc.Deploy.Replicas) //nolint:gosec // 副本计数域内
  }
  ```
  实测（overlay `TestR03ReplicasZeroObserved`）：`deploy: {replicas: 0}` → `replicas = 1`。
- 影响：用户用 compose 表达"该服务暂时缩零"（Compose 允许 0）时，fleetly 静默跑 1 副本：多占资源、
  可能是错误的对外服务行为（例如本应停用的 worker 继续消费）。负值被 `ValidateProcess` 拒（
  `spec.go:357-359`），0 恰好落进"未声明"分支。
- 建议：显式决策——若不支持缩零，受理期拒收并说明（"replicas: 0 is not declarable; remove the
  service or declare 1"）；若支持，则把 0 语义贯通到 IR/投影（当前 IR 0=缺省=1，需引入显式表达或
  禁止）。成本 S。
- 状态：已亲验（overlay 实测）

### R03-M7｜label 方言 job 的 secrets 长语法静默丢 target/uid/gid/mode（与服务面/顶层 job 面不一致）
- 维度：安全 / 功能（静默丢字段）
- 严重度：M（安全相关挂载选项被无声忽略；同一文件写成 service 或顶层 job 会精确拒绝，行为按方言分叉）
- 位置：`internal/application/engine/../composeintake/intake.go:490-498`（label 方言 job 直接取
  `sr.Source`）；对照拒绝面：`:939-961`（service secrets 拒 target 重映射/uid/gid/mode）、
  `:1103-1113`（顶层 job secrets 同拒）
- 证据：
  ```go
  // intake.go:492-498
  // secrets：typed 面直接取名（external 声明律同服务面；原始 any 形态
  // 的 jobSecretRefs 不吃 typed 切片）。
  if len(svc.Secrets) > 0 {
      secretRefs := make([]string, 0, len(svc.Secrets))
      for _, sr := range svc.Secrets {
          secretRefs = append(secretRefs, sr.Source)
      }
  ```
  实测（overlay `TestR03JobDialectSecretTargetObserved`）：`labels: {fleetly.job: init}` 服务 +
  `secrets: [{source: s, target: /etc/secret}]` → 无错误，IR `secret_refs=["s"]`（target 丢弃）；
  同形输入写在长驻服务上 → `secret "s": target remapping is not supported`。
- 影响：作业以为会把材料挂到自定义路径（如 `/etc/mysql/init/x.sql`），实际落在 `/run/secrets/<name>`；
  job 读到空路径失败或读到错误位置，而受理面没有任何信号。uid/gid/mode 同理静默失效。
- 建议：label 方言 job 复用 `serviceSecrets(jobSecrets...)` 或 `jobSecretRefs` 的长语法执法
  （target 只认缺省、uid/gid/mode 拒收）。成本 S。测试盲区：`TestFromYAMLJobDialectRejections`
  覆盖了官方面与扩展键，未覆盖 secrets 长语法。
- 状态：已亲验（overlay 实测）

### R03-M8｜external 声明的 `name` 覆盖只对 volumes/secrets 生效：networks（与 configs 声明字段）被静默忽略
- 维度：功能（保真 / 静默丢字段）
- 严重度：M（同一"external 声明律"的合法官方面在实体类型间行为不一致；代码注释明示 name 覆盖合法）
- 位置：`internal/application/composeintake/intake.go:397-429`（externalDeclarations 放行
  `name`/`external` 两键，注释"name 覆盖形态合法（声明键 → 平台实体名）"）；networks 消费面
  `:884-903`（只回名单，未解析 `decl.Name`）；对照 volumes `:928-932`、secrets `:955-959`；
  configs 声明 `:153-169`（只校验 external bool，无 name 解析、无其余字段拒绝）
- 证据：
  ```go
  // intake.go:897-903（network 分配只取 map 键）
  out := make([]string, 0, len(nets))
  for name := range nets {
      out = append(out, name)
  }
  sort.Strings(out)
  ```
  实测（overlay `TestR03NetworkNameOverrideObserved`）：`networks: {back: {external: true, name: real-net}}`
  → IR networks `["back"]`（预期 `real-net`）。`TestR03ConfigNameOverrideObserved`：configs 声明
  `name: real-config` 被忽略，config mount 仍引用声明键 `appconf`。
- 影响：从 Docker Compose 迁移的栈常用 `external: true + name:` 引用已存在的平台实体名；network/config
  面会按逻辑键去解析，部署期以"网络/配置不存在"失败（错误指向错误的名字），或挂到错误的同名网络。
  configs 声明里的 `labels/driver/driver_opts/extensions` 等也完全不执法（静默丢）。
- 建议：`serviceNetworks` 解析 `project.Networks[decl].Name`（与 volume/secret 同款）；configs 声明
  走 `rejectUnsupportedFields` 白名单（external/name 之外拒收）并在 config mount 解析 `Name` 覆盖；
  补两条正例测试。成本 S。
- 状态：已亲验（overlay 实测）

### R03-M9｜`BuildSpec.cache_from` 是"有契约、有校验、无消费方"的死字段
- 维度：功能 / 可维护性（IR 契约保真）
- 严重度：M（IR/proto 对外宣告字段，受理校验会拒绝空串（给用户"该字段生效"的暗示），实际构建完全忽略）
- 位置：`internal/application/engine/buildsource.go:220-241`（BuildRequest 组装未含 CacheFrom）；
  端口定义有字段 `internal/domain/shared/capability/ports.go:40-41`；IR 字段/校验
  `proto/fleetly/spec/v1/spec.proto:194`、`internal/domain/shared/spec/spec.go:246-250`；
  全仓 grep `CacheFrom|cache_from` 仅：genproto、spec 校验、capability 定义——**无生产读取点**
- 证据：
  ```go
  // buildsource.go:229-240（分派三 strategy，未拷 CacheFrom）
  switch strategy := spec.GetBuild().GetStrategy().(type) {
  case *specv1.BuildSpec_Railpack:
      input.Railpack = &capability.RailpackInput{PinnedVersion: strategy.Railpack.GetPinnedVersion()}
  case *specv1.BuildSpec_Static:
      input.Static = &capability.StaticInput{OutputDir: outputDir}
  default:
      input.Dockerfile = spec.GetBuild().GetDockerfile()
  }
  ```
  实测（overlay `TestR03BuildCacheFromDropped`）：`spec.Build.CacheFrom=["reg.test:5000/base:r1"]` →
  `input.CacheFrom=[]`。
- 影响：spec_file/CLI 用户声明缓存来源（ADR-0032 面），受理与冻结都成功，构建却完全不用——"声明了
  没效果"，且 golden/schema 面把该字段当正式契约展示（Agent 会照着写）。属"静默无操作字段"。
- 建议：二选一——a) 在 `prepareBuildInput` 映射 `CacheFrom: spec.GetBuild().GetCacheFrom()` 并由
  builders 消费（R07 同步）；b) 若本批不支持，从 IR/proto/校验中删除该字段或受理期显式拒收。成本 S。
  机制层建议加守卫：枚举 BuildSpec 字段 ↔ `prepareBuildInput` 覆盖表（防第二例）。
- 状态：已亲验（代码 grep + overlay 实测）

### R03-M10｜schema/explain 反射面把 int64/uint64 声明为 `integer`，protojson 实际输出字符串
- 维度：架构一致性 / 文档一致性（Agent 自描述面）
- 严重度：M（"schema 描述的就是线上字节形状"的承诺被违反；ADR-0026 形状钉扎面失真；校准：输入侧
  protojson 两种都收，但读侧/生成侧会错）
- 位置：`internal/application/schema/proto.go:79-83`（所有 int64/uint64 家族 → `{"type":"integer"}`）；
  运行证据 `internal/assembly` golden：`cmd/fleetly/cmd/testdata/golden/apps-spec-json.golden:16`
- 证据：
  ```go
  // proto.go:79-83
  case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind,
      protoreflect.Sint64Kind, protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind,
      protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind,
      protoreflect.Fixed64Kind:
      return map[string]any{"type": "integer"}
  ```
  `apps-spec-json.golden:16` 实际输出：`"replicas": "1"`（int64 → JSON 字符串，proto3 JSON 映射）。
  `explain-json.golden` 中同一字段的 schema 是 `"replicas":{"type":"integer"}`（另 `cpu_millis`、
  `memory_mb`、`ttl_seconds`、`desired_concurrency` 同病）。
- 影响：`fleetly explain`/schema 是 Agent 的"零文档发现面"（架构 §7 porter 模式）：Agent 读到 integer
  后按数字生成/比较，而 CLI `--json` 读回是字符串——类型判断、diff、回填会出现系统性错配；schema
  golden 漂移门只钉字段集不改类型，故 CI 不会红。
- 建议：64 位家族拆出返回 `{"type":"string"}`（或 `oneOf:[string,integer]` 表达 protojson 输入宽容），
  并加一条"schema 类型 ↔ protojson.Marshal 实际类型"一致性测试（对每个注册 Spec 抽样 int64 字段）。
  成本 S。
- 状态：已亲验（代码 + golden 实测对照）

### R03-M11｜git 批 compose 摄取不冻结 `source_text`——compose 来源原文在读面缺失
- 维度：功能（保真）/ 文档一致性
- 严重度：M（ADR-0074 决策 7 承诺"compose 源原文冻结"，git 批 compose 是第二个 compose 受理入口；
  校准：git 应用可从 repo+commit 复核，故非数据安全级）
- 位置：`internal/interfaces/grpc/fleetlygrpc/webhook.go:163-172`（SubmitSpec 未带 SourceText）、
  `:311-346`（gitBatchSpec 读 compose 文件 → `composeintake.FromYAML` 返回后把 body 丢弃）；
  冻结点 `internal/application/engine/deployspec.go:212`；参照 `delivery.go:123-125`（api compose 面
  传 `SourceText: req.GetComposeYaml()`）
- 证据：
  ```go
  // webhook.go:168-172
  _, d, adm, err := svc.s.Engine.SubmitSpec(ctx, engine.SubmitSpecRequest{
      AppID: appRow.ID, Spec: appSpec,
      HookOwned: true, Merge: engine.MergeMaterial, // 新材料 + 冻结运行时
      Submit: &engine.SubmitOptions{CommitSHA: commit},
  })
  ...
  // :341（compose 文件 body 用后即弃，无 SourceText 出口）
  out, err := composeintake.FromYAML(body, appRow.ID, appRow.ProjectID, varLookup)
  ```
- 影响：hook 文件字段为 `.yml/.yaml` 的 git 应用，每次 push 冻结的 Revision `source_text` 为空：
  Revision 读面/Console "Compose Source" 卡无原文，排障只能靠仓库+commit 侧回查；ADR-0074 决策 7
  （"compose 源原文，其他源 NULL"）的覆盖缺口，且该路径另有 `MergeMaterial` 方向（frozen material 已是
  旧 compose 推的 spec）使原文更不可追。
- 建议：`gitBatchSpec` 返回 `(spec, sourceText)`，`ingestGitMaterial` 透传 `SourceText: string(body)`；
  补 webhook 路径 apitest（断言 `source_text` 非空）。成本 S。
- 状态：已亲验（代码；行为未写测试，属覆盖缺口）

### R03-M12｜删除 SharedVariable/AppVariable 后，hook material push 仍沿用冻结值（ADR-0072 决策 6 不成立）
- 维度：功能 / 文档一致性（冻结链语义）
- 严重度：M（变量删除不生效且无信号；"回落 intake 值"的 ADR 承诺在 material 方向不可实现）
- 位置：`internal/application/engine/deployspec.go:134-178`（MergeMaterial 的 runtime 半边取冻结体，
  变量合成只做加法）、`internal/domain/shared/spec/merge.go:23-27`、`variables.go:43-88`；
  ADR 承诺 `docs/adr/0072-spec-ownership-split-and-app-variables.md:20`（决策 6："运行时半边的 env
  条目是'中间层'——AppVariable 同键覆盖之，删除 store 键则回落 intake 值"）
- 证据：
  ```go
  // deployspec.go:146-150（material 方向：运行时半边整份取冻结体）
  if req.Merge == MergeOverlay {
      req.Spec = spec.MergeHookOwned(frozen, req.Spec)
  } else {
      req.Spec = spec.MergeHookOwned(req.Spec, frozen)
  }
  // 随后 MergeSharedEnv/MergeAppEnv 只覆盖同键、不删除已不在 store 的键
  ```
  实测（overlay `TestR03DeletedVariablePersistsOnMaterialMerge`）：SharedVariable `LOG_LEVEL=v1` 冻结后
  软删；再次 `MergeMaterial` 冻结：`frozen env LOG_LEVEL="v1"`（未回落、未消失）。
- 影响：git 应用的运行时 env 在每次 push 时被"重冻"为含旧变量值的扁平 env；删除 AppVariable/共享变量
  永远不会生效（除非用户用 spec_file overlay 显式清键或改内容铸新 Revision），且控制台看不到残留来源。
- 建议：机制层——冻结体需要"变量来源账本"（哪些键来自 store 层），material 合并时按当前 store 重算这
  一层；或至少把冻结的 store 层 env 单独存字段（不进扁平的 process.env），合并时替换而非叠加。
  若不改机制，则修订 ADR-0072 决策 6 的措辞并在 Console 提示"删除变量需要 overlay/新 Revision"。
  成本 M-L（机制） / S（文档与提示兜底）。
- 状态：已亲验（行为实测；"是否接受为设计边界"待用户/ADR 裁决）

### R03-M13｜注册表 tags 代理的仓名归属校验只做前缀匹配：路径段注入面未拒（跨项目元数据探测）
- 维度：安全（多租户隔离面）
- 严重度：M（跨租户**元数据**读取/存在性探测；当前部署 per-Project 凭证应为 zot accessControl 兜底，
  故未定 H；校准见待复核项）
- 位置：`internal/application/engine/registrycontent.go:43-52`（校验仅
  `strings.HasPrefix(strings.ToLower(repository), prefix)`）；上游拼接
  `internal/infrastructure/providers/zot/content.go:51-58`（`"/v2/"+repository+"/tags/list"` 直接拼 URL，
  无转义/段校验）；入口校验缺口 `proto/fleetly/delivery/v1/registry.proto:71-78`（repository 仅非空）
- 证据：
  ```go
  // registrycontent.go:43-52
  func (e *Engine) RegistryTags(ctx context.Context, projectID, repository string) ([]capability.RegistryTag, error) {
      prefix := buildRepo(projectID, "") // lower(projectID) + "/"
      if repository == "" || !strings.HasPrefix(strings.ToLower(repository), prefix) {
          return nil, state.ErrNotFound
      }
      content, ep, err := e.registryContentFor(ctx, projectID)
      ...
      tags, err := content.Tags(ctx, ep, strings.ToLower(repository))
  ```
  实测（overlay `TestR03RegistryTagsTraversalReachesContent`）：repository
  `01jd0proj..../../other-project/app` 通过归属校验并原样到达 content 面
  （`lastRepo="01jd0proj00000000000000000/../other-project/app"`）。
- 影响：`registrycontent.go:4-7` 自称"前纲过滤与归属校验在本层（上游无服务端过滤）"，但 `..`/`?`/`#`
  等 URL 路径元字符可穿过校验进入上游请求；若 zot 路由对路径做 clean/重定向（gorilla/mux 默认行为）且
  凭证回退到平台凭证（ProjectEndpoints 面缺席或铸造失败时的既有回退，`registrycontent.go:91-100`），
  可读到其他 Project 的仓库 tag/digest/大小/时间等元数据；即使 per-Project 凭证拒绝，返回码差异
  也可能构成跨项目存在性探测。不涉及镜像内容拉取（拉取需 docker 凭证）。
- 建议：本层把 repository 收敛为"平台仓名"：必须匹配 `^<lower(projectID)>/[a-z0-9._-]+$` 且无 `..`
  段、无 `?`/`#`/空白/控制字符（与 secret 名白名单同风格）；proto 层加 CEL pattern；Provider 侧
  `url.PathEscape` 每个段（R07）。成本 S（engine+proto），zot 侧 S。
- 状态：已亲验（engine 层透传实测；上游路径归一/回退凭证可达性待复核 → 见 §7）

### 4.14 R03-L1｜构建终态错误文本未经脱敏（脱敏只覆盖日志出口）
- 维度：安全（纵深一致性）
- 严重度：L（未发现已知把 Secret 值写进 error 的路径；但与 P11"Secret 值零出现"的出口单点纪律不一致）
- 位置：`internal/application/engine/builder.go:196-214`（`m.Error = err.Error()` 直落 Build 行）、
  `internal/application/engine/buildlog.go:3-12`（脱敏链只包 writer）
- 证据：
  ```go
  // builder.go:208-213
  default:
      if _, terr := e.transitBuild(finishCtx, b,
          []build.State{build.StateBuilding}, build.StateFailed,
          func(m *build.Build) { m.Error = err.Error() }); terr != nil {
  ```
- 影响：Build.Error 经 API 读面回显；builders 的错误文本来自 buildkit/docker 上游 `msg.Error.Message`
  （`providers/builders/daemon.go:291`），一旦未来某实现把凭据/材料值带入错误文本，就是脱敏表外的旁路。
- 建议：终态错误同样过 `buildRedactionTable`（`m.Error` 用 `redact` 后的文本）；成本 S。
- 状态：已亲验（代码）；泄漏路径待复核（无已知实例）

### 4.15 R03-L2｜GitHub App 属主匹配大小写敏感，会静默落匿名 clone 并给出误导提示
- 维度：功能（可用性）
- 严重度：L（受理不受影响，部署失败可诊断但提示方向错误）
- 位置：`internal/application/engine/gitmaterial.go:84-99`（`GetByProjectAndOwner(ctx, projectID, owner)`
  精确匹配）、`:134-150`（owner 取 URL 首段原样）；DB 无大小写折叠
  `persistence/migrations/00030_github_apps.sql:23-24`（索引无 `COLLATE NOCASE`）
- 证据：
  ```go
  // gitmaterial.go:84-90
  row, err := e.repos.GitHubApps.GetByProjectAndOwner(ctx, projectID, owner)
  ...
  case !errors.Is(err, state.ErrNotFound):
  ```
- 影响：GitHub 的 login/repo 路径大小写不敏感（API 返回 canonical login 入库），用户以 `Acme/web`
  形态写 repo URL 时查不到 `acme` 行 → 退 `git:<host>` Secret → 再匿名；私有仓 clone 失败提示
  "configure a github app for this owner"，而 App 已配置（只是大小写不同）。
- 建议：owner 比较前 `strings.ToLower` 双侧归一（或 DB 列 `COLLATE NOCASE` + 解析面同归一）；成本 S。
- 状态：已亲验（代码）；未构造真实 GitHub 场景，属静态推断

### 4.16 R03-L3｜方言文档与代码漂移：`docs/reference/compose-dialect.md` 仍把 `configs` 列为未开放
- 维度：文档一致性
- 严重度：L（用户可见的方言契约文档滞后；代码/测试已支持 config external + config_mounts）
- 位置：`docs/reference/compose-dialect.md:57-65`（拒面列表含 `configs`）vs
  `internal/application/composeintake/intake.go:49`（topAllowedKeys 含 configs）、`:153-169`（external 校验）、
  `:509-519`（job configs→config_mounts）、`:581-591`（service configs→config_mounts）；实现批
  `6d279f55`（2026-10-14），文档最后一次改动 `3379d383`（2026-10-13）
- 证据：
  ```markdown
  ## 拒面（官方面未开放，白名单只增）
  受管语义：... 未开放官方面：`build`、`depends_on`、`env_file`、`profiles`、`include`、
  `extends`、`configs`、`container_name`、...
  ```
- 影响：迁移/编辑器/Agent 按文档会把合法 configs 面当拒面删掉；ADR-0074 决策 6 的拒面清单同样滞后
  （归 R15 核）。
- 建议：文档补 configs 收面与 external 形态；成本 S。
- 状态：已亲验

### 4.17 R03-L4｜apptemplate 注释仍引用已删除的 `spec.NormalizeCompose`，且「作者自带 external 声明」分支不可达
- 维度：可维护性 / 文档一致性
- 严重度：L（读者会以为存在一条实际不存在的白名单喉/声明通道）
- 位置：`internal/application/apptemplate/apptemplate.go:7,102,279`（NormalizeCompose 已随 ADR-0074 删除，
  全仓仅注释引用）；`internal/application/apptemplate/render.go:264-302`（"已声明的安全键引用
  （模板作者自带声明）不重写"分支）；对照 `apptemplate.go:113-119` 顶层白名单不含 `secrets/networks`
- 证据：
  ```go
  // apptemplate.go:279-281
  // compose 子集剥离平台键后成为渲染基座；services 在场由 NormalizeCompose
  // 喉执法（此处不重复，坏文档零可用产物的预检面已在顶层键域）。
  ```
- 影响：模板作者无法自带顶层 external 声明（Parse 拒），`declareExternalSecrets` 的 `declared` 恒为
  nil，该分支与"用户自带声明原样保留"的注释是死代码/错误指引。
- 建议：注释改指 `composeintake.FromYAML`；删除或显式注释死分支（若未来打算支持作者声明则补 Parse
  白名单与测试）；成本 S。
- 状态：已亲验

### 4.18 R03-L5｜`healthcheck.test: ["CMD"]`（空探针载荷）被受理，投影为无探针（静默丢）
- 维度：功能（静默丢字段）
- 严重度：L（畸形输入；但受理成功→`[NONE]` 之间无任何信号）
- 位置：`composeintake/intake.go:852-865`（CMD 分支不校验 `words[1:]` 非空）；叶子
  `domain/shared/spec/spec.go:406-421`（exec 非 nil 即算一种 probe，不校验命令非空）；投影
  `engine/projection.go:54-66`（空 argv 直通）→ `providers/swarm/translate.go:504-506`（缺省 `Test=["NONE"]`）
- 证据：
  ```go
  // intake.go:856-857
  case "CMD":
      out.Probe = &specv1.HealthcheckSpec_Exec{Exec: &specv1.ExecProbe{Command: words[1:]}}
  ```
  实测（overlay `TestR03HealthcheckEmptyCMD`）：`test: ["CMD"]` → `err = <nil>`。
- 影响：声明的探针静默消失（swarm 侧 `NONE`），健康门失去该探针；与"静默丢即静默失败"的 intake 纪律
  不符（属畸形但合法的官方 schema 形态）。
- 建议：CMD/CMD-SHELL 载荷为空即拒（"test must carry a command after CMD/CMD-SHELL"）；成本 S。
- 状态：已亲验（overlay 实测）

## 5. 交叉观察（不在本模块，但值得其他模块注意）

1. **R07 / zot content 代理**：`providers/zot/content.go:56,113,160` 用字符串拼接 `/v2/<repository>/...`
  无段转义；若 engine 不收敛仓名（R03-M13），R07 应在 Provider 侧加 `url.PathEscape` 与 dot-segment
   拒绝，并明确"repository 由调用方保证为平台仓名"的契约或双保险。
2. **R08 / proto 契约**：`ListRegistryTagsRequest.repository` 只有 `string.required` CEL
   （`proto/fleetly/delivery/v1/registry.proto:71-78`），无字符集/形态约束；且 HTTP 注解
   `/v1/registry/{repository}/tags` 的单段变量与"repository 形如 `<project>/<app>`（含 `/`）"存在
   REST 可达性张力——建议 R08 用 Console/CLI 实际调用形态核一遍路径编码。
3. **R15 / 文档对账**：`docs/reference/compose-dialect.md` 与 ADR-0074 决策 6 的拒面清单仍含已开放的
   `configs`；ADR-0072 决策 6 的"删除 store 键回落 intake 值"在 material 方向不成立（R03-M12）；
   ADR-0074 决策 9"受理喉唯一 delivery.go"在 git 批 compose 后已不是唯一受理入口（第二个 compose 入口
   webhook.gitBatchSpec，source_text 不冻结）。
4. **R02 / 冻结后未再校验**：`SubmitSpec` 在 MergeHookOwned + 变量合成后直接 protojson 冻结，未重新跑
   `spec.ValidateApp`（校验只在归一化入口）。合并可能把旧 Revision 的存量字段（或跨 schema 版本字段）
   带回冻结体；建议 R02/R13 评估是否需要在冻结前加一次叶子校验（成本一行）。
5. **R07 / BuildRequest 字段**：`BuildRequest.Args/CacheFrom` 当前均无生产写入/读取面；R03-M9 涉及
   `CacheFrom` 的 engine↔builders 双侧契约，若决定实现需两侧同批（含 provider 测试）。
6. **R02/R04 / pre-deploy job TTL 截断**：`predeploy.go:186` 用
   `int64(job.GetTtl().AsDuration().Seconds())` 铸造 `TtlSeconds`，亚秒 TTL（如 `"500ms"`，ValidateJob
   允许）会被截断为 0（Task 语义=无 TTL），而等待窗仍按 500ms 计；属 R02/R04 的字段面，建议核。
7. **R09 / 运维面**：R03-M3 的上下文清理需要 janitor 一个节拍面（`assembly/provides.go` 服务注入
   engine 清扫方法，模式同 `SweepTerminalTaskCarriers`）；同时数据根用量应有 doctor 项。
8. **R12 / 守卫**：R03-M9（IR 字段无消费方）与 R03-M7/M8（allowed key 的嵌套面未执法）是两类可机制化
   的守卫缺口：前者可做"BuildSpec 字段消费表"静态测试，后者可做"白名单键的嵌套官方字段 default 拒"
   表驱动测试（当前反射白名单只覆盖 key 层，不覆盖 key 内部子字段）。

## 6. 测试与守卫质量评估

- **composeintake**：`intake_test.go` 755 行、40+ 否定矩阵 + 幂等/确定性正例，质量在本模块最高；
  缺口集中在"allowed key 的嵌套子字段"与"value 语义"：job secrets 长语法（M7）、replicas:0（M6）、
  network/config name 覆盖（M8）、command `$$`（M5）、healthcheck 空载荷（L5）均无测试。
  建议把这些补成"每个被放行的官方键，其内部每个非零子字段必须被消费或被拒"的表驱动守卫。
- **engine 构建链**：hermetic 测试覆盖好——链路成功/失败、三 strategy 路由、未知 builder、crash-recovery
  三形态、停机排水、无输入终态、日志脱敏双面、材料解析错误传播；`waitFor` 轮询 2s 有界、无 sleep 竞态
  依赖。缺口：无"构建输入登记回收"断言（M1）、无上下文完整性标记测试（M2，`freezeGitBuildSpec`
  恰好预置目录绕开了 clone，等于把 M2 的假设固化进夹具）、无 cache_from 映射测试（M9）、无终态
  Revision 重部署恢复测试（M4）。
- **apptemplate**：Parse 未知字段/渲染三型/fail-closed/目录 digest+全量预校验测试扎实，
  `TestBuiltinCatalogHealthy` 用真实内嵌目录走全链。弱点：secret"值零出现"反扫用
  `strings.Contains(s,"hex")||Contains("password value")` 这类字面量断言（`apptemplate_test.go:174-176`），
  强度弱于 apitest 的 secret 链锚；真实值泄漏也可能不含这些字面量。
- **schema**：确定性/方言/注册期 fail-fast 测试好，golden 漂移门钉字段集；但没有"schema 类型 ↔
  protojson 实际序列化类型"的交叉断言，故 M10 这类"文档面与运行时不符"不会被 CI 抓住。
- **transact**：包内无测试（`[no test files]`），仅靠 engine/apitest 间接覆盖；`Do` 语义薄、可接受，
  但 `Stores` 字段完整性（新增聚合是否都进 Binder）无守卫断言，靠 compile + 使用期 nil 暴露。
- **总体**：该模块的守卫文化体现在 intake 的"否定矩阵"与外部出口的单点化，但对"契约字段是否真的被消费"
  与"白名单键内部子字段"这两类静默失效缺机制；本报告 13 条 M 中有 8 条恰好是这两类，值得作为 R12 的
  守卫补强输入。

## 7. 未决与置信度

**最不确定的 5 点**
1. **R03-M13 的可利用性**：engine 层透传已实测，但"zot 路由是否 clean/301 并跟随"以及"per-Project 凭证
   面缺席时是否会回退平台凭证"未在本机验证（Docker 不可达、无 zot 实例）。若 R07/真机确认路径归一
   生效且存在回退，则升级为 H（跨租户元数据读取）。
2. **R03-M12 的裁决**：行为已实测（删除不回落），但"这是可接受的设计边界还是缺陷"取决于 ADR-0072
   决策 6 的解释；若裁定为边界，则应改 ADR/提示而非改机制。报告按"文档-代码漂移 + 机制缺口"处理。
3. **R03-M11 的覆盖面**：git 批 compose 的 Revision `source_kind` 是 image（材料归 git），是否算
   ADR-0074 语义下的"compose 源"可争；若裁定不算，应在代码注释/方言文档显式写"git 批 compose 不冻结
   原文"，否则读面会持续让人误解。
4. **R03-M3 的清理策略**：与"旧 Revision 可重放"承诺冲突（重放需要上下文），保留策略需产品裁决
   （按部署链保留 vs 按龄 vs 重放时重建）；本报告只认定"无策略 + 无观测"的风险，不主张具体算法。
5. **R03-M2 的触发频率**：`git clone` 超时/中断后的半截目录机制已实测确认（完成判据只看目录存在），
   但"坏树上构建能成功出错误产物"依赖建树顺序/Dockerfile 位置，未真机复现；若真机按 5m 超时打断大仓
   clone，可升到 H 级证据。

**整体置信度**：**高**（13 条 M 中 8 条经 overlay 实测/日志取证；其余为代码路径直接可读；
L 级 5 条均有代码/文档对照）。未发现 H 级，但 M13/M2 是两条需真机/跨模块复核的升级候选。

**复验指引**（只读，不写仓库）：
```
go test -overlay=D:\Codes\qiulin\fleetly-review-2026-10-19\_tmp_r03_intake_overlay.json ^
  ./internal/application/composeintake/ -run TestR03 -v
go test -overlay=D:\Codes\qiulin\fleetly-review-2026-10-19\_tmp_r03_engine_overlay.json ^
  ./internal/application/engine/ -run TestR03 -v
```
（两个 overlay 的 Replace 目标均指向共享评审目录下的 `_tmp_r03_*` 夹具；仓库内无新增文件。）
