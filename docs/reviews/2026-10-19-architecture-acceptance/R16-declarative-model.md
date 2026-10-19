# R16 核心声明式模型专项评审报告

> 范围：`docs/design/2026-10-18-declarative-model.md`（全境 268 行）＋ ADR-0081/0082/0083 ＋ 程序计划 ＋ 落码（engine 收敛/重启面、spec diff、写类守卫、双 Provider 内容等价、CLI/Console/Skills、doctor/alerting、config 三旋钮）＋ 附录 A/B/C 29 项裁决。
> 定位：对 R02（部署/收敛链）与 R15（验收声明）的**对抗复核 ＋ 补充**——只计他们未覆盖/说错的；重叠项逐条标「已知，复核一致/不一致」。
> 基线：`main@35257f18`，只读；实验经 `go test -overlay` 仓外注入（临时件在评审目录 `_tmp_r16_*`）。

---

## 1. 覆盖清单

### 1.1 完整读过（生产件）

- 设计/立法：`docs/design/2026-10-18-declarative-model.md`（268 行全文）、`docs/adr/0081-write-class-author-class.md`（282 行）、`docs/adr/0082-converge-by-default.md`（97 行）、`docs/adr/0083-restart-seq.md`（60 行）、`docs/plan/2026-10-18-declarative-model-program.md`（38 行）、`CONTEXT.md` 相关词条（Write Class / Action / Declaration / Author Class / Converge / Posture / Spec）。
- engine 收敛与重启面：`converge.go`（568）、`drift.go`（413）、`observ.go`（313）、`observdomain.go`（310）、`transition.go`（484）、`admission.go`（329）、`teardown.go`（78）、`peers.go`（252）、`bluegreen.go`（245）、`drive.go`（405）、`restart.go`（67）、`processrestart.go`（58）、`componentrestart.go`（68）、`options.go`（267）、`materialize.go`（87）、`ensure.go`（74）、`render.go`（139）、`loop.go`（87）、`lifecycle.go`（141）、`rollback.go`（56）、`materials.go:236-312`（pin/pinning）。
- 契约/守卫/配置：`internal/domain/shared/spec/diff.go`（169）、`diff_restart_test.go`、`internal/guards/writeclassguard_test.go`（376）、`loopguard_test.go`、`capabilitypurity_test.go`、`domainpurity_test.go:1-110`、`freezeguard_test.go:80-165`、`idemguard_test.go:60-140`、`internal/domain/shared/capability/runtime.go:1-80,270-310`（另 grep `:400-440` 的 Workload/EnsureReport 字段）、`internal/config/config.proto:195-215`、`internal/config/defaults.go:20-110`、`internal/assembly/provides.go:487-523`（抽样）。
- 测试：`converge_test.go`（617）、`drift_test.go`（385）、`restart_test.go`（233）、`bluegreen_test.go`/`teardown_test.go` 测试名清单、`swarm/runtime_contentequiv_test.go:160-240`、`k3s/runtime_contentequiv_test.go:115-190`。
- 接口面：`fleetlygrpc/delivery.go:36-135`、`structure.go:433-566`（DeleteApp）、`networks.go:340-381`（RevokeNetworkPeer）、`identity.go:172-202`（SetUserPassword）、`alerting.go:362`（抽样）、`cmd/fleetly/cmd/verbs_structure.go:240-522`、`apps_restart_golden_test.go`、`workloads_view_golden_test.go:86-131`、`cmd/fleetly/cmd/doctor.go:250-260`。
- Providers：`swarm/runtime.go:100-280,354-446`、`swarm/translate.go:60-159`、`k3s/runtime.go:41-160,920-1000`、`k3s/translate.go:30-180`。
- 既有结论：`findings/R02-engine-deploy.md`（全量标题 + M1/M2/M3/M4 正文 + §5 交叉观察）、`findings/R15-acceptance.md`（标题 + §5 交叉观察 + §6）。
- 文档：`docs/runbooks/staging-fleetly.md:17-19`（freeze 优先逃生序）、`skills/deploy-diagnose/SKILL.md:105-160`、`docs/design/2026-09-30-domain-model.md:166`、`docs/design/2026-10-16-functions-api.md:111`、`docs/adr/0005:3`、`docs/adr/0022:5,38`。

### 1.2 抽样读过

`swarm/runtime.go` 其余段（ensure 网络解析/材质）、`k3s/runtime.go:160-920,993-1067`、`governance/freeze.go`（表与拦截器；表项经脚本对账 42）、`projection.go:120-140`、`console/src/api/{delivery,structure}.ts` 的 restart_seq 类型面、`internal/assembly/testdata/selfdescription.golden.json` 的 restart_seq 段（grep）、`docs/runbooks/staging-fleetly.md:17-19`（逃生序两行）。

### 1.3 未覆盖/无法覆盖

- 真机面：e2e:dind / e2e:upgrade（本机 Docker 不可达）；ADR-0083 第 8 锚「升级首拍零滚」仍未执行（ADR 自标 `[ ]`，属 R15 已记挂账）。
- Console UI 实渲染（仅有类型面；无重启控件，见 §4A-E 链路）。
- `go test -race` 全量（本轮只跑定向包，见附录）。
- swarm/k3s 真实编排器行为（内容等价用测试与代码推断）。

### 1.4 独立实验（仓外注入，未改仓库）

1. `_tmp_r16_restartseq_probe_test.go` + `_tmp_r16_spec_overlay.json`：`go test -overlay` 打入 `spec` 包，**证实 `RestartSeqOnly` 被 env 键名欺骗**（见 M1，实跑输出）。
2. 独立清点 proto：14 个服务文件 / 36 个 service / **153 rpc**（与 ADR-0081 台账一致；逐文件计数 automation 16 / delivery 22 / github_apps 7 / registry 2 / templates 4 / identity 23 / proxy 3 / exec 3 / runtime 6 / workloads 1 / structure 42 / governance 3 / system 7 / telemetry 14）。
3. 写类表 × proto × 冻结表机械对账：ACTION 27 / DECLARATION 53（无交集）/ FrozenVerbs 42 = 34 DECL + 8 ACTION，unmatched 0；`scopeguard` 豁免 5 条。
4. 定向测试：`go test ./internal/guards -run 'TestWriteClass|TestFreezeSurface|TestScope|TestIdem'` → ok；`go test ./internal/application/engine -run 'TestConverge|TestRestart|TestFreeze|TestAbsence|TestGhost|TestTeardown|TestGenLabel|TestTerminalRollout|TestPosture|TestCancel|TestCompareSpecs|TestProjection'` → ok。

---

## 2. 模块画像与边界

**职责**：把"期望态"与"运行时"的缺口收敛掉的那一整套机制——期望态持有（Revision 冻结 / 实体行）、差异检测（driftScan）、决策（裁决表 + 闸门）、动作（materialize→Ensure）、记账（签名/冷却/审计/事件），以及"重启"这个横跨声明面与动作面的双面动词。

**对外接口**（本模型新增/变更面）：
- RPC：`Deploy`/`Rollback`（声明写入口）、`RestartProcess`（动作面）、`GetAppWorkloads`/`InspectAppWorkloads`（只读观测）、`SystemConvergeAlertState`→`ListAlertStates`（可发现性）、config 三键（`engine.converge_posture` / `failure_threshold` / `cooldown_secs`）。
- CLI：`apps restart`（缺省声明面 / `--runtime` 逃逸）、`apps workloads`（RESTART_SEQ 列）、`doctor`（收敛冷却行）。
- 事件/审计：`workload.converged`（流+审计）、`workload.converge_failed`（流+告警沿）、`deployment.transit`（`; kind=restart` 注记）。

**依赖方向**：`converge.go` 只依赖 `deployment`/`audit`/`writes`/`capability`/`state`/`alertrule`，未越环；`spec/diff.go` 在 domain 叶子并 import `genproto/fleetly/spec/v1`——这是**已登记豁免**（`internal/guards/domainpurity_test.go:46-50`：subtree `internal/domain/shared/spec/`、只放行 `spec/v1` 自身类型、带理由、双向保鲜），非新违例。`RestartSeqOnly` 被 engine 与守卫共享为"收敛面/漂移面分际"的共同法源，位置合理。

**与声明的偏差（一句话版）**：
1. 四不变量只有"内容寻址等价"有真正执法面（两层测试）；"期望态单一真源"只在缺席检测一行实现，spec/gen 两行仍以内存账本为期望侧（M3）；"Ensure 唯一写动词"是端口形状的**弱**执法（Remove/Restart 同端口；且存在 RPC 路径直调 Ensure，M4）；"观测与收敛分面"靠结构惯例（Watch 消费者只发事件），无守卫。
2. 三作者模型只有 Doctrine（ADR-0081 §2）＋两处结构性实现（卷钉住、库 env 注入），**零可执行权属载体**；ADR 明示推迟到 autoscale，故不算违约，但"学说层已立、执法面空白"成立（L6）。
3. 裁决表十行里行 8/9/10 无检测落点（行 8/10 靠慢通道的 Ensure 副作用兑现，行 9 连"登记"事件都不存在），`convergeVerdict` 生产零引用（R02-M2 已报）。
4. `restart_seq` 八步全链有落点，但两处语义断点：CLI 取"最新冻结"而非生效基线（H1）、seq-only 判定用 diff 路径字符串后缀（M1）。

---

## 3. 优点（带证据）

1. **收敛动作的单一原语与竞态闭合做得扎实**：`attemptConverge` 把 posture→冷却→冻结→`lockApp`→teardown 标记→驱动权复查→基线 freshness 复查串成单点，且"扫描到锁之间基线已更替"用 `fresh.ToRevision != d.ToRevision` 放弃本拍（`converge.go:340-349`）——这条避免"拿旧基线把新代翻回去"的复查，比 ADR 文本（B2 只说驱动权）更严。`materialize` 是 release/rollback/基线重放/收敛四路唯一序列（`materialize.go:19-38` 注释 + `TestMaterializeThreePathsIdentical`），历史上"三份拷贝在卷钉住上分叉"的教训被结构性收口。

2. **ABI 级的防自激三件套是真的**：provider 侧 no-op 断路器（`swarm/runtime.go:166-178`）＋ canonical 内容等价（`:377-436`，含代戳与 restart_seq "0" 归一）＋签名生命周期绑定结果（`convergeSucceed` 才落 `resolved`，`convergeFail` 只计冷却）——三者都在同一拍序里生效，不是纸面。

3. **写面立法的执法面是"可静态执法 ADR"的成熟样板**：`writeclassguard_test.go` 以 line-scan proto 全集对账 153、断言 27/53/153 三实数、死条目红、scope 单向断言 + 豁免双向保鲜（`:183-376`）；本轮独立机械对账（proto 153、两表 27/53 无交集、FrozenVerbs 42=34+8、豁免 5）与守卫一致，且守卫实跑绿。`TestWriteClassCompleteClassification` 的失败路径是真 `t.Errorf`（会红），不是 skip。

4. **restart_seq 的升级零滚裁决落码完整**：seq=0 不铸标签（`swarm/translate.go:145-147`）、canonical 对 "0" 归一到缺失（`swarm/runtime.go:438-446`、`k3s/runtime.go:963-972`）、双侧四用例 + 存量×期望0 skip（`runtime_contentequiv_test.go` 两侧），并有 `bb5259d` 前车注释锚。这是"迁移语义"被当成一等设计问题处理的少数案例。

5. **可发现性三面齐全**：`workload.converge_failed` 进 eventcode 注册表 + golden + usage 反扫（`eventcode/events.go:52-53`、`events.golden:95`、`schema.golden:99`）；doctor 收敛冷却行带处置建议（`doctor.go:252-258`）；`SystemConvergeAlertState` 并入 `ListAlertStates`（`alerting.go:362`）并有测试断言 alert 沿（`converge_test.go:488`）。

6. **`restart_seq` 的执行时判定（B8）确实延迟到驱动器拾取时**并对实际 `LatestSucceeded` 重算（`restart.go:31-57`），且有构造"受理锚过期"的钉测（`restart_test.go:147-186`）；BG 舞步不被 kind=restart 改变也有钉测（`:191-225`）。

---

## 4. 发现清单

> 排序：H → M → L。每条带 `文件:行` + 摘录；重叠项标「已知」。

### R16-H1｜`apps restart` 以"最新冻结 Revision"为源，而非生效基线——失败/取消/回滚后的"重启"会重新下发未生效 spec 并重跑迁移
- 维度：功能（兼架构一致性、可靠性）
- 严重度：**H**。校准：ADR-0083 §1 步 8 的"迁移不得因重启重跑"与 CLI 自身 synopsis（`no migration re-run`）是**无条件承诺**；本路径在"部署失败→自动回滚→重启"这一日常处置序上必然破约，且静默把失败 spec 重新上线。若认为"最新冻结=操作者最后一次声明"是合法语义，则降为 M——但那样 CLI/skill 的承诺文案必须先改。
- 位置：`cmd/fleetly/cmd/verbs_structure.go:424,456,460-473,481,497-521`；`internal/application/engine/rollback.go:41-43`（回滚复用旧 Revision，不冻结新号）；`internal/application/engine/restart.go:42-55`（判定对 LatestSucceeded）；`internal/application/engine/processrestart.go:31`（动作面同样取 Revisions.Latest）；`skills/deploy-diagnose/SKILL.md:114-116`
- 证据：
  ```go
  // cmd/fleetly/cmd/verbs_structure.go:456-472
  latest, err := latestFrozenRevision(ctx, c, app)
  ...
  got, err := c.Revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: app, Seq: latest.GetSeq()})
  ...
  for _, p := range spec.GetProcesses() {
      if p.GetRestartSeq() == ^uint32(0) { ... }
      p.RestartSeq = p.GetRestartSeq() + 1
  }
  // :497-498 注释原话：latestFrozenRevision 取 App 的最新冻结 Revision（R 号最大者）
  ```
  ```go
  // internal/application/engine/restart.go:42-49——判定却对"实际最新 succeeded"重算
  if base.ToRevision == d.ToRevision { return nil, nil }
  fromSpec, err := e.loadSpec(ctx, base.ToRevision)
  ...
  seqOnly, err := specir.RestartSeqOnly(fromSpec, toSpec)
  ```
- 影响：复现链（全部亲验于代码）：① 部署 R2 失败 → 自动回滚成功，succeeded 行 `to_revision = R1`（`drive.go:354-367` 把 `ToRevision` 改回 `FromRevision`），R2 仍是最大冻结号；② 运维按 skill 的恢复动词执行 `fleetly apps restart --app A`；③ CLI 取 R2、对全进程 seq+1、以 `spec_file` 提交 → 冻结 R3；④ `prepare` 里 `markRestartSeqOnly` 以 `LatestSucceeded=R1` 为基线重算 → `diff(R1,R3)` 含镜像等真实变更 → **非 seq-only** → `PreDeploy` 不置 done → `preDeployJobs`（数据库迁移）重跑，并再次把失败的 R2 内容滚动上线（大概率再失败再回滚，抖动）。同源次生问题：`--runtime` 的进程存在性校验对 `Revisions.Latest` 做（`processrestart.go:31`），若新冻结版删了某进程而该进程正在基线里跑，动作面逃生通道会误报 `unknown process`（反之放行一个不存在的进程）。
- 建议：CLI/服务端二选一——(a) 新增 engine 侧 `RestartApp` 动词，以 `LatestSucceeded` 的 `ToRevision` 为源 bump seq（`RenderWorkloads(seq=0)` 已能给"当前生效"）；(b) CLI 改取 `apps workloads`/`GetAppWorkloads` 的生效 Revision（或 `revisions get` 前先 `deployments list --state succeeded`）。同时把 `--runtime` 校验改到生效基线。成本 S–M。测试：构造"失败+回滚后重启"，断言 `PreDeploy==done` 且新 ToRevision == R1+seq。
- 状态：**已亲验**（CLI 代码 + 冻结/回滚/判定三链）；未跑 e2e。与 R02 的重叠：R02 §5 交叉观察 #4 已用一句话提到"restart 会把较新的失败 Revision 重新冻结部署、静默撤销回滚声明"，但未计入其发现清单、未给证据链、未指出"迁移重跑"这一与 ADR 步 8 直接冲突的后果；R15 未覆盖。本轮建议计入主清单。

### R16-M1｜`RestartSeqOnly` 用 diff 路径字符串后缀判定：用户 env 键名 `restart_seq` 可把普通变更伪装成声明式重启，静默跳过 preDeployJobs
- 维度：功能（兼数据安全、测试与守卫）
- 严重度：M。校准：后果是"迁移被静默跳过"（数据面风险），但触发要求 env 键名精确等于 `restart_seq`（或以其结尾、带点号），概率低且无攻击增益，故不取 H。
- 位置：`internal/domain/shared/spec/diff.go:138,140-168`；`internal/application/engine/restart.go:49-56`；`internal/application/engine/drive.go:123-129`
- 证据（亲验，`go test -overlay` 实跑）：
  ```go
  // internal/domain/shared/spec/diff.go:138,163-168
  const restartSeqField = ".restart_seq"
  ...
  for _, e := range entries {
      if !strings.HasSuffix(e.Path, restartSeqField) {
          return false, nil
      }
  }
  return true, nil
  ```
  ```
  PROBE diff entry: "processes[0].env.restart_seq" old=v1 new=v2
  r16_probe_test.go:50: BUG CONFIRMED: env-only change classified as restart_seq-only
  (preDeployJobs would be skipped)
  ```
- 影响：`ProcessSpec.env` 是 `map<string,string>`（`proto/spec/v1/spec.proto` env=5），`Diff` 把 map 键拼成点分路径（`diff.go:47-79`），因此**任何以 `restart_seq` 结尾的 env 键**（`restart_seq`、`db.restart_seq` 等）都命中后缀判定。后果链：这类普通配置变更被 `markRestartSeqOnly` 判真 → `transitDeploymentAnnotated` 置 `PreDeploy=done` + 审计 `kind=restart` → release 直进 carrier 相位 → **迁移不跑**；BG 进程同样命中（判定不改舞步）。对 `pre_deploy_jobs[i].process.env.restart_seq` 同理。
- 建议：改成类型化判定：把两份 spec 的 `restart_seq` 全部清零后 `proto.Equal`（或 protoreflect 只认 ProcessSpec 字段号 18），并要求至少存在一条 seq 差异；顺带免疫未来任何嵌套 map 键碰撞。加回归测：env 键 `restart_seq` 变更 → `RestartSeqOnly == false` 且 `PreDeploy != done`。成本 S。
- 状态：**已亲验**（临时 overlay 测试，实跑失败即证）。

### R16-M2｜posture=detect 的语义双向错位：baseline 源（启动基线重放 + 撤销 peer 的隔离重放）绕过门限照写；慢通道被门限拦住导致 Cancel 遗留永不回卷
- 维度：架构一致性（兼可靠性、可观测性、文档一致性）
- 严重度：M。校准：detect 是运维"观察期逃生门"（CONTEXT Posture 词条、ADR-0082 §5），错位使逃生门在启动/撤销 peer 两个场景失效（静默写），同时使唯一能修 Cancel 遗留的慢通道失效；不改多租户/数据安全，故 M。
- 位置：`internal/application/engine/converge.go:296-301,304-314,416-418`；`internal/application/engine/drift.go:93-96`；`internal/application/engine/peers.go:166,213-215`；`internal/application/engine/converge_test.go:530-536`；`CONTEXT.md:286-287`；`docs/adr/0082-converge-by-default.md:65-67`
- 证据：
  ```go
  // converge.go:296-301——posture 门只拦非 baseline 源
  if src != convergeSourceBaseline && e.opts.Converge.Posture == ConvergePostureDetect {
      return
  }
  // drift.go:94-96——启动重放与 peer 隔离重放都走 baseline 源
  func (e *Engine) replayAppBaseline(ctx context.Context, a *app.App, d *deployment.Deployment) {
      e.attemptConverge(ctx, a.ID, d, "", convergeSourceBaseline)
  }
  ```
  ```go
  // converge_test.go:530-536——测试把"例外"钉成了期望行为，而不是抓住它
  // 启动基线重放不受 posture 门限（ADR-0005 旧行为保留：缓存重建是
  // 检测面的前提）。
  e2 := New(Deps{...Posture: ConvergePostureDetect})
  e2.rebuildBaselines(context.Background())
  assert.Greater(t, len(rt.calls()), before, "baseline replay still runs under detect")
  ```
- 影响：① 启动时 `rebuildBaselines` 对全部有基线 App 走 `attemptConverge` → `materialize` → `Ensure`：detect 语义下**仍会纠正载体漂移**，且因 `convergeSucceed` 对非 fast 源直接 return（`:416-418`），**不发 `workload.converged`、不落审计**——观察期里"谁动了我的载体"零信号；② `RevokeNetworkPeer` 的即时隔离（`peers.go:166`）与每拍 `enforcePeerIsolation`（`:214`）同用 baseline 源——detect 下仍会改网络附件；③ 反向：Cancel 遗留载体的唯一兑现面是慢通道重放（见 M3），慢通道 `src=slow` 被同一门限拦住 → **detect 下 Cancel 遗留永不回卷**（ADR-0082 行 10 的裁决在 detect 下静默失效）。ADR-0082 §5、CONTEXT Posture 词条都只写"只报事件不收敛"，未写 baseline 例外；代码用"ADR-0005 旧行为保留"作为法源，而 ADR-0005 的 opt-in 条款正是被 ADR-0082 推翻的那一条。
- 建议：(a) 让 detect 下的启动重放只重建期望账本（`recordEnsured`）而不下发 Ensure——"缓存重建是检测面前提"这个理由不支持"必须写载体"；(b) 把"隔离重放"从 baseline 源拆出，或同样受 posture 门限；(c) 无论选哪条，都在 ADR-0082 §5 + CONTEXT Posture 显式登记例外，并把 `TestPostureDetectSkipsConverge` 的断言从"仍会写"改为"不写/只记账"。成本 M。
- 状态：**已亲验**（代码 + 钉测）。与既有结论的关系：R15 §5 交叉观察 #8 已提"detect 的启动例外"但定为 L，且称"`TestPostureDetectSkipsConverge` 不覆盖启动基线路径"——**该判断不准确**（`converge_test.go:530-536` 明确覆盖并断言了该路径，正因为被钉成期望才使这一矛盾长期绿色）；此外其漏了第二个逃逸源（IsolateNetworkPeer）与反向后果（Cancel 永不回卷）。建议升 M 并修正描述。

### R16-M3｜期望账本被"未成功部署的物化"写脏：spec/gen 两类快通道的期望侧仍是内存账本，Cancel 后检测面对遗留载体失明（不变量 1 只在缺席检测一行执法）
- 维度：架构一致性（兼可靠性）
- 严重度：M。校准：后果是"该修的漂移只能等 60s 慢通道（或 detect 下永不修）"＋"重启冷窗检测盲"，不产生错误状态；但直接推翻"决策输入必须是期望态真源"的承诺，且是 R02-M5 现象（Cancel 只走慢通道）的根因。
- 位置：`internal/application/engine/materialize.go:75-85`（Ensure 后立即记账，不看部署终态）；`internal/application/engine/admission.go:284-307`（Cancel 不清账）；`internal/application/engine/drift.go:243-245,259-263,289-299`（spec/gen 期望侧取账本）；`internal/application/engine/converge.go:110-125`（recordEnsured 是唯一写点注释）；`observdomain.go:44-61`
- 证据：
  ```go
  // materialize.go:82-85——releasing 阶段物化即记账（与部署是否成功无关）
  for i := range ws {
      ws[i].Generation = uint64(report.WireGen(ws[i], capability.Generation(gen)))
  }
  e.recordEnsured(d, gen, ws)
  ```
  ```go
  // drift.go:243-245,259-263,289-299——spec/gen 对照的期望侧全是内存账本
  expected := e.expect.expected[appOwner(appID)]
  ...
  want, ok := e.obs.ensuredSpecOf(o.WorkloadID)
  if !ok { continue }
  ...
  anchor := expected
  if g, ok := e.obs.ensuredGenOf(o.WorkloadID); ok { anchor = g }
  if uint64(o.Generation) != anchor { signals["gen|"+o.WorkloadID] = ... }
  ```
- 影响：① **Cancel 遗留（rolling 形态）**：部署在 releasing 阶段已下发 gen2（部分物化）→ 运维 Cancel → 账本仍是 gen2 + 新 spec；此后观测（gen2/新镜像）与账本完全一致 → spec 与 gen 两路**零信号**；遗留载体是"多余观测"而非"缺席期望"，缺席检测也不响 → 只有 60s 慢通道整拍重放能把它拉回基线（与 R02-M5 观察一致），在 posture=detect 下永不拉回。**BG 形态是个例外**：期望集带代次化 ID，基线投影（g1）对观测（g2）会命中 `absent` 快信号，回卷仍由快通道兑现——所以"Cancel 遗留不可见"只发生在 rolling（含混合策略里的 rolling 进程）。ADR-0082 行 10 写的机制"缺席检测把遗留载体判为期望集外"与实现不符（遗留是 extra，不是 missing）。② **重启冷窗**：进程重启后账本为空 → spec/gen 两路 `continue` 跳过（`if !ok { continue }`），ADR-0082 §6 只为"缺席检测"成文了冷窗方向安全，未覆盖 spec/gen 两行的失明窗。③ 不变量 1（§1.1 #1）写"收敛判定与收敛动作的输入都必须是期望态真源（Revision/实体行），不是缓存"——只有行 2 做到；行 1/3 的期望侧是"最近一次 Ensure 的投影"（写侧账本），语义是"上次下发了什么"而不是"期望是什么"。
- 建议：在 Cancel / 无回滚的 failed 终态处把账本复位到 succeeded 基线（`forgetOwner` + 以基线 gen 记账，等价 `replayAppBaseline` 的记账半边），或让 spec/gen 对照也走 `projectRevision(LatestSucceeded)` + isolate 重投影（与缺席同源）；至少补测：Cancel 后 `expect.expected[app] == 基线 gen`，且载体回gen2 时产生 `gen|` 快信号。成本 M。
- 状态：**已亲验**（物化记账时序 + Cancel 不清账 + 对照面读账本；`TestCancelLegacyConvergesBackToBaseline` 仅断言最后一次 Ensure 调用，见 §6）。与 R02 重叠：R02-M5 已报"Cancel 实际走慢通道、无审计无事件"，本条的增量是**根因与行 10 机制描述不符**、以及 detect 下永不修复。

### R16-M4｜`RevokeNetworkPeer` 在 gRPC 处理器内同步执行收敛写（Ensure），是环/管线之外的第二个写入口
- 维度：架构一致性（兼性能、可靠性）
- 严重度：M。校准：不产生错误状态（`appMu` 互斥仍在），但把"写永远经收敛机构（环或部署管线）"变成不成立，且请求延迟随项目内 App 数线性叠加（每 App 一次 Ensure，`boundedStep` 上限 30s）。
- 位置：`internal/interfaces/grpc/fleetlygrpc/networks.go:369-375`；`internal/application/engine/peers.go:135-168`（循环 App 逐个 `replayAppBaseline`）；`internal/application/engine/converge.go:289-356`
- 证据：
  ```go
  // networks.go:369-375——撤销事务提交后，在请求 goroutine 里同步收敛
  if err := svc.s.commit(ctx, writeFact{...}); err != nil { ... }
  // 即时隔离（A.4）：撤销已生效，剥离是收敛动作——失败记日志，漂移拍自愈。
  if serr := svc.s.Engine.IsolateNetworkPeer(ctx, p.NetworkID, p.PeerProjectID); serr != nil {
      svc.s.Log.Warn("network peer revoke: isolation reconverge deferred to the drift tick", ...)
  }
  ```
  ```go
  // peers.go:145-167——逐 App 同步 replay（→ attemptConverge → materialize → Ensure）
  for i := range apps {
      ...
      e.replayAppBaseline(ctx, &apps[i], d) // materialize 落点即 isolate 模式
  }
  ```
- 影响：① 单写者纪律被破：Ensure 可在任意 gRPC 请求 goroutine 上执行（`loopguard` 只拦"自建 ticker"，拦不到这里）；② 撤销一个跨项目 peer 会让 RPC 阻塞"受影响 App 数 × Ensure 时延"（每 App 最多 30s 的 `ManagedStepTimeout`，串行）；③ 空 `agg` 的 baseline 源会释放该 App 的收敛冷却（R02-M1 的生产可达路径，R02 已报后果，未报"这里是请求路径写"）。同一隔离每秒都在 `driftScan` 末尾兜底（`peers.go:212`），同步调用不是正确性必需。
- 建议：把即时隔离改成"标记 + Kick/唤醒 driftScan 的隔离拍"，请求返回不等 Ensure；或至少异步化并带独立 ctx。成本 S。
- 状态：**已亲验**；无测试覆盖"撤销 peer 时正在冷却/正在部署"的组合。

### R16-M5｜ADR-0081 的 DECLARATION 桶定义与近半成员自相矛盾；`SetUserPassword` 的"未变重放 = no-op"在 bcrypt 随机盐下不成立
- 维度：架构一致性（兼文档一致性、测试与守卫）
- 严重度：M。校准：无运行时错误；但这是写面"唯一法源"的第一条判据，未来配置面/新 RPC 会按它分类，属"立法内部矛盾"级设计债。
- 位置：`docs/adr/0081-write-class-author-class.md:20-23,68,83-98,148-168`；`CONTEXT.md:263-264`；`internal/interfaces/grpc/fleetlygrpc/identity.go:174-201`
- 证据：
  ```markdown
  // ADR-0081:20（DECLARATION 判据）
  | **DECLARATION**（声明写） | 经部署管线或实体行落期望态；引擎负责把运行时收敛到满足该状态；未变声明重放 = 零扰动 no-op |
  ...
  // ADR-0081:155（SetUserPassword 的例外理由）
  | SetUserPassword | DECLARATION | 密码行落定即状态即事实（无收敛面接棒——与 Rotate 系的判据分界） |
  ```
  ```go
  // identity.go:177-184——同明文每次生成不同 hash（随机盐）
  hash, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcrypt.DefaultCost)
  ...
  return st.Users.SetPassword(ctx, req.GetUserId(), string(hash))
  ```
- 影响：① "引擎负责把运行时收敛到满足该状态"对 identity（23 RPC 中 11 条写）、governance（2）、alerting（4）这批成员**没有任何运行时面**；② "未变声明重放 = 零扰动 no-op"对 `SetUserPassword` 直接不成立（bcrypt 每次新盐，行内容必变），对 `RevokeToken` 之类"状态即事实"的写也只是"幂等"而非"no-op 声明"；③ 守卫只查 scope 方向（`TestWriteClassScopeDirection`），**语义判据零执法**，所以这条自相矛盾不会被任何红测试抓住。真实分类学其实是"实体行写 / 期望态声明写 / 动作写 / 读"四类，三桶把前两者合并了。
- 建议：修订 ADR-0081 §1 的 DECLARATION 定义（"落可 diff 的持久态；有运行时面时引擎欠收敛，无运行时面时状态即事实"），附录每条加一列 `convergence_face: yes|no`（identity/governance/alerting 全 no），并把 `SetUserPassword` 的理由从"状态即事实"补一句"重放重算盐→非 no-op，幂等性不适用"。成本 S（纯文档），可选加"DECLARATION 必须标明有无收敛面"的表结构断言。
- 状态：**已亲验**（bcrypt 调用点；写类表无 no-op 执法）。

### R16-M6｜慢通道/基线重放的成功收敛完全静默：行 8/10 的唯一动作面可以改载体而无审计、无事件
- 维度：可观测性（兼架构一致性）
- 严重度：M。校准：只在"真的改了东西"时才有害；但 ADR-0082 §2 对收敛原语的承诺是"审计行 source=system（AfterFP=基线 digest）+ `workload.converged` 事件"，且 ADR-0066 文化是"一切写操作留痕"。
- 位置：`internal/application/engine/converge.go:398-418`；`docs/adr/0082-converge-by-default.md:44-51`
- 证据：
  ```go
  // converge.go:416-418——只有快通道发事件/审计
  if src != convergeSourceFast {
      return
  }
  // :419-436 之后的 writes.Emit(... Source: audit.SourceSystem, AfterFP: digest)
  ```
- 影响：以下写入都经慢通道（src≠fast）发生，因而无事件、无审计：域内多余载体移除（裁决表行 8 的唯一兑现面）、Cancel 遗留回卷（行 10）、peer 撤销隔离、启动基线重放、稳态每 60s 重放中"域外改动的纠正"。等价于"系统能改你的运行时且不留痕"。R02-M5 已报 Cancel 这一实例；本条是其一般化——尤其行 8（`docker service create` 出来的域内多余载体被 Ensure 移除）在日常运维里就可能发生。
- 建议：审计行与事件解耦——审计行按"是否发生实际变更"发（让 provider 在 `EnsureReport` 里回报 changed 集，或比较 pre/post 观测），事件维持 fast-only（避免节拍垃圾）；或给事件加 `trigger=slow|baseline` 并允许慢通道合并发。成本 S–M（需 EnsureReport 扩字段）。
- 状态：**已亲验**（代码路径）；R02-M5 的 Cancel 子集一致。

### R16-L1｜ADR-0082 的翻案只登记了 `0046:74` 一处；被推翻的 ADR 本体仍标 Accepted，两份下游设计文档仍写"收敛 opt-in 不变"
- 维度：文档一致性
- 严重度：L。校准：现状无执行影响，但"决策真源"本身在误导下一个实现者（尤其 functions 面立项时会照抄）。
- 位置：`docs/adr/0005-state-semantics-inherited.md:3`（只有 ADR-0010 的注记）；`docs/adr/0022-drift-detection-scope.md:5,38`（"ADR-0005（收敛 opt-in 维持）"、"收敛 opt-in 不变"）；`docs/design/2026-09-30-domain-model.md:166`（"ADR-0004 漂移检测开 / 收敛 opt-in"）；`docs/design/2026-10-16-functions-api.md:111`（"Converge opt-in 不变"）；`docs/adr/0082-converge-by-default.md:24`
- 证据：
  ```markdown
  // docs/design/2026-10-16-functions-api.md:111（2026-10-16，ADR-0082 前两天的 functions 设计）
  - **Ensure/Drift/Converge 不变量**：... **Converge opt-in 不变**；...
  // docs/design/2026-09-30-domain-model.md:166
  | ADR-0004 漂移检测开 / 收敛 opt-in | 场景 7 原样继承 |
  ```
- 影响：ADR-0082 的翻案纪律（ADR-0080 范式：被翻条款显式在案）只在 0082 自己的头部成立；读 0005/0022 的人仍会得到"opt-in"结论；functions 设计将在错误前提下重新发明漂移面。R15-L1 已覆盖 3 处**代码注释**（`capability/runtime.go:600`、`eventcode/events.go:34`、`engine/events.go:61`），本条的增量是**ADR 本体状态**与**两份设计文档**。
- 建议：给 0005/0022 头部加"被 ADR-0082 部分推翻（decision 1）/全部推翻（opt-in）"注记；两份设计文档各加一行翻案注记；若要机制化，把 "opt-in" 加进 CONTEXT 的 _Avoid_ 表并由 `wording_test` 扫全仓（ADR-0007 既有通道）。成本 S。
- 状态：**已亲验**（grep 全仓）。

### R16-L2｜收敛域 9 张 per-App 内存表在 App 删除时不清：内存随 App 生命周期累积，已删 App 的冷却可让内置告警永久 firing
- 维度：可靠性（兼可观测性）
- 严重度：L。校准：R02-L7 已报告警面；本条补"全部表格 + projMemo 永久增长"的泄漏面。
- 位置：`internal/application/engine/converge.go:87-133`；`internal/application/engine/teardown.go:55-77`（只清 obs/expect/drift）；`internal/application/engine/ensure.go:70-74`（`appLocks` 同样只增）
- 证据：
  ```go
  // converge.go:87-120——memo/resolved/fails/failSig/posture/cooldown/eventSeen/projMemo/teardown
  memo map[string]ensureMemo
  resolved map[string]string
  fails map[string]int
  ...
  projMemo map[string][]string
  ```
  ```go
  // teardown.go:57-76——只清 obs/expect/drift，未触 converge 域
  wids := e.obs.forgetOwner(appOwner(appID))
  e.expect.mu.Lock(); delete(e.expect.expected, appOwner(appID)); e.expect.mu.Unlock()
  ...
  e.converge.markTeardown(appID)   // 反而又写一张永不回收的表
  ```
- 影响：`projMemo` 每个 (Revision, Generation) 一条（键 = `ToRevision + "\x00" + Generation`，`converge.go:135-142`）、`resolved/cooldown` 等每 App 一条，长期进程里随 App/部署数单调增长；已删 App 若正处冷却，`SystemConvergeAlertState` 永远 firing（无 attempt 去释放），doctor 永久告警。R02-L7 已报后者，前者（内存）是新增面。
- 建议：`TeardownApp` 末尾加 `e.converge.forget(appID)`（清 9 张表）并在删除成功后清 `appLocks`（注意锁身份，先取后删或延后清）；`projMemo` 给个 LRU 上限。成本 S。
- 状态：**已亲验**（结构 + teardown 路径）。

### R16-L3｜Runtime 缺 Inspector 子面时"缺省收敛"整体静默停摆，无启动告警/doctor 行
- 维度：可靠性（兼可观测性）
- 严重度：L。校准：当前两个 Provider 都实现 Inspector（`swarm/runtime.go:687`、`k3s/inspector.go:18`），属潜伏缺口；但 posture=all 的承诺没有前置条件声明。
- 位置：`internal/application/engine/drift.go:98-102,147-149`
- 证据：
  ```go
  // drift.go:147-149——能力缺席时整拍 continue（连收敛判决都不进）
  if inspector == nil {
      continue
  }
  ```
- 影响：第三个 Runtime / 编译期选装 Provider 若没实现 `RuntimeInspector`，`driftScan` 仍发稳态事件但**永不检缺席/spec/gen/停摆，也永不收敛**；`doctor` 不会提示"收敛已失效"，配置 posture=all 的用户以为一切正常。
- 建议：装配期 fail-fast（posture=all 且 Inspector 缺席 → 启动失败或明确降级日志），doctor 增"convergence capability"行。成本 S。
- 状态：**已亲验**（代码）；未构造第三 Provider 复现（推断）。

### R16-L4｜写类守卫的三个盲点：ACTION/DECLARATION 不查互斥；READ 桶是名字前缀余集；FrozenVerbs 42=34+8 只 Logf 不断言
- 维度：测试与守卫
- 严重度：L。校准：当前表无冲突、无写语义误读前缀的成员、42 实数也对；是"下一次改动可能漏网"的守卫强度问题。
- 位置：`internal/guards/writeclassguard_test.go:190-216,298-323`
- 证据：
  ```go
  // :200-212——ACTION 先命中即 continue：同一方法若同时出现在两表，DECLARATION
  // 的 scope 单向断言会被静默跳过（表长度对账可被"两边各删一条补一条"糊过去）
  for m := range all {
      if _, ok := writeClassActions[m]; ok { continue }
      if writeClassDeclarations[m] { continue }
      if isReadVerb(m) { continue }
      unclassified = append(unclassified, m)
  }
  // :321-322——台账只打日志
  t.Logf("FrozenVerbs reconciliation: %d entries = %d DECLARATION + %d ACTION", ...)
  ```
- 影响：① 两表互斥未断言；② READ 是 `freezeReadPrefixes`（Get/List/Wait/Stream/Query/WhoAmI/Explain/Diff/Download）余集——一个带副作用的 `StreamXxx` 会自动归 READ 且无人复核（"153 一一对应"实际是 80 条显式 + 73 条前缀推断）；③ ADR-0081 验收锚"42 条逐一可对账"只以 `t.Logf` 呈现，改表不会红。
- 建议：加三条断言（互斥、`len(actions)+len(decls)+len(read==prefix)=153` 的显式 READ 集合或至少对 READ 成员的 scope/描述性白名单、FrozenVerbs 34/8）。成本 S。
- 状态：**已亲验**（读代码 + 实跑守卫绿）。

### R16-L5｜测试"通过但承诺未兑现"的六处（断言强度不足或把矛盾钉成期望）
- 维度：测试与守卫
- 严重度：L（单条），但合起来是 ADR-0082/0083 验收锚的可信度问题。
- 位置/证据：
  1. `converge_test.go:552-571`（`TestCancelLegacyConvergesBackToBaseline`）只断言**最后一次 Ensure 调用的 image/gen**；未断言遗留载体被移除、无审计、无事件、无 `workload.converged`——ADR-0082 验收锚却称"Cancel 遗留回卷钉测"。
  2. `converge_test.go:530-536`（`TestPostureDetectSkipsConverge`）把 baseline 逃逸**钉成期望**（M2），等于用测试合法化 ADR 矛盾。
  3. `converge_test.go:493-511`（`TestBaselineReplaySharesCooldownLedger`）手工把 `failSig` 置空，绕开真实形态（R02-M1，复核一致）。
  4. `converge_test.go:178-193`（`TestFreezeGateFailClosedOnUnresolvableTeam`）在 `driftScan` 的 `appTeam` 解析处就 `continue`，**从未进入** `convergeFrozen` 的 fail-closed 分支（R02-L5，复核一致）。
  5. `converge_test.go:575-591`（`TestSucceededWithoutExpectCacheConverges`）通过的原因是"B 面纳入 face + 首拍慢通道重放"，不是 ADR §6 所说的"扫描面分支"（无此分支）；测试无法区分机制。
  6. `converge_test.go:77-108`（`TestConvergeVerdictGolden`）钉的是生产零引用的死函数（R02-M2，复核一致）。
- 影响：验收锚勾选（"[x] 钉测族八类全钉"）在字面上成立、在强度上不成立；最关键的两条承诺（Cancel 回卷可发现、冻结 fail-closed）实际无有效断言。
- 建议：按 §4-H1/M1/M2/M3 的建议补四类负向测试（失败+回滚后重启、env 键碰撞、detect 下 Cancel、撤销 peer 的请求路径写）＋加强三条既存断言（载体移除/审计存在/fail-closed 分支打点）。成本 M。
- 状态：**已亲验**（逐个测试读 + 实跑绿）。

### R16-L6｜Author Class 三铁律零可执行载体（ADR 已声明推迟，故非违约；但"学说层已立、执法面空白"成立）
- 维度：架构一致性
- 严重度：L。校准：ADR-0081 §2 明示"字段所有权注册表与守卫推迟到 autoscale 同批（不造无人消费机制）"——这是**经裁决的推迟**，不是漏做；风险在 autoscale 动工时无法验证"既有字段从未被系统写过"。
- 位置：`docs/adr/0081-write-class-author-class.md:52`；现状可指认的 SYSTEM 写点：`internal/application/engine/materials.go:239-267`（卷钉住落 Volume 行）、`materials.go:289-311`（effective Placement 合并）、`materialize.go:70-74`（`injectDatabaseEnv`）、`bluegreen.go:41-65`（代戳/ID）、`k3s/runtime.go:65-74`（egress/netpol 平台对象）。
- 证据：
  ```markdown
  // ADR-0081:52
  字段/实体级权属注册表与守卫**推迟到 autoscale 落地同批**——不造无人消费的机制（与第 3 条裁决同一原则）。
  ```
- 影响：三铁律中 ①（授权槽位）今天靠"没有 autoscale"这一事实兜底（`replicas` 只从 spec 投影，未见系统写点）；②（drift 对照 effective）靠"`ensuredSpec` 记录的是注入后的 effective 投影"结构性满足（`materialize.go:59-85` 顺序），但 drift 只比较 image/command/replicas，effective env/pinning **根本不进对照面**（无假阳性，也无有效性证明）；③（声明重放不清洗系统态）靠"钉住落行再重放读取"实现（`pinVolumes` 只钉未钉卷）。三条都没有守卫，也没有测试以"系统写用户字段"为反例。
- 建议：维持推迟，但在 ADR 里把"今天已有的 SYSTEM 写点清单 + 各自为何不碰用户字段"列成附录（现在散在代码注释）；autoscale 立项时把字段注册表与"engine 不得写 ProcessSpec.replicas"的静态守卫同批落地。成本 S（文档）/M（守卫）。
- 状态：**已亲验**（写点清单逐个读过）。

---

## 4A. 十行裁决表落点表（触发 / 判定 / 动作）

> ADR-0082 §1 十行逐行落到真实代码；"判定"列指出**期望侧真源**（这是本次评审的关键分野）。`convergeVerdict` 不计入（生产零引用，R02-M2）。

| # | 漂移类 | 触发（检测）代码 | 判定代码 | 动作代码 | 结论 |
|---|---|---|---|---|---|
| 1 | App spec 失配 | `drift.go:300-318`（`signals["spec|wid"]`，期望侧 = `ensuredSpecOf` **账本**） | `converge.go:262-281`（fast）+ `:289-356` | `materialize(fresh, ToRevision, Gen, true)` → `Ensure` | 已落码；期望侧是缓存，见 M3 |
| 2 | App 载体缺席 | `drift.go:231-241` → `missingExpectedCarriers`（`converge.go:210-254`，`projectRevision` 重投影 + 代戳） | 同上 | 同上 | **已落码且合规**（唯一真源行） |
| 3 | 稳态 gen 偏离 | `drift.go:289-299`（`signals["gen|wid"]`，锚 = `ensuredGenOf` **账本**） | 同上 | 同上 | 已落码；期望侧是缓存，见 M3 |
| 4 | 终态后滚动停摆 | `drift.go:272-285`（`o.RolloutStalled`） | 同上 | `materialize` → provider `resumeStalledRollout`（`swarm/runtime.go:330-352`） | 已落码（5min 护栏在 provider） |
| 5 | spec 相符 + stopped | **无信号**（仅 `drift.go:180-208` 发 `workload.stopped` 事件） | 无（"不进收敛"由不产信号实现） | 无 | 正确（alert-only） |
| 6 | 活跃部署中（含 failed 待回滚） | 信号照常产生 | `converge.go:332-339`（`DrivingByApp`，与 `ListDriving` 同谓词 `repo.go:153-176`） | 跳过 | 已落码（有钉测） |
| 7 | 冻结中 | 信号照常产生、事件照发 | `converge.go:304-314`（`convergeFrozen` fail-closed；`ActiveForTeam` 含全局 `''`，`freeze/repo.go:58-74`） | 挂起 | 已落码；测试未打中 fail-closed 分支（R02-L5） |
| 8 | 域内多余载体 | **无独立检测**（无 extra 信号） | 无（不产信号；由慢通道无条件重放兜底） | `Ensure` 域内移除（`swarm/runtime.go:217-225`；k3s `listDomainObjects` 收敛） | **部分**：只有慢通道副作用，≤60s+一拍；无审计（M6） |
| 9 | 域外/无主孤儿 | **无检测、无登记事件**（`detectDrift` 对非管辖载体直接 return，`observ.go:251-255`；eventcode 无 `WorkloadOrphaned`） | 无 | 无（只留在观测 map，last-write-wins） | **部分**：ADR 的"只登记"在用户可见面上不存在（与 R02-M2 一致） |
| 10 | Cancel 遗留载体 | **无检测**（账本被取消部署写脏 → spec/gen 零信号；遗留是 extra 非 missing） | 无（慢通道无条件重放） | `replayAppBaseline` → 基线 `Ensure`（`converge.go:398-437` 对非 fast 源**不发事件/审计**） | **部分**：回卷只由慢通道兑现；行 10 的机制描述（缺席检测）错误；detect 下永不兑现（M2/M3） |

**统一原语**：`attemptConverge` 闸门序 = 冷却 → posture → 冻结（fail-closed）→ `lockApp` → teardown 标记 → 驱动权复查 → 基线 freshness 复查 → `materialize`；**不做 Deployment 行**（`converge.go:283-356`）。与 ADR §2 一致（除"expect 缓存成员资格复查"被 teardown 标记替代，语义等价且更精确——R02 亦认可）。

**机制清单核对**（ADR 声称 vs 代码）：
- 缺席检测用 `LatestSucceeded` 重投影：**是**（`drift.go:165-177,231-241`）。
- 快/慢通道：**是**（`converge.go:262-281`；慢通道 = `ensureFresh` memo 到期，`ensure.go:31-39`）。
- 签名生命周期（成功才落、失败/挂起不落）：**是**（`convergeSucceed/ConvergeFail`）。
- 冷却 N=3/10min 三径：**是**（`:362-396,442-475`；`options.go:207-215`），但解除径②在空签名源上恒真（R02-M1，复核一致）。
- 冻结 fail-closed：**是**（`:304-314`）。
- posture=detect 全部逃逸面：**baseline 源两处绕过**（启动重放 + peer 撤销隔离），ADR 未写（M2）；慢通道被拦 → Cancel 永不修（M2/M3）。
- 自激三件套：**是**（断路器 + 内容等价 + 签名绑定）。
- Cancel 遗留回卷且无审计事件：**是**（`:416-418`），且 ADR 行 10 机制描述错误（M3/R02-M5）。
- teardown 竞态闭合：**是**（mark 在 `appMu` 内写，`teardown.go:71-76`；复查在 `appMu` 内，`converge.go:318-328`）。
- BG 旧代幽灵 / 进程缩减幽灵：**是**（`converge_test.go:274-339`，重投影对照）。
- 终态停摆 / gen 偏离：**是**（`:365-402`、`:386-402`）。

---

## 4B. 29 项对抗裁决三态表

> 三态：**已落码** = 代码/测试/文档落点均可指认；**部分** = 有落点但不完整（机制、执法或证据缺一）；**未落码** = 无落点。
> 统计：**已落码 22 / 部分 7 / 未落码 0**。

### A 路（立法与架构一致性）

| # | 裁决 | 态 | 证据/缺口 |
|---|---|---|---|
| A1 | 分类表重编（153、铸造族、identity 判据） | 已落码 | `writeclassguard_test.go:190-198`（27/53/153）；本轮独立清点 proto 153 一致 |
| A2 | Converge 词条同批修订 | 已落码 | `CONTEXT.md:282`；缺口：下游文档未回改（L1） |
| A3 | push/spec_file 归 ADR-0072 | 已落码 | ADR-0081:46、设计 §2.3（文档） |
| A4 | 翻案对象定位 0022 decision 1；0046:74 登记 | 部分 | ADR-0082 头登记 ✓；但 0005/0022 本体仍 Accepted、下游 design 未登记（L1） |
| A5 | scope 单向 + 铸造族豁免 | 已落码 | `:241-293`；豁免 3 条；实跑绿 |
| A6 | 期望缓存 vs 不变量 1 立锚 | 部分 | 行 2 重投影 ✓；行 1/3 仍账本（M3）；账本写点无守卫（R02-L6） |
| A7 | 冻结挂起×签名去抖互锁；补行 6/7 | 已落码 | 冻结闸门先于签名消费（`:304-314`）；表行是装饰（R02-M2） |
| A8 | ADR-0069 决策 6 张力记录 | 已落码 | ADR-0083 头 + §3 + CLI |
| A9 | 裁决表不落 CONTEXT；Posture/restart_seq 入词条 | 已落码 | `CONTEXT.md:259-266,286-287` + Spec 词条 restart_seq 注记 |
| A10 | ACTION 同词异轴消歧 | 已落码 | `CONTEXT.md:263-264` |
| A11 | 扩展号论证修正 | 已落码 | ADR-0081:56（文档；无代码面） |
| A12 | Stores 已含 Freezes | 已落码 | `convergeFrozen` 直用 `e.repos.Freezes`；装配注入在 |

### B 路（并发与分布式正确性）

| # | 裁决 | 态 | 证据/缺口 |
|---|---|---|---|
| B1 | 缺席检测改 LatestSucceeded 拍内重投影 | 已落码 | `converge.go:210-254` + `drift.go:165-177`；但账本语义有洞（M3） |
| B2 | 复查口径改驱动权同源 | 已落码 | `converge.go:332-339` + `DrivingByApp`；钉测 `converge_test.go:197-229` |
| B3 | gen 偏离下沉 driftScan 独立签名 | 已落码 | `drift.go:289-299`；钉测 `:365-382` |
| B4 | 签名生命周期绑定结果 | 已落码 | `converge.go:402-475`；钉测 `:417-489` |
| B5 | Cancel 遗留升待裁决→回卷基线 | 部分 | 动作只有慢通道、无审计事件、行 10 机制描述错误、detect 下不兑现（M2/M3；R02-M5 已知） |
| B6 | teardown×缺席检测竞态闭合 | 已落码 | `teardown.go:71-76` + `converge.go:318-328`；钉测 `:343-361`；无 teardown-abort 路径测试 |
| B7 | 冻结判定 fail-closed | 已落码 | `converge.go:304-314,496-509`；**测试未打中分支**（R02-L5） |
| B8 | seq-only 判定延迟到执行时 | 已落码 | `restart.go:31-57`；钉测 `restart_test.go:147-186`；但判定器有路径后缀洞（M1） |
| B9 | 终态后停摆补恢复面 | 已落码 | `drift.go:272-285`；provider `resumeStalledRollout`；钉测 `:386-402` |
| B10 | 冷却内存态声明 + 基线共用记账 | 已落码 | 文档 + `:442-475`；钉测注入态弱化（R02-M1） |
| B11 | rebuildBaselines 失败 → 自愈盲修复 | 部分 | B 面纳入 face（`drift.go:115-141`）+ 慢通道首拍重放；无 ADR 所说的"扫描面分支"，测试不可区分机制（L5-5） |
| B12 | seq 单调约定文档化、不加受理执法 | 已落码 | `spec.proto:560-570` + ADR-0083 §1；CLI 有 uint32 上限检查（`verbs_structure.go:469-471`） |
| B-② | 双通道（快事件 + 慢节拍兜底） | 已落码 | `converge.go:262-281` + `ensureFresh` |
| B-③ | 驳回"kind=reconcile 部署行"，走 SourceSystem 审计 | 已落码 | 无 Deployment 行；`:428-436` |

### C 路（产品面与工程账）

| # | 裁决 | 态 | 证据/缺口 |
|---|---|---|---|
| C1 | seq=0 不铸 + 缺失归 0 + 存量零滚用例 + e2e 预记 | 部分 | 代码/四用例 ✓（双侧）；e2e:upgrade 断言仍未执行（ADR-0083:54 自标 `[ ]`；R15 §5#6 已记挂账） |
| C2 | runbook 手动面盘点 + 冻结优先逃生序 | 已落码 | `docs/runbooks/staging-fleetly.md:17-19`（标准逃生序 freeze 优先、scale-0 收敛法） |
| C3 | 执法面 Go 表 + proto 注解推迟 | 已落码 | 守卫 + ADR-0081:54-56；proto 零触碰 |
| C4 | 冷却可发现/可解除/接告警 | 已落码 | `doctor.go:252-258`、`alerting.go:362`、eventcode 三链；R02-L7 缺口（删 App 永 firing） |
| C5 | 收敛写径审计 source=system | 部分 | 仅快通道（`:416-418`）；慢/基线静默（M6；R02-M5 的 Cancel 子集） |
| C6 | skills 同批（deploy-diagnose 翻向 + restart 双面） | 已落码 | `skills/deploy-diagnose/SKILL.md:110-135,153`（漂移行改为"confirm convergence instead of acting"） |
| C7 | golden 清单（selfdescription/schema/explain/revisions/workloads） | 已落码 | `testdata/golden/{apps-restart,apps-workloads-restartseq,revisions-get-restartseq,explain,schema}*`、`assembly/testdata/selfdescription.golden.json:386,601,3151` |
| C8 | 期望账本命名/写点纪律/冷窗成文 | 部分 | 注释与 ADR 成文 ✓；无守卫（R02-L6）；结构仍寄居 observations 域（注释自认"结构析出留实现裁量"） |
| C9 | 双面操作者契约 | 已落码 | CLI 缺省声明面 + `--runtime`；ADR-0083 §3；skills 双面；缺口见 H1 |
| C10 | SetUserPassword 与 Rotate 系界线 | 已落码 | ADR-0081:68；但判据定义自相矛盾（M5） |

---

## 5. 交叉观察（供 Lead 去重/汇总）

### 5.1 对 R02 的复核结论

| R02 项 | 复核 | 说明 |
|---|---|---|
| R02-H1（BG 旧代 egress） | 未复核（不属本模块） | — |
| R02-M1（冷却空签名解除） | **一致，且可达性比其表述更具体** | R02 已精准指出 `IsolateNetworkPeer`/`enforcePeerIsolation` 路径；本轮补充：该路径同时是"请求 goroutine 内写"（M4），且空 `agg` 来源不止基线重放，慢通道在"信号消失"时也传空 agg（语义上无害，因为漂移已消失） |
| R02-M2（裁决表装饰） | **一致** | 全仓 `convergeVerdict` 仅定义 + 测试；行 8/9/10 无检测落点。本轮补行 10 的机制描述错误（ADR 写"缺席检测"，实为慢通道）与行 9"只登记"无事件码 |
| R02-M3（动作面 BG 载体 ID） | 未复核（R06 面） | `processrestart.go:56-57` 与 R02 描述一致 |
| R02-M4（teardown 对 failed 窗失明） | **一致** | `teardown.go:27-33` 用 `ActiveByApp`、`DrivingByApp` 才是驱动权口径；`DeleteApp` 预检同病 |
| R02-M5（Cancel 慢通道、无审计、行 10 描述不符、注释持 N0） | **一致** | `admission.go:281` 注释仍写"N0 不自动拆除"；`:304` 只清 release 备忘。本轮补根因（M3）与 detect 下永不修复（M2） |
| R02-M6（构建源重启重建） | **一致（推断）** | 新 Revision → 无 Build 行 → 重新构建；与 H1 叠加时更糟 |
| R02-M7（Loop.done 无消费面） | **一致** | `Engine.Loop()` 无调用方（grep 仅定义）；`Loop.done` 无消费 |
| R02-L1（resolved 不清零） | **一致** | `resolved` 只在成功时覆盖，漂移消失不清零；≤90s 由慢通道兜底 |
| R02-L2（payload.Kind 未填） | **一致** | `deploymentEventPayloadJSON` 无 Kind 赋值；kind 仅在审计 AfterFP 后缀（L5/4B-C7） |
| R02-L3（陈旧注释/行锚） | **一致** | `events.go:61` 仍"只观测不纠正（ADR-0005 opt-in）"；ADR-0082 引用的 `admission.go:275-277` 已漂移到 281 |
| R02-L4（converged 非事务） | **一致** | `writes.Emit` 直落 outbox+audit，无 tx；`StreamOnly` 只用于观测事件 |
| R02-L5（fail-closed 测试未打中） | **一致** | `TestFreezeGateFailClosedOnUnresolvableTeam` 在 `driftScan` 的 `appTeam` 处就 continue（`drift.go:152-155`） |
| R02-L6（账本写点无守卫） | **一致** | 仅注释 + ADR；`recordEnsured` 唯一调用点 `materialize.go:85` 可被 grep 但无测试守卫 |
| R02-L7（冷却记账不清） | **一致** | 且扩展到 9 张表 + `appLocks` + `projMemo`（L2） |
| R02-L8（egress fail-open） | 未复核（不属本模块） | — |

**R02 §5#4 交叉观察（restart 取最新冻结）**：原文仅一句"会把较新的失败 Revision 重新冻结部署、静默撤销回滚声明"。本轮把它升级为 **H1**：补齐 CLI→冻结→回滚→执行时判定的完整证据链，并指出其与 ADR-0083 步 8 / CLI synopsis / skill 承诺**直接冲突**（迁移会重跑），以及 `--runtime` 校验同源问题。建议 Lead 计入主清单，避免只当"观察"。

**R02 §5#5 交叉观察（spec_file 复活已删变量）**：与 H1 同路径（restart 重提交冻结体 → 服务端只叠当前变量）。本轮未单独取证（属 R03 变量合成面），但提醒：H1 修法若改"从基线渲染再 bump seq"，该问题会一并消失（基线体含当年 env 仍会被重提交），需 R03 一起裁。

### 5.2 对 R15 的复核结论

- **R15 §5#8（detect 启动例外）**：方向对、定级偏低、**事实有一处错误**——其称"`TestPostureDetectSkipsConverge` 不覆盖启动基线路径"，实际 `converge_test.go:530-536` 明确覆盖并断言"baseline replay still runs under detect"。真正的问题是**测试把 ADR 矛盾钉成了期望**（M2），且漏第二个逃逸源与反向后果。建议升 M 并修正描述。
- **R15 §5#6（计划/ADR 勾选未回写）**：一致；本轮不重报（C1/C2 三态表已引用）。
- **R15 §5#7（kind=restart 形态）**：一致，本报告不再单列（并入 §4-E 断点表）。
- **R15 §5#1（R02-M2+M5 合并）**：同意合并；但请注意 M5 与 M3 的根因不同（M5 是"慢通道不出声"，M3 是"快通道看不见"），修复也应是两处。
- **R15 对 ADR-0081 的"验收锚全部真实"结论**：**部分不一致**——五条验收锚的描述与代码一致，但"DECLARATION 语义"这一**学说层**判据无执法（M5），"153 一一对应"实为 80 显式 + 73 前缀推断（L4）。建议把"锚真实"限定为"计数与从属断言真实"。
- **R15 对 ADR-0082 的"行为面承诺基本真实"**：**部分不一致**——主体机制真实，但行 8/9/10 的动作面/可发现面与 ADR 文本有实质差距（4A 表），且 detect 语义被执行钉成相反（M2）。

### 5.3 其他模块可关注

- **R05（持久化）**：`LatestSucceeded`/`DrivingByApp`/`ActiveByApp` 三谓词的"活跃 vs 驱动"分叉是全模型的地基；本报告发现 teardown（R02-M4）与收敛（已修）两处误用，建议加一条 repo 层测试：三谓词对 `failed/rollback_attempted` 的包含关系表。
- **R08（接口层）**：`RevokeNetworkPeer` 在请求路径同步 Ensure（M4）；建议接口层统一原则"写 RPC 只落行 + Kick"。
- **R10（CLI/SDK）**：H1 + `--runtime` 校验同源；另外 `apps restart --json` 的响应是 `DeployResponse`，没有"这是重启"的机器可读标记（只有人类文案），Agent 面难以区分声明式重启与普通部署。
- **R11（Console）**：`restart_seq` 仅进 `src/api` 类型（`delivery.ts:756-766`、`structure.ts:1030-1040`），无控件/展示；ADR 的"最小不动"评估在案，但 Console 用户无法发起声明式重启、也无法在部署列表看出"本次是重启"（kind 不在行上）。
- **R12（守卫）**：L4 三个盲点；另建议把"写 RPC 内不得直调 engine 收敛方法"做成 import/调用图守卫（M4 类）。
- **R14（安全）**：`enforcePeerIsolation` 复核读失败时 `ok=true`（`peers.go:193-197`，"假隔离比迟隔离糟"）——是经论证的 fail-open，建议在安全面复核一次该取舍（撤销 peer 后若存储故障，跨项目网络访问会延续到下一拍）。

---

## 6. 测试与守卫质量评估

**覆盖强度（强）**：engine 收敛/重启面 40+ 测试文件；`converge_test.go` 19 个 Test 覆盖冻结/驱动/stopped/缺席/双幽灵/teardown/gen/停摆/冷却三径/Cancel/账本/投影短路；`restart_test.go` 5 个（含 B8 执行时重算、BG 舞步）；双侧 contentequiv 四用例；`writeclassguard` 153 全分类。夹具质量高（真 SQLite + fake clock + fake runtime + 手动 step），不依赖真实节拍。

**断言强度（分层）**：
- **强**：`TestConvergeOnSpecMismatchRestoresBaseline`（断言 Ensure 的 gen/image、事件、审计 source/AfterFP/Resource、去抖、不建 Deployment 行）；`TestAbsenceRebuildsMissingCarrier`（Ensure 参数）；`TestGhost*`（负向：不重建旧代/缩容进程）；`TestRestartSeqOnly*`（游标 + 零新 job task + 审计标记）；contentequiv（update/skip 计数 + 双侧标签）。
- **中**：`TestConvergeCooldownLifecycle`（三径可走，但"签名变化"与"超窗"都靠注入态，未覆盖生产形态的空 agg）；`TestConvergeSkipsDrivingAppIncludingFailedRollbackWindow`（好，但只断言 ensure 计数）。
- **弱/伪**：L5 六条。尤其 `TestCancelLegacyConvergesBackToBaseline` 只断言"最后一次 Ensure 调用的 image"，把"回卷"降格为"调用参数"——遗留载体是否真的被移除、是否有审计，全未断言。

**守卫能否拦住本模块典型错误**：
- 能：写类/冻结/idem/scope 交叉漂移（三表挂同一法源）；循环骨架唯一（`loopguard`）；分层（`dep_matrix`）。
- 不能：① "写 RPC 不得在请求路径跑收敛"（M4 类无守卫）；② "决策输入不得是缓存"（M3 类无守卫）；③ "系统不得写用户字段"（L6，已声明推迟）；④ "README/ADR 状态回写"（L1）；⑤ "posture 语义与代码一致"（M2 只能靠人读 ADR）。
- 失效模式：本报告 6 条 M 中有 4 条（M1/M2/M3/M6）是"测试全绿但语义错"——共同点是**断言的是调用形状/计数，而非终态与副作用**。

---

## 7. 未决与置信度

**最不确定的 5 点**：
1. **H1 的定级**：若产品意图明确是"重启=重放你最后一次声明（最新冻结）"，则 H1 降 M，但 CLI synopsis/skill 的"no migration re-run"必须改。需产品裁决（ADR-0083 未写"源 = 最新声明 vs 生效基线"，R02 已注意到）。
2. **M1 的触发现实性**：需要用户 env 键名恰以 `restart_seq` 结尾；我未在全仓/文档里搜到用户示例使用该键（概率判断，未经真机）。
3. **M3 的影响面**：账本写脏在"Cancel 部分下发"下确定成立；但生产上 Cancel 多数发生在 queued（未物化），实际触发频率未知（无遥测）。
4. **M4 的延迟量化**：未测真实 `IsolateNetworkPeer` 在 N 个 App 下的 RPC 时延；只从代码串行结构推断（每 App 一次 Ensure，带 30s 界）。
5. **k3s 侧 restart_seq 的 canonical 归一**只核到测试与 `canonicalJSONModuloGen` 的 "0" 剥离；未在真实 k3s 上验证"存量无标签 × 期望 0"首拍零滚（属 e2e 面）。

**整体置信度**：**高**（H1/M1/M2/M3/M4/M5/M6 均有代码或实跑证据；29 项三态表与十行落点表逐项对到行）。低置信部分仅"发生频率/真机行为"，已逐条标注。

**未覆盖面**：e2e（Docker 不可达）、Console UI 渲染、race 全量、真实编排器升级首拍。

---

## 附录 A：实验与命令记录（可复核）

```text
# 1) 独立清点 proto（14 文件 / 36 service / 153 rpc；逐文件计数与 ADR-0081:85 一致）
Get-ChildItem proto -Recurse -Filter *.proto | % { ... '^\s*rpc\s+(\w+)\s*\(' ... }

# 2) 写类表 × proto × 冻结表机械对账（脚本解析 Go 表字面量；结果：27 / 53 / 无交集 / 42=34+8）
# 3) 守卫与收敛/重启定向测试
go test ./internal/guards -run 'TestWriteClass|TestFreezeSurface|TestScope|TestIdem' -count=1   → ok
go test ./internal/application/engine -run 'TestConverge|TestRestart|TestFreeze|TestAbsence|TestGhost|TestTeardown|TestGenLabel|TestTerminalRollout|TestPosture|TestCancel|TestCompareSpecs|TestProjection' -count=1 → ok
# 4) RestartSeqOnly env 键名欺骗（仓外 overlay，未改仓库）
go test -overlay=D:\Codes\qiulin\fleetly-review-2026-10-19\_tmp_r16_spec_overlay.json ./internal/domain/shared/spec -run TestR16 -v
  PROBE diff entry: "processes[0].env.restart_seq" old=v1 new=v2
  BUG CONFIRMED: env-only change classified as restart_seq-only (preDeployJobs would be skipped)
  --- FAIL: TestR16EnvKeyNamedRestartSeqFalsePositive   （失败即证；另 TestR16RealSeqBumpStillTrue PASS）
```

**临时文件**（均在评审目录，未写入仓库）：`_tmp_r16_restartseq_probe_test.go`、`_tmp_r16_spec_overlay.json`。
