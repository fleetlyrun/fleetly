# R12 守卫、测试设施与 e2e 评审报告

> 基线：`main` @ `35257f18`（工作树干净）。全程只读；未运行任何 e2e、未跑生成任务、未改仓库文件。
> 唯一执行的动态验证：`go test ./internal/guards -count=1` → `ok ... 17.510s`（41 个守卫测试全绿，故本报告全部"会不会红"的判断都以"当前绿"为前提做红灯反推）。

## 1. 覆盖清单

**完整读过（`internal/guards/**`，6693 行 / 41 文件）**

- 扫描底座：`scan_test.go`
- 依赖/纯度：`dep_matrix_test.go`、`importguard_test.go`、`domainpurity_test.go`、`capabilitypurity_test.go`、`uowguard_test.go`（头注 + 断言语义）
- 写路径与事实：`acceptanceguard_test.go`、`transitionguard_test.go`、`factappend_test.go`、`factdual_test.go`、`writeclassguard_test.go`、`freezeguard_test.go`、`scopeguard_test.go`、`idemguard_test.go`、`rowmintguard_test.go`、`enginebackref_test.go`
- 契约/资源面：`gatewayguard_test.go`、`errmappingguard_test.go`、`registrysourceanchor_test.go`、`coverageguard_test.go`、`nilfacelaw_test.go`（头注 + 律文）、`statekitlaw_test.go`（头注 + 律文）、`aggregatetestbelt_test.go`、`sizeguard_test.go`、`teamaxisguard_test.go`
- 过程执行面：`shellguard_test.go`、`loopguard_test.go`、`wording_test.go`（全量 478 行）
- 对账：`e2eciaccount_test.go`

**抽样读过（`internal/testsupport/**`，10114 行）**

- `apitest/harness.go`（934 行：前 600 行逐行，其余按符号定位——FakeRuntime/FakeBuilder/FakeRegistry、装配序、`AssertAllRegisteredHavePolicy`）
- `testruntime/testruntime.go`（258 行全读）
- `persistence/statetest/statetest.go`（68 行全读）
- apitest 68 个测试文件：按面抽样 12 个（`apitest_test.go`/`teamauth_test.go`/`delete_*`/`wait_test.go`/`events_*`/`exec_test.go`），其余用定向检索（`t.Parallel`/`time.Sleep`/`Clock.Advance`/`t.Skip`）统计

**抽样读过（`e2e/**`，5782 行 / 13 脚本 + 4 helper）**

- 全文：`dind-upgrade.sh`（471 行）、`dind-bluegreen.sh`（436 行）、`hermetic-build.sh`（58 行）
- 结构 + 断言面：`dind-backup.sh`（574 行：按 `log`/`fail` 行定位后精读 postgres 全链、四引擎断言、Platform/S3 腿）、`dind-smoke.sh`（786 行：doctor 段、零 drift 段、alert 段、收尾段精读，其余按大纲）
- 定向抽查：`dind-h2c-route.sh`/`dind-two-node.sh`/`dind-template.sh`/`dind-browse.sh`/`dind-runtimeswitch.sh`/`dind-k3s*.sh`（断言密度统计 + 关键 grep）
- `e2e/h2cserver`、`e2e/h2cclient`、`e2e/browseprobe`、`e2e/webhookrecv`：只读了被脚本引用的接口面

**对账面**

- `.github/workflows/ci.yml`（444 行全读）、`mise.toml`（229 行全读）、`.golangci.yml`、`AGENTS.md` 守卫条款、`docs/adr/0082`、`docs/adr/0083`（验收锚节）

**未覆盖 / 无法覆盖**

- e2e 真机复验：本机 Docker daemon 不可达（PROFILE §1.1 已裁定），**本报告不对"e2e 是否真绿"作任何断言**，只评断言强度与假阳性风险。
- `dind-k3s.sh`（948 行）/`dind-k3s-two-node.sh`/`dind-k3s-ha.sh` 未逐行读（R06 主审 provider 语义；我只看它们的断言形态）。
- `internal/guards` 中 `bluegreen_test.go`、`buildernamesguard_test.go`、`dbtemplateguard_test.go`、`entityid_guard_test.go`、`grpcguard_test.go`、`irguard_test.go`、`k3spin_test.go`、`migrationchainanchor_test.go`、`perfthresholdanchor_test.go`、`railpackpin_test.go`、`resticpin_test.go`、`statefacade_test.go` 只读了头注与断言主体，未逐行核对每一条豁免（这些是"钉版/snippet 存在性"类，形态与已读的 `resticpin`/`railpackpin` 同族）。
- `go test -race ./...` 全量未跑（PROFILE §1.1 已给基线，属重复劳动）。

## 2. 模块画像与边界

### 2.1 三层职责

| 层 | 实体 | 规模 | 定位 |
|---|---|---|---|
| 守卫 | `internal/guards` 41 个 `_test.go` | 6.7k | 架构"由测试承载"的执法层：不产出可执行代码，只把设计律翻译成扫全仓的断言 |
| 测试设施 | `internal/testsupport/apitest`（68 测试 + harness 934 行）+ `testruntime`（共享假底座核）+ `persistence/statetest`（真 SQLite + 假时钟） | 10.1k | hermetic 与 dind e2e 之间的中间层：真 assembly + 真 SQLite + bufconn 传输 + 假 Provider |
| e2e | `e2e/dind-*.sh` 12 条 + `hermetic-build.sh` + 4 个 Go helper | 5.8k | dind 内真 swarm/k3s 全链；12 条全部进 CI（G20 对账零豁免） |

### 2.2 依赖方向与对外接口

- `guards` 是**元层**：`scanGoFiles` 明确把它排除在自身执法面之外（`scan_test.go:97`），所以守卫可以 import 任意层（`assembly`/`governance`/`idem` 都是其"法源"）。
- `testsupport` 同样登记为元层（`dep_matrix_test.go:68-72` `allowAll: true`），并可经 `assembly.NewAPIDeps`/`NewInterceptors`/`GRPCServerOptions` 复用生产组合根——这是它与"另一套生产装配"的关键区别（见 §3-4）。
- `e2e/*.go` 进 Go 执法面（`scan_test.go:54` 的 `scanModulePrefixes` 含 `e2e/`，P1a-4），但 `e2e/*.sh` **不在**任何静态执法面（词汇扫描只收 `install.sh`）。

### 2.3 与架构文档/ADR 声明的偏差

- 声明：`AGENTS.md`「ADR 含可静态执法的承诺时，须同批开守卫任务（ADR-0001→irguard 为范式）」。
  - ADR-0081（写类/作者类）→ **已同批**：`writeclassguard_test.go` 把 153 RPC 的三桶分类、27/53 台账实数、scope 方向、FrozenVerbs ⊂ 写面、idem ⊆ 写面五条从属断言全部挂上同一法源（`writeclassguard_test.go:183-234, 241-293, 298-323, 330-376`）。这是本次验收里质量最高的一组。
  - ADR-0082（缺省收敛）→ §6「期望账本写点纪律」是**可静态执法**的写点约束（"只由 Ensure 写入"），但只有注释 + 行为测试，**无守卫**（见 M3）。
  - ADR-0083（restart_seq）→ 声明"两个守卫测试以此为共同法源"，落点是 `drift_test.go:372 TestCompareSpecsIgnoresRestartSeq` 与双侧 contentequiv 四用例，均为普通单测而非 `internal/guards` 结构守卫；承诺本身是行为面（不可静态执法），故不计缺陷；但验收锚最后一条（e2e 升级首拍零滚）仍为未勾 `[ ]`（`docs/adr/0083-restart-seq.md:54`）。
- 声明：`PROFILE`/`AGENTS.md`「G20 mise↔CI↔e2e 对账」。实况：对账守卫只认 `e2e:*` 前缀任务（见 M5），mise 里另外两个验证任务不受任何对账。

## 3. 优点（带证据）

1. **依赖矩阵是"登记即债"的真正双向保鲜**，不是单向白名单。行/允许前缀/豁免三层都做了零命中即红，并且有"未登记子树即红"的防盲分支——新顶层子树（如将来 `internal/foo/`）无法偷偷绕过。
   `internal/guards/dep_matrix_test.go:199-227`（子树保鲜/前缀保鲜/豁免保鲜/元层前缀保鲜四段）+ `:156-158`（未登记子树红）+ `:129-131`（零文件红）。

2. **同一个律文被多个从属断言挂到单一法源**，防"各表各自漂移"。ADR-0081 的写类法源同时约束冻结面与幂等面，且三张表的死条目都在同一测试里反向对账。
   `internal/guards/writeclassguard_test.go:298-323`（FrozenVerbs ⊆ DECLARATION∪ACTION + 死条目红）、`:330-376`（idem ⊆ DECLARATION∪豁免）。

3. **守卫自带"形状用例"（注入临时树/临时根自证红灯）**——这是守卫本体的回归锚，而不是只测生产代码。`registrysourceanchor` 五个 t.Run 覆盖存在/悬空/形态坏/不误报/防盲；`aggregatetestbelt` 四种夹具形态；`enginebackref` 有 `namedStructs ≥ 60` + `sawEngine` 双防盲。
   `internal/guards/registrysourceanchor_test.go:192-250`、`aggregatetestbelt_test.go:119-181`、`enginebackref_test.go:196-203`。

4. **apitest 夹具与生产面"同源"而非"同形"**：不只复刻结构，而是直接调用生产组合根（binder/policy/interceptors/GRPCServerOptions），并在夹具里对 RPC×policy 覆盖面做 fail-closed 断言——夹具自身漂移会当场红。
   `internal/testsupport/apitest/harness.go:149-176`（`assembly.NewPolicySet` / `NewInterceptors` / `GRPCServerOptions` / `AssertAllRegisteredHavePolicy`）、`:141-145`（bootstrap 流与生产同源）。

5. **e2e 的"零扰动"断言是条件化预算而不是拍脑袋阈值**：升级腿按两代二进制的 dbtemplate digest 集差授予 0/1 次滚动预算，且新代 digest 读 HEAD 归档快照（不读工作树，避免把未提交态泄进预算判定）。这是少见的"断言本身也有设计"。
   `e2e/dind-upgrade.sh:409-432`（`db_engine_digest` + `DB_ROLL_BUDGET` + 有符号 delta 判定）。

6. **备份演练断言到"值"而不是"状态"**：四引擎各自 seed→backup→verify(digest 重算)→restore→**回读精确值 `DRILL_OK`**，并且额外断言 utility 失败/成功两路径零匿名卷泄漏、仓密钥不进自身快照。
   `e2e/dind-backup.sh:319-327`（pg）、`:349-360`（mysql）、`:381-394`（mongo）、`:411-423`（redis）、`:272-277` 与 `:313-317`（匿名卷泄漏双向）、`:437-449`（密钥自锁排除）。

## 4. 发现清单

> 计数：**H 0 / M 7 / L 7**。
> 为什么没有 H：本次评审的守卫层没有发现"当前存在实质违规且无兜底"的形态；所有 M 都是"承诺存在、守卫/覆盖缺位"（下一行违规不会被拦住），按 PROFILE 的 H 定义（已实质违反且会产生系统性错误）不成立。最接近 H 的是 M1（API 层手搓事务已无检测器），今天恰好无违规实例，故按 M 报。

### R12-M1｜API 层"手写四件一"守卫的针已失配当前事务 API，检测器实际为零断言

- 维度：测试与守卫
- 严重度：M（守卫本体完好但靶子消失了：它不是"扫不到违规"，而是"什么都不扫"；ADR-0080 把 `DB.Tx` 换成了 `Tx.Do`，针没跟）
- 位置：`internal/guards/acceptanceguard_test.go:134,141`；被执法面 `internal/interfaces/grpc/fleetlygrpc/acceptance.go:55`
- 证据：

  ```go
  // internal/guards/acceptanceguard_test.go:133-144
  rel := filepath.ToSlash(path[len(repoRoot(t))+1:])
  if rel == "internal/interfaces/grpc/fleetlygrpc/acceptance.go" {
      return nil // 原语本体：唯一允许的 DB.Tx 直调点
  }
  content, rerr := os.ReadFile(path)
  ...
  if strings.Contains(string(content), ".DB.Tx(") {
      t.Errorf("%s hand-rolls the four-in-one-tx choreography via DB.Tx; declare a writeFact and go through Services.commit (ADR-0024)", rel)
  }
  ```

  当前唯一的事务入口是 ADR-0080 的事务作用域形态，字面量 `.DB.Tx(` 在 `fleetlygrpc` 包内**一次都不出现**（含被豁免的 `acceptance.go` 本体）：

  ```go
  // internal/interfaces/grpc/fleetlygrpc/acceptance.go:55
  return s.Tx.Do(ctx, func(st transact.Stores) error {
  ```

  `grep '.DB.Tx('` 全仓命中只有：`application/transact/transact.go:122`（`t.db.Tx`）、`assembly/provides.go:796`（`s.db.Tx`）、apitest 夹具的 `h.DB.Tx(`（4 处）、以及守卫自己这一行的针——没有一处在守卫的扫描面 `internal/interfaces/grpc/fleetlygrpc` 内。
- 影响：这是 fleetlygrpc 包里"手写四件一拍（行写+事件+审计+回滚）不得回潮（2026-10-01 曾达 29 处）"的唯一直接检测器。今天任何 handler 写 `s.Tx.Do(ctx, ...)` 直接编排仓库写，本守卫全绿；补位守卫 `TestAcceptanceWritePathsGoThroughCommit` 只覆盖 `Create/Put/Set/Deploy/Submit/Rollback/Delete` 七个前缀（`acceptanceguard_test.go:26`），对 ACTION 类写面（`RotateDatabasePassword`/`RebuildNetwork`/`InstantiateTemplate`/`TriggerBackup`…）没有要求。也就是说"守卫红"与"真的违规"在当前形态下无法同时发生。
- 建议：把针改为 AST 级双形态——① `*.Tx.Do(`/`*.db.Tx(` 选择器调用（含任意接收者别名）；② `transact.NewTx`/`Tx.Do` 的调用点必须落在 `acceptance.go`。同时补一条正向锚（`acceptance.go` 里必须恰有 1 处 `Tx.Do`），否则下一次 API 换形会再次静默失明（对照 `transitionguard_test.go:194-202` 的 `sawTransitFront` 写法，M 级守卫里已有范式）。成本 S。
- 状态：已亲验（读了守卫、被执法面、全仓字面量分布与两代 API 形态）。

### R12-M2｜"Runtime 端口覆盖反扫"用裸子串判存在性，对 6 个核心方法零判别力

- 维度：测试与守卫
- 严重度：M（守卫的全部价值就是"新方法落地没测就红"，而它连"方法名出现在任何注释里"都算通过；这类守卫一旦被当作覆盖凭据，会系统性高估覆盖）
- 位置：`internal/guards/coverageguard_test.go:80-136`；接口面 `internal/domain/shared/capability/runtime.go:17`
- 证据：

  ```go
  // internal/guards/coverageguard_test.go:101-112
  for _, face := range faces {
      for i := 0; i < face.typ.NumMethod(); i++ {
          name := face.name + "." + face.typ.Method(i).Name
          if strings.Contains(corpus, face.typ.Method(i).Name) {
              continue
          }
          if reason, ok := portCoverageExemptions[name]; ok && reason != "" {
              usedExemptions[name] = true
              continue
          }
          missing = append(missing, name)
      }
  }
  ```

  `capability.Runtime` 的方法名是 `Ensure` / `Remove` / `Watch` / `Addresses` / `DescribeCluster` / `Enrollment`（`runtime.go:17` 起）——全部是通用英文动词，在 `engine`+`swarm`+`k3s`+`apitest` 四棵测试树（本仓 `_test.go` 合计 351 文件 / 5.16 万行，这四棵是其中的主体）里必然出现。`corpus` 的防盲下限是 4096 字节（`:85-87`），而真实语料大两三个数量级，故防盲分支也永远不触发。
- 影响：`TestRuntimePortCoverage` 永远绿，与"每个 Runtime 方法至少被引用一次"这一承诺脱钩。它与 `TestDeleteRPCCoverage`（`:182-191`，同样子串）一起构成覆盖面的"看起来有"叙事：新增一个 Runtime 方法、四个 Provider 全不实现不被测，守卫仍绿。
- 建议：把 `strings.Contains(corpus, name)` 升级为 AST 判据——解析四棵树的 `*_test.go`，要求方法名出现在 `SelectorExpr.Sel` 或方法值位置（解析成本与 `transitionguard`/`factappend` 同级，且这些 AST 工具在同包已存在）。成本 S。
- 状态：已亲验（读了守卫实现、接口方法集与语料下限）。

### R12-M3｜ADR-0082 §6「期望账本只由 Ensure 写」是可静态执法的承诺，但只有注释没有守卫

- 维度：架构一致性 / 测试与守卫
- 严重度：M（这是一条"观测播种即静默击穿 controller-wins"的红线；违反它不会让任何现有测试变红，且违反形态只需一行 map 赋值）
- 位置：`internal/application/engine/observdomain.go:44-73`（唯一写点 + 纪律注释）；缺位守卫：`internal/guards/`（41 文件内零处提及 `ensuredSpec`/`ensuredGen`）
- 证据：

  ```go
  // internal/application/engine/observdomain.go:48-58
  // 期望账本纪律（ADR-0082 §6 钉死）：ensuredSpec/ensuredGen 只由 Ensure
  // 路径写入（recordEnsured ← materialize 单序列），禁止观测播种——受管域
  // 的 gen 观测播种先例（managed.go seedFromRuntime）不得照抄到 App 域，
  // 否则手动热修被播种成期望态，静默击穿 controller-wins。
  func (o *observations) recordSpecs(owner workloadOwner, gen uint64, ws []capability.Workload) {
      o.mu.Lock()
      for _, w := range ws {
          o.workloadApp[w.ID] = owner
          o.ensuredGen[w.ID] = gen
          o.ensuredSpec[w.ID] = w
      }
  ```

  ADR-0082 自己的验收锚把这一条写成"（recordSpecs/recordEnsured 注释钉死）+「succeeded 无期望缓存 → 收敛触发」分支钉测"（`docs/adr/0082-converge-by-default.md:96`）——即**承认只靠注释**。对照同一批的 ADR-0081：写类承诺在同批拿到了 376 行的结构守卫。
- 影响：把 `ensuredSpec`/`ensuredGen` 的写点从 1 处扩到 2 处（例如在 `drift.go` 里"看到 running 就播种期望"）是本次收敛改造最容易发生的回退，且它会同时污染 drift 判据与收敛触发——故障形态是"手动热修被平台当成期望态"，无任何红灯。写点面极小（`observdomain.go` 的 4 处写 + 4 处删），静态执法成本极低。
- 建议：新增 `ensuredledger_test.go`：AST 扫 `internal/application/engine` 全部生产文件，对 `*.ensuredGen[...] =`/`*.ensuredSpec[...] =`/`delete(*.ensuredGen, …)` 的写点断言只出现在 `recordSpecs`/`recordOwners`/`forgetOwner(s)` 三个函数体内（函数粒度的白名单 + 无命中即红的双向保鲜），并把"观测播种"反例写进形状用例。成本 S。
- 状态：已亲验（写点面 + ADR 验收锚 + 守卫面三处对照）。

### R12-M4｜ADR-0082 缺省收敛在 13 条 e2e 里零承载：只有"不许漂移"的否定断言，没有"漂移必须收敛"的肯定断言

- 维度：测试与守卫
- 严重度：M（最新架构增量的头号能力"缺席检测→收敛"只有单测（R02 已核）；一旦某个 Provider/装配层的边把收敛环整体关掉，全 e2e 仍绿——因为现有断言只禁止多滚，不要求该滚时滚）
- 位置：`e2e/*.sh` 全 13 脚本；证据位 `e2e/dind-upgrade.sh:443-469`、`e2e/dind-smoke.sh:379-405`
- 证据（e2e 里唯一与收敛/漂移相关的断言，全部是"计数为 0"）：

  ```sh
  # e2e/dind-upgrade.sh:445-457
  log "zero-drift assertion over post-upgrade window (>= 2 scan beats)"
  while :; do
    DRIFT_EVENTS=$(cli --json events list --limit 200 | grep -c '"name": *"workload.drift_detected"') || true
    ...
    if [ "$DRIFT_EVENTS" -ne 0 ]; then
      echo "drift detected after upgrade: $DRIFT_EVENTS event(s)" >&2
  ```

  全仓 `e2e/*.sh` 对 `workload.converged` / `workload.converge_failed` 的引用数为 **0**；"人工删除一个载体 / scale 到 0 / `docker service rm` 一个 workload 服务"的演练只出现在 `dind-runtimeswitch.sh:272-278`，且上下文是"停控制面 → 显式数据处置"，不是收敛演练。
- 影响：ADR-0082 的后果节承诺"人工改载体/整删载体/停摆/Cancel 遗留，最迟 60s+一拍内收敛"（`docs/adr/0082-converge-by-default.md:81`）在 CI 里没有一条端到端证据。反向风险同样存在：把 `converge_posture` 配成 `detect`（只报不收敛）是 ADR 明确保留的逃生门，e2e 无法区分"posture=all 正常无漂移"与"posture=detect 从不收敛"——两种世界都零 drift 事件、零重启。
- 建议：在 `dind-smoke.sh` 加一段（成本最省，已有 App/Route/零 drift 段）：①`docker service scale` 或 `docker rm -f` 删一个 workload 任务 → ②轮询 `workload.converged` 事件或 task 行数回到基线（≤90s，覆盖 60s 慢通道 + 一拍）→ ③断言期间无 `converge_failed`。若不愿动 smoke，至少加一个 `e2e:converge` 腿并登记 CI（e2e ↔ CI 对账守卫会自动要求登记）。成本 M（脚本 ~40 行 + CI 15min 预算复核）。
- 状态：已亲验（13 脚本关键字扫描 + 两处断言原文 + ADR 后果节）。

### R12-M5｜mise 的两个"验证类"门禁任务（`generate:verify` / `verify:hermetic`）不在 CI、不在 `/lint` 组合、也不在 G20 对账守卫的射程

- 维度：测试与守卫 / 架构一致性
- 严重度：M（生成物纪律与换装 hermetic 通道都是"出事过"的红线，其回归锚目前只由人记得手动跑；CI 无法复核 ADR/plan 里大量"generate:verify 零漂移"的验收声明）
- 位置：`mise.toml:70-75`（`generate:verify`）、`mise.toml:136-138`（`verify:hermetic`）、`.github/workflows/ci.yml:46-48`（显式说明不在 CI 跑 codegen 漂移）、`internal/guards/e2eciaccount_test.go:27`（对账正则只认 `e2e:` 前缀）
- 证据：

  ```go
  // internal/guards/e2eciaccount_test.go:27
  var miseTaskRe = regexp.MustCompile(`\[tasks\."(e2e:[A-Za-z0-9._-]+)"\]`)
  ```

  ```toml
  # mise.toml:70-75
  [tasks."generate:verify"]
  description = "基线通道重生成后零漂移断言（genproto/config/wire）"
  run = [
    { task = "generate:all" },
    "git diff --exit-code -- genproto internal/config internal/assembly",
  ]
  ```

  ```yaml
  # .github/workflows/ci.yml:46-48
  # codegen 漂移检查（buf generate / wire 同步）在本地门禁（mise run
  # generate:all + git diff）：CI 冷环境重跑生成受 tidy/网络影响会产生
  # 本地不复现的 diff（torchwood 2026-09-23 裁决沿用）。
  ```

  `mise run lint`（`mise.toml:92-98`）= `lint:go + lint:golangci + lint:proto`，不含 `generate:verify`；`mise run test`（`:140-142`）也不含。两者都不被任何 composite task 引用（全仓引用只出现在文档与 runbook 的"手动门禁"叙述里，如 `docs/runbooks/staging-fleetly.md:10`）。
- 影响：①`genproto/**`、`internal/config/config.pb.go`、`internal/assembly/wire_gen.go` 被手改后，CI 的 buf lint/breaking 与 `go vet`/`go test` 都不会红（手改若自洽则编译通过），console 侧的同款纪律却有 CI 门禁（`ci.yml:73-74` `console:verify`）——同一纪律两条标准。②`verify:hermetic` 是 2026-10-11 换装事故（工作树构建把未提交改动带进部署）的回归锚，探针本身写得很好（junk 文件入包即改 sha256），但没人自动跑它；`runbook` 换装序靠人执行，一旦某次换装跳步，"通道退化"这件事没有任何机器信号。③G20 对账宣称"mise ↔ CI 一一对账"，实况只对账了 `e2e:*`。
- 建议：①把 `e2eciaccount_test.go` 的律文从"`e2e:*` 任务"扩到"全部含 `verify`/`generate` 语义的任务"（或显式登记 `reconciled: false` + 理由），让"没有 CI 承载"成为一条要写理由的登记而不是沉默；②`generate:verify` 进 CI backend 一个 optional step（网络可用时跑，失败即红），或改成"只在 proto/wire 相关文件变更时跑"的路径过滤，避开冷环境 diff 噪声；③`verify:hermetic` 挂在 `e2e` 或 backend（9s 成本，`docs/plan/2026-10-14-architecture-convergence-plan.md:120` 自称 9s）。成本 S–M。
- 状态：已亲验（mise/CI/守卫三处原文 + 全仓引用位置统计）。

### R12-M6｜词汇冻结的机器执行力约 1/3：`skippedTokens` 113 条 vs `bannedPatterns` 55 条，且"人工评审"无流程载体

- 维度：测试与守卫 / 文档一致性
- 严重度：M（"更名即设计缺陷"是 AGENTS.md 的硬条款；守卫形制上全仓覆盖，但三分之二的词条只写在"人工评审把关"的注释里，没有任何周期性载体证明评审发生过）
- 位置：`internal/guards/wording_test.go:25-115`（banned 55 条）、`:128-255`（skipped 113 条）、`:402-478`（扫描面与豁免）
- 证据：

  ```go
  // internal/guards/wording_test.go:126-129
  // skippedTokens：语境限定词分诊表（token → 理由）。机械扫描会大量误伤
  // 的技术通用词在此登记；人工评审时按 CONTEXT.md 语境把关。
  var skippedTokens = map[string]string{
      // ---- 组织与访问 ----
      "account":    "User 同义词禁用仅限身份实体语境；账目/解释义（account for）为通用英文",
  ```

  实测计数：`bannedPatterns` 55 条、`skippedTokens` 113 条、`wordingExemptions` 7 组 / 35 个文件条目；`TestAvoidTokensTriaged`（`:293-319`）只验证"每个 _Avoid_ 词条被分诊到其中一张表"，即**把词条放进 skipped 与放进 banned 在守卫眼里等价**。
- 影响：`CONTEXT.md` 的 _Avoid_ 表是"更名即设计缺陷"的执法源，而实际被机械执法的只有 55 个词（其中不少是第三方专名形态，如 `lambda`/`ssh`/`middleware` 靠豁免表放行）。113 条 skipped 里的高频词（`service`/`job`/`run`/`agent`/`snapshot`/`release`/`pipeline`/`stage`/`namespace`/`config`…）正是命名漂移最容易发生的语汇面；它们一旦被用作实体同义词，只会在人工读代码时被发现，而仓库里没有"人工评审轮次"的产物（无 checklist、无每月报表、`docs/plan` 也无此类条目）。
- 建议：二选一收口——①给 `skippedTokens` 加第三字段/旁表"上次人工评审日期 + 载体链接"，到期即红（把"人工评审"变成有截止日的债务，与 `sizeguardWhitelist` 的"豁免是债"同构）；②把其中可机械判定的子集（如"实体同义"限定语已在括号里出现的词条：`service`/`stage`/`profile`/`environment` 等）改成"仅当与实体名同现时命中"的双词正则，扩大 banned 面。成本 M（②） / S（①）。
- 状态：已亲验（两表条目数、测试逻辑、豁免表规模均为实测）。

### R12-M7｜apitest 的假 Runtime 恒返回空 `EnsureReport` 且观测靠测试手动泵：内容等价记账与收敛判定在全栈夹具里结构性不可达

- 维度：测试与守卫 / 可靠性
- 严重度：M（"全栈夹具"是本仓 hermetic 与 dind 之间唯一的集成层；它现在只能测"控制面机械"，凡"引擎对运行时观测的反应"都必须下沉到另一套假底座，两层假底座的语义各自生长——`testruntime` 的诞生注释正是在收拾这类成对生长）
- 位置：`internal/testsupport/apitest/harness.go:533-542`（Ensure）、`:590-592`（Watch）、`:594-596`（Addresses）；对照 `internal/application/engine/fakeruntime_test.go:150-155`（engine 侧有 wireGens 注入）
- 证据：

  ```go
  // internal/testsupport/apitest/harness.go:533-542
  func (f *FakeRuntime) Ensure(_ context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) (capability.EnsureReport, error) {
      f.mu.Lock()
      defer f.mu.Unlock()
      spec := make(map[string]capability.Workload, len(ws))
      for _, w := range ws {
          spec[w.Process] = w
      }
      f.ensures = append(f.ensures, EnsureCall{NS: ns, Gen: gen, Spec: spec, Materials: m})
      return capability.EnsureReport{}, nil
  }

  // :590-596
  func (f *FakeRuntime) Watch(context.Context) (<-chan capability.WorkloadEvent, error) {
      return f.obs, nil
  }
  func (f *FakeRuntime) Addresses(context.Context, capability.NamespaceRef, []capability.Workload) ([]capability.Endpoint, error) {
      return nil, nil
  }
  ```

  `EnsureReport` 的唯一字段是 `Gens`（`internal/domain/shared/capability/runtime.go:272-279`），空报告 = "全部载体按调用 gen 下发"（回滚兼容语义）。engine 侧假底座为此专门开了 `wireGens` 注入口（`fakeruntime_test.go:150-155`），apitest 侧没有对应能力。
- 影响：①ADR-0064 的"内容等价未变载体保留旧代次 → L1 门按回执 gen 等待"这条链，在 `apitest`（真 handler + 真 engine + 真 SQLite）里永远走不到，只在 `engine` 单测与 swarm/k3s 的 contentequiv 单测里各自验证；②`RolloutStalled` 观测位无假实现（`Addresses` 恒 nil、`Watch` 恒空），ADR-0082 的"滚动停摆→收敛"与"缺席检测"在全栈层不可复现，只能靠 engine 单测的假底座——两套假底座的 `WorkloadObservation` 语义没有对账面（`testruntime` 只收编了 NetworkMaintenance 与 BlockGate）。
- 建议：给 apitest 的 `FakeRuntime` 补三件最小能力（不改生产代码）：①`Ensure` 可配置回执 `Gens`（按 workload 记录"轮次"——第一次下发 gen=1、等价重下发回旧 gen）；②`ReportObservation` 已有泵口，补一个 `SetRolloutStalled(wid, bool)`；③加一个跨夹具的一致性断言（同一 `capability.WorkloadObservation` 构造，engine 假底座与 apitest 假底座喂进同一断言函数），把"两套底座语义漂移"变成红灯。成本 M。
- 状态：已亲验（两处实现、接口字段、engine 侧注入口）。

### R12-L1｜domain 三红线①"无 I/O"不拦裸 `net`，而兄弟守卫拦——同一红线两套禁列

- 维度：测试与守卫
- 严重度：L（当前 domain 内零 `net` import，属未来漏网；修法一行）
- 位置：`internal/guards/domainpurity_test.go:20-25`；对照 `internal/guards/capabilitypurity_test.go:25-32`
- 证据：

  ```go
  // domainpurity_test.go:20-25
  var systemImportMatchers = []string{
      "os",
      "os/",
      "io/ioutil",
      "net/http",
  }
  // capabilitypurity_test.go:25-32
  var capabilityDenyMatchers = []string{
      "os", "os/", "net", "net/",
      "google.golang.org/grpc", genprotoImportPrefix,
  }
  ```

- 影响：`internal/domain/**` 里写 `net.Dial`/`net.Listen` 不红（`net/http` 被拦但裸 `net` 不在列）；反之 capability 子树允许 `net/url` 却连裸 `net` 都拦。两条守卫描述的是同一条"纯领域/纯类型"纪律，禁列却不一致——将来只改其中一处会加剧分叉。
- 建议：把三红线禁列抽成共享表（`net` + `io/ioutil` + `syscall` 一并入列，`net/url` 走显式白名单），两守卫引用同一 var。成本 S。
- 状态：已亲验（两表原文 + 全 domain `net` import 实测为 0）。

### R12-L2｜`loopguard` 只认字面 `time.NewTicker` 且无正向锚：把骨架换成 `time.After`/`time.Tick` 环，守卫静默变哑

- 维度：测试与守卫
- 严重度：L（当前唯一骨架确实在 `loop.go`；缺口在"下一次重构"）
- 位置：`internal/guards/loopguard_test.go:13-43`
- 证据：

  ```go
  // loopguard_test.go:27-37
  if name == "loop.go" {
      return nil // 骨架本体
  }
  data, readErr := os.ReadFile(path) //nolint:gosec // 守卫扫描仓库自有源文件
  ...
  if strings.Contains(string(data), "time.NewTicker") {
      rel, _ := filepath.Rel(root, path)
      t.Errorf("%s builds its own ticker loop — converge via engine.NewLoop (架构 §0 唯一骨架)", filepath.ToSlash(rel))
  }
  ```

- 影响：①`time.Tick`/`time.AfterFunc`/`for { select { case <-time.After(d) } }` 三种等价环形态全不命中；②无"`loop.go` 必须真有一处 ticker"的正向断言——把骨架改成 `time.After` 形态后，全守卫零命中即绿，而"唯一骨架"承诺已经名存实亡；③扫面只到 `internal/application/engine`（`filepath.WalkDir(engineDir, …)`），仓内另有三处 Provider/装配层 ticker（`assembly/gateway_events.go:90` SSE 心跳、`providers/k3s/runtime.go:583` watch 轮询、`interfaces/grpc/fleetlygrpc/delivery.go:490` 日志追尾轮询）——前两处是合理异类，第三处是接口层的轮询循环，守卫看不见（当前判定为合理：非收敛骨架，但守卫无法自证这一点）。
- 建议：AST 判据取代字符串（`time.NewTicker`/`time.Tick`/`time.After` in `for`+`select`），补 `loop.go` 正向锚，并把扫描面扩到 `internal/**` + 显式豁免表（SSE 心跳/Provider watch/日志追尾三条带理由）。成本 S。
- 状态：已亲验（守卫原文 + 全仓 ticker 分布实测）。

### R12-L3｜注册表 Source 锚只校验"文件存在"，不校验"函数限定符存在"——锚的函数名腐烂不红

- 维度：可观测性 / 测试与守卫
- 严重度：L（排障入口劣化，不产生错误行为）
- 位置：`internal/guards/registrysourceanchor_test.go:21,116-127`
- 证据：

  ```go
  // registrysourceanchor_test.go:20-21（律文）
  //   - 每条锚在 repoRoot 下 os.Stat 必须命中常规文件——指向不存在路径
  //     即红(存在律)。行号不锚(易腐),限定符一律函数名。
  // :118-127（实现：只 Stat）
  func missingSourceAnchors(root string, anchors []string) []string {
      for _, a := range anchors {
          info, err := os.Stat(filepath.Join(root, filepath.FromSlash(a)))
          if err != nil || !info.Mode().IsRegular() {
              missing = append(missing, a)
          }
      }
  ```

- 影响：`Source: "internal/x/y.go oldFuncName"` 在 `oldFuncName` 被改名/删除后仍然全绿。ADR/守卫的初衷是"排障者从码索锚"——路径对、函数错，排障者仍被引向一个不存在的符号。守卫自己确立了"限定符一律函数名"的规范，却不执法它。
- 建议：主锚后第一个 token（非路径、非括注、非 `+`/`;`）若形如 Go 标识符，则解析该文件 AST 断言存在同名 `FuncDecl`/`TypeSpec`（`gatewayguard` 已在用同款 `walkGoDecls`）。成本 S。
- 状态：已亲验（律文 + 实现 + shapes 用例覆盖范围）。

### R12-L4｜错误映射守卫只有一根针，且注释声称的兜底（"go vet 风格评审"）并不存在

- 维度：测试与守卫
- 严重度：L（当前 interfaces 面零违规；缺口在注释给了错的安心感）
- 位置：`internal/guards/errmappingguard_test.go:15-35`
- 证据：

  ```go
  // :15-18
  // errTextMappingLiteral 是禁用的匹配形态（含前导调用点形态
  // `strings.Contains(err.Error()`——err 变量名以任何拼写出现均命中：
  // 扫描以统一前缀 + err.Error() 组合为准，见下方实现）。
  const errTextMappingNeedle = "strings.Contains(err.Error()"
  // :25-26
  // err 为最常见变量名；其他拼写的
  // err 检测由 go vet 风格评审承载，本守卫拦主流形态）。
  ```

- 影响：`strings.HasPrefix(err.Error()`、`strings.Contains(e.Error()`、`strings.Contains(errText, "…")`（其中 `errText := err.Error()`）、`err.Error() == "…"` 全部漏网；而 `.golangci.yml` 启用的集合是 `standard + bodyclose/gosec/noctx/sqlclosecheck`，**没有 errorlint**，`go vet` 的 `errorsas`/`stringintconv` 也不覆盖"错误文案分支"。注释把补偿寄托在一个不存在的检查上。
- 建议：把判据从"针"改为 AST 形态（`err.Error()` 结果进入任何字符串谓词/比较），或启用 `errorlint` 并补自定义 rule；至少把注释改成"未覆盖，靠 review"。成本 S。
- 状态：已亲验（守卫原文 + `.golangci.yml` 启用集）。

### R12-L5｜e2e↔CI 对账守的防盲余量过大（阈值 8/5 vs 实测 12/12），且不校验任务指向的脚本存在

- 维度：测试与守卫
- 严重度：L（漏挂仍会红，但"解析退化吞掉 1/3 任务"不会红；脚本改名忘改 mise 只在运行期红）
- 位置：`internal/guards/e2eciaccount_test.go:56-61`；`mise.toml:144-229`（12 个 `e2e:*`）；`ci.yml`（12 处 `mise run e2e:*`）
- 证据：

  ```go
  // e2eciaccount_test.go:56-61
  // 防盲：实测值之下即解析退化。
  if len(mise) < 8 {
      t.Fatalf("mise.toml scan found only %d e2e tasks (expected >= 8) — guard is blind, fix the scan", len(mise))
  }
  if len(ci) < 5 {
      t.Fatalf("ci.yml scan found only %d e2e invocations (expected >= 5) — guard is blind, fix the scan", len(ci))
  }
  ```

- 影响：若 `mise.toml` 任务表被重排成不同 TOML 形态、或 12 条里 4 条改了写法不再被正则命中，`len(mise)=8` 仍 ≥ 阈值 → 这 4 条从对账面消失，而"每个任务要么进 CI 要么白名单"的律文对它们不再生效。同族守卫（`writeclassguard` 钉 153、`transitionguard` 钉 150 函数）都用了实测值贴边，此处留了 33%/58% 的余量。另外解析只读任务头，`run = "sh e2e/xxx.sh"` 指向不存在的脚本不会红。
- 建议：阈值提到实测值（`>= 12`/`>= 12`，注释里已写明实测 12）；补一条 `run` 行提取 + `os.Stat` 脚本存在性断言（与 `registrysourceanchor` 的存在律同构）。成本 S。
- 状态：已亲验（阈值原文 + mise/CI 双向计数 + 12 个脚本与任务一一对应核对）。

### R12-L6｜蓝绿"零 5xx"探针把 4xx 一并算作健康：切换窗里若掉成 404，演练仍绿

- 维度：测试与守卫 / 可用性
- 严重度：L（脚本头注诚实地写的是"计数 5xx/传输错误"，ADR-0048 锚也是"零 5xx"；但用户可见断流在 traefik 侧就表现为 404）
- 位置：`e2e/dind-bluegreen.sh:217-227, 253-259`
- 证据：

  ```sh
  # :220-226
  out=$(/root/bins/h2cclient -host "$ROUTE_HOST" "http://$MGR_IP/" 2>&1)
  case "$out" in
    "STATUS 2"*|"STATUS 3"*|"STATUS 4"*) ;;
    *) echo $(( $(cat /tmp/bg-fails) + 1 )) > /tmp/bg-fails ;;
  esac
  echo $(( $(cat /tmp/bg-hits) + 1 )) > /tmp/bg-hits
  ```

- 影响：双代窗切换时若 router 快照短暂缺失或 host 规则未就绪，traefik 返回 404（默认后端），探针会计为成功 → "zero failed probes across the whole deploy" 结论掩盖了真实的用户可见中断。升级腿的同一探针反而更严（`dind-upgrade.sh:277-289` 要求 `code != 200` 即失败）。
- 建议：蓝绿探针改成白名单 2xx（与 upgrade 腿同口径），或至少在 4xx 时把 status 落到诊断文件并在收尾打印分布；若刻意保留（例如允许 3xx 重定向），把 4xx 从"通过"里摘出来。成本 S。
- 状态：已亲验（探针分支 + 收尾断言 + 升级腿对照）。

### R12-L7｜smoke 的告警阈值全链在 CI 常态环境下走"响亮 SKIP"，退出码仍 0——ADR-0041 锚 2/6 无自动承载

- 维度：测试与守卫
- 严重度：L（脚本诚实标注、staging 已验；缺口是"CI 绿"与"该链被验证"之间没有机器可读的区分）
- 位置：`e2e/dind-smoke.sh:706-774`
- 证据：

  ```sh
  # :715-717
  LABELED=$(docker exec "$DIND_CID" sh -c \
    'wget -q -O - http://127.0.0.1:8080/metrics 2>/dev/null | grep -c "container_label_fleetly_ns_app=\"0"' || true)
  if [ "${LABELED:-0}" -gt 0 ]; then
  ...
  else
    log "SKIP: cAdvisor container labels unavailable in this environment (nested visibility) — alert firing + real delivery chain validated on staging (ADR-0041 anchors 2/6 deviation note); asserting rule lifecycle + channel test delivery only"
  ```

- 影响：告警 firing→真投递→resolved 三跳（ADR-0041 锚 2 前后半、锚 6）在 CI dind 里通常不可达（脚本自己说"部分嵌套环境（CI dind）cadvisor 看不到内容器"），因此 CI 常态绿并不包含该链；跳过的唯一痕迹是一行日志，`fail:0` 与整套 job 成功无法区分"跑过"与"跳过"。这类"环境能力门控"是本仓 e2e 里唯一一处会静默降级断言的形态。
- 建议：跳过分支写一个机器可读标记（`echo "::warning::alert-chain skipped"` 或写入 `$GITHUB_STEP_SUMMARY`），并在某个具名 job（如 nightly/特权 runner）上把"跳过=失败"打开，保证"至少有一处环境真跑过三跳"。成本 S。
- 状态：已亲验（分支原文 + CI job 环境声明）。

## 5. 交叉观察（不在本模块但值得其他模块注意）

1. **R09（CI）**：`ci.yml` backend 的 codegen 漂移豁免是**唯一**未进 CI 的生成物纪律（console 侧有 `console:verify`），且注释给的"冷环境 diff"理由可以用路径过滤绕开。与 R12-M5 同一处，Lead 汇总时可归并。
2. **R02（engine）**：`observdomain.go` 的期望账本写点纪律（ADR-0082 §6）目前只有注释与行为测试；R12-M3 建议的守卫是本条承诺的最小机械承载，若 R02 侧已有同类静态检查请合并。
3. **R06（Provider）**：`capability.EnsureReport.Gens` 的回执记账在 apitest 面不可达（R12-M7）。若 R06 认为 contentequiv 的单测已足够，至少应把"两套假底座的 observation 语义"抽成共享断言（`testruntime` 已开了收编先例）。
4. **R15（验收声明）**：`docs/adr/0083-restart-seq.md:54` 的 `[ ] e2e:upgrade 升级首拍零滚断言` 未勾，但 `dind-upgrade.sh:401-433` 的 web/worker 零 task 增量断言**已隐含覆盖该面**（存量载体无 seq 标签 × 期望 0 → 若新代铸零标签，canonical 差 → 滚 → task 数变 → 红）。缺的是 ADR 说的"两分支判读口径"（现役载体带非零 seq 的预期红需人工判读）与 db 轴；建议要么补分支断言要么把该锚改勾并注明"隐含覆盖"。
5. **R08（接口层）**：`interfaces/grpc/fleetlygrpc/delivery.go:490` 有一个接口层 `time.NewTicker(500ms)` 轮询追尾循环（`StreamBuildLogs` follow）。语义上不是收敛骨架，但它是"接口层持有轮询环"的孤例，且 `loopguard` 扫不到它（R12-L2）——若 R08 认为可接受，建议在该处加一行"非收敛骨架，loopguard 例外"的锚注，避免下次架构守卫扩容时被误判。
6. **R14（安全）**：`e2e/dind-backup.sh:9-10` 把"密码经容器内材料文件宿侧捕获"标注为"演练专用面；生产面永不回显"，实现上凭据确以 `-p"$MY_PW2"` 这类形态进入 `docker exec` 的 argv（`:358`、`:391`、`:421`），在 dind 容器内 `ps` 可见。属一次性演练库的诚实边界，不建议按发现计；若 R14 认为需要，可在该处补一行"演练凭据允许进 argv（一次性库、容器内可见）"的显式边界注，避免读脚本的人误以为 argv 清洁律在 e2e 也成立。

## 6. 测试与守卫质量评估

**总评：守卫层是本仓质量最高的子系统，但"守卫文化"的强度分布极不均匀——AST 级、带形状用例与正向锚的守卫（dep_matrix/transitionguard/enginebackref/gatewayguard/registrysourceanchor/writeclassguard/statekitlaw/nilfacelaw/aggregatetestbelt）与字符串单针守卫（acceptanceguard/errmappingguard/loopguard/coverageguard）差了一个数量级；后者恰是覆盖面最"看起来关键"的几个。**

### 6.1 覆盖强度（本模块 41 守卫的红灯实验结论）

| 守卫 | 违规写入会红吗 | 判定依据 |
|---|---|---|
| dep_matrix | **会**（且新增子树/僵尸豁免双红） | AST import 集 + 四段保鲜 + 零文件 fatal |
| domainpurity | 会，但裸 `net` 漏网 | 三红线匹配器表（L1） |
| importguard | **会**（providers 圈禁/叶子纯度/providers 唯一消费方三向） | AST + 前缀表 |
| transitionguard | **会**（且接收者泛化到域 struct，`sawTransitFront` 防盲） | AST 调用链 + 150 函数下限 |
| writeclassguard | **会**（153 总数钉死，新增 RPC 不改表即红） | proto 行扫描 + policy 真 descriptor |
| freezeguard | 会，但读前缀自动归类给出一条"命名即免冻结"通道（`isReadVerb` 9 前缀）；39 条豁免 vs 42 条冻结 | `freezeguard_test.go:92-106`、`:20-88` |
| factdual | 部分：判据是"声明窗口内有任意注释"，**注释质量不判**——一句 `// TODO` 即可满足 | `factdual_test.go:121-128`（`hasReason` 只问 commentLines 非空） |
| factappend | **会**（`.Append(` 选择器 + kernel 目录唯一） | AST |
| acceptanceguard | **不会**（针失配，M1）；create/delete 面由另一测试兜 | M1 |
| sizeguard | **会**（1200 行 + 白名单失效双红；现零豁免） | `sizeguard_test.go:48-65` |
| registry*（sourceanchor） | 会（路径存在律），函数限定符不执法 | L3 |
| e2eci | 会（漏挂/悬空/收编过期三向），防盲余量过大 | L5 |
| coverage | **不会**（裸子串） | M2 |
| loopguard | **不会**（单字面量 + 无正向锚） | L2 |
| errmapping | 只会拦一种拼写 | L4 |
| gatewayguard | **会**（AST 符号面，注释掉注册行即红） | `gatewayguard_test.go:94-101` |
| enginebackref | **会**（87 具名 struct 下限 + Engine 声明在场双防盲） | `enginebackref_test.go:196-203` |
| scopeguard | **会**（未知动词即红——动词表完备性双向把守，这条比 freeze 的"读前缀缺省"更严） | `scopeguard_test.go:107-110` |
| wording | 会，但只对 55/168 词条 | M6 |
| teamaxis / shellguard / rowmint / statekitlaw / nilfacelaw / aggregatetestbelt | 均为 AST（或目录/文件形态）级且带豁免双向保鲜 | 逐条已读 |
| idem | 会（proto 行状态机 × `idem.EnforcedMethods` 双向；但动词前缀集是闭集，见下） | `idemguard_test.go:44-81, 137-188` |
| uow | 会（`state.Runner` 用源文本子串、`database/sql` 用 AST import，混合判据） | `uowguard_test.go:57-84` |

**空白面（没有守卫也没有点名去处）**：
- ADR-0082 §6 期望账本写点（M3）；
- `interfaces/` 层的"读径不得写"（`dep_matrix` 只执法依赖方向，`acceptanceguard` 只执法 7 个前缀的 commit 调用，两者之间的缝隙就是 M1 的位置）；
- 幂等面的反向完备性：`idemguard` 的创建型动词集是 6 个前缀的**闭集**（`idemguard_test.go:33`），闭集外的 create 型 RPC 靠人工显式入表（`DeclareNetworkPeer`/`ReceiveWebhook`/`TriggerBackup`/`RebuildNetwork` 均有注记）；`InstantiateTemplate`/`UploadSource`/`EnrollNode` 当前不在表内，靠各自 handler 的 create-or-reuse 语义自证，而 `writeclassguard` 只做 `idem ⊆ 写面` 单向断言（`writeclassguard_test.go:330-376`），不会反向要求"新 create 动词必须入幂等面或登记豁免"。今天无违规实例（三者语义上重放安全），但"新 create 动词忘了入表"这件事没有任何守卫会红；
- `console/src` 之外的前端纪律（无损，属 R11）；
- `e2e/*.sh` 的静态纪律（无词汇扫描、无"必须含 fail 出口"这类形态断言）。

### 6.2 断言强度（apitest）

- 真 SQLite 文件库 + 假时钟（`statetest.go:54-67`），无 `t.Parallel`（0 处），仅 3 处 `time.Sleep`（`events_stream_test.go:81` 的 2×poll 间隔、两处 50ms 让位）→ **flaky 面很小**，确定性设计合理。
- 假时钟被显式推进 19 处（`h.Clock.Advance(...)`：61s/120s/8d/10h），说明超时/保留窗/租约类语义是真测的，不是"等墙钟"。
- `TestSkip` 全仓仅 5 处（`build_test.go` 2 处需 `FLEETLY_TEST_DOCKER`/`FLEETLY_TEST_REGISTRY_ADDR`、`dbtemplate_live_test.go` 需 `FLEETLY_TEST_LIVE=1`、`platformbackup_test.go:183` 反向跳过、`cli/cmd/coverage_test.go:21` 仅 `-update` 模式）→ 没有被悄悄跳掉的主路径。
- 弱面：假 Runtime 的回执/观测面（M7）；`slog.DiscardHandler`（`harness.go:115`）意味着"日志里不得出现 secret"这类可观测性断言在 apitest 面不可能做（今日由 e2e 的 `dind-smoke.sh:539` "hook token absent from fleetlyd logs" 承担，属正确分工）。

### 6.3 e2e 断言强度（13 脚本横向）

- **强项**：状态/数据双校验（备份演练断言到值）；否定断言密度高（零 drift、零 5xx、无残留载体、无匿名卷、无 hook token 泄漏）；清理齐备（13/13 有 `trap`，`dind-upgrade.sh` 还清 git worktree）；超时全部有界（`while i<N` + 失败出口）；预拉镜像写进 CI 且脚本内有 save|load 兜底；`dind-upgrade.sh` 用 HEAD 归档快照构建新代（hermetic 同口径）。
- **假阳性风险清单**：①蓝绿 4xx 容忍（L6）；②smoke 告警链 SKIP（L7）；③cAdvisor/嵌套可见性类的"能力门控"若继续增长，会让 CI 绿与实际覆盖进一步脱钩（当前 1 处，形态干净但无机器标记）；④状态等待一律"取 JSON 里第一个 `state`"（`dind-upgrade.sh:186`、`dind-bluegreen.sh:135`）——JSON 是缩进多行所以当前安全，但它依赖 CLI 输出字段序与"每行一字段"，一旦 `--json` 改紧凑输出或多行嵌套，取值会静默错位；建议改为 `grep -o '"state": *"[a-z]*"' 并断言唯一`。
- **对 CI 的依赖假设**：脚本内注释声称"dind 内不走外网"（`ci.yml:101-102`、`dind-upgrade.sh:24-27`），实况是预拉只覆盖脚本明列的基础镜像（smoke 只预拉 `docker:29-dind` + `nginx:1.27`，但受管 VM/cAdvisor/zot/traefik 由 dind 内拉取），因此这些腿仍依赖 runner 出网。这不是缺陷（CI runner 有网），但"离线确定性"的叙述与实现有偏差，建议措辞收窄。

### 6.4 假阳性/假阴性总结

- 假阴性（该红不红）：M1、M2、M3（无守卫）、L2、L4、M6（2/3 词条）。
- 假阳性（不该红会红）：未发现明显项。`wordingExemptions`/`sizeguard`/`aggregatetestbelt` 的"零命中即红"会随合法重构报红，但这是设计意图（债务到期提醒），且 35/0/0 条的规模可控。
- 豁免面增长实况（供 Lead 判断治理成本）：`freezeExemptions` 39 条、`skippedTokens` 113 条、`wordingExemptions` 35 条、`depExemptions` 5 条、`actionReadScopeExemptions` 3 条、`idemActionExemptions` 4 条、`scopeOpExemptions` 5 条、`nilFaceExemptions` 2 条、`acceptanceExemptions` 3 条、`sizeguard/aggregatetestbelt/shellguard/portCoverage/deleteCoverage` 均 0 条。**除 skippedTokens 外全部带双向保鲜与理由，机制本身没有腐烂**。

## 7. 未决与置信度

**最不确定的 3-5 点**

1. **M4 的"该不该由 e2e 承担"**：ADR-0082 未在验收锚里要求 e2e（全部锚都是单测/golden），因此"缺 e2e"可以读成"设计取舍"而非缺口。我的判断依据是 PROFILE §7 把"收敛缺省在 App 域的缺席检测"列为高风险优先区、且该能力的失败形态（自激/不收敛）是**时间相关**的，单测无法覆盖真实时钟 + 真实 Provider 的组合。若 Lead 认为单测足够，本条可降 L 或撤销。
2. **M7 的成本收益**：给 apitest 补假回执/观测会扩大夹具面积（与"夹具必须与生产同源"的初衷有张力）。我倾向补"跨夹具一致性断言"而非补全 Provider 语义——这条建议的形态我置信度中等。
3. **M6 是否算缺陷**：`skippedTokens` 的存在是**显式设计**（注释与 `CONTEXT.md` 分诊机制都写了"人工评审"），把它报成缺陷有"评设计不评实现"的风险。我把它保留为 M 的理由是"人工评审"没有任何流程载体可复核，属于可验证的机制缺口而非口味。
4. **freezeguard 的读前缀自动归类**：我通读了两张表并抽查了跨表一致性（含最像"铸造族"的读前缀成员 `DownloadBackup`，核实其 handler 为纯读流、不落行不铸凭证，归类正确），未发现写动词被读前缀吃掉。因此没有单列发现，只在 §6.1 标注为通道；完整逐条语义复核属 R08。
5. **e2e 未真跑**：所有"e2e 会红"的判断都基于脚本静态阅读；若某脚本存在未被 CI 覆盖的本地路径（例如 `.tmp-local-restic` 分支），我的评估仅覆盖 CI 路径。

**整体置信度：高**（守卫层为纯静态可复核对象，41 个守卫全部亲读或精读，且已实测 `go test ./internal/guards` 全绿；e2e 结论限于断言文本层面，已明确标注未真机复验）。

**本模块最值得先修的三件事（按投入产出）**

1. R12-M1（S）：API 层手搓事务检测器现在是零断言，修法是一段 20 行 AST。
2. R12-M3（S）：ADR-0082 §6 写点守卫，写点面只有 3 个函数、4 处赋值，成本极低而防线价值高。
3. R12-M5（S–M）：把 `verify:hermetic`（9s）塞进 CI，并让 G20 对账覆盖 `generate:verify`/`verify:hermetic` 这两个"只活在文档里"的门禁。
