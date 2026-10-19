# R01 domain 层与端口契约 评审报告

> 评审基线：`D:\Codes\qiulin\fleetly-review-2026-10-19\PROFILE.md`（2026-10-19，HEAD `35257f18`）。
> 评审方式：只读静态全量阅读 + 目标包测试 + 调用方/相邻实现交叉核实。**未改动仓库任何文件。**
> 本机验证动作：`go test -count=1 ./internal/domain/...` → **12 包 ok / 0 失败 / 38 包无测试**；
> 脚本核对：36 个 errcode 全部有 `apperr.New` 生产引用；99 个 eventcode 全部有非注释生产字面量；
> `internal/domain` 生产码 import 环外 fleetly internal 包计数 = **0**。

## 1. 覆盖清单

### 完整读过（生产文件，逐个列）

- `shared/spec/`：`spec.go`(664)、`normalize.go`、`merge.go`、`diff.go`、`variables.go`、`dokploy.go`
- `shared/capability/`：`capability.go`、`runtime.go`(745)、`ports.go`(506)、`exec.go`、`faces.go`、`nodeinspect.go`、`registryauth.go`、`routevalidate.go`、`workloadsignature.go`
- `shared/model/`：`errcode/{codes.go,errcode.go}`、`eventcode/{events.go,eventcode.go}`、`entityid/entityid.go`
- `shared/apperr/apperr.go`、`shared/state/state.go`
- `identity/`：`scope.go`、`roles.go`、`token.go` + 7 聚合（audit/invitation/membership/role/team/token/user 的 row+repo）
- `services/`：`anchor/anchor.go`、`material/material.go`、`writes/writes.go`、`dbtemplate/*`(6 文件)、`dbbrowser/*`(6 文件 + `assets/router.php`)
- 其余 8 上下文 **37 个聚合**的 row 类型 + Repo 接口（automation 4、data 3、delivery 7、runtimeops 2、structure 10、telemetry 4；其中 structure/delivery 目录名分别为 `config`/`catalog` 等，逐一读过）
- 测试：`internal/domain` 全部 **31 个 `_test.go`**（identity 2、anchor 1、dbbrowser 1、dbtemplate 3、material 2、writes 1、apperr 1、capability 5、entityid 2、errcode 2、eventcode 1、spec 11）

### 抽样读过（用于交叉核实，不越界评审）

- guards：`domainpurity`、`capabilitypurity`、`irguard`、`factdual`、`aggregatetestbelt`、`registrysourceanchor`、`statefacade`、`statekitlaw`、`scopeguard`
- 消费方：engine `deployspec.go`/`predeploy.go`/`projection.go`/`browseloop.go`/`restart.go`/`minttask.go`/`database.go`/`databaseenv.go`；providers `swarm/translate.go`、`k3s/translate.go`；`interfaces/grpc/apperrstatus/apperrstatus.go`；`assembly/policy.go`、`assembly/errors.go`
- 契约/文档：`proto/fleetly/spec/v1/spec.proto`、`proto/fleetly/runtime/v1/runtime.proto`、ADR-0051、ADR-0075
- 测试/脚本：`internal/testsupport/apitest/browse_test.go`、`e2e/dind-browse.sh`、persistence 侧 8 个聚合实现签名（deployment/task/run/outbox/node/secret/browse/catalog）

### 未覆盖/无法覆盖（及原因）

- providers 实现全量（R06/R07）、persistence SQL 全量（R05）、proto/接口全量（R08）、CLI/Console（R10/R11）——只按 R01 需要抽读消费面
- 本机 Docker daemon 不可达：无法真机复验 browse / dbtemplate / provider 翻译失败文本；`dbtemplate_live_test.go` 缺省 SKIP（需网络与 `FLEETLY_DBTEMPLATE_LIVE=1`）
- 生成物/构建命令（`mise run generate*`/`build`）按约束未运行

## 2. 模块画像与边界

- 规模：**114 个生产 Go 文件 / 8,708 行**；测试 31 文件 / 3,781 行；37 个 `Repo` 接口 ↔ persistence **37 个聚合包，逐一 1:1 对应**（脚本 Compare-Object 零差异）。
- 环内职责：IR（spec 纯函数：校验/归一/合并/变量合成/重启判定/dokploy 迁移）、七类 capability 端口（核心 6 方法 + 9 个可选子面）、errcode/eventcode/entityid 三注册表、apperr 纯信封、state 持久化契约面（Runner/Clock/DB 窄面）、identity 种子（colon Scope 语言 / 角色模板 / Token 材质）、services（anchor 归属解析图、material age 密码学、dbtemplate 五引擎、dbbrowser 四方言、writes 事实发射 kernel）、37 个聚合的 row 类型 + Repo 接口。
- 依赖方向：`internal/domain` 生产码对环外 fleetly 包 **0 import**（我的扫描 + `domainpurity`/`dep_matrix` 双守卫）；叶子例外均在册：spec→`genproto/fleetly/spec/v1`（豁免表带理由）、material→`filippo.io/age`、schedule→`robfig/cron`、state→`database/sql`、identity→`lynx-go/grpcapi/authz`（外部库，非 RPC 运行时）。
- 与 ADR 声明偏差：无实质偏差。`Repo` 签名把分页/CAS 语义写成 `afterX/limit/from []State/mut func(*Row)`，未泄漏 SQL；`state.Runner/DB` 的 `database/sql` 接缝是 ADR-0075/0080 的显式设计（state 包注释自证"纯契约零 I/O"）。
- 用例事实：2026-10-19 本机 `go test -count=1 ./internal/domain/...` 12 包全绿。

## 3. 优点（带证据）

1. **IR 叶子校验（fail-closed）覆盖面与字段级精度都在业界上游**：镜像引用控制字符/空白/反引号注入闸（`shared/spec/spec.go:270`）、secret 名路径逃逸白名单（`:554`）、static 产物目录字符集（`:182`）、build 路径逃逸（`:256`）、跨 Project 引用的平台 ID 形态锚（`:629`），错误全部带精确点分字段路径；对应注入家族测试成矩阵（`spec_test.go:94`、`:328`、`build_validate_test.go:69`）。这是"受理位拒绝而非下游清洗"的正确形态。
2. **内容寻址确定性可复现**：Revision digest = sha256(protojson `EmitUnpopulated:false + UseProtoNames:true`)（`delivery/revision/revision.go:27`、`shared/spec/diff.go:146`）；变量合成有逐字节确定性性质测试（`variables_test.go:88`）；`RestartSeqOnly` 是 ADR-0083 的纯函数单源且测试钉死"缺席↔0 无差异 / 空差异恒假 / 多进程其一变更即假"（`diff.go:146`、`diff_restart_test.go:30`）。
3. **writes kernel 真正收口了"声明一条事实、两行同源落库"**：全仓 `Outbox.Append`/`Audits.Append` 只有 kernel 内两处调用（grep 实证），双旗偏离必须带理由注释（`guards/factdual_test.go`），惰性求值与失败隔离有实测（`writes_test.go:194`）；kernel 零系统副作用、零日志，类型面只有 domain repo 接口。
4. **capability 端口"窄面 + 单点协商"纪律成立**：核心 6 方法（`runtime.go:17`）+ 按需子面用单点 `FacesOf` 探测（`faces.go:42`），降级语义在调用方就近自明；跨层哨兵单源（`ErrExecNoRunning`/`ErrExecTargetGone`/`ErrNodeNotFound`/`ErrObjectNotFound`）在 providers 与 engine 双端均被真实消费（grep 55 处）。`Workload.RestartSeq` 的 `json:",omitempty"` 指纹稳定性承诺有明确注释（`runtime.go:421`），属"字段演进不破坏窄面"的正确做法。
5. **注册表三链（构造 fail-fast / golden / usage 反扫）实际有效**：`MustRegister` 校验格式/重复/Source/GRPC（`errcode.go:57`），golden 逐字钉死；我的独立计数显示 **36 个 errcode 全部有 `apperr.New(...)` 生产引用、99 个 eventcode 全部出现在非注释生产代码**——没有悬空码；`registrysourceanchor` 守卫把 Source 锚做成"必须指向现存文件"，防排障入口腐烂。event payload schema 与 eventcode 双向对账（`assembly/schema_gate_test.go:47`）由组合根承载。
6. **dbtemplate/dbbrowser 的领域知识沉淀质量高**：五引擎 digest 双段钉定 + `TestImageDigestsPinned` 全值域门禁（`dbtemplate_test.go:418`）、镜像 VOLUME 遮蔽契约静态表 + live 核对双源消费（`dbtemplate_test.go:342`、`dbtemplate_live_test.go`）、渲染面密码字符集闸（`dbtemplate.go:160`）、备份/恢复 argv 逐字钉死与敌意载荷矩阵（`dbtemplate_test.go:156/235`）、vendored adminer router.php sha256 钉死（`dbbrowser_test.go:105`）。这些是"领域知识住在 domain 并带执法"的正面样例。

## 4. 发现清单

（按严重度排序；每条均已读调用方排除误读）

### R01-H1｜redis browse 全链不可用：dialect 的"用户必填"闸与 redis 连接串的"无用户名"形态自相矛盾
- 维度：功能
- 严重度：**H**（校准：单引擎 browse 面 100% 不可用，且 API 是**假成功**——返回必然不可用的 URL；ADR-0051 宣称的四方言矩阵实际只有三支可用，e2e/单测均未覆盖该支，无守卫兜底）
- 位置：
  - `internal/domain/services/dbtemplate/redis.go:52`（redis ConnURL 铸空用户名）
  - `internal/domain/services/dbbrowser/mongoku.go:45`（`validateConn` 共享闸要求 user 非空；`dbbrowser/rediscommander.go:29` 调用它）
  - `internal/application/engine/browseloop.go:297`（引擎把 `u.User.Username()` 原样交给方言）
  - `internal/application/engine/browseloop.go:187`（render 失败即 log+return，永不 Ensure）
  - 测试盲区：`internal/domain/services/dbbrowser/dbbrowser_test.go:60`、`e2e/dind-browse.sh:110`
- 证据：
  ```go
  // internal/domain/services/dbtemplate/redis.go:52 —— 空用户名是冻结形态
  func (redisTemplate) ConnURL(host, password string) string {
      u := &url.URL{
          Scheme: "redis",
          User:   url.UserPassword("", password), // Username() == ""
          Host:   fmt.Sprintf("%s:%d", host, 6379),
          Path:   "0",
      }
      return u.String()
  }
  ```
  ```go
  // internal/domain/services/dbbrowser/mongoku.go:45 —— 四方言共用闸
  func validateConn(conn Conn) error {
      if conn.Host == "" { ... }
      if conn.Port <= 0 { ... }
      if conn.User == "" {
          return fmt.Errorf("dbbrowser: connection user must not be empty")
      }
      ...
  }
  ```
  ```go
  // internal/application/engine/browseloop.go:297
  return dbbrowser.Conn{
      Host: host, Port: port, User: u.User.Username(), // redis → ""
      Password: password, DB: strings.TrimPrefix(u.Path, "/"),
  }, nil
  ```
- 影响：任一 redis Database 的 `BrowseDatabase` 受理成功（行落、URL/ticket 返回 200），browseLoop 每拍在 `renderBrowseDialect → Render` 处报 `dbbrowser: connection user must not be empty`，`reconcileBrowseSession` 只打服务端日志并 return——载体永不 Ensure、路由永不发布，用户打开的 URL 永久 404/不可用；无 API 错误面可诊断。`dbtemplate_test.go:98` 钉死的 `redis://:secretpw@...` 形态证明空用户名是**预期**连接串形态，矛盾在方言闸而非铸造面。
- 建议：按方言收窄校验（redis 无用户名概念：`Render` 的 redis 分支不要求 user；或 engine 铸 `Conn` 时按 cred URL 形态给 redis 占位 user），并在 `dbtemplate.ConnURL → browseConnFromURL → Render` 五引擎往返表测上钉死（不依赖 Docker，**机制层**修法，S）；`dind-browse` 至少跑两个引擎。
- 状态：**已亲验**（代码链唯一、调用方唯一；未跑 Docker 真机）

### R01-M1｜Mongoku 方言自铸连接串丢弃 DB/authSource，与 dbtemplate 连接串单真源分歧（待复核）
- 维度：功能
- 严重度：**M**（若真机确认认证失败，则与 H1 同族、应升为 H）
- 位置：`internal/domain/services/dbbrowser/mongoku.go:32`（自铸 `mongodb://user:pw@host:port`，无库段）；对照 `internal/domain/services/dbtemplate/mongo.go:73`（ConnURL 含 `/fleetly`，即 authSource 隐含来源）、`mongo.go:65`（init 脚本把用户建在 `fleetly` 库）；`browseloop.go:297`（Conn 明明携带 DB，方言却丢弃）
- 证据：
  ```go
  // internal/domain/services/dbbrowser/mongoku.go:32
  env := map[string]string{
      "MONGOKU_DEFAULT_HOST": fmt.Sprintf("mongodb://%s:%s@%s:%d",
          conn.User, conn.Password, conn.Host, conn.Port), // 无 "/db"、无 authSource
      "MONGOKU_SERVER_PORT": "8080",
  }
  ```
  ```go
  // internal/domain/services/dbtemplate/mongo.go:65 —— 用户在 fleetly 库
  initJS := "db.getSiblingDB('" + moDBName + "').createUser({user: '" + moUser +
      "', pwd: '" + password + "', roles: [{role: 'readWrite', db: '" + moDBName + "'}]});\n"
  ```
- 影响：按 MongoDB 连接串规范，无库段时 driver 的 `authSource` 缺省为 `admin`，而平台用户建在 `fleetly` 库——mongoku 大概率认证失败、无法列出业务库；即 browse 四方言中第二支也不可用。`dbtemplate_test.go:131` 钉死的连接串是 `mongodb://fleetly:secretpw@db-01j8:27017/fleetly`（含库段），证明单真源形态被方言改写丢失。
- 建议：`MONGOKU_DEFAULT_HOST` 用 `Conn.DB` 补 `/db`（或显式 `?authSource=<db>`）；与 H1 共用五引擎往返表测。成本 S。
- 状态：**待复核**（URL 分歧已亲验；"认证失败"依赖 MongoDB driver `authSource` 缺省语义与 mongoku 连接实现——本机 Docker 不可达、未能真机：需 mongo+mongoku 真机一次或 mongoku 源码确认）

### R01-M2｜ValidateJob 校验面与注释承诺不符：config 面声称 fail-closed 拒绝，实际未校验且铸造面透传渲染
- 维度：架构一致性
- 严重度：**M**（校准：受理承诺失真 + 故障点后移到 releasing；当前被"引用必须存在于项目内"的解析兜住，不是直接注入/静默错账）
- 位置：
  - `internal/domain/shared/spec/spec.go:81`（注释承诺"Task 域不渲染的面（卷/config）…一律拒绝"）
  - `internal/domain/shared/spec/spec.go:129`（实际只拒 volumes）
  - `internal/domain/shared/spec/spec.go:108`（job 校验自建循环：db refs/name/ttl/ports/healthcheck/placement/replicas/strategy/networks；**从不调用** `ValidateProcess`，故 `config_mounts`（`spec.go:314` 面）与 `secret_refs`（`spec.go:386` 面）的全部校验对 job 失效）
  - 透传面：`internal/application/engine/predeploy.go:198`（SecretRefs/DatabaseRefs/ConfigMounts 随铸面进 TaskSpec，:198-204）
  - 渲染面：`internal/application/engine/projection.go:221` + `:70`（`configMountDecls` 原样投影 target）
- 证据：
  ```go
  // spec.go:81 注释
  // ValidateJob 校验部署期一次性作业（preDeployJobs，ADR-0030）。禁面
  // fail-closed：Task 域不渲染的面（卷/config）与一次性执行无意义的面
  // （探针/端口/placement/多副本）一律拒绝且理由精确；……
  ```
  ```go
  // spec.go:129 —— 只有卷被拒，config 从未提及
  if len(p.GetVolumes()) > 0 {
      return invalidf(field+".process.volumes", "deploy-time jobs cannot mount volumes; use a Task for volume-mounting jobs")
  }
  ```
  ```go
  // predeploy.go:198-204 —— 铸造面反而透传 config/secret（与注释相反）
  if len(p.GetSecretRefs()) > 0 { minted.Process.SecretRefs = p.GetSecretRefs() }
  minted.Process.DatabaseRefs = p.GetDatabaseRefs()
  minted.Process.ConfigMounts = p.GetConfigMounts()
  ```
- 影响：pre-deploy job 的 `config_mounts.target`（绝对路径/`..` 拒绝、`(config,target)` 去重）与 `secret_refs` 路径白名单两处既有 fail-closed 校验对该面整体失效；非法/错配声明可冻结进 Revision，直到 releasing 铸 job 时才在 provider/解析处失败（故障点后移、错误面从"受理位精确字段路径"退化为编排器报错）。安全面当前被"config/secret 名必须存在于项目内"的解析兜住（非法名到不了容器），故定为 M 而非 H。
- 建议：把 `ValidateProcess` 的共享子面抽成 `validateProcessCommon`（路径/名称/去重），`ValidateJob` 复用；修正 `spec.go:81-84` 注释；补 job config/secret 正反用例。成本 S。
- 状态：**已亲验**

### R01-M3｜IR 值域校验缺口：resources/副本上界与探针节律/形态未校验——非法声明越过受理位、双 Runtime 翻译分叉
- 维度：可靠性
- 严重度：**M**（校准：所有四源都放行非法值，故障点后移到 releasing 且错误来自编排器；无 per-App 副本配额使共享集群暴露于单 AppSpec 的无界副本/资源声明——多租户 blast radius，但需部署写权限）
- 位置：`internal/domain/shared/spec/spec.go:288`（ValidateProcess 无 `resources` 校验、`replicas` 仅拒负）、`:402`（validateHealthcheck 只查形态计数与 retries）；投影 `internal/application/engine/projection.go:148`、`:21`；消费面 `internal/infrastructure/providers/swarm/translate.go:288`（memory 负值原样）、`:486`/`:493`（durations 原样、http_path 进 CMD-SHELL）、`internal/infrastructure/providers/k3s/translate.go:343`（负值被静默丢弃）、`:542`（int32 截断，注释引用不存在的"平台配额"）
- 证据：
  ```go
  // spec.go:357 —— 唯一的 replicas 校验：只拒负，无上界
  if p.GetReplicas() < 0 {
      return invalidf(field+".replicas", "must not be negative")
  }
  // ValidateProcess 全文无 p.GetResources() 校验（spec.go:288-400）
  ```
  ```go
  // k3s/translate.go:542 —— 注释前提与代码事实不符
  replicas := int32(0)
  if w.Replicas > 0 {
      replicas = int32(w.Replicas) //nolint:gosec // 副本计数域内（平台配额远小于 2^31）
  } else if w.Replicas < 0 {
      replicas = 0
  }
  // 全仓无 per-App replicas 上限；仅 task 并发有配额（taskverbs.go:45 MaxTaskConcurrencyPerProject = 200）
  ```
  ```go
  // swarm/translate.go:486-493 —— 探针节律原样下发 + http_path 进 shell
  hc := &mobycontainer.HealthConfig{
      Interval: h.Interval, Timeout: h.Timeout, StartPeriod: h.StartPeriod, Retries: int(h.Retries),
  }
  case h.HTTPPath != "":
      hc.Test = []string{"CMD-SHELL", fmt.Sprintf(`wget -q -O /dev/null http://127.0.0.1:%d%s || exit 1`, h.HTTPPort, h.HTTPPath)}
  // k3s/translate.go:414 则把 HTTPPath 交给结构化 HTTPGet.Path——同一 IR 两 Runtime 语义不同
  ```
- 影响：
  1. `spec_file`/upload/compose/webhook 四源都可声明负 `cpu_millis`/`memory_mb`、`replicas = 2^63-1`；`ValidateProcess` 全放行，故障点后移到 releasing：k3s 侧 `int32` 截断成负副本数（k8s API 拒绝）、负资源被 `>0` 条件静默丢弃；swarm 侧负 memory 原样进 service spec、巨副本可能对调度器产生无界压力——两 Runtime 行为分叉，且 k3s 注释假设的"平台配额"对 App 域不存在。
  2. 探针值域同样放行：`exec` 空命令、`tcp_port` 越界、负 `interval/timeout`；`http_path` 无形态校验，swarm 把它拼进 `CMD-SHELL`（含空白/引号/shell 元字符时探针语义被 shell 解释），k3s 用结构化 path——健康门语义随部署 runtime 漂移（注入面仅限于用户自有容器，不构成跨租户安全面）。
- 建议：`ValidateProcess` 补 resources 非负+上界与 replicas 上界（或在 governance 加 per-Project replicas 总和配额，与 `MaxFunctionReplicasPerProject=64` 设计稿同口径）；`validateHealthcheck` 补三形态值域、duration 正数、exec 非空、http_path 形态（建议 `/` 开头的 pchar 子集，与 `capability.ValidateRoutePath` 同源思路）；修正 k3s 注释。成本 S–M。
- 状态：**已亲验**（拒绝文本未真机复验——Docker 不可达；但"受理位不校验"与"双 Runtime 代码分叉"为确定事实）

### R01-L1｜FacesOf"全子面提供"测试名不副实：14 个面只断言 11 个，Exec/NodeInspect/Content 三面无探针断言
- 维度：测试与守卫
- 严重度：**L**（校准：探测逻辑是简单类型断言，当前 FacesOf 实现与字段全对应；风险在"新增面漏写探测器"未来态）
- 位置：`internal/domain/shared/capability/faces_test.go:14`（fake 方法集不含 `RuntimeExec`/`RuntimeNodeInspect`/`RegistryContent`）、`:55`（TestFacesOfAllOffered 断言 11 个非 nil）、`:69`（Offered 期望串缺 exec/node-inspect/content）；对照 `internal/domain/shared/capability/faces.go:13`（ProviderFaces 14 字段）、`:42`（FacesOf 14 个分支）
- 证据：
  ```go
  // faces_test.go:55 —— 名称声称全提供，但 fake 未实现 Exec/NodeInspect/Content
  p := &fakeFaceProvider{}
  f := FacesOf(p)
  assert.NotNil(t, f.Logs)   // 11 个断言，止于 ProjectScopedMaterials
  ...
  assert.Equal(t, []string{"logs", "admin", "inspector", "hygiene", "utility",
      "network-maintenance", "managed", "materials", "config",
      "project-endpoints", "project-materials"}, f.Offered(), "enumeration order is frozen")
  // faces.go:27-36 的 Exec / NodeInspect / Content 三个字段无任何断言覆盖
  ```
- 影响：`ProviderFaces` 新字段或 `FacesOf` 新分支漏写/错写时，domain 侧测试仍绿；"optional face 单点协商"的枚举面没有域内冻结锚（消费方各自 nil-check 也测不到探测本身）。
- 建议：fake 补三面方法集 + 断言 14 面全非 nil 与 Offered 全序（14 项）。成本 S。
- 状态：**已亲验**

### R01-L2｜注册表守卫强度：usage 反扫把"任意引号字面量（含注释）"当 usage；errcode.GRPC 码名无有效性校验
- 维度：测试与守卫
- 严重度：**L**（校准：现存 36 码/99 事件均无悬空——我独立计数确认；缺口在未来新增/改码时的假绿面）
- 位置：`internal/domain/shared/model/errcode/usage_test.go:62`、`internal/domain/shared/model/eventcode/eventcode_test.go:80`（判据 = 文件全文 `strings.Contains(content, "\""+id+"\"")`，注释也计入）、`internal/domain/shared/model/errcode/errcode.go:67`（GRPC 只拒空/OK）、`internal/interfaces/grpc/apperrstatus/apperrstatus.go:42`（未知码名回落 `codes.Unknown`）
- 证据：
  ```go
  // usage_test.go:62 —— 只做文件内容包含断言（行注释里的 "E_XXX" 同样算 usage）
  content := strings.ReplaceAll(string(data), "\r\n", "\n")
  for _, id := range IDs() {
      if strings.Contains(content, `"`+id+`"`) {
          hits[id]++
      }
  }
  ```
  ```go
  // errcode.go:67 —— GRPC 码名合法性不校验（拼错如 "NotFoud" 在册）
  if c.GRPC == "" || c.GRPC == "OK" {
      panic(...)
  }
  ```
- 影响：新错误码只需在注释/文档字符串出现带引号字面量即可让"零引用即红"假绿（生产线断了码没有任何 apperr.New 也测不出）；GRPC 码名拼错注册期不红、golden 只钉当前字面，运行时 `grpcCodeFor` 静默回落 `Unknown`——gRPC 错误族分类失真（REST 因 `errcodeToHTTP` 全量覆盖不受影响）。`Code.GRPC` 设计上刻意不依赖 grpc-go（ADR 层裁决），所以"名字正确性"需要独立断言。
- 建议：usage 反扫改 AST/正则断言字面量出现在 `apperr.New(`（errcode）与 `FactRow{Name:`/`registerEventPayload(`（eventcode）生产调用点；在 `apperrstatus` 或轻量测试里加 `GRPC ∈ codes.Code 全名集` 断言（测试文件可 import grpc-go，不污染 domain）。成本 S。
- 状态：**已亲验**

## 5. 交叉观察（不在本模块，供 Lead 汇总去重）

1. **R04（browse 文件组）**：H1/M1 的症状落点在 `internal/application/engine/browseloop.go`（render→Ensure 前只 log+return），但根因是 domain 两份契约（`dbtemplate.ConnURL` 与 `dbbrowser` 方言闸/URL 拼装）不兼容——请与 R04 的 browse 评审去重/合并，别按两处缺陷重复计数。
2. **R06/R07（translate 面）**：`swarm/translate.go:493` 把 `Healthcheck.HTTPPath` 原样插进 `CMD-SHELL`，`k3s/translate.go:414` 用结构化 `HTTPGet.Path`——即使 IR 侧补 path 白名单（R01-M3 建议），两 Runtime 的探针方言差异仍存在；R06 应对"k8s 结构化 vs swarm shell 方言"给统一语义锚。另 `k3s/translate.go:544` 注释引用"平台配额远小于 2^31"，该配额在 App 域不存在（R01-M3）。
3. **R05（persistence）**：抽样的 8 个聚合 Repo 接口与实现签名逐一对应、无门面泄漏；CAS/tombstone/软删语义在接口注释而签名不表达（`Transit(from []State)` 之外无谓词），请 R05 核实现侧是否全部覆盖非法/合法迁移与 `ErrConflict`/`ErrNotFound` 归一。
4. **R08/R14（授权面，建议重点裁决）**：`identity/roles.go:70` 的 builtin-admin = 全资源 `:admin`（**含 `platform:admin`**），而 `EnrollNode`（`proto/fleetly/runtime/v1/runtime.proto:47`，platform:admin）与 Drain/Cordon（nodes:write）在 handler 无平台 owner 门（`internal/interfaces/grpc/fleetlygrpc/contexts.go:82/153`）——若"Team admin ≠ 平台运维"是口径，则他队 admin/owner 可铸造集群 join 材料、排空共享节点，属跨租户能力泄漏；roles.go 注释只显式排除了 member/自定义 nodes 域角色（`roles.go:45`），未说明 admin 为什么该有集群钥匙。R01 未列为发现（策略意图在文档中未明确），交 R14 按 ADR-0035/C3 裁决。
5. **R02（ADR-0083）**：`RestartSeqOnly` 纯函数与 engine 调用点（`engine/restart.go:31`）契约一致、测试覆盖 seq-only/混入/空 diff/缺席↔0/多进程；`PreDeployJobs` 内的 `restart_seq` diff 也会命中（路径后缀判定），语义上是"跳迁移"——请 R02 确认这是否是 ADR-0083 想要的形态（当前注释未覆盖 job 进程面）。
6. **R08（proto 面）**：`capability.Workload.RestartSeq` 用 `json:",omitempty"` 保证 managedFingerprint 跨二进制零漂移（`runtime.go:421`）；该承诺的消费面在 `engine` 的 `managedFingerprint`（json.Marshal 全量），R02/R06 升级矩阵验收前建议一起复看一眼。
7. **R15（文档一致性小项）**：ADR-0051 §3 表格"端口"列写 pgweb/redis-commander 为 8081，而 adapter 实际全部绑定 8080（`dbbrowser/dbbrowser.go:76` 注释"四件统一 8080"、`pgweb.go:55`、`rediscommander.go:37`）——ADR 表格与代码表述不一致，供 R15 核。

## 6. 测试与守卫质量评估

**覆盖强度（好的一面）**
- 31 个测试文件 / 3,781 行覆盖了 domain 全部有行为面的包；31 个文件无一 `t.Skip` 逃逸（`dbtemplate_live_test.go` 是显式 env-gate 的 live 核对，缺省 SKIP 属设计）。
- 断言强度普遍是**值/字节级钉死**而非"只断言不 panic"：dbtemplate 五引擎完整输出面逐字（`dbtemplate_test.go:38`）、备份/恢复 argv 逐字（`:156`）、digest 双段形态全值域（`:418`）、router.php sha256 钉死、golden 注册表逐字、`Diff` 路径字典序、`Meet` 语义矩阵 9 例 + 稳定性 100 次。
- 安全/注入家族测试做成了机制而非点：敌意密码 ×5 引擎 ×3 渲染面（`dbtemplate_test.go:241`）、image ref 注入族（`spec_test.go:94`）、route host/path 白名单边界（`routevalidate_test.go`）、build 路径与 output_dir 字符集（`build_validate_test.go`）。
- 用了真 SQLite + 真 repo 的跨层集成（`anchor_test.go`、`writes_test.go` 经 `statetest`），没有纯 mock 掩盖"事实双行"的落库语义。

**盲区与假阴性机制（重点）**
1. **约定成对的"铸造/解析"缝没有往返测试**：`dbbrowser` 用合成 `Conn{User:"fleetly"}` 直接测 Render（`dbbrowser_test.go:60`），从不把 `dbtemplate.ConnURL` 的真实输出喂进 `browseConnFromURL → Render`——H1/M1 正是被这层遮蔽。建议把"连接串 → Conn → Render"五引擎表测作为 dialect registry 的一部分（机制层，S）。
2. **FacesOf 面**："全提供"测试未随 ProviderFaces 扩到 14 面（L1）。
3. **preDeployJobs 校验无负向用例**：`ValidateJob` 测试只覆盖被显式拒绝的字段（`spec_test.go:173`），config/secret/env 面既无正例也无反例——与 M2 直接相关。
4. **relay 帧 codec 无域内单测**：`MarshalRelayFrame/ParseRelayFrame`（`capability/exec.go:176/188`）的畸形分支（<2B、session id 截断、未知 kind、255 上界 panic）只经 engine/provider 测试间接覆盖，domain 内没有直接用例。
5. **注册表反扫判据弱**（L2）：把注释字面量当 usage；GRPC 码名合法性无断言。
6. **守卫对 domain 本模块典型错的拦截**：`irguard`（spec/model 无编排器词汇）、`domainpurity`（三红线）、`capabilitypurity`、`irguard` descriptor 对齐（entityid/spec 正则与 proto CEL 字节一致）覆盖了"语言/依赖/契约单源"类错误；但"域内两模块语义不兼容"（H1/M1）这类错误**没有守卫形态**——跨模块契约对只在消费方集成处才暴露，靠 e2e 覆盖兜（而 browse e2e 只跑 postgres）。

**结论**：domain 测试在"协议/值域/确定性/注入"维度属高质量（项目最强面之一）；薄弱点集中在**跨模块 seam**（ConnURL↔方言、spec↔provider 值域）与**注册表守卫判据**，均为低成本机制补齐（合计 S）。

## 7. 未决与置信度

最不确定的 5 点：

1. **H1 未真机复验**（本机 Docker 不可达）：代码链完整（铸造→解析→方言闸→调用方唯一），但"API 返回的 URL 实际 404/不可用"的最终用户影响是由代码推演；置信度高（~95%）。
2. **M1 mongoku 认证失败**依赖 MongoDB driver `authSource` 缺省为 admin 的规范语义与 mongoku 内部实现（未取得源码/真机）；URL 丢库/authSource 的分歧本身已亲验；端到端影响置信度中（~75%）。
3. **M3 的 provider 失败文本**未真机（swarm/k3s 对负资源、巨副本的拒绝/接受行为）；"IR 未校验 + 双 Runtime 代码分叉"为确定事实，影响评级依赖编排器行为，置信度高（~90%）。
4. **R08/R14 权限口径**：builtin-admin 含 `platform:admin` 是刻意（团队管理员=平台运维）还是遗漏，仓内文档无明确口径（`roles.go` 注释只排除 member），故未列为 R01 发现；需产品/ADR 裁决。
5. **ADR-0083 的 job 进程 restart_seq 语义**：域内纯函数会把 `pre_deploy_jobs[*].process.restart_seq` 的差异也判为 seq-only（跳迁移），是否符合 ADR 意图需 R02/R15 对照附录 B 裁决；域内测试未覆盖 job 路径。

整体置信度：**高**（domain 生产 114 文件全量静态阅读 + 31 测试全读 + 目标包 fresh 全绿 + 关键结论均交叉读取消费方；唯一未亲验的是需要 Docker 的运行时行为）。
