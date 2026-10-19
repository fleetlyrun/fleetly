# R10 CLI、SDK 与 Agent 面 评审报告

> 评审基线：`D:\Codes\qiulin\fleetly-review-2026-10-19\PROFILE.md`（2026-10-19，HEAD `35257f18`，工作树干净）。
> 评审方式：只读静态全量阅读 + 本机 `go build`/`go test` + **overlay 注入探针**（overlay 把 `verbs_exec.go` 的 `syscall.SIGWINCH` 换成空操作后，CLI 全包测试可在 Windows 本机编译运行；overlay/探针文件全部落在评审目录 `_tmp_r10_*`，仓库零改动，结束 `git status` 干净）。
> 关键本机事实：`go build ./cmd/fleetly`（Windows）失败于 `verbs_exec.go:159 syscall.SIGWINCH undefined`；`GOOS=linux go build/vet` 通过；`go test ./sdk/go/...` 通过；overlay 修补后 `go test ./cmd/fleetly/...` **74 个测试全绿**（golden 比对零漂移）。

## 1. 覆盖清单

### 完整读过（生产文件，逐个列）

`cmd/fleetly/main.go`；`cmd/fleetly/cmd/` 下全部 38 个生产文件：
`app.go`、`group.go`、`verb.go`、`conn.go`、`dial.go`、`rpc.go`、`idem.go`、`render.go`、`reuse.go`、
`auth.go`、`init.go`、`status.go`、`version.go`、`doctor.go`、`doctor_probe.go`、`doctor_exposure.go`、`doctor_disk_unix.go`、`doctor_disk_windows.go`、
`quickstart.go`、`templates.go`、`dokploy.go`、
`verbs_exec.go`、`verbs_delivery.go`、`verbs_telemetry.go`、`verbs_structure.go`、`verbs_uploads.go`、`verbs_automation.go`、`verbs_identity.go`、`verbs_databases.go`、`verbs_hooks.go`、`verbs_githubapps.go`、`verbs_alerting.go`、`verbs_governance.go`、`verbs_platform.go`、`verbs_system.go`、`verbs_components.go`、`verbs_registry.go`。

`skills/`：`deploy-diagnose/SKILL.md`、`database-provision/SKILL.md`、`task-pool/SKILL.md`（3 个全读，324 行）。
`sdk/go`：`client.go`、`errors.go`、`client_scheme_test.go`、`errors_test.go`、`go.mod`（全读）。

### 抽样读过（用于交叉核实/测试质量）

- 测试：`coverage_test.go`、`golden_test.go`、`golden_helpers_test.go`、`verbs_golden_test.go`、`alerting_golden_test.go`、`apps_restart_golden_test.go`、`builds_wait_golden_test.go`、`databases_download_golden_test.go`、`databases_guard_test.go`、`databases_rotate_golden_test.go`、`dial_test.go`、`exec_golden_test.go`、`init_golden_test.go`、`logs_axes_golden_test.go`、`main_test.go`、`skills_guard_test.go`、`status_test.go`、`system_golden_test.go` 全读；`doctor_golden_test.go`（1-130/195）、`templates_golden_test.go`（80-154）抽样读；十余个 `testdata/golden/*.golden` 抽样读（含全部 0 字节 JSON golden）。
- 对照读：`README.md`、`AGENTS.md`、`docs/runbooks/staging-fleetly.md`（1188/1193 公网 CLI 接法）、`docs/adr/0079-api-public-host-managed-route.md`、`internal/domain/shared/capability/routevalidate.go`、`internal/domain/shared/model/errcode`（仅经 CLI 使用面）、`internal/interfaces/grpc/fleetlygrpc/{wait.go,contexts.go:454-485,contexts.go:195-197}`、`internal/application/idem/enforcer.go`（幂等头名）、`.github/workflows/ci.yml`（runner 面）、`lynx-go/commands@v0.3.0/commands.go`（root flag 剥取与 help 路径）。
- 未读的近 20 个 `cmd/fleetly/cmd/*_golden_test.go`（identity/structure/telemetry/hooks/githubapps/registry 等）：它们与已读 golden 测试同构，仅按需要抽查了 golden 文件内容。

### 未覆盖/无法覆盖（及原因）

- **无真机控制面**：未拨真实 fleetlyd，所有 CLI 行为经 in-process `apitest` bufconn 夹具（真 gRPC 服务链 + 假 runtime）验证；TLS 公网直连只由 SDK 自带真握手测试覆盖（本机 `go test ./sdk/go/...` 绿）。
- 本机 Windows 无法原生编译 CLI（见 R10-M6）：语义级验证靠 overlay 补丁后的测试二进制；**未运行 `go test -race`**（先决条件是先过编译门）。
- 未运行 e2e（约束禁止）与 `-update`（禁止写仓）。
- Console 对 SSE/等待流的消费只按交叉观察读，不属本模块。

## 2. 模块画像与边界

- **规模（实测）**：`cmd/fleetly` 76 个 Go 文件（38 生产 / 38 测试），生产 **7,905 行**、测试 **3,824 行**；golden **474 个文件（231 个 `-json`、243 个人类/场景形态；加仓内另 3 个非 CLI golden 恰为 PROFILE 所述 477）**；`sdk/go` 441 行（含测试）；`skills` 324 行。
- **职责**：CLI 是瘦客户端（包注释与 SDK 文档均声明"CLI-over-SDK 是唯一通道"），单点结构为 `flaggedVerb`（`verb.go:10-37`）把 `--json`（root bool）与人类形态收口进同一次 `run`；`render.go` 是输出单点（protojson `UseProtoNames:true` + `EmitUnpopulated:false`）；`dial.go`/`rpc.go` 收口拨号、变量级 deadline、Bearer 与 `x-fleetly-client-source`；`idem.go` 透传 `idempotency-key` metadata；`app.go` 的 `ExitCode` 钩子实现 0/1/2/64 四态；错误信封经 `apperrstatus` 还原后在 stderr 渲染 errcode/suggestion/docs/error_id。SDK 是公开模块（零 `internal/**` 依赖，仅 genproto+grpc）。
- **依赖方向（偏差/边界）**：CLI 虽自称"只走 SDK"，实际还 import 了 `internal/interfaces/grpc/{apperrstatus,apivalidate,fleetlygrpc}`、`internal/application/authn`、`internal/domain/{identity,shared/apperr,errcode}`。这是仓内 cmd 的显式设计（`render.go:61-66` 注释"CLI 是输入适配器，依赖 interfaces 层共享件"），不违 AGENTS 分层；**但副作用是 CLI 不走公开 SDK 的 `FromError`**，公开 SDK 的错误投影面因此几乎没有真实消费者（见 R10-M9）。
- **与外部声明的偏差**：README 把 `fleetly` 定为"宿主 Linux 二进制"（README:12）却仓库内多处做 Windows 开发机适配（`doctor_disk_windows.go`、ADR-0036、runbook 大量"Windows 本机"），CLI 实际在 Windows 不能编译——**支持矩阵未声明**（R10-M6）。README:62 宣称"`--json` 全命令覆盖"，实测两个成功路径的 delete 动作为空输出（R10-M1）。`connFlags` 注释称"贯穿全部 RPC 动词"（`conn.go:24`），实测仅 `status` 使用（R10-M5）。

## 3. 优点（带证据）

1. **四态退出码与双形态契约有真测试，不是注释承诺**：`exitCodeFor`（`app.go:147-167`）区分 errChanges=2、exec 载体码透传、UsageError/UnknownVerb=64；`TestExitCodesAreStable`（`coverage_test.go:54-89`）钉死未知动词/坏旗标/缺参=64、diff 有变化=2，并断言 changes 不得渲染错误信封。我以 overlay 探针复验 `projects create shop --team default` → 64、`templates instantiate …` 错序 → 64（见 M2），与契约一致。
2. **错误出口纪律到位**：`renderErrorFor`（`render.go:69-95`）对 E_INTERNAL 只显示脱敏文案+error_id，不把 cause 打进终端；docs 链接走 `errcode.DocsURL` 单源；解码与拦截器同一 `apperrstatus`。探针复验 quickstart 失败时 stderr 是 `E_INVALID_ARGUMENT: … / suggestion: … / docs: https://docs.fleetly.sh/errors/E_INVALID_ARGUMENT` 三行完整信封，退出码 1。
3. **流式 deadline 抽象正确且有用例**：`dialOptions.noDeadline` 单点（`dial.go:25-37`）+ `TestDialFromEnvDeadline`（`dial_test.go:16-32`）断言非流式带 120s、流式不带；18 处生产调用点（logs/follow/exec/shell/wait 族/备份/网络重建/上传）均走该抽象。**唯一例外是 deploy --wait 与 download-backup（H1/M4）**——说明抽象本身是成立的，缺陷是漏用。
4. **skills 字面命令围栏守卫真的执行真实 CLI**：`skillLint` 把围栏行与内联 span 逐条喂给 `NewApp(...).Run`（`skills_guard_test.go:218-235`），`dialClient` 毒化保证零网络；`TestSkillsGuardRedLight`（`:85-147`）对死动词/死旗标/旗标后置/围栏元字符/frontmatter/缺章节七类漂移做常驻红灯实验。这是"A 面文档由测试承载"的稀缺实践。
5. **golden 走真链路而非打桩**：`newGoldenHarness` 用 apitest 全链（真 gRPC 服务、真 SQLite、假 runtime/clock）+ ULID/指纹/error_id 归一（`golden_helpers_test.go:17-53`、`verbs_golden_test.go:27-68`），474 个 golden 由 74 个测试驱动；本机 overlay 后全绿（4.9s），说明体系可维护、无 flake。
6. **SDK 公开面纪律**：`sdk/go/go.mod` 只依赖 genproto+grpc（无 internal）；`grpcs://` 的 TLS 不是"注释承诺"——`TestDialGRPCSSchemeNegotiatesTLS` 用自签证书服务端断言失败原因是 x509 校验（`client_scheme_test.go:84-98`），`grpc://` 剥前缀与裸地址各有往返测试；`FromError` 全字段投影/error_id 提升/三类负例有测试（`errors_test.go:31-84`）。

## 4. 发现清单

（按严重度排序；每条均已读调用方/相邻实现排除误读。带"亲验"的条目在报告头描述的 overlay 探针中复现过。）

### R10-H1｜`deploy --wait` 的等待流被请求级 120s deadline 腰斩：超过 2 分钟的部署必然假失败
- 维度：可靠性 / 功能
- 严重度：**H**（校准：`--wait` 是最主要的机器编排锚，README/skills 均以此示范；构建/发布超 120s 是常态而非边角。实测 2m0s 后退出码 1、stderr 归因为"部署停在 queued"——**部署本身仍在服务端继续**，Agent/CI 会把它当失败并重试（重试可能 merged/supersede 正在进行的部署）。无任何测试覆盖 `--wait` 旗标，故无守卫兜底。）
- 位置：`cmd/fleetly/cmd/verbs_delivery.go:242-247`（120s deadline 写入 ctx）、`:254-255`（同一 ctx 交给 `WaitDeployment`）；相关：`dial.go:71-73`（noDeadline 本意）、`verbs_delivery.go:312`（standalone `deployments wait` 反而无此上限）。
- 证据：
  ```go
  // verbs_delivery.go:222-225：--wait 已声明"流式长面，豁免请求级 deadline"
  if wait || fromDir != "" { dialOpts = append(dialOpts, noDeadline()) }
  ...
  // :242-247 —— 为未带 deadline 的 unary Deploy 补 120s 上限
  if _, ok := ctx.Deadline(); !ok {
      var dctx context.Context
      dctx, cancel2 := context.WithTimeout(ctx, cliRequestTimeout)
      defer cancel2()
      ctx = dctx              // ← 覆写后的 ctx 继续用于等待流
  }
  resp, err := c.Deployments.Deploy(ctx, req)
  ...
  if wait {
      final, werr := waitDeploymentFrames(ctx, c, depID, ...)  // :255 继承 120s
  ```
- 亲验（overlay 探针 `TestR10ProbeDeployWaitDeadline`，manual 夹具不驱动 → 部署恒 queued）：
  `deploy --wait => code=1 elapsed=2m0s stdout="deployment web-1-d1 queued" stderr="deployment web-1-d1 ended in state queued"`；
  随后 SDK 读回 `deployment row after wait: id=web-1-d1 state=queued`（服务端部署未失败）。恰为 `cliRequestTimeout=120s`。
- 影响：所有 `fleetly deploy … --wait`（含 `--from-dir`/railpack/static 构建）只要构建+滚动超过 120s 即退出 1；`deploy --from-dir` 上传不受限（在 deadline 块之前），但 Build 阶段照样被算进 120s。skills 的 `deploy-diagnose`（SKILL.md:14、:136-139 均示范 `--wait`）与 Agent 的"提交-等待-验证"循环会把成功部署误判为失败；standalone 的 `deployments wait` 不受影响（无该 deadline），恰可作为临时规避。
- 建议：把 deadline 只作用于 Deploy unary（子 ctx + 立即 cancel），等待流用原始流式 ctx：
  `dctx, cancel := context.WithTimeout(ctx, cliRequestTimeout); resp, err := c.Deployments.Deploy(dctx, req); cancel(); if wait { waitDeploymentFrames(ctx, ...) }`。成本 **S**。机制层：给"`--wait` 旗标"补一个可注入短超时的测试接缝（如 `var cliRequestTimeout = 120*time.Second` 可变包变量），断言等待流 ctx 无 deadline——当前 `cliRequestTimeout` 是 const，无法在测试里缩短，这是该缺陷零覆盖的根因。
- 状态：**已亲验**（overlay 探针，120s 实测）。

### R10-M1｜两个 delete 成功路径的 `--json` 输出为空：机器契约在"成功但不回包"处断裂，且 golden/守卫把空输出钉成合法
- 维度：功能 / 测试与守卫
- 严重度：**M**（校准：不影响数据面，直接违背 README:62"`--json` 全命令覆盖"与 Agent 面的"一律可解析"预期；`| jq` 类消费方直接报错。对比 `tasks delete --json` 输出 `{"deleted": id}`（`verbs_automation.go:349-351`）与 `schedules delete --json`（`:691-693`），同一类动词三种形态。）
- 位置：`cmd/fleetly/cmd/verbs_alerting.go:190-193`（channels delete）、`:296-299`（alerts rules delete）；`cmd/fleetly/cmd/testdata/golden/channels-delete-json.golden`（0 字节）、`alerts-rules-delete-json.golden`（0 字节）；`coverage_test.go:44-46`（只查文件存在）。
- 证据：
  ```go
  // verbs_alerting.go:186-194（alerts rules delete 同构，:292-300）
  if _, err := c.Alerting.DeleteNotificationChannel(ctx, &telemetryv1.DeleteNotificationChannelRequest{ChannelId: channelID}); err != nil {
      return err
  }
  if !jsonOut {
      _, _ = fmt.Fprintf(env.Stdout, "deleted channel %s\n", channelID)
  }
  return nil        // ← --json 形态什么都不写
  ```
- 亲验（overlay 探针 `TestR10ProbeDeleteJSONOutput`）：`channels delete --json => code=0 stdout.len=0 stdout=""`。
- 影响：以 `--json` 契约编排的 Agent 在删除成功后收不到任何可解析对象；空 stdout 还可能被脚本误判为"命令未执行"。guard 只按文件名检查（`TestAllVerbsHaveDualGoldens`），0 字节 golden 反而让缺口"合法化"。
- 建议：与 `tasks delete`/`schedules delete` 统一回 `writeJSON(map[string]string{"deleted": id})`（或删除响应 protojson，若服务端回 Empty 则 `{}` 也可解析）；成本 **S**。机制层：给 `--json` golden 增加"成功路径必须 `json.Valid`"断言（见 §6），并对 delete 族统一形态。
- 状态：**已亲验**。

### R10-M2｜18 条 `usage:` 把旗标排在位置参数之后：照抄 `--help` 即失败（exec 更会把旗标当命令 argv 静默下发）
- 维度：功能 / 文档一致性
- 严重度：**M**（校准：Go `flag` 在首个位置参数处停析——这是仓内自己写进 skills 守卫红灯实验的"旗标前置铁律"。`--help` 是 Agent 的首选发现面（README:65 甚至把 `schema/explain` 称为零文档发现面），给出不可执行用法是把发现面变成陷阱；exec 的形态会把 `--instance ID` 并入容器命令，语义错位而非报错。）
- 位置（旗标在位置参数后的 usage 行，逐条为 `文件:行`）：
  `verbs_exec.go:214`（exec）、`:240`（shell）；`templates.go:182`（instantiate TEMPLATE 在前）；
  `verbs_databases.go:28`（create … NAME [--restore-from-backup]）、`:188`（backups DATABASE_ID [--after]）、`:254`（download-backup … BACKUP_ID --out）、`:363`（browse DATABASE_ID [--write]）；
  `verbs_alerting.go:26`（metrics query PROMQL [--start]…）；
  `verbs_structure.go:59`（projects create NAME [--team]）、`:312`（apps vars set … NAME [--value]）、`:559`（secrets put）、`:635`（configs put）、`:720`（shared-variables put）、`:828`（volumes create）、`:889`（networks create … NAME [--egress-none]）；
  `verbs_identity.go:27`（tokens create NAME --role）、`:122`（users create NAME --role）、`:306`（roles create NAME --scope）；共 18 条。
- 证据（同一解析律的反证已在仓内）：
  ```go
  // exec_golden_test.go:57-59 —— 仓内明文"旗标前置铁律"
  {"exec-anchored", []string{"exec", "--instance", apitest.FakeExecInstance, appID + "/web", "--", "echo", "anchored"}},
  // verbs_exec.go:214 —— 但 usage 写的是反序
  usage: "exec <app-id>/<process> [--instance ID] -- <command> [args...]",
  // templates_golden_test.go:88-89 的真实调用同样是"位置参数在最后"
  ```
- 亲验（overlay 探针 `TestR10ProbeUsageLineFlagOrder`）：
  - `projects create shop --team default` → `code=64 stderr="expected exactly one NAME argument"`（照抄 `verbs_structure.go:59`）；
  - `templates instantiate nginx --project p --app a --no-wait` → `code=64 "expected 1 positional argument(s), got 6: [nginx --project p --app a --no-wait]"`（照抄 `templates.go:182`）；
  - `exec APPID/web --instance i-1 -- echo x` → `code=1 "E_NOT_FOUND: app not found"`（**未返回 64**）：Go flag 在首个位置参数处停析，`--instance i-1` 不会被解析而落入 argv；本次探针用假 appID，先在会话受理处失败，未走到 argv 下发观测（下发形态由 `splitExecArgs` 语义确定）。
- 影响：所有照抄 `--help` 的 Agent/用户在这 18 个动词上直接吃 64；exec/shell 的实例锚定（ADR-0067）在文档形态下静默失效。skills 围栏守卫恰好只执法 skills 文本、不执法 CLI 自己的 usage 行，故长期共存。
- 建议：逐条把 usage 改为旗标前置（或把位置参数写在末尾）；成本 **S**（18 字符串）。机制层：新增守卫把每个动词 `Usage()` 的 `[--x]` 与位置占位顺序喂给真实 parser（与 skills 守卫同款断言），这能把"帮助面即契约"变成可执行承诺。
- 状态：**已亲验**。

### R10-M3｜quickstart / templates instantiate 的 sslip 缺省 host 不剥 `grpc(s)://`：ADR-0079 公网接法下必然失败且留下半途资源
- 维度：功能
- 严重度：**M**（校准：只在 `FLEETLY_ADDR` 带 scheme 时触发；但 runbook:1188/1193 明确把 `FLEETLY_ADDR=grpcs://api.dev.fleetly.run` 记为"现行 CLI 公网接法"，ADR-0079 验收锚之一。一次 onboarding 命令 100% 失败，且失败发生在 project/app/deploy 已铸之后。）
- 位置：`cmd/fleetly/cmd/quickstart.go:36-49`（`sslipHost`）、`:84`（`serverAddr := envOr(envAddr, defaultAddr)`，原样传 scheme）、`:148-150`；`cmd/fleetly/cmd/templates.go:201`、`:310`（`parseTemplateSets` 缺省 domain 同源）；服务端拒绝面：`internal/interfaces/grpc/fleetlygrpc/contexts.go:195-197` + `internal/domain/shared/capability/routevalidate.go:20-28`。
- 证据：
  ```go
  // quickstart.go:36-49
  func sslipHost(addr, name string) string {
      host := addr
      if h, _, err := net.SplitHostPort(addr); err == nil { host = h }
      if host == "" || host == "localhost" { host = "127.0.0.1" }
      if net.ParseIP(host) == nil {
          return name + "." + host          // "demo.grpcs://api.dev.fleetly.run"
      }
      return name + "." + host + ".sslip.io"
  }
  // quickstart.go:84 —— 与 dialFromEnv 不同，这里没有 SDK 的 scheme 剥离
  serverAddr := envOr(envAddr, defaultAddr)
  ```
- 亲验（overlay 探针 `TestR10ProbeQuickstartSchemeAddr`，`FLEETLY_ADDR=grpc://127.0.0.1:9080`）：
  `quickstart (grpc:// addr) => code=1`，stderr=`E_INVALID_ARGUMENT: host: must be a DNS hostname …`；随后 `projects list` 显示 `quickstart-1` 已被创建（**半途副作用**：project 已铸、deployment 已提交，仅 Route 失败）。
- 影响：ADR-0079 公网用户执行 `fleetly quickstart` 得到误导性的 host 校验错误 + 残留资源；templates instantiate 的缺省 domain 同样坏（用户以为自己在用一键部署）。裸 `host:port` 形态与测试夹具（默认地址）不受影响，故 474 golden 全绿也发现不了。
- 建议：`sslipHost` 先按 SDK 同源规则剥 `grpcs://`/`grpc://`（或让 CLI 复用 `fleetly` 包的 scheme 解析单点），再分类；成本 **S**。机制层：为 quickstart/templates 的缺省 host 加一个"带 scheme 的 FLEETLY_ADDR"用例（既有 `addrFlagHint` 的三形态正好是值域）。
- 状态：**已亲验**。

### R10-M4｜`databases download-backup`：服务端流未豁免 120s，且"先截断目标文件、逐帧追加、失败留半文件"——恢复通道在大对象/断链时既超时又污染本地文件
- 维度：可靠性 / 数据安全（本地）
- 严重度：**M**（校准：大库备份 >2 分钟是常态，恢复路径必然撞 120s；更糟的是失败时 `--out` 原有文件已被截断，只剩半截新文件，且命令只回 RPC 错误、不提示文件不完整。恢复/取证场景下这属于"用户以为拿到了备份"。）
- 位置：`cmd/fleetly/cmd/verbs_databases.go:269`（`runRPC(ctx, nil, …)`，未 `noDeadline`）、`:276-279`（先 `os.WriteFile(out, []byte{}, 0600)` 截断）、`:289`（逐帧 `appendFile`）、`:348-356`（每帧 open/close 追加）。
- 证据：
  ```go
  // :269 —— 服务端流式下载，但走普通 unary 的 120s 预算
  return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
      stream, err := c.Databases.DownloadBackup(ctx, &structurev1.DownloadBackupRequest{...})
      ...
      // :276-279 —— 先清目标文件
      if err := os.WriteFile(out, []byte{}, 0o600); err != nil { return err }
      for {
          frame, rerr := stream.Recv()
          if rerr == io.EOF { break }
          if rerr != nil { return rerr }        // ← 半文件留在磁盘
          if err := appendFile(out, frame.GetData()); err != nil { return err }
      }
  ```
- 影响：`fleetly databases download-backup --out backup.tar.gz` 在 >120s 时退出 1 且磁盘留下被截断的备份；重复执行到同一 `--out` 会先毁掉上一次的文件。CLI 侧 happy path **零测试**（`databases_download_golden_test.go:3-5` 明说 happy 流由 engine 单测钉，CLI 只测 not-found 错误面），文件语义（截断/追加/半文件）无任何断言。另：不校验台账 `size_bytes`/digest（`databases verify` 是另一条手动命令），EOF 即视为完整。
- 建议：①拨号加 `noDeadline()`；②写临时文件（同目录 `.part`）成功后 `os.Rename` 到 `--out`（失败删临时）；③一次 `os.OpenFile` 持句柄而非逐帧开关（顺带修大对象 O(n) 系统调用）；④成功后可选 `size==台账 size_bytes` 或调 `VerifyBackup`。成本 **S/M**。
- 状态：**已亲验（代码路径与测试缺口）**；120s 行为与 H1 同源机制，未单独跑大对象实测。

### R10-M5｜连接参数面声明与实现不一致：凭据文件 `addr` 只写不读、`--addr` 仅 4 个动词可用、`dialFromEnv` 从不读动词旗标
- 维度：功能 / 文档一致性
- 严重度：**M**（校准：`login --addr <远程>` 成功写入凭据后，下一条 `whoami/apps list` 静默拨回 `127.0.0.1:9080`——用户刚登录成功却"连不上"，且没有提示要 export FLEETLY_ADDR（init 有该提示，login 没有）。文档承诺的解析序 `flag > env > 凭据文件` 中 addr 一整条链不成立。）
- 位置：`cmd/fleetly/cmd/dial.go:39-52`（`resolveToken` 只对 token 读凭据）、`:59-64`（addr 只取 env/默认，注释明确"凭据文件的 addr 不参与"）；`cmd/fleetly/cmd/auth.go:3-7`（头部注释却写"解析序 … > 凭据文件 > 内建默认"）、`:76-88`（login/init 都把 addr 写入文件）；`cmd/fleetly/cmd/conn.go:24`（`connFlags` 注释"贯穿全部 RPC 动词"，实际仅 `status.go:17,27` 使用）、`:35-48`。
- 证据：
  ```go
  // dial.go:59-64
  var cf connFlags
  // addr 缺省从环境取（凭据文件的 addr 不参与：endpoint 只信显式配置）
  cf.addr = envOr(envAddr, defaultAddr)
  token := resolveToken(cf.token)
  // auth.go:3-7（同一仓库内的相反声明）
  // 解析序 显式旗标 > FLEETLY_* env > 凭据文件 > 内建默认
  ```
- 亲验（overlay 探针 `TestR10ProbeConnAddrResolution`，凭据文件 `addr = grpc://remote.example:9080`、`FLEETLY_ADDR` 清空、捕获 `dialClient` 入参）：
  - `whoami => code=1 dialed="127.0.0.1:9080"`（凭据 addr 被忽略，token 被使用）；
  - `whoami --addr grpc://remote.example:9080 => code=64 "flag provided but not defined: -addr"`；
  - `status --addr grpc://remote.example:9080 => dialed="grpc://remote.example:9080"`（仅 status 生效）。
- 影响：ADR-0079/runbook 的公网接法必须靠环境变量；`login --addr` 的旗标帮助与 `credentials saved` 回执让用户以为地址已被记住。Agent 若按 usage 给远程动词加 `--addr` 会直接 64。
- 建议：二选一并让文档同源——(a) addr 也按"flag > env > 凭据文件"解析（凭据文件来自 `os.UserConfigDir`，已有 `.env` 同类威胁模型注释，可接受）；或 (b) 明确 addr 只信 env/flag，login/init 不再写 addr 并在 login 回执里提示"set FLEETLY_ADDR=…"。同时决定 `--addr/--token` 是否作为所有 RPC 动词的通用旗标（若要，`dialFromEnv` 需接收旗标值；若不要，删掉 `connFlags` 的"贯穿全部"注释）。成本 **S**。
- 状态：**已亲验**。

### R10-M6｜Windows 上 `cmd/fleetly` 不可编译（`syscall.SIGWINCH` 无 build tag 拆分），连带整个 CLI 契约测试/技能守卫在项目自己的 Windows 开发机上不可运行
- 维度：可移植性 / 测试与守卫（开发体验）
- 严重度：**M**（校准：CI 全 Ubuntu，故不红；产品运行面是 Linux。但仓库明确在 Windows 开发机上开发（runbook 大量"本机 Windows"、ADR-0036 专门做 Windows 确定性、`doctor_disk_windows.go` 提供 Windows 实现），且 CLI 被裁定"一律本地执行"——Windows 笔记本正是 CLI 的典型运行位。修复成本极低（S），且修复后 74 个 CLI 测试 + 474 golden 在 Windows 全绿（我以 overlay 证明）。若 Windows CLI 在支持矩阵内，应升 H。）
- 位置：`cmd/fleetly/cmd/verbs_exec.go:147-171`（SIGWINCH 传播，无 `_unix.go` 拆分）；`main.go:18`（`syscall.SIGTERM` 在 Windows 有定义，非问题）；对照 `cmd/fleetly/cmd/doctor_disk_unix.go:1`/`doctor_disk_windows.go`（项目已有正确的 build 拆分范式）；`README.md:12`（"宿主 Linux 二进制"）；`.github/workflows/ci.yml:15…`（14 个 job 全 `ubuntu-latest`，无 Windows/GOOS 矩阵）。
- 证据：
  ```go
  // verbs_exec.go:157-160
  winch := make(chan os.Signal, 1)
  signal.Notify(winch, syscall.SIGWINCH)   // Unix-only 符号，所在文件无 build tag
  defer signal.Stop(winch)
  ```
  本机实测：
  `go build ./cmd/fleetly`（windows/amd64）→ `verbs_exec.go:159:33: undefined: syscall.SIGWINCH`；
  `GOOS=linux go build -o <tmp> ./cmd/fleetly` → 通过；`GOOS=linux go vet ./cmd/fleetly/...` → 干净；
  overlay 仅把该行换成空操作后：`go test ./cmd/fleetly/...` → `ok … 74 tests / 0 FAIL`（含全部 474 golden）。
- 影响：Windows 开发者无法 `go build`/`go test ./cmd/...`，skills 守卫（同包测试）也跟着失明；CLI 若想分发给 Windows 用户则完全不可用。项目未声明支持矩阵，属"未声明的平台假设"。
- 建议：把 `pumpExecUpstream` 的 resize 传播拆到 `exec_resize_unix.go`（`//go:build !windows`）与 `exec_resize_windows.go`（no-op 或 Console API），主文件只调用 `notifyResize(stream, dims)`；成本 **S**。机制层：CI 增加 `GOOS=windows go build ./cmd/...`（甚至 `go test` 只需一个 job 的编译步），成本 S；并在 README 声明支持矩阵（Linux 服务端 / 任意 OS CLI 或仅 Linux CLI）。
- 状态：**已亲验**（build 失败 + overlay 后全绿）。

### R10-M7｜`exec`/`shell` 上行泵有两个 goroutine 并发 `stream.Send`（且 `CloseSend` 与 Send 可能并发）——违反 gRPC 流契约
- 维度：可靠性 / 并发
- 严重度：**M**（校准：gRPC 明示 **"not safe to call SendMsg on the same stream in different goroutines"**、**"not safe to call CloseSend concurrently with SendMsg"**（grpc-go `stream.go` ClientStream 契约）；触发条件是交互 TTY 会话中的窗口 resize（人类常态），失败形态可能是帧错乱/发送错误而非确定性崩溃，故不升 H。当前无 TTY 测试可覆盖（测试 stdin 非终端，resize 分支不进）。）
- 位置：`cmd/fleetly/cmd/verbs_exec.go:158-171`（resize goroutine 内 `stream.Send`）；`:182-195`（同一 stream 的 stdin `Send` 与 EOF 时 `CloseSend`）。
- 证据：
  ```go
  // :161-171 —— goroutine A：resize 帧
  go func() {
      for range winch {
          if w, h, err := term.GetSize(int(stdin.Fd())); err == nil {
              _ = stream.Send(&runtimev1.StreamExecSessionRequest{...Resize...})
          }
      }
  }()
  // :182-195 —— goroutine B（当前 goroutine）：stdin 帧 + CloseSend
  for {
      n, err := stdin.Read(buf)
      if n > 0 {
          ... stream.Send(&runtimev1.StreamExecSessionRequest{...Stdin...})
      }
      if err != nil { _ = stream.CloseSend(); return nil }
  }
  ```
- 影响：`fleetly shell`/`exec -tty` 会话在调整终端窗口时可能撞 gRPC 帧写入不变量；另 `for range winch` 在函数返回后永不退出（`signal.Stop` 不关 channel），进程内泄漏一个 goroutine（对一次性 CLI 进程影响小，对测试/嵌入复用有影响）。
- 建议：单一发送者——把 resize 事件送入 channel，由 stdin 泵的同一 goroutine `select` 合并发送（或加 `sendMu`）；`winch` goroutine 用 `done` channel 退出。成本 **S/M**。机制层：为 `pumpExecUpstream` 抽一个可注入的假 stream（记录 Send 并发），配 `-race` 用例。
- 状态：**已亲验（代码路径 + gRPC 公开契约）**；未做运行时复现（需要真 TTY）。

### R10-M8｜skills `database-provision` 与 CLI 现状实质漂移：说"轮换未暴露/备份恢复不存在"、引擎枚举少两枚，且自相矛盾——围栏守卫只查解析层，抓不到
- 维度：文档一致性 / 功能（Agent 面）
- 严重度：**M**（校准：skills 是 Agent 的工作流真源；该技能会让 Agent 选择"重建数据库"而非现成的 `rotate-password`，并主动回避已上线的 backup/restore/verify 面。守卫按设计"反向覆盖不执法"，所以这类语义漂移无守卫兜底，正是 PROFILE 要求审查的"技能内容与 CLI 现状是否漂移"。）
- 位置：`skills/database-provision/SKILL.md:14-15`（"rotated by overwriting the secret"）、`:104`（"Engine values are exactly postgres, pgvector, redis"）、`:108`（"In-place rotation is not exposed yet — recreate the database"）、`:113-114`（"restore/verify surfaces do not exist yet — do not promise them"）；CLI 现状：`verbs_databases.go:28-34`（`--restore-from-backup`）、`:157-180`（backup）、`:183-213`（backups）、`:216-241`（verify）、`:248-307`（download-backup）、`:320-344`（rotate-password）；`app.go:84-87`（组帮助列五引擎）、`README.md:54`（"backup/verify/恢复"）；守卫：`skills_guard_test.go:218-227`（只把 exit 64 当漂移，0/1 视为通过）。
- 证据：
  ```md
  <!-- skills/database-provision/SKILL.md:104（引擎枚举漏 mysql/mongo） -->
  | `E_INVALID_ARGUMENT` on `--engine` | (error text lists the legal engines) | Engine values are exactly `postgres`, `pgvector`, `redis`; ...
  <!-- :108 -->
  | Rejected when writing secret `database:...` | ... | ... In-place rotation is not exposed yet — recreate the database ...
  <!-- :113-114 -->
  - Backups land with F2 (local object store target is registered already);
    restore/verify surfaces do not exist yet — do not promise them.
  ```
  ```go
  // verbs_databases.go:320-326 —— CLI 实际已提供轮换
  func newDatabasesRotatePasswordVerb() commands.Command {
      const name = "rotate-password"
      ...
      usage: "databases rotate-password DATABASE_ID",
  ```
- 影响：Agent 按技能建议重建库（新 id、新凭证，旧凭证与卷保留）而非轮换；或认为备份/恢复不可用而不使用已上线的动作。技能内部还自相矛盾（:15 说"覆盖 secret 即轮换"，:108 说写入 `database:` 前缀被拒——后者才是实现事实：`internal/interfaces/grpc/fleetlygrpc/structure.go:596` 拒保留前缀、`internal/application/engine/database.go:29,45-48` 是前缀单源）。
- 建议：随批更新技能（轮换/备份/恢复/验证四组命令与 mysql/mongo 引擎），并把"能力存在性"断言做成守卫：例如从 `app.go` 注册表生成动词清单，比对 SKILL.md 中提到的"未暴露/不存在"类断言（至少对"not exposed/do not exist"关键词做反向检查）。成本 **S**（文档）/ **M**（守卫）。
- 状态：**已亲验**（CLI/README 对照；试跑因 Windows 编译失败未执行技能守卫，但守卫代码明示只查 exit 64）。

### R10-M9｜SDK `FromError` 文档称"errors.As 风格"，实现用直接类型断言：包装后的 `*fleetly.Error` 无法投影
- 维度：功能（公开 API）
- 严重度：**M**（校准：公开 SDK 是外部集成面；`fmt.Errorf("deploy failed: %w", sdkErr)` 是 Go 消费方常态，`FromError` 会返回 false，调用方丢失 errcode/suggestion/docs。仓内无消费者（CLI 用 internal apperrstatus），故现有测试永远发现不了。修复 2 行。）
- 位置：`sdk/go/fleetly/errors.go:44-58`（doc 与实现）；测试缺口：`errors_test.go:31-84`（只有直接 `*Error`/status/nil/普通 error 四类，无包装）。
- 证据：
  ```go
  // errors.go:44-53
  // FromError 从错误链还原信封投影（errors.As 风格；nil-safe；...）
  func FromError(err error) (*Error, bool) {
      if err == nil { return nil, false }
      if e, ok := err.(*Error); ok { return e, true }   // ← 直接断言而非 errors.As
      st, ok := status.FromError(err)
      if !ok { return nil, false }
      ...
  ```
- 亲验（overlay 探针 `TestR10FromErrorWrapped`，替换 sdk 的 errors_test.go 后执行）：
  `FromError(wrap(*Error)) => ok=false e=<nil>`；同一错误 `errors.As(wrap(*Error), **Error) => true`。
- 影响：外部集成方无法在包装错误链上还原平台信封；文档与行为不符。同理 `*Error` 实现 `GRPCStatus()` 也不存在，`status.FromError` 对包装后的 SDK 错误只能拿到 `Unknown`（当前实现直接 false，反而更"诚实"，但不符合注释承诺）。
- 建议：改为 `var e *Error; if errors.As(err, &e) { return e, true }`；补包装用例；成本 **S**。
- 状态：**已亲验**。

### R10-L1｜双形态 golden 守卫只查"文件名存在"且只递归一层：空 JSON 通过与嵌套组员失守
- 维度：测试与守卫
- 严重度：**L**（校准：当前 474 golden 实际齐全、套件全绿；这是"未来缺口不被拦截"的守卫质量缺陷，非现网错误。与 M1 的空 JSON 合法化是同一机制的两个面。）
- 位置：`cmd/fleetly/cmd/coverage_test.go:28-47`；组嵌套：`app.go:133-136`（`alerts` 组套 `rules` 组）、`app.go:69-71`（`apps` 套 `vars`）。
- 证据：
  ```go
  // coverage_test.go:28-47（节选）
  for _, name := range names {
      verbNames := []string{name}
      cmd, ok := app.Lookup(name)
      if ok {
          if fv, ok := cmd.(*flaggedVerb); ok {
              for _, sub := range fv.subcommands {
                  verbNames = append(verbNames, name+" "+sub)   // 只一层，不递归
              }
          }
      }
      ...
      human, _ := filepath.Glob(filepath.Join("testdata", "golden", file+".golden"))
      assert.NotEmpty(t, human, ...)   // 文件存在即通过（0 字节也通过）
  ```
- 影响：`alerts rules create/list/delete`、`apps vars list/set/unset` 等嵌套组员的 golden 缺失不会被守卫发现；任何 golden 只要文件存在（哪怕从未被 compareGolden 引用、或 0 字节）即通过。guard 的注释"覆盖守卫据此递归枚举组内子命令"（`verb.go:20-22`）与实现不符。
- 建议：递归展开（`flaggedVerb.subcommands` 再查 `app.Lookup` 的嵌套组）；对成功退出码的 `-json` golden 断言 `json.Valid` 且非空；并统计"每个 golden 在本包测试源码中被 `compareGolden` 引用"（可用 `go/ast` 扫描 + 动态命名白名单）。成本 **S/M**。
- 状态：**已亲验（静态 + 空 golden 实测）**。

### R10-L2｜`tasks scale` 的 `--concurrency` 缺省 0 且未强制显式给出：漏一个旗标即静默排空 resident 池
- 维度：功能 / 可靠性
- 严重度：**L**（校准：帮助文本确实写了"0 drains the pool to zero"，因此属"文档化了的脚枪"；但对 Agent 编排是危险默认——`tasks scale --task X` 语法合法且退出 0，生产池被清零无二次确认。`0` 是合法值域（`automation.proto:342-344`），所以服务端不会拒绝。）
- 位置：`cmd/fleetly/cmd/verbs_automation.go:273-292`。
- 证据：
  ```go
  fs.StringVar(&id, "task", "", "task id (required)")   // :275
  fs.Int64Var(&concurrency, "concurrency", 0, "desired run count (0 drains the pool to zero)")  // :276
  ...
  if id == "" { return usageErr(name, "--task is required") }   // :282-284：未检查 --concurrency 是否显式提供
  resp, err := c.Tasks.ScaleTask(ctx, &automationv1.ScaleTaskRequest{Id: id, DesiredConcurrency: concurrency})
  ```
- 亲验（overlay 探针 `TestR10ProbeTasksScaleImplicitZero`）：`tasks create … --concurrency 2` → `tasks scale --task pool-1` → `tasks scale (no --concurrency) => code=0 out="task pool-1 desired concurrency set to 0"`。
- 建议：用 `fs.Visit` 判定旗标是否出现（或默认 -1 + 显式值域校验），缺省即 64；或对 0 要求 `--drain` 显式确认。成本 **S**。
- 状态：**已亲验**。

### R10-L3｜根命令 `--json` 仍是人类帮助文本：组是 JSON 契约面，根不是
- 维度：功能（自描述）
- 严重度：**L**（校准：Agent 仍可逐组 `fleetly <group> --json` 拿到子命令，或用服务端 `schema/explain`；只是根级"零文档发现面"不完整、与 `groupVerb` 的自我声明不一致。）
- 位置：`cmd/fleetly/cmd/app.go:32-46`（只设 `RootBoolFlags` 与帮助文本，无根级 JSON 分支）；`cmd/fleetly/cmd/group.go:26-35`（组无参时 `writeJSON(subNames)`，注释称"组本身也是 --json 契约面"）。
- 证据（overlay 探针 `TestR10ProbeRootJSONHelp`）：
  - `root --json => code=0 stdout.first="fleetly - lightweight PaaS for humans and agents"`（人类帮助）；
  - `apps --json => code=0 stdout="[\"create\",\"delete\",\"list\",…]"`（JSON）。
- 建议：根 `--json` 输出 `{"commands":[{name,synopsis,usage}...]}`（从 `app.Names()`/`Lookup` 生成），与组形态同源；成本 **S**。
- 状态：**已亲验**。

### R10-L4｜`events list` synopsis 过期：仍写"streaming follow lands in N1"，follow 早已在册
- 维度：文档一致性
- 严重度：**L**
- 位置：`cmd/fleetly/cmd/verbs_telemetry.go:25`（synopsis）；`:52-109`（`events follow` 实现与 golden 均在册）。
- 证据：
  ```go
  name: name, synopsis: "List events from the outbox (cursor: after-seq; streaming follow lands in N1)",
  ```
- 影响：`fleetly --help`/`fleetly events --help` 的族描述把已上线能力标成未上线，Agent 可能避开 `events follow`。
- 建议：改为"list events from the outbox (cursor: after-seq; use `events follow` to stream)"；成本 **S**。
- 状态：**已亲验**。

## 5. 交叉观察（不在本模块但值得其他模块注意）

1. **R08/R04（等待流的取消语义）**：`internal/interfaces/grpc/fleetlygrpc/contexts.go:477-482` 的 `subscribeEvents` 在 `ctx.Done()` 时 `return nil`（干净收流），于是服务端把"客户端 deadline/断开"呈现为 **status OK + EOF**。H1 的探针正是因此把 120s 超时显示成"ended in state queued"而非 DeadlineExceeded；`WaitRun`/`WaitBuild`/`events follow` 的所有消费方（含 Console SSE）都不能把"干净 EOF"等同于"终态"。建议等待面在 EOF 后由客户端检查 `ctx.Err()`，或服务端在取消路径返回可识别状态（R08 裁定）。
2. **R09（CI 平台矩阵）**：`.github/workflows/ci.yml` 14 个 job 全 `ubuntu-latest`，无 `GOOS=windows` 编译步；M6 因此永不红。加一条 S 成本步骤即可（`GOOS=windows go build ./cmd/...`）。
3. **R12（技能守卫的执法边界）**：skills 守卫只验"命令解析成立"（exit≠64），已被 M8 的语义漂移穿透；建议把"能力存在性"（CLI 动词/旗标清单 vs 技能正文的承诺句）纳入守卫，或至少在 ADR-0031 记录该面的诚实边界。
4. **R15（文档-代码漂移）**：README:62（全命令 JSON）、README:57（`events（follow SSE）`——CLI 走 gRPC stream，SSE 是 Console 面）、README:12（Linux 二进制 vs Windows 开发机）；skills 漂移见 M8；另有一次 golden 滞后提交（`3857ddee` 落地 `Revision.source_text` 后，`bd0ec3aa` 才补 `revisions-list-strategies-json.golden`，相隔 3 个 commit/26 分钟——见 §6）。
5. **R11（Console 的等待流兜底）**：F3.7 记录 Console 部署叙事有"wait 流断线重连轮询兜底"；CLI 的 wait 族没有任何重连/续 Subscription 能力（断线即退出 1），若该健壮性属于产品承诺，两侧应同口径。
6. **R06/R13（exec 并发契约）**：M7 是客户端侧；服务端 exec relay 的帧写入并发纪律建议 R04/R06 对照检查（同一会话两条写入路径的序列化）。
7. **R09/R14（`--addr` 与公网形态）**：`addrFlagHint` 已把 `grpcs://` 写进所有展示位，但真正消费 scheme 的只有 SDK `Dial`；M3/M5 都是"scheme/地址在 CLI 内多处各自解析"的后果，建议在 CLI 内建立单一 `endpoint` 解析件（scheme 剥离、host/port 拆分、sslip 拼接共用）。

## 6. 测试与守卫质量评估

**覆盖强度（事实）**：474 个 golden（231 `-json`）由 74 个测试驱动；`go test ./cmd/fleetly/...`（overlay 仅补 SIGWINCH）**74 PASS / 0 FAIL / 4.9s**；报错面有专门测试（`apps_restart_golden_test.go` 把 E_NOT_FOUND 信封整段 golden 化、`registry/system/databases_rotate` 等对 `E_*` 做 contains 断言）；`TestExitCodesAreStable` 钉 0/2/64；`TestFlagVerbsRejectPositionalArgs`（`coverage_test.go:96-151`）把 44 个旗标形态动词的多余位置参数逐一钉 64。**结论：CLI 的"人类形态 + 退出码 + 常见错误面"是被真测试承载的，不是注释承诺。**

**断言强度与假阳性/假阴性**：
- **无 JSON 语义校验**：全包测试对 `json.Valid`/`json.Unmarshal` **零命中**（grep 证据），`--json` 只做字符串 golden 比对。因此 0 字节 JSON golden（5 个：`alerts-rules-delete-json`、`channels-delete-json`、`builds-logs-json`、`metrics-query-json`、`metrics-query-empty-json`，前两者是成功路径，后三者是失败/空流路径）不会被判非法。M1 由此"合法化"。
- **守卫只查文件名**：`TestAllVerbsHaveDualGoldens` 存在性即通过、不递归嵌套组、不校验 golden 是否真被 `compareGolden` 消费（详见 L1）。
- **`--wait` 旗标零覆盖**：全包测试 grep `--wait` **零命中**；`deploy --wait` / `rollback --wait` / `tasks create --wait` 三条 promise 仅靠 standalone 等待动词与 quickstart/templates 的默认等待间接背书——H1 正落在这个洞里。
- **CLI happy path 的文件语义零覆盖**：`databases download-backup` 只测 not-found 错误面（`databases_download_golden_test.go:13-29`），截断/追加/半文件语义无断言（M4）。
- **SDK 包装错误零覆盖**：`FromError` 四类用例全为"直接形态"（M9）。
- **错误信封多数只 `strings.Contains("E_X")`**：完整 suggestion/docs 仅少数 golden 钉住（例如 `apps-restart-unknown.golden`、`templates-show-missing.golden`）；`renderErrorFor` 的 E_INTERNAL 脱敏分支**无针对性断言**（`grep E_INTERNAL` 在 cmd 测试仅两处注释命中，无 golden/断言），属轻度盲区（未单列为发现）。
- **`-update` 纪律**：`compareGolden` 的 `-update` 是无条件重写、`TestAllVerbsHaveDualGoldens` 在 `-update` 时跳过（`coverage_test.go:21-23`），因此 `-update` 一轮可以"自证"任何行为。实际发生过一次 golden 滞后提交（`3857ddee` → `bd0ec3aa`），说明"同 commit 纪律"无自动化守卫（属 R15 面，这里作为测试体系风险记录）。
- **fixture 自证性**：golden 夹具是 apitest 全链（真 gRPC + 真 SQLite + 假 runtime/clock），不是打桩 handler——这点显著优于常见 CLI 测试；但"手动 Drive/ReportRunning" 意味着 engine 的时间/观测语义未参与，CLI 只验证了"服务端某个响应会渲染成什么"，而非真实部署时序。**不构成缺陷**，但解释了为什么 H1（120s）与 M4（长流）不会被发现。
- **平台面**：CLI 测试在 Windows 上不可编译（M6），技能守卫因此也失明；这是"守卫在开发者本机不执行"的系统性风险（CI 仍覆盖，故不是失效）。

## 7. 未决与置信度

**最不确定的 3-5 点**：
1. **H1 的根因归属**：我确认了"2 分钟上限 + 服务端仍继续 + 退出码 1"，并定位到 `verbs_delivery.go:242-247` 的 ctx 覆写；但流在客户端 deadline 与服务端 `ctx.Done()→nil` 之间存在竞态——EOF 也可能是服务端先返回干净流。修复点在 CLI（把 deadline 只给 unary）无论如何成立；若 R08 认为服务端应返回可识别取消状态，则两层都要改。置信度：高（用户可见缺陷已实测；根因归属中高）。
2. **Windows 支持是否在范围内**：README 只说"宿主 Linux 二进制"，没有支持矩阵。若产品只支持 Linux CLI，M6 可降 L（开发体验）；若 Windows 用户/开发者是目标，应升 H（CLI 不可用）。置信度：对"未声明矩阵"高，对"意图"低。
3. **M7（exec 并发 Send）的实际故障率**：gRPC 契约明确禁止，但 http2 传输层可能碰巧串行化写入，因此"必然错乱"不能断言；且测试 stdin 非 TTY，无法构造 resize。置信度：对"违反契约"高，对"用户可感知频率"低。
4. **golden 全覆盖性**：74 个测试确实驱动 474 个文件（套件全绿），但 `goldenFile(st.verb)` 是动态拼接，无法静态证明"每个 golden 都被某次 compareGolden 命中"（孤儿 golden 风险未完全排除）；L1 的守卫缺口使这种漂移可以静默。置信度：中。
5. **H1 在所有部署形态的复现边界**：我用 manual 夹具（部署恒 queued）复现；真实部署若在 120s 内没有状态迁移（长 build），同样会撞上限；若服务端在客户端 deadline 前恰好终态，则正常。未跑真机（Docker 不可达）。置信度：高（机制层面必然）。

**整体置信度：高**（H1、M1-M6、M8-M9、L2-L4 均有代码路径 + 亲验探针；M7 为契约级代码审查；未覆盖面已在 §1 列明）。
