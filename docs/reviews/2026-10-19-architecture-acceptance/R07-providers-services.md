# R07 服务类 Providers 与外部适配 评审报告

> 评审基线：`fleetly` HEAD `35257f18`（2026-10-19），只读评审。模块范围：`internal/infrastructure/providers/{builders,traefik,zot,victorialogs,victoriametrics,localobjectstore,s3objectstore}`、`internal/infrastructure/{github,upload,keyring}`（约 4.6k 生产行）。
> 证据标准：每条发现带 `文件:行` + 代码摘录；结论前已读调用方（engine/assembly/interfaces）排除误读。本机环境性失败（Windows ACL、docker daemon 缺席）按 PROFILE §1.1 不计代码缺陷，但在 §6 作为测试质量面记录。
> 本机复核命令：`go test -count=1 ./internal/infrastructure/providers/{traefik,zot,victorialogs,victoriametrics,localobjectstore,s3objectstore} ./internal/infrastructure/{upload,keyring,github,providers/builders}`。

## 1. 覆盖清单

### 完整读过（生产文件）

- `internal/infrastructure/providers/builders/`：`daemon.go`、`dockerfile.go`、`railpack.go`、`static.go`
- `internal/infrastructure/providers/traefik/`：`provider.go`、`config.go`
- `internal/infrastructure/providers/zot/`：`provider.go`、`content.go`
- `internal/infrastructure/providers/victorialogs/provider.go`
- `internal/infrastructure/providers/victoriametrics/provider.go`
- `internal/infrastructure/providers/localobjectstore/provider.go`
- `internal/infrastructure/providers/s3objectstore/provider.go`
- `internal/infrastructure/github/client.go`
- `internal/infrastructure/upload/store.go`、`extract.go`
- `internal/infrastructure/keyring/keyring.go`

### 调用方/契约对照（完整或按节读）

- `internal/domain/shared/capability/{capability.go,ports.go,runtime.go,faces.go,routevalidate.go}`
- `internal/application/engine/{buildsource.go,managed.go,routepublish.go,lifecycle.go,ensure.go,observdomain.go,drift.go,apipublish.go,console.go,registrycontent.go,options.go,engine.go(字段)}`
- `internal/assembly/{provides.go,proxyconfig.go}`（含 `NewBuilderProviders`/`NewObjectStore`/`NewHealthCheckers`/`NewServices`）、`wire_gen.go` 相关行
- `internal/interfaces/grpc/fleetlygrpc/{alerting.go,contexts.go(logs 全轴),delivery.go(build logs 回填),githubapps.go(受理/校验/错误映射),registry.go}`、`internal/interfaces/grpc/systemgrpc/systemgrpc.go`
- `internal/domain/identity/roles.go`、`internal/domain/shared/spec/spec.go`（Build 校验）
- `proto/fleetly/spec/v1/spec.proto`（BuildSpec）、`proto/fleetly/delivery/v1/registry.proto`、`proto/fleetly/telemetry/v1/telemetry.proto`
- ADR：0019（附录 B 摘要处）、0021/0032/0040/0041/0042 全文、0047/0073 相关节；`docs/plan/2026-09-30-feature-checklist.md` F1.10/F1.11/F2.4/F2.5/F2.8 节；`e2e/dind-upgrade.sh` 探针段

### 抽样读过（测试与夹具）

- `traefik/config_test.go`（全）、`builders/{railpack_test.go,daemon_test.go,build_test.go,static/dockerfile 相关}`、`keyring/keyring_test.go`（全）、`upload/extract_test.go`（全）、`s3objectstore/provider_test.go`（前半）、`victorialogs/provider_test.go`（Query/follow/health 段）、`zot/provider_test.go`（材料/凭证断言段）、`internal/guards/{railpackpin_test.go,resticpin_test.go}`

### 未覆盖 / 无法覆盖

- k3s/swarm Runtime Provider（R06 主审，仅作为受管 Workload 消费面引用）；engine 状态机内部（R02/R04）。
- 真实 e2e（本机 docker daemon 不可达，PROFILE 禁止声称真机复验）：所有"真机行为"类结论均按静态推理并标待复核。
- traefik/zot/VL/VM/cadvisor 上游二进制的拒载/ACL/解析细则（无法在本机启动载体；涉及处标注外部文档依据或待复核）。

## 2. 模块画像与边界

**职责**：R07 是七个 Capability 端口的「服务类实现 + 外部适配」层——`Builder`（dockerfile/railpack/static 三路由，ADR-0032）、`Proxy`（traefik，受管自宿 + HTTP provider 拉取全量动态配置，ADR-0004/0047）、`Registry`（zot 受管 + per-Project 凭证/accessControl，ADR-0019 附录 B/ADR-0036 N2）、`Logging`（VictoriaLogs，ADR-0040）、`Metrics`（VictoriaMetrics + cadvisor 全局采集，ADR-0041）、`ObjectStore`（local/s3，ADR-0020/0042）、以及三类非 Capability 适配：GitHub App 客户端（ADR-0073）、上传 blob/tar 面（ADR-0019 附录 A）、age KEK 文件面（ADR-0014）。

**对外接口**：全部经 `internal/domain/shared/capability` 的端口 + 可选子面（`FacesOf`：Managed/MaterialsSource/ConfigSource/ProjectEndpoints/ProjectScopedMaterials/RegistryContent）；Provider 三件套 `Describe()/Health()/init 自注册工厂`（`capability/capability.go:29-82`）。可选面经装配 ctx 注入配置（`WithRegistryAddr/WithLoggingAddr/WithMetricsAddr/WithObjectStoreS3/WithProxyAuthToken`）——无 env 双通道（traefik/zot 保留 env 旧通道兜底，ADR-0036 记档）。

**依赖方向**：providers 只 import `capability`（+ builders 的 moby/buildkit 外部 SDK）；providers 互不 import（s3/local 的 `safeKey` 是纪律性重复）；assembly 是唯一边界消费方。抽查无违反。

**与 ADR/架构声明的偏差（详见发现清单）**：

1. ADR-0015/`traefik.Describe().Notes` 宣称"路由零中断 / 旧配置继续服务"，实际只覆盖"发布失败"面；控制面重启窗口 + 冷缓存发布可把 LKG 替换为空/部分路由集（H1）。
2. ADR-0041 宣称"查询隔离由平台查询构造执法（Console/API 面按 App 域字段过滤）"，但唯一服务端强制点不存在，`QueryMetrics` 原样透传用户 PromQL（H2）。
3. `capability.Provider.Health` 注释定位为"降级矩阵驱动"，实际只有 `systemgrpc.GetStatus` 聚合消费；traefik Health 恒 true、其余 Provider 仅 TCP 探测（不可用 vs 未配置/未就绪语义混合；M5）。
4. `BuildRequest.CacheFrom/Args` 无生产者（ADR-0032 已记 `cache_from` 无消费者，属**已记档占位**，不单独报缺点）；`SecretFiles` 亦无 Builder 消费（ADR-0032 记"railpack secrets 面不进本批"）——三者均为诚实边界，但三处注释均未互指，易被误读为已实现。
5. zot 镜像 GC 延后（ADR-0019:85 记档"只增不清"）、S3 切换不迁移对象（ADR-0042 决策 6 记档）、traefik 令牌进 argv（ADR-0036 记档）——均为**已裁决取舍**，本报告只补充残余风险与记档缺口，不重复历史裁决。

## 3. 优点（带证据）

1. **Provider 契约三件套 + 编译期断言 + 自注册单点**：`capability.Provider`/`RegisterFactory` 单源（`capability/capability.go:29-104`），各实现带编译期契约断言（`traefik/provider.go:48-51`、`zot/provider.go:124-130`、`victorialogs/provider.go:92-96`、`victoriametrics/provider.go:70-74`、`localobjectstore/provider.go:33`、`s3objectstore/provider.go:50`），重复注册/未知名启动期 fail-fast；可选子面经 `FacesOf` 单点协商（`capability/faces.go:42-87`），降级行为可枚举（启动日志 + `Offered()`）。
2. **traefik 全量快照 + 发布前预检 + 失败保 LKG 的闭环**：`PublishRoutes` 先 `buildDynamicConfig` 再 `validateDynamicConfig`，任一失败不换快照（`traefik/provider.go:95-111`，`traefik/config.go:236-284`）；预检含 JSON 严格回读/引用闭合/规则语法/URL scheme 四类，且 `TestPrecheckFailureKeepsLastGoodSnapshot`/`TestPrecheckAcceptsGeneratedConfigs` 把"生成物必须过预检"与"拒绝不换快照"钉死（`traefik/config_test.go:194-274`）；host/path 白名单纵深复用同一真源（`capability/routevalidate.go`）且 `routeKey` 用 FNV 后缀解决归一撞键（Q-10，`config.go:207-227` + 测试 :108-139）。
3. **认证材料不进 argv（VL/VM）与材料字节稳定（E28）**：VL/VM 的 `-httpAuth.password=file:///run/secrets/...` + `MaterialsSource` 文件载体（`victorialogs/provider.go:209-245`、`victoriametrics/provider.go:186-254`），密码只经材料通道；zot 的 htpasswd bcrypt 行与 per-Project 凭证持久化（tmp+rename、0o600、重启复用不重铸）解决"重启即滚替/拉取凭证雪崩"（`zot/provider.go:418-469,505-596`），且材料铸造对输入集排序去重（`dedupeSorted`）保证同集同字节（`zot/provider.go:598-610`）。
4. **上传 tar 安全解包面完整**：白名单=普通文件+目录，绝对路径/`..` 段/反斜杠/symlink/hardlink/重复条目一律拒绝，超限带 `ErrTooLarge`（`upload/extract.go:20-124`）；负例表覆盖 parent escape/nested escape/absolute/backslash/symlink/hardlink/duplicate 七类（`upload/extract_test.go:53-98`），另钉 CLI 目录 tar roundtrip（`:115-144`）。
5. **ObjectStore 双 adapter 同构契约**：`safeKey` 键钳制同规则复刻（`localobjectstore/provider.go:298-315`、`s3objectstore/provider.go:286-303`）；Put 流式 sha256 回执（local `:103-123`、s3 `:191-197`）；`ErrObjectNotFound` 哨兵（local `:139-147`、s3 `:264-280` 的 NoSuchKey/ObjectDoesNotExist 映射）；Delete 幂等、List 稳定排序、local 原子 tmp+rename+fsync 文件（`:98-123`）；s3 Get 先 `obj.Stat()` 把缺键即时暴露，避免惰性 GET 掩盖（`s3objectstore/provider.go:111-123`）。
6. **构建链的钉版与隔离纪律**：railpack 双侧钉版（spec pin == 平台常量，且二进制 `--version` 探测 == 常量，错误文本带平台版本与 CLI 旗标形态；`builders/railpack.go:156-174,246-258`），并有 `install.sh ↔ 常量` 静态守卫（`internal/guards/railpackpin_test.go:13-18`）；构建上下文顶层 VCS 元数据排除（`.git/.svn/.hg`，`builders/daemon.go:131-156` + `daemon_test.go:22-57`）；static 生成 Dockerfile 的 `output_dir` 与受理面共用 `spec.ValidStaticOutputDir` 白名单（`builders/static.go:132-144`、`spec/spec.go:182-190`）；dockerfile 的 `BUILDKIT_CACHE_MOUNT_NS` 平台后置覆写用户 build-arg（`builders/dockerfile.go:59-78`）。

## 4. 发现清单

按严重度排序。每条的"状态"指该结论证据强度：**已亲验**=代码路径+调用方已核；**待复核**=需真机/上游行为确认的部分已单独标注。

---

### R07-H1｜Route 全量发布缺"就绪门"：控制面重启后 traefik 可能以空/部分路由集替换 last-known-good（用户路由中断最长 ~60s）

- 维度：可靠性（可用性）/ 可观测性
- 严重度：H — 影响所有用户路由的可用性，且与 ADR-0015「升级路由零中断」的验收承诺直接冲突；触发条件是"重启时基线重放慢于首拍发布"，非偶发代码瑕疵而是启动序缺闸。
- 位置：`internal/infrastructure/providers/traefik/provider.go:65-72,115-119`；`internal/assembly/proxyconfig.go:64-69`；`internal/application/engine/routepublish.go:44-49,61-68,79-84,122-145`；`internal/application/engine/lifecycle.go:33-52`；旁证 `e2e/dind-upgrade.sh:266-294`
- 证据（两段）：
  ```go
  // routepublish.go:44-49 —— 签名只覆盖"行集 + browse 会话集"，不含后端可解析性
  sig := routesFingerprint(routes) + "\x00" + e.browseRoutesFingerprint()
  forced := e.proxyMemo.pubNow.CompareAndSwap(true, false)
  _, fresh := e.ensureFresh(e.proxyMemo.pub, routesPubKey, sig, now)
  if fresh && !forced {
      return
  }
  ```
  ```go
  // traefik/provider.go:65-72 —— 首次成功发布前快照即"空全量配置"
  return &Provider{
      configEndpoint: configEndpoint,
      ...
      schema:         emptyDynamicConfig(),
  }, nil
  ```
- 影响（机制已亲验；触发概率待 dind 实测）：
  1. **首拉空快照窗口**：`Engine.Start` 派生环 goroutine 后立即返回（`lifecycle.go:33-44` 非阻塞），`NewServices` 把 proxyconfig **最后**注册启动（`assembly/provides.go:881-898`）。首个 `managedStep` 完成前，`/proxy/config` 已对 traefik 返回 `emptyDynamicConfig()`（空 routers/services，格式上"非裸 {}"但语义就是空配置）。traefik 5s 轮询命中即清空全部 router——存量路由断至首次成功发布。
  2. **冷缓存部分发布 + 签名短路固化**：`resolveBackend` 的后端解析依赖 `obs` 期望缓存（由**异步**基线重放 `rebuildBaselines` 填充，`lifecycle.go:48-52`）。首拍若重放未完成，逐 route "backend unresolved, skipping route" 只记 Warn（`routepublish.go:61-68`），发布**仍然成功**并 `ensureRemember` 记住签名（`:84`）；基线重放完成后无任何缓存失效/再发布通道（全仓 `ensureForget(e.proxyMemo.pub, ...)` 仅 `routepublish.go:38,81` 两处：list 失败/发布失败，重放收口不触达），只能等 `ReconcileReplayInterval` 默认 60s 的强制重放（`ensure.go:31-39`、`options.go:102-108`）→ **用户路由最长缺席 ~60s**。`resolveBackend` 注释宣称"重放完成下一拍即解"（`routepublish.go:124-126`）与实现不符：签名未变时下一拍直接 return，不会重发。
  3. 触发规模：`managedStep` 先做 reconcileNodes + 5 个受管域 Inspect/Ensure（约十余次 Docker API）；基线重放按 App 串行 attemptConverge。App 数 ≳4 时"重放慢于首拍发布"即成立。e2e 升级矩阵只有 2-3 个 App（`dind-upgrade.sh`），重放通常先完成，故 CI 常绿——该 bug 只在真实规模暴露。
- 建议（机制层，两处都应补）：(a) **Provider 侧**：`ConfigSnapshot` 在首次成功 `PublishRoutes` 前返回不可用（HTTP 503）而非空全量配置，traefik 对非 2xx 拉取失败保留 LKG——1 处 S 成本；(b) **engine 侧**：`publishRoutes` 增加"基线重放完成"就绪闸（或在冷缓存/有 route 被 skip 时 `ensureForget(proxyMemo.pub)` + 不 remember），并在基线重放收口处 `PublishRoutesNow()`——M 成本。守卫建议：dind-upgrade 增"重启后路由探针 + 多 App 慢重放"形态（如注入 FLEETLY_TEST 慢 Ensure）以覆盖竞态。
- 状态：已亲验（机制与调用方）；"真实重启必然触发"待 dind 用多 App + 时序探针复核（本机 docker 不可用，PROFILE 禁止声称真机复验）。

---

### R07-H2｜Metrics 查询面无服务端租户域过滤：持 `metrics:read` 的任意 Team 成员可读全平台容器指标与租户标签

- 维度：安全（多租户隔离）
- 严重度：H — 多租户隔离被实质绕过（PROFILE H 判据）；且 ADR-0041 的"查询隔离由平台查询构造执法"承诺无落地强制点。
- 位置：`internal/interfaces/grpc/fleetlygrpc/alerting.go:54-87`；`internal/domain/shared/capability/ports.go:390-403`；`internal/domain/identity/roles.go:80-86`；`proto/fleetly/telemetry/v1/telemetry.proto:85-98,266-275`；租户标签来源 `internal/application/engine/metrics.go:243-272`
- 证据：
  ```go
  // alerting.go:83-87 —— 原样透传用户 PromQL，无 authorizeProjectID/无命名空间过滤
  step := time.Duration(req.GetStepSeconds()) * time.Second
  series, err := svc.s.Metrics.QuerySeries(ctx, req.GetQuery(), start, end, step)
  if err != nil {
      return nil, mapStateError(err, "metrics")
  }
  ```
  ```go
  // capability/ports.go:399-402 —— 端口签名没有任何作用域锚（对比 Logging.Query 携带 Namespace）
  // ImportPrometheus ...
  // QuerySeries 查询指标序列（PromQL 透传；...）
  QuerySeries(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Series, error)
  ```
- 影响：`QueryMetrics`（proto 注解 `api_key_scope: {resource:"metrics" op:READ}`）对 Query 字符串零校验零过滤；而内置 `member` 角色即授予全部业务资源 `:read`，其中含 `metrics:read`（`roles.go:80-83` + `assembly/policy.go:45-53` 资源表），且 scope 只做"资源:动作"级校验、没有 Team 行级约束。任何 Team 成员可发 `container_cpu_usage_seconds_total` / `{__name__=~".+"}` / `container_label_fleetly_ns_project="<other>"`，直接拿到：
  - 其他 Team/Project/App/Database 的容器序列与 label（`container_label_fleetly_ns_project/_ns_app/_ns_database`、`container_label_fleetly_workload_id`、容器名 `name`、`image`——`engine/metrics.go:243-272` 的采集注入面）；
  - 全平台资源规模/镜像版本/上下线时刻等侧信道。
  另外该面没有结果规模上限（见同函数 :84 下游 `io.ReadAll` 无界），成员可发起大序列数查询放大控制面内存。ADR-0041:28-29 与 proto 注释把这写成"Console/API 面按 App 域字段过滤"——实际所有客户端（CLI/SDK/REST）都可绕过该约定；`trace` 不到任何服务端 enforcement（唯一消费点即 `alerting.go:84`）。
- 建议：机制层修在**端口**而非逐 handler 补：`Metrics.QuerySeries` 增加 `NamespaceRef`（或 AppIDs/ProjectIDs）入参并强制拼 `container_label_fleetly_ns_*` matcher（与 Logging 的 `LogQuery.Namespace` 同构）；handler 先按 `project_id/app_id` 做行级授权（`authorizeTeamForProject`），平台级 PromQL 诊断面单独走 platform-admin scope。成本 M（端口签名 + engine 调用点 + Console/CLI 查询构造，2-4 处）；若短期止血，先把 `QueryMetrics` 收归 platform admin（S）。需与 R08/R14 去重（修复落点跨模块，根因是 R07 端口契约缺作用域锚）。
- 状态：已亲验（handler/端口/角色种子/proto 注解/标签注入面均已读）。未验证点：实际部署中是否已发放含 `metrics:read` 的 Team Token（默认种子角色即含，判定为是）。

---

### R07-M1｜VictoriaLogs follow 零锚点时 `start_offset` 被算成 ~292 年：`--follow --text` 无 `--since` 且积压为空会回放整个保留窗历史

- 维度：功能 / 可靠性
- 严重度：M — 用户可见语义错误（follow 本应只尾随新行）+ 一次全保留窗扫描（VL 端 CPU/内存与网络放大），但不损坏数据。
- 位置：`internal/infrastructure/providers/victorialogs/provider.go:316-364`（锚点 325-334）、`:581-587`（`formatOffset`）
- 证据：
  ```go
  // provider.go:326-334 —— 注释称"都无 = 当前时刻（0s = 尾随新行，不回读）"
  anchor := newest
  if anchor.IsZero() {
      anchor = q.Since
  }
  form := url.Values{}
  form.Set("query", query)
  form.Set("start_offset", formatOffset(time.Since(anchor)))
  ```
  ```go
  // provider.go:581-587 —— 只有 <=0 才归 0s；零值 Time 的 Since 是 maxDuration(~292y)
  func formatOffset(d time.Duration) string {
      if d <= 0 {
          return "0s"
      }
      return fmt.Sprintf("%ds", int(d.Seconds()))
  }
  ```
- 影响：`anchor` 为 `time.Time{}` 时 `time.Since(anchor)` 饱和为最大 Duration（≈292 年），序列化为 `"9223372036s"`。VL 的 `/select/logsql/tail` 以 `start = end - start_offset` 建立首个窗口（上游实现：`end := now - offset; start := end - startOffset`），于是首个尾随窗口从"公元 1 年"起算 = **扫描/返回该 query 过滤域内全部保留窗历史**；去重边界 `newest` 为零值故不丢帧，全部历史帧写进用户流。触发路径：`fleetly logs --text <x> --follow` 且不传 `--since`、且该 App 无匹配积压（`logQueryFromRequest` 的 Since 只在显式传参时赋值，`interfaces/grpc/fleetlygrpc/contexts.go:715-728`）。现有测试只覆盖 anchor 非零（`provider_test.go:312-439` 两测均设 `Since`），零锚点分支无守卫——注释即错误规格，实现与注释相反。
- 建议：`if anchor.IsZero() { anchor = /* now */ }` 或直接 `start_offset = "0s"`（与注释一致）；补一条零锚点单测（断言 `tailForm.Get("start_offset") == "0s"`）。成本 S。
- 状态：已亲验（provider 代码 + 调用方 + 上游 tail 窗口语义依据）；"全保留窗返回量"取决于 query 命中量，未真机量化。

---

### R07-M2｜traefik router/middleware 名可从合法 Route path 生成 `@`（traefik 明示不允许），发布预检未覆盖名字字符集；大写 host 永不命中

- 维度：功能 / 测试与守卫
- 严重度：M — 白名单允许的输入可生成上游明确非法的 router 名，且现有预检/测试把"名字 DNS 安全"写成已保证；最坏形态（traefik 整快照拒载）会冻结全部路由更新（待复核）。
- 位置：`internal/domain/shared/capability/routevalidate.go:20,33,37-52`；`internal/infrastructure/providers/traefik/config.go:96-118,135-141,215-227,236-245`；测试 `traefik/config_test.go:108-139`
- 证据：
  ```go
  // routevalidate.go:33 —— path 白名单刻意包含 '@'
  const routePathAllowed = "/._~!$&'()*+,;=:@%-"
  ```
  ```go
  // traefik/config.go:215-226 —— 只替换 . * / _，path 其余白名单字符原样进 router 名
  func routeKey(r capability.Route) string {
      key := r.Host
      if r.Path != "" && r.Path != "/" {
          key += "-" + strings.Trim(r.Path, "/")
      }
      normalized := strings.NewReplacer(".", "-", "*", "-", "/", "-", "_", "-").Replace(key)
      h := fnv.New64a()
      ...
      return fmt.Sprintf("%s-%012x", normalized, h.Sum64()&0xFFFFFFFFFFFF)
  }
  ```
- 影响：
  1. `@`：路径 `/@scope/pkg`、`/users/@me` 等合法输入 → router 名 `host-@scope-pkg-<hash>`、middleware 名 `<同上>-fw`。Traefik 官方文档明确 *"The character `@` is not authorized in the router name"*（[traefik v3.5 Rules & Priority](https://doc.traefik.io/traefik/v3.5/reference/routing-configuration/http/routing/rules-and-priority/)），同页亦注明 middleware 名的 `@` 限制。控制面预检 `validateDynamicConfig` 只校验 rule 语法/service 引用/URL（`config.go:246-284`），**不校验 router/service/middleware 名字字符集**，故非法名字能进快照；随后 traefik 是"丢弃该 router"还是"整份快照拒载"未核实——若后者，则一个租户的非法 path 会让**全平台路由变更冻结在 LKG**（page/route 跨租户 DoS）。另 path 白名单还允许 `~ ! $ & ' ( ) * + , ; = : %` 与**大写字母**（host 白名单 `[a-zA-Z0-9]`，`routevalidate.go:20`），这些均进入名字；注释与测试却声称"键仍落在 traefik 名字的 DNS 安全字符集 [a-z0-9-] 内"（`config.go:213`、`config_test.go:132-138` 的断言仅覆盖小写简单样例）——**规格声明与实现不符**。
  2. 大写 host：`Host(`Shop.Example.com`)` 原样内插（`config.go:135-141`），而 traefik Host 匹配按小写请求 host（官方文档："These matchers will match the request's host in lowercase"），规则值未做 lower 归一 → 大写 host 路由静默永不命中（Route 创建面 `contexts.go:196-201` 只做白名单校验，落库原样，`traefik/provider.go` 也不归一）。属同一"受理白名单 ↔ 上游匹配语义"闭合缺口，待真机确认 traefik 是否对规则值同样 lowercase。
- 建议：`routeKey` 的 normalized 段改为白名单折叠（非 `[a-z0-9-]` → `-`，或仅用 hash 前缀），使名字恒 DNS 安全；`validateDynamicConfig` 增名字字符集校验（含 `@` 拒绝）；受理面把 host 归一为小写（或直接拒绝大写）。成本 S（名字折叠+预检）~M（host 归一需考虑存量行）。守卫：补 `TestRouteKeyNameCharset`（path 含 `@ ~ : %`、host 含大写）与预检负例。
- 状态：机制与文档依据已亲验；traefik 非法名字的具体拒载粒度、以及大写规则值是否 lowercase 归一=**待复核**（需真机 traefik v3.5.4 实验）。

---

### R07-M3｜zot per-Project 凭证并发首铸竞速：共享 tmp 名 + 无 single-flight，先铸者可能持有未落盘的凭证（首构建推送 401，重试耗尽）

- 维度：可靠性（并发）
- 严重度：M — 后果是新建 Project 的首次构建 401 失败（推送 401 退避是既有机制，但重试用的是**同一份**凭证，等不到正确行）；窗口小但机制确实存在，且与 ADR-0036 "首构建竞速双修"同族。
- 位置：`internal/infrastructure/providers/zot/provider.go:505-525`（缓存检查）、`:527-596`（铸造/落盘，共享 tmp 在 :587）、`:571-573`（并发容忍注释）
- 证据：
  ```go
  // provider.go:510-525 —— 先查缓存、miss 后无 single-flight，两个调用方都会进入铸造
  func (p *Provider) projectCredential(projectID string) (*projectCredential, error) {
      p.projectsMu.Lock()
      if c, ok := p.projects[projectID]; ok { p.projectsMu.Unlock(); return c, nil }
      p.projectsMu.Unlock()
      c, err := loadOrPersistProjectCredential(p.dataRoot, projectID)
      ...
  ```
  ```go
  // provider.go:587-595 —— tmp 名固定为 path+".tmp"（无唯一名），rename 成功前另一写者可截断同一 inode
  tmp := path + ".tmp"
  if err := os.WriteFile(tmp, b, 0o600); err != nil { ... }
  if err := os.Rename(tmp, path); err != nil {
      _ = os.Remove(tmp)
      return fmt.Errorf("zot provider: publish project credential: %w", err)
  }
  ```
- 影响：同一 Project 的首次凭证铸造有两个并发入口：build 环 `prepareBuildInput → projectPushCred → EndpointForProject`（`engine/buildsource.go:287-303`）与 managed 环 `ManagedMaterialsFor → materialsFor`（`engine/managed.go:210-351`，每拍都算签名）。若二者在 bcrypt（~60ms）窗口内同时 miss：A 写 tmp→B 截断 tmp→A rename 把 B 正在写的 inode 挪到正式路径→A 返回自铸密码 pwA，而文件/cache 最终是 pwB。A 的 pwA 用于 ImagePush → 401；`pushBuiltImage` 用同一 `auth` 重试 9×10s（`builders/daemon.go:250-277`）后仍失败 → 首构建 failed。落盘注释"后到 rename 失败即读先行者"只在"rename 失败"分支成立，未覆盖上述 rename 成功但内容属于对方写的交错。
- 建议：`projectCredential` 加 per-project `singleflight`/`sync.Once`（惰性 map），或 `os.CreateTemp` 唯一 tmp 名 + rename 后回读校验一致性；成本 S。守卫：并发首铸单测（两 goroutine 同时 miss，断言返回凭证 == 落盘文件凭证）。
- 状态：已亲验（代码路径与并发入口）；真实触发概率低（窗口 ~bcrypt 时长 + 需两环同拍首访），未真机复现。

---

### R07-M4｜GitHub 客户端出站面无重定向约束与超时：受理面"私网拒绝"可被 302/DNS 重绑定绕过（盲 SSRF），上游挂起无界

- 维度：安全 / 可靠性
- 严重度：M — api_base 由具备 GitHub App 写权者控制（Project 级成员即可，`githubapps.go:220-226`），防线只在受理字符串层（`validateAPIBase`），出站层无约束；盲 SSRF 可命中平台内网/元数据端点（Go 默认 client 跨域重定向会剥离 Authorization，所以 JWT 不必然泄露，但请求本身会发出）。
- 位置：`internal/infrastructure/github/client.go:73-81,217-246`；构造点 `internal/assembly/provides.go:610`、`internal/application/engine/construct.go:77`；防线 `internal/interfaces/grpc/fleetlygrpc/githubapps.go:422-447`
- 证据：
  ```go
  // client.go:73-81 —— 生产装配传 nil → http.DefaultClient（无 Timeout、默认允许 10 次重定向）
  func NewClient(httpClient *http.Client, now func() time.Time) *Client {
      if httpClient == nil { httpClient = http.DefaultClient }
      ...
  }
  // client.go:230-246 —— base 由调用方给（来自聚合行 api_base），直接拼 path 后 Do
  req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
  ...
  resp, err := c.http.Do(req)
  ```
- 影响：(1) `validateAPIBase` 只拒 https + 字面私网/环回/链路本地 host（`githubapps.go:427-447`，`mapping.go:342 rejectPrivateHost`），但 `http.Client` 默认跟随重定向且未设 `CheckRedirect`——`api_base=https://attacker.tld` 可通过 302 让 fleetlyd 向 `http://169.254.169.254/...`、内网服务发 GET/POST（307/308 保留方法与 body），DNS 重绑定同理；控制面以 root 出站，绕过 P0-7.3"写权者不得借 api_base 扫内网"的裁决（`githubapps.go:423-426`）。(2) `http.DefaultClient.Timeout == 0`，调用方 ctx 无 deadline（`ResolveInstallation` 走 API 请求 ctx），GitHub/GHES TCP 黑洞时请求可长期挂起；engine 侧 clone 前的 token 铸造（`gitmaterial.go:77`）同样无界，叠加部署环步进体感变慢。(3) `ListRepos` 分页无上限（`client.go:169-186`），恶意/畸形 base 恒返 100 条可无限循环（依赖 base 校验与 GitHub 行为，风险低）。
- 建议：在 R07 客户端层收口（而不是只在受理面）：`NewClient` 强制注入 `CheckRedirect`（跨 host 拒绝或仅允许白名单 host）+ `Timeout`（如 10s）；更稳的形态是自定义 `DialContext` 对解析后的 IP 复用 `rejectPrivateHost` 判定（彻底封 DNS 重绑定）。成本 S-M。
- 状态：已亲验（客户端无超时/无 CheckRedirect、装配传 nil、受理面校验位置与内容）；"内网可达性"取决于部署网络，未真机验证 SSRF 命中。

---

### R07-M5｜Health 语义分裂：traefik Health 恒 true，其余 Provider 仅 TCP 拨号；`fleetly status` 的"降级矩阵"对 Proxy/认证面失明

- 维度：可观测性 / 架构一致性
- 严重度：M — `Health` 是 GetStatus/DEGRADED 的唯一数据源（受管组件自愈与运维判断依据），Proxy 面恒健康意味着"路由已挂但 status 全绿"无法被平台自证；TCP 通 ≠ 服务就绪（认证错/启动中/迁移中均报健康）。
- 位置：`internal/infrastructure/providers/traefik/provider.go:89-93`；对照 `zot/provider.go:231-241`、`victorialogs/provider.go:192-202`、`victoriametrics/provider.go:166-175`；消费方 `internal/interfaces/grpc/systemgrpc/systemgrpc.go:56-71,75-115`；契约注释 `capability/capability.go:31-36`
- 证据：
  ```go
  // traefik/provider.go:89-93 —— 恒健康，不探活受管 traefik 载体
  func (p *Provider) Health(context.Context) capability.HealthReport {
      return capability.HealthReport{Healthy: true, Details: "config endpoint served by this control plane"}
  }
  ```
  ```go
  // capability/capability.go:33-35 —— 契约声明 Health 是"降级矩阵的驱动信号"
  // Health 报告当前可用性（架构 §8 降级矩阵的驱动信号）。
  Health(ctx context.Context) HealthReport
  ```
- 影响：受管 traefik 载体被 `docker service` 杀掉/滚动卡死/端口占用失败时，配置端点（在 fleetlyd 进程内）照常 200，`GetStatus` 聚合仍 `HEALTHY` → `fleetly status`/doctor 无法报出"全站路由不可达"；反之 zot/VL/VM 的 TCP 探测只能证明端口有 listener（认证/存储只读/迁移中仍报健康）。"不可用 vs 未配置"分层靠装配期 nil（未配置=组件缺席，合理），但"已配置且不可用"缺乏可信信号。ADC-0004 的受管 reconciler 有 Generations/watch，Health 面没有接。
- 建议：traefik Health 至少 `net.Dial` 集群入口（或经 Runtime Inspector 查受管 Workload 观测 State == running）；受管类 Provider 统一"载体观测优先、TCP 兜底"；把 Health 的语义（liveness vs readiness）写进 `ProviderDescriptor.Notes` 并让 GetStatus 区分 `unavailable`/`not_configured`。成本 S-M。
- 状态：已亲验（实现+消费方）；真实载体故障下的 status 输出未真机取证（静态推理）。

---

### R07-L1｜VictoriaLogs 上游 4xx 未类型化：用户输入可把"检索参数错"落成 E_INTERNAL 500（与 VM 的 `MetricsQueryError` 分层不一致）

- 维度：可靠性 / 错误分流
- 严重度：L — 语义分类错误（用户错 vs 平台错），无数据影响。
- 位置：`internal/infrastructure/providers/victorialogs/provider.go:409-452`（`post/respError`）、`:302-309`（Query）；对照 `internal/infrastructure/providers/victoriametrics/provider.go:318-323`（4xx → `capability.MetricsQueryError`）
- 证据：
  ```go
  // victorialogs/provider.go:447-452 —— 一律 fmt.Errorf，无状态码类型
  func (p *Provider) respError(prefix string, resp *http.Response) error {
      snippet := make([]byte, 200)
      n, _ := io.ReadFull(resp.Body, snippet)
      return fmt.Errorf("%s: status %d: %s", prefix, resp.StatusCode, string(snippet[:n]))
  }
  ```
- 影响：`Query` 的 LogsQL 由平台构造（注入面已转义），正常不该 4xx；但 `Text`/`Containers` 是用户输入，`quoteLogSQL`/`escapeRegexp` 只转义 `\` 与 `"`（`provider.go:569-579`），**不处理控制字符/换行**——`text` 含换行、`containers` 含换行时 LogsQL 字符串字面量可能被 VL 拒（400），错误经 `mapStateError` 落 E_INTERNAL 而不是 E_INVALID_ARGUMENT（与 F-B1 对 VM 的裁决相反）。另 `io.ReadFull` 对 <200 字节体返回 ErrUnexpectedEOF 被忽略（行为正确，仅记录）。
- 建议：对称 VM 引入 `LoggingQueryError{Status, Message}`（或复用同一类型族），4xx 映射 E_INVALID_ARGUMENT；`quoteLogSQL` 增控制字符转义（`\n`/`\r`/`\t` → LogsQL 转义序列或拒绝）。成本 S。
- 状态：已亲验（代码分层差异 + 转义函数）；"VL 对换行字面量回 400"依赖上游解析器=待复核（未真机）。

### R07-L2｜localobjectstore.Put 固定 `.tmp` 名 + rename 后未 fsync 目录：并发同键写会互相截断，掉电可能丢"已回执"对象

- 维度：可靠性
- 严重度：L — 并发同键 Put 在现有单写者备份环下不常见；掉电窗口影响"备份刚成功后立刻断电"的窄窗口。
- 位置：`internal/infrastructure/providers/localobjectstore/provider.go:87-123`（tmp :98、fileSync :110、rename :119）
- 证据：
  ```go
  // provider.go:98-99 —— 固定 tmp 名；并发同键 Put 共用同一 inode（O_TRUNC 互踩）
  tmp := path + ".tmp"
  f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
  ```
- 影响：两个同键 Put 并发时（不同键不同 tmp，无冲突）：双方 OpenFile 同一路径，交错 Write 可产出"内容混合、但各自回执 digest 只覆盖自己写的字节"的最终对象 → 对象与回执 digest 不匹配，restore verify 失败而 Put 报成功。另 POSIX 下 `rename` 持久性需 `fsync(parent dir)`，代码只 fsync 文件（:110）——崩溃后 rename 目录项可能未落盘（回执已返回、对象丢失/回退为 tmp 名），备份可信度承诺（注释 :82-86）不完整。建议：`os.CreateTemp` 唯一名 + rename + `dir.Sync()`；成本 S。
- 状态：已亲验（代码）；触发场景需并发/掉电，未实测。

### R07-L3｜keyring 首启 KEK 写非原子（无 tmp+rename、无 fsync、无权限复核），与 zot/VL/VM 的落盘纪律不一致

- 维度：可靠性
- 严重度：L — 只在"首次启动生成 KEK 的瞬间崩溃/掉电"时触发；后果是下次启动解析失败拒绝启动（此时尚无密文行，无数据丢失），需运维删除文件重建。
- 位置：`internal/infrastructure/keyring/keyring.go:42-62`（写点 :58）、`:82-98`（装载/解析）
- 证据：
  ```go
  // keyring.go:50-60 —— 单次 WriteFile，无 tmp+rename、无 fsync
  dir := filepath.Join(dataRoot, keysDirName)
  id, err := age.GenerateX25519Identity()
  if err != nil { ... }
  if err := os.MkdirAll(dir, 0o700); err != nil { ... }
  if err := os.WriteFile(filepath.Join(dir, kekFileName), []byte(id.String()), 0o600); err != nil { ... }
  return material.NewCipher(id, nil), nil
  ```
- 影响：崩溃窗口内 `master.agekey` 可为空/半截 → 下次 `LoadCipher` 走 `age.ParseX25519Identity` 失败即 fail loud（不可自愈，daemon 起不来）；EE28 文化下 zot/VL/VM 的凭证全部 tmp+rename，KEK（安全等级最高的文件）反而不符合该纪律。另装载路径不复核文件权限（如数据根被错误 rsync 成 0644 时无告警；Windows 上 0600 无 ACL 语义仅测试覆盖问题）。建议：tmp+rename+fsync(文件与目录)，装载时对 POSIX 校验 `Perm&0o077 == 0` 并 warn；成本 S。
- 状态：已亲验（代码）；无真机掉电复现。

### R07-L4｜测试可移植性/顺序耦合：keyring 权限断言在 Windows 恒红；railpack Health 测试假设先命中二进制分支（本机实测红）

- 维度：测试与守卫
- 严重度：L — 不改变生产行为，但让"本机 `go test ./...` 全绿"不可达、并掩盖真实的 Health 语义缺陷（M5）。
- 位置：`internal/infrastructure/keyring/keyring_test.go:52-64`；`internal/infrastructure/providers/builders/railpack_test.go:118-131`；被测顺序 `builders/railpack.go:137-146`（先 ping daemon）
- 证据：
  ```go
  // railpack.go:139-146 —— Health 先探 daemon，daemon 不可达即返回，永不进入二进制分支
  func (p *RailpackProvider) Health(ctx context.Context) capability.HealthReport {
      if err := p.d.ping(ctx); err != nil {
          return capability.HealthReport{Healthy: false, Details: "docker daemon unreachable: " + err.Error()}
      }
      if p.version == "" { return capability.HealthReport{Healthy: false, Details: fmt.Sprintf("railpack binary %s is not runnable; ...")} }
  ```
  ```go
  // railpack_test.go:121-130 —— 期望 Details 含钉版号与 FLEETLY_RAILPACK_BIN，但本机（无 daemon）返回 daemon 文本 → 断言必红
  withFakeCmd(t, func(context.Context, string, []string) cmdOutcome { return cmdOutcome{Err: errors.New("not found"), ExitCode: -1} })
  ...
  h := p.Health(context.Background())
  require.False(t, h.Healthy)
  assert.Contains(t, h.Details, railpackPinnedVersion)
  assert.Contains(t, h.Details, "FLEETLY_RAILPACK_BIN")
  ```
- 影响：本机复跑实测——`keyring`：`expected 0x1c0/0x180 actual 0x1ff/0x1b6`（Windows `Perm()` 无 owner-only 语义）；`builders`：`"docker daemon unreachable: ... npipe ..."` 不含 `0.39.0`。前者应在 `runtime.GOOS == "windows"` 时 `t.Skip`（或改用 ACL 断言）；后者应用可控 seam 同时钉"两分支"（daemon 健康 + 二进制缺席；daemon 不可达），否则该测试只在 Linux CI（runner 有 daemon）有效，本地/无 docker 环境给出假失败。CI 全 Linux 掩盖了这两点。
- 建议：加平台/环境 skip 或拆分测试面；成本 S。属守卫文化"测试在开发者默认环境可红"的盲区（PROFILE §1.1 已列，模块面补具体修法）。
- 状态：已亲验（本机 `go test -count=1` 实测红，输出见上）。

### R07-L5｜upload blob GC 的 refcount 收口存在 TOCTOU；`SweepUnreferenced` 注释称"返回被删行"实为只读 SELECT

- 维度：可靠性 / 文档一致性
- 严重度：L — 窄竞态（7d 过期行清扫与同内容重传恰好并发），后果是"上传成功但 blob 缺失"→ 部署期精确失败，不损坏数据。
- 位置：`internal/assembly/provides.go:787-808`（GC 收口）、`internal/infrastructure/upload/store.go:140-161`（Commit dedup）、`internal/infrastructure/persistence/sourceupload/repo.go:114-123`（注释漂移）
- 证据：
  ```go
  // assembly/provides.go:802-807 —— 先删行，再 COUNT==0 才删 blob；与新 Commit 的"先看 blob 在不在→dedup"交错可产生悬挂行
  swept++
  if n, cerr := s.uploads.CountByDigest(ctx, u.Digest); cerr == nil && n == 0 {
      if derr := s.blobs.DeleteBlob(u.Digest); derr != nil { ... }
  }
  ```
- 影响：交错序——新上传 Commit 先 `os.Stat(blob)` 命中 → 返回 dedup（不重写 blob）→ 清扫方 `CountByDigest==0` 删 blob → 新上传事务落行。结果：行指向已删 blob（`extractUploadContext` 的 `os.Open` 失败 → 部署失败；用户侧上传 API 已返回 deduplicated=true）。修法：GC 时在**同一事务**内"删行 + 锁性 count"，或 blob 删除前做一次 `Stat` 二次确认并把删除延后一拍（两阶段墓碑）；成本 S-M。另 `SweepUnreferenced` doc comment 写"清扫…返回被删行"但实现是 `SELECT ... WHERE NOT EXISTS`（无 DELETE），真正删除在 janitor 循环（R05 文件，跨模块观察）。
- 状态：已亲验（代码路径）；触发概率极低，未实测。

### R07-L6｜traefik 拉取端点共享令牌进受管 argv（平台"密值不进 argv"纪律的已知例外，Describe/Notes 未记档披露面）

- 维度：安全 / 架构一致性
- 严重度：L — 唯一可用通道即 traefik `providers.http.headers` CLI flag（VL/VM 的 file:// 通道 traefik 不支持）；argv 可见面等价宿主 root（已全权），实际提权增量小，但违反 ADR-0014 的表述纪律且未在能力自描述面披露。
- 位置：`internal/infrastructure/providers/traefik/provider.go:160-168`、`:82-86`（Notes）；对照纪律注释 `victorialogs/provider.go:210-213`
- 证据：
  ```go
  // traefik/provider.go:163-168 —— 令牌值拼进受管 Workload 的 Command（swarm service Args）
  if p.authToken != "" {
      // 端点共享令牌（ADR-0036 N2 兑现）：traefik http provider 的自定义头通道；
      // 令牌变更 = 载体 spec 变更 = 一次滚动替换（opt-in 动作窗口，操作者自知）。
      cmd = append(cmd, "--providers.http.headers.X-Fleetly-Proxy-Token="+p.authToken)
  }
  ```
- 影响：受管 Workload spec 的 `Command` 会进 swarm Service.TaskTemplate.ContainerSpec.Args → `docker service inspect` 可见（任何宿主 root/运维可见）；与 VL/VM"密码绝不进 argv（swarm service argv 集群可 inspect）"的判词形成双标准。若未来有低权运维/审计面 dump 受管 spec（当前没有），会扩大暴露面。建议：在 `Describe().Notes` 明确记"proxy config token rides argv (traefik header channel; visible to host-root)"，或改成 traefik 环境变量通道（若上游支持 map 型 static config env，需实测）；成本 S。
- 状态：已亲验（argv 构造 + 受管 spec 通道）；traefik 是否支持 env 替代=待复核。

### R07-L7｜zot 内容代理把 `repository` 直插上游 URL path（`..`/`%`/`?` 未校验），安全依赖上游清理与 ACL

- 维度：安全（纵深防御）
- 严重度：L — 有效利用需 zot 对 `..` 的清理语义 + engine 回退平台凭证（per-Project 凭证铸造失败才回退）同时成立；端口"不信任调用方"纪律在该代理面缺位。
- 位置：`internal/infrastructure/providers/zot/content.go:51-58,121-153,157-184`；调用方归属校验 `internal/application/engine/registrycontent.go:40-61`；proto 仅非空约束 `proto/fleetly/delivery/v1/registry.proto:71-78`
- 证据：
  ```go
  // zot/content.go:121-124 —— repository 直接进 URL path（无段校验/编码）
  func (p *Provider) contentGet(ctx context.Context, ep capability.RegistryEndpoint, path string, out any) error {
      ...
      req, err := http.NewRequestWithContext(cctx, http.MethodGet, "http://"+ep.Addr+path, nil)
  ```
- 影响：`RegistryTags` 的防御是 `strings.HasPrefix(strings.ToLower(repository), lower(projectID)+"/")`（`engine/registrycontent.go:44-47`）——纯字符串前缀，不拒 `..` 段、不拒 `%2e%2e%2f`、不拒 `?`/`#`。平台正常路径使用 per-Project 凭证（zot ACL 在清理后的 repo 上仍会拒跨项目），但 `registryEndpointFor` 在 per-Project 铸造失败时**回退平台凭证**（`engine/registrycontent.go:91-100`，adminPolicy 全域读）——此时上送路径若被上游规范化为他项目仓，即可读到跨项目 tag 事实。建议：Provider 侧按 registry repository 语法白名单校验（`[a-z0-9]+([._-][a-z0-9]+)*(/...)`，拒 `..`/编码/半角标点）并对每段 `url.PathEscape`；成本 S。
- 状态：已亲验（拼接与调用方面）；上游 zot 的路径规范化/ACL 求值顺序=待复核。

---

## 5. 交叉观察（不在本模块，值得其他模块注意）

1. **R08/R14**：`QueryMetrics` 的服务端无租户过滤（H2 的修复落点在 interfaces；根因是 R07 `Metrics` 端口签名缺作用域锚）。建议 R08 复核所有"单租户存储 + 平台凭证 + 透传查询"面是否都存在同类缺 enforcement 的约定（Logs 面正确携带 Namespace，可作范式）。
2. **R02/R13**：Route 发布就绪门（H1 的 engine 侧）属启动序/单写者环协议；`resolveBackend` 注释"重放完成下一拍即解"与签名短路实现矛盾，建议 R02 统一复核所有 `ensureMemo` 域的"失败清签名"约定（route 的 skip 分支未清）。
3. **R05**：`sourceupload.SweepUnreferenced` 注释称"返回被删行"，实现为只读 SELECT（`persistence/sourceupload/repo.go:114-123`）；真正的删除在 `assembly/provides.go:795-807`。属文档-代码漂移。
4. **R03**：`BuildRequest.SecretFiles/Args/CacheFrom` 在 engine `prepareBuildInput`（`engine/buildsource.go:220-241`）中均未赋值，Builder 侧也无消费者（CacheFrom 已由 ADR-0032:64 记档占位，SecretFiles 由 ADR-0032"railpack secrets 面不进本批"记档；Args 无任何 ADR 记档）——三面均为"契约已声明、实现悬空"，建议 R03/R15 判断是否需要显式 Notes 或从端口注释收窄。
5. **R09**：`assembly.NewBuilderProviders` 为三个 Builder 各构造一套 `daemonClients`（moby + buildkit 双通道 ×3）；`NewRailpack` 装配期同步探测版本（15s 上限，`builders/railpack.go:246-249`）会拉长启动。功能无碍，属资源/启动时延观察。
6. **R12**：`Health` 的消费方只有 `systemgrpc.GetStatus`（R08 文件），降级矩阵的"驱动"承诺（`capability.go:34`）尚未接到收敛/告警环；e2e 升级探针（`dind-upgrade.sh:266-294`）只验证 HTTP 200 与"3 次 1s 重试"，对 H1 的 60s 级缺口不敏感——建议 R12 评估"重启时序 + 多 App"矩阵。

## 6. 测试与守卫质量评估

**覆盖强度（好）**：

- traefik：golden 全形态（http/h2c/tcp/browse forwardAuth/console+api rateLimit/TLS）+ 预检正/负两类 + 撞键单射 + 发布拒绝保快照 + 受管声明/令牌/CA 开关（`config_test.go` 274 行，断言具体到 JSON 字段与错误文本）。
- builders：railpack 版本解析/双侧 pin 执法/prepare 退出码语义/env 瘦身/上下文 VCS 排除/推送 401 退避/digest aux 与 RepoDigests 兜底均有 seam 单测；静态面另有 env-gated 真机测试（`build_test.go:22-29`，FLEETLY_TEST_DOCKER 门控——正确形态）。
- upload：tar 七类恶意形态 + 限额 + CLI 目录形态 roundtrip。
- ObjectStore：s3 以六操作接口缝做内存 fake，roundtrip/哨兵/幂等/排序/健康语义均有断言；local 有 fsync 注入缝钉错误路径（`provider.go:126-127`）。
- VL/VM：Ingest ndjson 字段/`_stream_fields`、Query form 契约、follow 两段式与重叠去重、错误分流（VM 4xx → `MetricsQueryError`）、凭证跨重启字节稳定均有测试。

**盲区 / 弱点（与 §4 对应）**：

1. **重启时序面零覆盖**：没有任何测试覆盖"Engine.Start → 首拍 Route 发布 vs 基线重放"的竞态，也没有"ConfigSnapshot 在首发布前的行为"断言（H1）。e2e 探针的 3×1s 重试只对短抖动敏感。
2. **名字字符集面零覆盖**：`routeKey` 测试只验证小写简单键（M2），白名单与上游名字/匹配语义的闭包无守卫；`validateDynamicConfig` 不校验 map key 字符集。
3. **并发首铸零覆盖**：zot per-Project 凭证与 upload Commit/GC 的并发交错没有测试（M3/L5）。
4. **VL follow 零锚点分支零覆盖**（M1）；且测试注释把"尾随段锚点 = 最新积压帧"当作全覆盖，掩盖"都无"分支。
5. **上游 4xx 分流只覆盖 VM**（L1）：Logging 无对应类型化错误与测试。
6. **Health 语义测试只测"返回 bool"**：无"载体故障时 Health 必须不健康"的行为锚（M5），traefik Health 的恒真无法被现有测试发现（因为没有消费面/载体注入）。
7. **环境可移植性**（L4）：`keyring` 权限断言与 `builders` 的 Health 测试在无 docker/Windows 开发机常红，CI 全 Linux 掩盖；PROFILE §1.1 已记录，本模块可修。

**假阳性/假阴性风险**：s3 fake 缝与 local 行为同构可信，但 minio-go 真实链路（multipart abort/NoSuchKey 形态/ctx 断流）只有 dind e2e 覆盖，单测无法证明；railpack/victoria 镜像 argv/form 契约均有 staging ground-truth 注释，可信度较高。

## 7. 未决与置信度

**最不确定的 5 点**：

1. **H1 的触发概率**：机制确定，但"重启时基线重放慢于首拍发布"依赖 App 数量/载体 API 时延；e2e 小规模未触发。需 dind 注入慢重放 + 多 App 做一次时序探针（若更小概率则降 M，但修复建议不变）。
2. **M2 的 traefik 行为粒度**：`@` 名字是"丢弃单 router"还是"整快照拒载"未核实；前者影响单路由，后者可冻结全平台路由变更（可能升 H）。大写 Host 规则值是否被 traefik lowercase 归一同理待真机。
3. **H2 的实际暴露面**：已确认内置 member 角色含 `metrics:read` 且 handler 零过滤；但未验证真实部署是否把 Team Token 发放给"仅成员"用户（若实际只发 platform admin，则降 M）。修复建议与 R14 裁决一致。
4. **M3/L5 的并发触发概率**：窗口/交错均为静态推理，未复现；风险等级取"低概率高代价"的保守 M/L。
5. **M4 的内网可达性**：SSRF 是否命中取决于部署网络（容器网络/VPC 路由/元数据端点），未真机验证；`CheckRedirect` 缺失本身已亲验。

**整体置信度：中高。** 所有 H/M 均基于完整读过实现+调用方；未覆盖部分集中在"需真机载体/上游二进制行为"的面（traefik 拒载粒度、zot ACL 求值顺序、VL 解析器对控制字符的容忍度）。

**范围声明**：本报告只做 R07 范围；未派生子代理、未修改仓库任何文件（唯一写入为本 findings 文件）、未运行任何写仓命令；未运行 e2e（本机 docker daemon 不可达，PROFILE 禁止声称真机复验）。
