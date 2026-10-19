# R09 组合根、配置、守护进程、安装与 CI 评审报告

> 评审基线：`PROFILE.md`（2026-10-19，HEAD `35257f18`）。全程只读，未运行任何会写仓的命令
> （未跑 `mise run generate*` / `install.sh` / e2e / `go test -update`）。
> 亲验命令：`go vet ./internal/assembly/... ./internal/config/... ./cmd/fleetlyd/...`（exit 0）、
> `go test ./internal/config/... ./internal/assembly/... ./cmd/fleetlyd/...`（三包 ok）。
> 第三方语义以 module cache 内 `github.com/lynx-go/lynx@v1.20.0`（go.mod:19）源码为准。

## 1. 覆盖清单

### 完整读过（生产文件）

- `internal/assembly/`：`provides.go`(899)、`wire.go`、`wire_gen.go`、`bootstrap.go`、`policy.go`、
  `grpc.go`、`proxyconfig.go`；`provides_test.go`、`internal/config/config_test.go`、`cmd/fleetlyd/main_test.go`（测试质量抽样）。
  注：`gateway*.go`/`errors.go` 按 MODULES 归 R08，本报告只在「装配完整性」维度扫过其注册清单与 handler 挂法。
- `internal/config/`：`config.proto`(216)、`defaults.go`(274)、`bind.go`、`config.pb.go`（抽读：头注、`Runtime/K3S` 字段、
  descriptor 与 proto 的一致性抽样）。
- `cmd/fleetlyd/`：`main.go`、`verbs.go`、`admin.go`、`relay.go`。
- `install.sh`（276 行全文）、`mise.toml`（229 行全文）、`.github/workflows/ci.yml`（444 行全文）、
  `buf.yaml`、`buf.gen.yaml`、`buf.gen.local.yaml`、`buf.lock`、`.golangci.yml`、`go.work`、`.gitignore`、
  `configs/config-example.yaml`。
- 框架语义：`lynx@v1.20.0` 的 `boot/bootstrap.go`、`lifecycle.go`、`shutdown.go`、`options.go`（关停阶段/预算/actor 启动语义）。

### 抽样读过（为排除误读而追的调用方/相邻代码）

- `internal/application/engine/`：`lifecycle.go`（Start/Stop 排水）、`loop.go`（Loop.Run）、`managed.go`（nil proxy 分支）、
  `drift.go`（rebuildBaselines 的 ctx 检查）、`builder.go`（构建 goroutine 入 wg）。
- `internal/infrastructure/persistence/state.go`（Open/连接池/迁移，无单实例锁）。
- `internal/application/authn/bootstrap.go`（`EnsureBootstrapToken` 的落盘权限与 journal 去重）。
- `internal/infrastructure/providers/{zot,localobjectstore,victorialogs,victoriametrics,traefik}` 的**工厂注册面**（env 读取点）。
- `internal/infrastructure/keyring/keyring.go`、`upload/store.go`、`upload/extract.go`（数据根内文件模式，用于校准权限面）。
- `internal/guards/`：`e2eciaccount_test.go`、`shellguard_test.go`、`railpackpin_test.go`、`resticpin_test.go`（守卫真实拦截面）。
- `e2e/dind-smoke.sh`、`e2e/dind-h2c-route.sh`、`e2e/dind-upgrade.sh`、`e2e/hermetic-build.sh`（CI↔脚本↔install.sh 对账；未运行）。
- `cmd/fleetly/cmd/doctor_exposure.go`（代理端点缺失时 doctor 的信号强度）。

### 未覆盖 / 无法覆盖

- **无法本机复验 install.sh 的真机路径**（Docker daemon 不可用、非 Linux 宿主）：systemd unit 分支、下载通道、
  `install` 覆盖运行中二进制的内核行为（ETXTBSY）均只做静态推断，标「待复核」。
- **POSIX 文件模式**无法在 Windows 判定（PROFILE §1.1 已知面）：SQLite 库文件与数据根的权限结论只能到「目录权限由
  install.sh 决定」这一层，库文件模式标「待复核」。
- `wire` 重生成未运行（会写仓）：`wire_gen.go` 与 `wire.go` 的一致性只做静态核对（ProviderSet 逐项 vs injector 调用序列）。
- 14 个 CI job 的实际运行历史（本机无 GitHub 访问面）：门禁「是否真的会红」按静态推演 + 守卫源码判定。

## 2. 模块画像与边界

**职责**：`internal/assembly` 是 fleetlyd 唯一组合根（wire 单站点），把 config、SQLite、7 类 Capability Provider、
engine、authn/idem/governance 拦截器、gRPC 服务、REST gateway、proxy 配置端点、保留窗 janitor 装成 `boot.Bootstrap`
（hook + 服务批量挂载物）；`cmd/fleetlyd` 只保留动词分发（admin/relay/version）、lynx runner 装配与版本探查拦截。
依赖方向符合 ADR-0075：assembly → 全部，cmd → assembly/config，无反向引用（`go vet` 与 dep_matrix 守卫均绿）。

**对外接口**：`Bootstrap(app, info) (*boot.Bootstrap, func(), error)`（bootstrap.go:18）、`Bootstrap.Apply`（框架侧）、
以及为 apitest/golden 复用的 `NewInterceptors`/`GRPCServerOptions`/`NewAPIDeps`/`NewServices`（生产与测试同源，是设计优点）。

**与声明的偏差（要点，详见 §4）**：

1. **配置真源实际有三条通道**，不只有 `config.proto`：
   ① `AppConfig`（proto + env `FLEETLY_*`）；② **Provider 工厂内的 `os.Getenv`**（`FLEETLY_PROXY_CONFIG_ENDPOINT`、
   `FLEETLY_PROXY_ACME_EMAIL`、`FLEETLY_PROXY_AUTH_TOKEN`、4 处 `FLEETLY_DATA_ROOT`、`DOCKER_HOST`）；
   ③ `capability.With*` 装配 ctx 注入（registry/logging/metrics/s3/token）。
   装配注释反复声明「config 是唯一契约源（ADR-0036/0038/0042）」，但 ① 对 ② 完全无表达力——`config.proto` 里根本没有
   proxy endpoint 键，`data.root` 也没有进 provider 的注入面。这是本模块**一条根因**，其后果分散在 H1 / M4 / M8。
2. **生命周期 hook 四个槽位全为空**（`NewDrainHooks`/`NewPreStopHooks`/`NewPostStopHooks` 直接返回 nil，
   provides.go:869-879），引擎排空实际由「engine 作为 lynx 服务、被 LIFO 最后 Stop」承担；注释仍写「随部署链挂入」，
   与已发货状态不符（L3）。
3. 装配预算与运行时契约不齐：`DrainTimeout=30s`/`ShutdownTimeout=30s` 显式设置，但**逐服务 Stop 预算沿用框架缺省 5s**
   （main.go:108-111 未设 `WithStopTimeout`），与 grpc.go:34 声明的 60s 优雅关停相互矛盾（M5）。

## 3. 优点（带证据）

1. **wire 单站点 + 编译期/启动期双 fail-closed**：`ProviderSet` 是唯一依赖图声明（provides.go:81-118），
   injector 由 `wireBootstrap` 独占（wire.go:13、bootstrap.go:18-20）；已注册方法缺 authz 策略即拒绝启动
   （grpc.go:163-167 `AssertAllRegisteredHavePolicy`），策略收集清单集中在 `authzFiles()`（policy.go:21-38，14 个含服务的
   proto 与 `proto/**` 实测 14 个文件/36 service 一一对应）。
2. **生成物与工具链钉版自洽且可验证**：`config.pb.go:1-4` 的 `protoc-gen-go v1.36.11 / protoc v6.31.1` 与
   mise.toml:12-13 的 `protoc=31.1`/`protoc-gen-go=1.36.11` 同源；`buf.gen.yaml` 的四个 remote 钉版与
   `buf.gen.local.yaml` + `tools:protoc-plugins`（mise.toml:26-34）逐版本对应，且「唯一已知通道差（openapiv2 $ref）」被
   显式记录（buf.gen.local.yaml:11-15）——比"声称一致"更可信。
3. **install.sh 的 env 键与 config 键同源**：`FLEETLY_DATA_ROOT`→`data.root`、`FLEETLY_REGISTRY_ADDR`→`registry.addr`、
   `FLEETLY_LOGGING_ADDR`→`logging.addr`、`FLEETLY_METRICS_ADDR`→`metrics.addr`（install.sh:190-199 与
   config.proto:132/140/150/194 对照），由 viper 的 `.`/`-`→`_` 替换规则（bind.go:21-23）机械保证；
   `/etc/fleetlyd.env` 0600 落盘（install.sh:233）且被崩溃恢复通道复用（install.sh:227-236），失败面想清楚了。
4. **hermetic 构建通道是真的钳子**：`build:hermetic` 走 `git archive HEAD` 快照 + `-trimpath`（mise.toml:120-134），
   探针脚本会向 `cmd/fleetlyd/` 落一枚未跟踪 junk 源文件再双构建比对 sha256（e2e/hermetic-build.sh:19-45）——
   若通道退化为工作树构建，产物哈希必变，红得明确。
5. **关停阶段序列的骨架是对的**：组合根显式声明服务注册序（provides.go:887-899），DB 等资源 close 挂在 `OnPostStop`
   （main.go:62-74 注释与实现一致），lynx 后置 hook 逆序执行（shutdown.go:295-303），cleanup 因此最后执行——顺序语义本身没有错，
   缺陷只在预算（M5）。
6. **危险默认被"显式收窄 + 文档化"处理**：`WithDefaults` 是唯一缺省落点（defaults.go:55-126），空值语义在 proto 注释里逐字段
   写明（如 `registry.addr` 空=停用、`browse.host_suffix` 空=整面停用），装配侧还补了归一化（`browse.tls` 空→none，
   provides.go:506-511）；nil 链容忍访问器（defaults.go:130-274）让装配只取单字段时不会 panic。

## 4. 发现清单

### R09-H1｜install.sh 未物化 proxy 拉取端点：一行安装后 Proxy 面静默停用，Route/Console/API 发布全链失效

- 维度：功能 / 可靠性（安装链）
- 严重度：**H**。校准：文档主推的安装路径（README.md:21 `curl -fsSL https://fleetly.sh/install.sh | sudo sh`）产出的控制面
  缺一条核心能力面，而用户侧**没有错误**——Route CRUD 成功、`fleetly routes list` 有行、公网主机永不应答；不是"部署时报错"
  而是静默功能缺失。有经验者可从启动 Warn 与 `fleetly doctor` 的 exposure fail 发现（见影响段），故不按"完全静默"计。
- 位置：`install.sh:186-218`（另：`internal/infrastructure/providers/traefik/provider.go:57-60`、
  `internal/assembly/provides.go:90-100`、`internal/application/engine/managed.go:36-41`、
  `docs/runbooks/staging-fleetly.md:26`、`e2e/dind-h2c-route.sh:86`、`install.sh:13-21`（脚本头 env 清单亦无此键））
- 证据：

  ```sh
  # install.sh:188-200（systemd 分支：EXTRA_ENV 只拼 registry/logging/metrics 三面，无 proxy endpoint）
    EXTRA_ENV=""
    if [ -n "$REGISTRY_ADDR" ]; then
      EXTRA_ENV="Environment=FLEETLY_REGISTRY_ADDR=$REGISTRY_ADDR
  "
    fi
    if [ -n "$LOGGING_ADDR" ]; then
      EXTRA_ENV="${EXTRA_ENV}Environment=FLEETLY_LOGGING_ADDR=$LOGGING_ADDR
  "
    fi
    if [ -n "$METRICS_ADDR" ]; then
      ...（同款，第三面；脚本头 env 清单 install.sh:13-21 亦无 proxy endpoint 键）
  ```
  ```sh
  # install.sh:209-210
    Environment=FLEETLY_DATA_ROOT=$DATA_ROOT
    ${EXTRA_ENV}Restart=always
  ```

  ```go
  // traefik/provider.go:57-60 —— 端点只能来自 os.Getenv，空即工厂失败
  func New(configEndpoint, acmeEmail, authToken, acmeCAServer string) (*Provider, error) {
  	if configEndpoint == "" {
  		return nil, fmt.Errorf("traefik provider: config endpoint is required")
  	}
  ```
  ```go
  // provides.go:96-100 —— 工厂失败被降级为 Warn + nil（装配不 fail-fast）
  p, err := capability.Build(ctx, capability.KindProxy, "")
  if err != nil {
  	app.Logger().Warn("proxy provider unavailable; route publishing disabled", "err", err)
  	return nil, func() {}, nil
  ```
  ```go
  // engine/managed.go:38-41 —— nil proxy 直接跳过 Route 发布环
  if e.proxy == nil {
  	return // Proxy 未装配（可选项）：无 Route 发布面
  }
  e.publishRoutes(stepCtx)
  ```
- 影响：新主机按 README 安装后，(a) 没有受管 traefik 载体（`reconcileManaged` 里的 Proxy 域缺席）；(b) `publishRoutes`
  永不执行，App Route / `console.host` / `api.host` / `browse.host_suffix` 全部无效果；(c) 用户拿不到任何错误——
  `Route` 行照常创建。staging 单元是**手工注入** `FLEETLY_PROXY_CONFIG_ENDPOINT=http://10.124.0.3:9082/proxy/config`
  （runbook:26）；全仓 8 个调用 install.sh 的 e2e 腿里只有 5 个显式 `-e FLEETLY_PROXY_CONFIG_ENDPOINT=...`
  （bluegreen:78 / browse:74 / h2c:86 / template:75 / upgrade:148），其余 3 个（smoke/backup/twonode）不带该 env 且
  不断言任何 Route；4 个 k3s 腿则在手起 daemon 时注入（如 dind-k3s.sh:312）。即"带 Route 的安装链"从未以 install.sh 的
  原生 env 面在 CI 跑过一次。缓解信号：启动 Warn、`fleetly doctor` 的 `proxy config exposure` 在未配置时返回 checkFail
  （`cmd/fleetly/cmd/doctor_exposure.go:158-159` "not configured (proxy config endpoint stays disabled)"）。
- 建议：**根因修法**——把端点派生放进 config 面：由 `server.proxy_config.addr` + advertise/网关地址在装配期算出
  `http://<host>:<port>/proxy/config`，经 `capability.WithProxyConfigEndpoint(ctx, ...)`（与 registry/logging/metrics 同款）
  注入 traefik 工厂，`os.Getenv` 仅作旧通道兜底；同时 install.sh 在派生 advertise 地址后物化该 env（它与 registry 的
  `<advertise>:5000` 是同源信息，脚本已算过 ADDR）。成本 S（改动 3 文件）。机制层：加一条守卫/单测断言
  「install.sh 物化的 env 集合 ⊇ 各 Provider 工厂读取的 required env 集合」，防下一个 Provider 重演。修完
  H1 同时消灭 M4/M8 的同类通道。
- 状态：**已亲验**（env 读取点、工厂失败路径、nil proxy 分支、install.sh 全文、e2e 脚本注入面均已核对；
  未跑真机 install 验证"新装后 Route 不生效"的端到端现象，但四条代码链首尾相接，无其他注入通道——
  全仓 `FLEETLY_PROXY_CONFIG_ENDPOINT` 的设置点只有 e2e/runbook/doctor，daemon 自身不 setenv）。

### R09-M1｜CI 无生成物零漂移门禁：`genproto/**`、`config.pb.go`、`wire_gen.go` 的基线断言只活在本地

- 维度：测试与守卫
- 严重度：M。校准：不是现存缺陷，是**门禁 false-green 的直接通道**——本仓把生成物纪律写成硬约定
  （AGENTS.md「生成物纪律：手改即错」），但唯一执法通道 `mise run generate:verify` 不在 CI 里。
- 位置：`.github/workflows/ci.yml:34-51`（另：`mise.toml:70-75`、`AGENTS.md`「生成物纪律」、对照 `ci.yml:57-74` 的 console 漂移门禁）
- 证据：

  ```yaml
  # ci.yml:40-51 —— backend 全部步骤：无 generate:verify
        - name: Format check (gofmt) + go vet
          run: mise run lint:go
        - name: golangci-lint
          run: mise run lint:golangci
        # codegen 漂移检查（buf generate / wire 同步）在本地门禁（mise run
        # generate:all + git diff）：CI 冷环境重跑生成受 tidy/网络影响会产生
        # 本地不复现的 diff（torchwood 2026-09-23 裁决沿用）。
        - name: Tests (race, three modules)
          run: go test -race ./... ./genproto/... ./sdk/go/...
  ```
  ```toml
  # mise.toml:70-75
  [tasks."generate:verify"]
  run = [
    { task = "generate:all" },
    "git diff --exit-code -- genproto internal/config internal/assembly",
  ]
  ```
- 影响：(a) 改 `config.proto` 不加生成 → 旧 `config.pb.go` 照常编译，新键被运行时静默忽略（最坏形态：运维配了键、不生效）；
  (b) 手改/漏重生成 `wire_gen.go` 与 `wire.go` 不一致时编译仍绿（injector 只是函数体）；(c) proto 改了却漏 `buf generate` 时
  genproto 与 proto 金标准脱钩，而 `buf lint/breaking` 只看 proto 源，不看生成物。对照面：console 的生成物漂移**在** CI 里
  （`console:verify`，ci.yml:73-74），Go 侧缺失是明显的不对称。CI 注释的「tidy/网络」理由可被绕开（`generate:proto:local`
  走 go install + Go proxy，CI 上不比本地差；`wire` 任务里的 `go mod tidy` 本身可以在 CI 用 `GOFLAGS=-mod=mod` 固定）。
- 建议：新增 backend 之后的 `codegen` job（或 backend 内一步）跑 `mise run generate:all && git diff --exit-code`，
  失败即红；若担心网络，给该 job 加 `continue-on-error: false` + 明确的 mise 缓存，不做降级。同批应把
  `verify:hermetic` 也拉进 CI（它现在同样只在本地，且 `e2eciaccount` 守卫只对账 `e2e:*` 任务，覆盖不到它）。
  另建议补一条守卫：断言 `ci.yml` 的 gate step 集合覆盖 `mise.toml` 里的 `generate:verify`/`verify:hermetic`/`lint:*`/`test`，
  否则「把某个门禁 step 删掉」这类 PR 全绿无人拦（现 `e2eciaccount_test.go:50-103` 只做 e2e 子集对账）。
  成本 S/M。
- 状态：已亲验（ci.yml 全文无 generate/verify:hermetic 调用；mise 任务与守卫源码已读）。

### R09-M2｜install.sh 无升级语义：不重启服务、不留旧二进制、健康探测分不清代次

- 维度：可靠性（运维）/ 安全（回滚能力）
- 严重度：M。校准：`install.sh` 是唯一对外安装通道，重跑即升级是用户的自然动作；当前形态会"报成功但没换版本"，
  且失去回滚原料（staging runbook 已记「install 前未留 .bak」的教训）。
- 位置：`install.sh:201-218`（unit 写入 + `enable --now`）、`install.sh:52-53`/`69-70`（直接 `install` 覆盖 `/usr/local/bin`）、
  `install.sh:245-259`（健康探测）
- 证据：

  ```sh
  # install.sh:201-218
    cat > /etc/systemd/system/fleetlyd.service <<UNIT
  ...
    systemctl daemon-reload
    systemctl enable --now fleetlyd >/dev/null
  ```
  ```sh
  # install.sh:245-259 —— 探测只问"有没有 daemon 应答"，不问"哪一个"
  while [ "$i" -lt 60 ]; do
    if "$BIN_DIR/fleetly" status >/dev/null 2>&1; then break; fi
    i=$((i + 1)); sleep 1
  done
  ```
- 影响：(a) `systemctl enable --now` 对已 active 的 unit 是 no-op，**已运行的旧进程不会被重启**——升级后新二进制在盘上、
  旧进程在跑，脚本仍打印 "fleetlyd healthy"；(b) `install -m 0755` 直接覆盖运行中的 `/usr/local/bin/fleetlyd`（Linux 上
  取决于 coreutils 是否 unlink 后重写：要么 ETXTBSY 让 `set -e` 在安装中途退出、要么静默换 inode，两种都不是"升级"）；
  (c) 无 `.bak`/版本目录，`FLEETLY_VERSION` 回退要靠外部手段；(d) 重写 unit 时**覆盖手工编辑**（staging 的 drop-in
  `browse.conf` 形态能幸存，但直接改 unit 的改动会被吞）。e2e 升级矩阵只用 install.sh 装**旧版**（dind-upgrade.sh:147-149），
  换装本体是 `pkill -TERM` + `docker cp` + `setsid` 手拼（dind-upgrade.sh:349-375）——所以上面三条无任何 CI 覆盖。
- 建议：升级分支显式化——检测到 unit 已存在/服务在跑时，先备份 `fleetlyd`→`fleetlyd.prev`，安装后 `systemctl restart`
  并轮询 `fleetly --json version` 的 commit 戳与新二进制 `--version` 对齐（探测代次而非存活）。成本 S。
  机制层：把「install 前留 .bak」写进脚本而不是 runbook 教训。
- 状态：已亲验（脚本静态）；`install` 覆盖运行中文件的内核行为**待复核**（需 Linux 真机）。

### R09-M3｜下载物无任何完整性校验：fleetly/railpack/restic 与 `curl | sh` 安装 docker

- 维度：安全（供应链）
- 严重度：M。校准：TLS 是唯一信任锚，仓库尚无签名/校验和发布物；对"root 权限一次性安装控制面"的场景，
  上游或 CDN 被替换即等于平台被接管。
- 位置：`install.sh:67-70`（fleetly tarball）、`install.sh:84`（`curl -fsSL https://get.docker.com | sh`）、
  `install.sh:144-146`（railpack）、`install.sh:163-165`（restic）
- 证据：

  ```sh
  curl -fsSL "$URL" -o "$TMP/fleetly.tar.gz" || die "download failed: $URL"
  tar -xzf "$TMP/fleetly.tar.gz" -C "$TMP"
  install -m 0755 "$TMP/fleetlyd" "$BIN_DIR/fleetlyd"
  install -m 0755 "$TMP/fleetly" "$BIN_DIR/fleetly"
  ...
  curl -fsSL https://get.docker.com | sh || die "docker installation failed"
  ```
- 影响：三类外部二进制以 root 落入 `/usr/local/bin` 且立即执行（docker 安装脚本当场执行），中间无 sha256/签名/发布物校验；
  `VERSION` 来自 GitHub API 的正则抽取（install.sh:59-61），无白名单形态校验。`curl | sh` 的失败也无法被 `set -e` 捕获
  （POSIX sh 无 pipefail）。
- 建议：发布批次生成 `fleetly-<os>-<arch>.tar.gz.sha256`（或 cosign/SLSA 证明），install.sh 下载后 `sha256sum -c`；
  railpack/restic 同法（两者已是钉版下载，补校验和成本极低）。docker 改用官方 apt 仓库 + 钉版。成本 S/M（依赖发版批次）。
- 状态：已亲验（脚本内无 checksum 任何形态；`grep -i sha256 install.sh` 零命中）。

### R09-M4｜配置面三分裂（`data.root`）：config.proto 的 `data.root` 对受管 Provider 无效

- 维度：架构一致性 / 可靠性
- 严重度：M。校准：install.sh 恒以 env 注入，故文档主路径不触发；但 daemon 明示支持 `--config-dir`（main.go:102）与
  `configs/config-example.yaml` 的 `data.root` 写法，一旦有人按 YAML 配数据根，则 DB 与受管面材料落在两个根。
- 位置：`internal/infrastructure/providers/zot/provider.go:616-628`、
  `providers/localobjectstore/provider.go:320-326`、`providers/victorialogs/provider.go:593-601`、
  `providers/victoriametrics/provider.go:408-416`（另：`internal/assembly/provides.go:339-454` 的注入对照面）
- 证据：

  ```go
  // zot/provider.go:617-628 —— addr 有 ctx 通道，dataRoot 只有 env
  capability.RegisterFactory(capability.KindRegistry, "zot", func(ctx context.Context) (capability.Provider, error) {
  	addr := capability.RegistryAddrFromContext(ctx)
  	if addr == "" { addr = os.Getenv("FLEETLY_REGISTRY_ADDR") }
  	...
  	dataRoot := os.Getenv("FLEETLY_DATA_ROOT")
  	if dataRoot == "" {
  		dataRoot = "./data" // 与 config.DefaultDataRoot 同缺省（install.sh 恒注入 env，此处兜底）
  	}
  ```
  ```go
  // provides.go:344-346 —— 对照：其它面都走装配 ctx
  ctx := capability.WithRegistryAddr(context.Background(), cfg.RegistryAddr())
  p, err := capability.Build(ctx, capability.KindRegistry, "")
  ```
- 影响：若 `data.root=/srv/fleetly` 只写在配置文件里（DB、KEYS、uploads 都在 /srv），则 zot 的 htpasswd/仓目录、
  localobjectstore 的 Platform Backup 本地仓、VL/VM 的受管数据卷全部落到 daemon 工作目录下的 `./data`：
  备份与恢复的目标根错位、`Restore` 找不到仓、数据根迁移后受管域材料留在旧盘——都是**静默**发生。
  同类偏差还有 `DOCKER_HOST`（4 处 builders/swarm，可接受，属 docker 惯例）与 `FLEETLY_PROXY_*`（H1/M8）。
- 建议：给 Provider 工厂开一个 `capability.WithDataRoot(ctx, cfg.DataRoot())` 通道（与 registry/logging 同款），
  `os.Getenv` 降为旧通道兜底并在注释里写真话（现注释「install.sh 恒注入 env」把环境假设写成了保障）。
  成本 S。机制层：一条守卫扫描「providers 包内 `os.Getenv` 键集合 ⊆ 白名单」，新加 env 必须走 config 通道。
- 状态：已亲验（四点 env 读取 + 注入面对照）。触发概率**待复核**：文档全仓未找到 fleetlyd 的 YAML 全量示例
  （只有 `configs/config-example.yaml` 15 行），YAML 形态是否为真实用户路径需 Lead 判断。

### R09-M5｜关停预算不一致：gRPC 60s 优雅关停被框架 5s 逐服务预算截断，engine 排空同受 5s 限

- 维度：可靠性（优雅关停 / 升级零扰动）
- 严重度：M。校准：顺序对、预算错；超时后框架**继续**走 post-stop，最终关掉 DB 时引擎 goroutine 可能仍在跑。
- 位置：`internal/assembly/grpc.go:34`（`grpcShutdownTimeout = 60s`）、`internal/assembly/grpc.go:149-157`、
  `cmd/fleetlyd/main.go:108-111`、`internal/assembly/provides.go:835-842`、
  框架侧 `lynx@v1.20.0/shutdown.go:73-87` + `options.go:18-19`（`DefaultStopTimeout = 5s`）
- 证据：

  ```go
  // cmd/fleetlyd/main.go:108-111 —— 只设了 drain/shutdown，未设逐服务 Stop 预算
  		// 排水窗口：生产默认 30s（LB 摘流 + 在途请求收口）。
  		lynx.WithDrainTimeout(30*time.Second),
  		lynx.WithShutdownTimeout(30*time.Second),
  ```
  ```go
  // grpc.go:34 —— 声明 60s 优雅关停
  const grpcShutdownTimeout = 60 * time.Second
  // grpc.go:152
  		lynxgrpc.WithShutdownTimeout(grpcShutdownTimeout),
  ```
  ```go
  // lynx shutdown.go:73-78 —— 实际等待上界是 StopTimeout（缺省 5s）
  func (app *lynx) stopServiceBounded(ctx context.Context, service Service) {
  	stopCtx, cancel := context.WithTimeout(context.Background(), app.o.StopTimeout)
  	defer cancel()
  	stopErr, timedOut := callBounded(stopCtx, func() error { return service.Stop(ctx) })
  ```
- 影响：(a) gRPC 服务器自己的 60s 优雅关停永远走不到底——框架 5s 就判超时并继续，仍在途的 unary（通用预算 30s，
  RebuildNetwork 更长）与 SSE 流（gateway 的 `WriteTimeout=0`、长连接）被硬切断，升级时客户端见 `Unavailable`
  而非平滑收口；(b) `engineService.Stop` 把服务自身 ctx 传给 `Engine.Stop`，而该 ctx 在 `serviceActor.stop` 里是
  **stop 之后**才 cancel（框架语义），因此 `Engine.Stop` 内的 `<-ctx.Done()` 分支基本不可达，真正上界也只有 5s
  （engine 的 wg 含构建 goroutine、baseline replay、drift 扫描、relay 环）——超时后 5s 返回错误、框架继续 post-stop
  执行 `db.Close()`，而引擎 goroutine 仍可能在写库（日志出现 `database is closed`，部署行停在中间态，重启重放可恢复）。
- 建议：显式 `lynx.WithStopTimeout(...)`（≥ grpcShutdownTimeout 与引擎排空上界，如 65s），并让
  `engineService.Stop` 用**自己派生**的带 deadline ctx（`context.WithTimeout(ctx, drainBudget)`）传给 `Engine.Stop`，
  使"预算耗尽"由引擎自己记账而不是靠框架 kill。成本 S。机制层：加一条启动期断言/测试，把
  「StopTimeout ≥ 各服务自报排空预算」钉成不变量。
- 状态：已亲验（框架源码 + 组合根参数 + engine.Stop 实现三面对照）。「实际是否经常 >5s」无法测量（本机无真机、e2e 跑不了），
  属**风险**而非已观测故障。

### R09-M6｜`RetentionJanitorService.Stop` 不 join 在途 sweep，DB 关闭可能先于清扫收尾

- 维度：可靠性
- 严重度：M。校准：与 M5 同类但独立面——这个服务甚至没有"等待"，Stop 立即返回。
- 位置：`internal/assembly/provides.go:740-783`（另：`internal/application/engine/loop.go:63-82`）
- 证据：

  ```go
  // provides.go:740-741 / 783
  func (s *RetentionJanitorService) Start(ctx context.Context) error {
  	engine.NewLoop("retention-janitor", s.log).Run(ctx, retentionSweepInterval, func(ctx context.Context) {
  ...
  func (s *RetentionJanitorService) Stop(context.Context) error { return nil }
  ```
  ```go
  // loop.go:63-82 —— ctx 取消后「当前 step 收尾才返回」，不会打断在途 step
  func (l *Loop) Run(ctx context.Context, tick time.Duration, step func(context.Context)) {
  	for {
  		select {
  		case <-ctx.Done():
  			return
  ```
- 影响：框架停服务是 LIFO：proxyconfig → gateway → grpc → janitor → engine。janitor.Stop 立刻返回，其 ctx 由
  `serviceActor.stop` 在 Stop 返回后才 cancel，因此**在途一拍**（七段清扫：idem sweep、outbox trim、upload sweep、
  `SweepOrphanSecretCarriers`/`Volume`/`PeerGrant`/`TerminalTask` 走 Runtime Provider 调用）会继续跑；等 engine 5s 排水
  结束、post-stop 执行 cleanup 时 `db.Close()`，在途清扫的写/删才会撞上"数据库已关"——错误日志、部分清扫半途而废
  （幂等可重放，故不丢数据）。同一形态也适用于 `engineService`（见 M5）。
- 建议：服务持有 loop 的 cancel 与 done，`Stop` 内 `cancel(); <-loop.Done()`（Loop 已提供 `Done()`，loop.go:57），
  预算由引擎/框架给定。成本 S。机制层：给 `internal/guards` 加一条「阻塞型服务（Start 阻塞）必须实现 Stop 等待」的
  结构检查很难，退而求其次：在 testsupport 里加一个关停顺序/收口契约测试夹具，新服务必须过。
- 状态：已亲验（Start/Stop 与 Loop.Run 语义对读）。

### R09-M7｜无单实例约束：同一数据根可起两个 fleetlyd，端口冲突是唯一且偏晚的失败点

- 维度：可靠性 / 架构一致性
- 严重度：M。校准：单写者收敛环是引擎的核心不变量（loop.go 文件头），但进程层没有任何执法；SQLite WAL 允许多进程写，
  所以"两个写者共享一库"在数据层不会报错，只会让不变量静默失效。
- 位置：`internal/infrastructure/persistence/state.go:91-116`（Open 无锁）、`cmd/fleetlyd/main.go:94-111`、
  `internal/assembly/provides.go:239-246`
- 证据：

  ```go
  // state.go:91-116 —— 建目录 → open → migrate → 返回；无 flock/lockfile
  func Open(ctx context.Context, path string, clock state.Clock) (*DB, error) {
  	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { ... }
  	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(10000)" + ...
  	db, err := sql.Open("sqlite", dsn)
  	db.SetMaxOpenConns(1)
  	if err := migrate(ctx, db); err != nil { ... }
  ```
  ```go
  // main.go:94-97 —— 只有动词分发防线（流浪 daemon 的历史事故面），无实例互斥
  	if hasVerbArgs(os.Args[1:]) {
  		env := &commands.Environment{Stdout: os.Stdout, Stderr: os.Stderr}
  		os.Exit(newVerbApp(info).Run(context.Background(), env, os.Args[1:]))
  	}
  ```
- 影响：(a) 误起第二个 daemon（root 手跑、systemd 之外的调试进程）时，DB 打开 + goose 迁移 + `EnsureBootstrapToken`
  先于端口绑定发生，失败点在 lynx 服务 Start 的 bind，**已经写过盘**；(b) 若第二实例用不同端口（`--config-dir` 指向
  另一份只改端口的配置）则两个 daemon 静默共存于同一库：双收敛环、双 drift 扫描、双 janitor（清扫预算翻倍）、
  双 bootstrap 判定，单写者假设失效；(c) `max(1)` 连接池是**进程内**语义，跨进程不成立。
- 建议：数据根 flock（`$DATA_ROOT/.lock` 上的 `flock(LOCK_EX|LOCK_NB)`，失败即 "another fleetlyd is using this data root"）
  放在 `persistence.Open` 之前（admin rewrap 也走同锁，正好符合"停机窗口"语义）。成本 S。机制层：单实例锁是运维面
  唯一能兜住"手滑"的位置，建议不要只写在 runbook。
- 状态：已亲验（无锁实现 + 打开顺序 + 端口绑定时点）。"实际发生过"无证据（runbook 未见），按预防性设计债计。

### R09-M8｜proxy 配置端点默认全接口 + 无认证；traefik 侧 token env 与 config 键不同名（注释称"同键"）

- 维度：安全（危险默认）
- 严重度：M。校准：ADR-0036 已把"无认证端点"写成显式决策并要求防火墙兜底，代码与 install.sh 都**不执行**该兜底、
  也不告警；控制面主机若公网可达，任何人可读全量路由快照并覆盖路由（流量劫持），此时为 H。
- 位置：`internal/config/defaults.go:11-15`、`internal/config/config.proto:114-125`、
  `internal/assembly/proxyconfig.go:34-36,57-63`、`internal/infrastructure/providers/traefik/provider.go:196-204`
- 证据：

  ```go
  // defaults.go:11-15 —— 缺省通配绑定（注释自己写明：无认证端点，须防火墙封或钉内网）
  DefaultProxyConfigAddr = ":9082"
  ```
  ```go
  // proxyconfig.go:34-36,57-63 —— token 空 = 完全不校验请求头
  	authToken := cfg.ProxyConfigAuthToken()
  ...
  		if authToken != "" {
  			got := r.Header.Get(proxyAuthTokenHeader)
  			if subtle.ConstantTimeCompare([]byte(got), []byte(authToken)) != 1 { ... 401 ... }
  		}
  ```
  ```go
  // traefik/provider.go:196-204 —— 注释说"同键兜底"，实际键名不同
  // 契约源）+ env FLEETLY_PROXY_AUTH_TOKEN 同键兜底（ADR-0036 形态）；...
  		token := capability.ProxyAuthTokenFromContext(ctx)
  		if token == "" { token = os.Getenv("FLEETLY_PROXY_AUTH_TOKEN") }
  ```
- 影响：(a) 配置了 `server.proxy_config.addr`（默认 `:9082` 即零配置）后，端点对全部接口开放且无认证，可读取全量
  traefik 动态配置（路由/上游地址/forwardauth 声明）并写入任意路由；(b) 运维若只设旧 env `FLEETLY_PROXY_AUTH_TOKEN`
  （traefik 侧认它，注释还说是"同键"），**服务端 config 里没有对应值 → 端点仍无认证**，而 traefik 会带头发请求——
　"我设了 token 所以受保护"的错觉正是最危险的形态；(c) `config.proto:124` 的正确键是
  `FLEETLY_SERVER_PROXY_CONFIG_AUTH_TOKEN`，两处注释（config.proto:120-123、traefik:196）应点名这一差异；
  (d) install.sh 既不设 token 也不提示，staging 靠 unit 手工注入（runbook:26 只注 endpoint，未见 token）。
- 建议：启动期对"端点已监听且 auth_token 为空"打一次 `WARN`（含修复指引），并在 install.sh 的下一步指引里显式提示；
  更彻底的是缺省绑定 `127.0.0.1:9082`（收窄是显式动作，与 ADR-0036 的"不静默改绑"冲突，需 ADR 追记）；同时统一
  token 的 env 名（保留旧名做兼容读时注释写真话）。成本 S。R14 会从多租户面上再判一次，本条从"配置默认值 + 安装链"面提。
- 状态：已亲验（默认值、handler 分支、两处 token 读取点与键名逐字对照）。

### R09-L1｜8.6MB 的 Linux ELF 二进制 `browseprobe` 被提交在仓根

- 维度：可维护性（生成物纪律）
- 严重度：L。校准：不影响运行，但违反本仓「生成物不入仓/构建产物进 bin」的自身纪律，且 git 历史永久携带。
- 位置：仓库根 `browseprobe`（`git ls-files -s` → `100755`，8633147 字节；ELF 头 `7f 45 4c 46 …`）；
  引入 commit `51dfbf00`；真正的源码在 `e2e/browseprobe/main.go`，使用方 `e2e/dind-browse.sh:36` 构建的是 `./e2e/browseprobe`。
- 证据：

  ```text
  git ls-files -s browseprobe  →  100755 3d8bf6b6… 0	browseprobe
  git cat-file -s HEAD:browseprobe  →  8633147
  head bytes: 7f 45 4c 46 02 01 01 00 …  (ELF64, x86-64)
  git log --diff-filter=A --oneline -- browseprobe  →  51dfbf00 feat(e2e): F3.6 dind-browse 腿全绿…
  ```
- 影响：每个 clone/CI checkout 多 8.6MB；仓根出现无名二进制容易被误执行/误打包；`mise run build` 与 hermetic 快照都
  不经它，属纯噪声。也说明 `.gitignore` 缺一条「仓根产物」防线（现有规则只列 `/bin/`、`/fleetlyd`、`/fleetly`）。
- 建议：`git rm --cached browseprobe` + 在 `.gitignore` 加根级产物规则；机制层：守卫扫 tracked 文件中 >1MB 且 ELF/PE
  魔数的条目（`internal/guards` 现已有扫描文件系统的基础设施）。成本 S。
- 状态：已亲验（git 元数据 + 魔数 + 引用面全仓 grep 零命中）。

### R09-L2｜`configs/config-example.yaml` 只覆盖 11 个配置面中的 3 个，缺 ADR-0082 收敛旋钮

- 维度：文档一致性
- 严重度：L。校准：模板最小化可以接受，但它自称「配置模板……全部字段可缺省」且被 feature-checklist 记为
  ADR-0017 A.4 的交付物；最新验收重点（ADR-0082）的旋钮一条都没有。
- 位置：`configs/config-example.yaml:1-15`（对照 `internal/config/config.proto:10-34` 的 11 个顶层 message）
- 证据：

  ```yaml
  # configs/config-example.yaml（全文 15 行，去掉注释仅 6 个键）
  server:
    grpc:
      addr: ":9080"
    http:
      addr: ":9081"
  data:
    root: "./data"
  engine:
    schedule_overlap_policy: "skip"
  ```
- 影响：缺 `server.proxy_config{addr,auth_token}`、`server.templates_catalog_url`、`registry`、`platform_backup{s3 五元组}`、
  `logging`、`metrics`、`browse`、`runtime{provider,k3s}`、`console`、`api`、以及 `engine.converge_posture/
  converge_failure_threshold/converge_cooldown_secs`。运维按模板配完会以为"配全了"，而这些恰恰是受管五件与服务面开关；
  叠加 M4（`data.root` 只对 DB 生效）与 M8（auth_token 缺省无认证），模板的信息缺口会被放大。
- 建议：模板补齐全部顶层段（每段一条注释 + env 名 + 空值语义），并在 CI/守卫里加一条「proto 顶层字段 ⊆ 模板覆盖集」
  的机械对账（反射 descriptor 即可，成本 S）。成本 S。
- 状态：已亲验（模板全文 + proto 全文对照）。

### R09-L3｜组合根注释与框架/已发货状态不符（启动顺序、"随批次挂入"的过期承诺）

- 维度：可维护性 / 文档一致性
- 严重度：L。校准：纯注释，但三条都会误导后来者做出错误推断（尤其"按注册顺序启动"——它不存在）。
- 位置：`internal/assembly/provides.go:881-886`、`provides.go:869-879`、`provides.go:856-858`
- 证据：

  ```go
  // provides.go:881-886
  // NewServices 返回服务注册顺序：engine → idem-janitor → grpc → gateway →
  // proxyconfig。lynx 按注册顺序启动、逆序停止——引擎最后停：...
  ```
  ```go
  // provides.go:869-879（三处同款过期承诺）
  // NewDrainHooks 排水钩子（OnDrain，摘流窗口内执行）：当前为空；Managed
  // Provider 摘流随 Proxy 批次挂入。
  // NewPreStopHooks ...：当前为空；引擎排空（in-flight Ensure 可安全中断，ADR-0005）随部署链挂入。
  // NewPostStopHooks ...：当前为空；wire cleanup 已单独经 Bootstrap 返回值挂载。
  ```
  ```go
  // 对照 lynx lifecycle.go:45-49 —— 所有 actor 并发启动，只有停止是 LIFO
  	for _, a := range actors {
  		a := a
  		go func() { done <- actorResult{name: a.name, err: a.execute()} }()
  	}
  ```
- 影响：(a)「按注册顺序启动」是错的：`grpc` 与 `gateway` 并发起，gateway 可能先于 gRPC 监听，启动瞬间经 :9081 的请求
  会失败（有 60s 重试兜底，但设计推断已失真；真正需要顺序时应使用框架的 `NewOrderedServices`）；(b)「引擎排空随部署链
  挂入」「Provider 摘流随 Proxy 批次挂入」——部署链与 Proxy 都已发货，实际承载者是 engine 服务 Stop（provides.go:835-842）
  与 nil-check，`Proxy` 批次也没有 Drain hook，读者会去找不存在的接线；(c)「Console SPA embed 等可选编译面随对应批次挂入」
  （provides.go:856-858）——console 已 embed 并挂进 gateway（gateway.go:24）。
- 建议：三条注释改成现状描述（并写明"启动并发、仅停止有序"这一框架事实），把历史承诺移到 ADR/挂账表。成本 S。
- 状态：已亲验（注释原文 + 框架源码 + gateway 引用）。

### R09-L4｜`wire` 任务跑 `go mod tidy`，但零漂移断言不覆盖 `go.mod`/`go.sum`

- 维度：测试与守卫
- 严重度：L。校准：`generate:verify` 是唯一 codegen 门禁，其 diff 白名单没有 `go.mod`/`go.sum`，而任务链里明确会动依赖文件。
- 位置：`mise.toml:55-59`（wire 任务）、`mise.toml:70-75`（verify 的 diff 范围）
- 证据：

  ```toml
  [tasks.wire]
  dir = "internal/assembly"
  env = { GOWORK = "off" }
  run = ["go mod tidy", "go run -mod=mod github.com/google/wire/cmd/wire"]
  ...
  "git diff --exit-code -- genproto internal/config internal/assembly",
  ```
- 影响：本地跑 `generate:verify` 时若 `go mod tidy` 改了 `go.mod`（增删 require、toolchain 行），断言仍绿——
  依赖漂移与 wire 漂移在同一门禁里，却被允许其中一个不报。CI 侧的 M1 修好后这条会变成"门禁放行 go.mod 变更"的实际风险。
- 建议：把 `go mod tidy` 拆成独立任务（或加 `-diff` 检查：`go mod tidy -diff` 需要 Go 1.23+），并让 assert 范围包含
  `go.mod go.sum go.work`；成本 S。
- 状态：已亲验（任务定义与 diff 范围逐字对照）。

### R09-L5｜install.sh 预建数据根 0755，压过 daemon 自身的 0750 意图

- 维度：安全（加固）/ 一致性
- 严重度：L。校准：token 文件 0600、`keys/` 0700 仍然保护最关键的两项，故只到"目录可枚举 + 库文件模式待复核"这一层。
- 位置：`install.sh:173-174`（另：`internal/infrastructure/persistence/state.go:95`、
  `internal/application/authn/bootstrap.go:87`（`os.MkdirAll(dataRoot, 0o750)`）、`bootstrap.go:90`（token 文件 0o600））
- 证据：

  ```sh
  # install.sh:173-174 —— 默认 umask 022 → /var/lib/fleetly 为 0755
  DATA_ROOT="${FLEETLY_DATA_ROOT:-/var/lib/fleetly}"
  mkdir -p "$DATA_ROOT"
  ```
  ```go
  // persistence/state.go:95 —— 目录已存在时 MkdirAll 不会收紧权限
  	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
  ```
- 影响：代码侧统一的 0750 意图在真机上失效（数据根 0755），本地任意用户可枚举 `fleetly.db`、`uploads/`、`bootstrap-token`、
  `keys/` 的名字；实际读取仍受文件自身模式保护（bootstrap token 0600 已亲验，KEK 0600/`keys/` 0700 已亲验，
  upload blob 经 `os.CreateTemp` 0600）。SQLite 库文件由驱动创建、仓内**无显式 chmod**，POSIX 下常见 0644 →
  库可读（用户/token 哈希/审计/revision spec）——**待复核**（Windows 无法验证 POSIX 位）。
- 建议：install.sh 里 `install -d -m 0750 "$DATA_ROOT"`；若确实存在老部署，加一次 `chmod 0750` 幂等收敛；
  `persistence.Open` 创建库后 `os.Chmod(path, 0o600)`（新文件才需要的显式钉法）。成本 S。
- 状态：目录权限已亲验（脚本 + 代码）；**库文件模式待复核**（需 Linux 真机 `stat`）。

### R09-L6｜`buf.yaml` 的一次性更名豁免已过期（`proto/fleetly/edge/v1/edge.proto` 已不在树内）

- 维度：测试与守卫（白名单腐烂）
- 严重度：L。校准：无害但违反"豁免落地即移除"的自我纪律，也是白名单长期腐烂的第一块砖。
- 位置：`buf.yaml:22-26`（`ignore:`）、`buf.yaml:31-35`（`ignore_only:` 两条 runtime 更名）
- 证据：

  ```yaml
  breaking:
    use:
      - FILE
    # ADR-0047 一次性更名豁免：fleetly.edge.v1 整包迁移至 fleetly.proxy.v1
    #（删除 edge.proto、新增 proxy.proto，词条更名非契约破坏）。豁免仅此
    # 旧路径、不开全局口子；落地 origin/main 后本条目可移除。
    ignore:
      - proto/fleetly/edge/v1/edge.proto
  ```
  ```text
  Test-Path proto/fleetly/edge  →  False   （现树无该目录；proxy 包已在）
  ```
- 影响：`ignore` 指向不存在的路径 → 等于永久空豁免；`ignore_only` 的两条（`runtime.proto` 的
  `FIELD_SAME_NAME`/`FIELD_SAME_JSON_NAME`）在更名落地后同样到期。将来若有人重建 `proto/fleetly/edge/v1/edge.proto`
  （例如复用旧名），这条 ignore 会**静默放行**该路径上的破坏性变更，而注释读起来像"历史特例、已无害"。
- 建议：删除 `ignore` 项；`ignore_only` 若仍被 `buf breaking --against origin/main` 需要（比对基线仍含旧名），
  改为跟随"基线 ref 到达含新名的提交后移除"的具体条件，并在守卫里加一条「ignore 路径必须存在于现树或基线」。
  成本 S。
- 状态：已亲验（buf.yaml 原文 + 目录存在性）。

### R09-L7｜CI 无跨平台编译门禁（Windows 构建失败是已知事实，但没有一道一行的闸）

- 维度：测试与守卫（覆盖面）
- 严重度：L。校准：生产部署是 Linux-only，不影响运行时；但仓库自称 mise/Windows 开发流（mise.toml:7），
  且 PROFILE §1.1 的 `syscall.SIGWINCH` 破坏面由 ABI 级符号决定，CI 永远不会红。**代码面主审归 R10**，
  本条只评"CI 门禁缺位"。
- 位置：`.github/workflows/ci.yml:12-51`（唯一 Go 编译通道是 `go test -race ./...`，Linux amd64）；对照
  `mise.toml:7`（Windows 需 Git Bash 的 sh）、`cmd/fleetly/cmd/verbs_exec.go:159`（PROFILE 已实测的失败点）
- 证据：

  ```yaml
  # ci.yml:13-15 —— 全部 Go 面 job 都是 ubuntu-latest
    backend:
      name: Backend (lint + proto + test)
      runs-on: ubuntu-latest
  ```
  ```text
  # 本机（Windows）基线，PROFILE §1.1 已实测：
  cmd/fleetly/cmd/verbs_exec.go:159: syscall.SIGWINCH undefined (Windows)
  ```
- 影响：任何新增的 Unix-only 符号（signal/TTY/term/权限/路径分隔符）都能合入且全绿，开发者直到在自己机器上
  `go build ./...` 才发现；对一个"钉版工具链 + 双平台开发"的仓库，这是最低成本可补的盲区。
- 建议：加一个 1-2 分钟的 `GOOS=windows GOARCH=amd64 go build ./cmd/... ./internal/...`（或
  `go vet -tags` / `go build` 全部包）的 job，只编译不测试；顺带 `GOOS=darwin`（若宣称支持）。成本 S。
- 状态：已亲验（workflow 无交叉编译步骤；Windows 破坏面引用 PROFILE §1.1 的实测结论，未在本轮重跑）。

## 5. 交叉观察（不在本模块但值得其他模块注意）

1. **R07 / R14**：traefik 的 `FLEETLY_PROXY_CONFIG_ENDPOINT`（H1）与 `FLEETLY_PROXY_AUTH_TOKEN`（M8）是"工厂 env 通道"
   的两个实例，其根因在 Provider 工厂的构造参数面，不在 assembly。建议 R07 报告与本文 H1/M4/M8 并读，
   由 Lead 合并为一条"Provider 构造参数绕过 config"的横切发现（R13 也适用）。
2. **R06 / R14**：`fleetlyd relay --join-token TOKEN` 把**集群成员凭证**放在 argv（relay.go:46-50,71-73）。
   `docker inspect`/`ps --no-trunc`/容器 spec 可见；虽 read 它的人通常已有 docker 组（≈root），但同一 token 会随
   `RelayCommand` 落进 enroll 脚本与 shell 历史。建议 R06 评估改为 stdin/一次性文件通道（其 golden 已钉命令形态，改动需同批）。
3. **R08**：gateway（`:9081`）与 gRPC（`:9080`）在 lynx 下**并发启动**（L3 证据），gateway 的 gRPC client 是懒拨号，
   启动瞬间的请求会短暂失败；若 R08 需要在启动期做就绪门控，应使用 `lynx.NewOrderedServices` 或 Ready 接口，而不是依赖注册序。
   `proxyconfig` 端点同样在并发启动窗口内可能先于 gateway 可用（traefik 首次拉取会得到空快照 → 端点侧已做"永不返回裸 {}"兜底）。
4. **R10**：`fleetly doctor` 的 exposure 检查在 proxy endpoint 未配置时返回 **fail**（doctor_exposure.go:158-159），
   这是 H1 的唯一用户可见信号；若 R10 认为该文案会被读成"暴露风险"而非"能力缺失"，建议改建议文案（现文案
   "not configured (proxy config endpoint stays disabled)" 已较准确，但 check 级别是 fail 可能被当成环境噪音）。
   另：install.sh:249 用 `fleetly status` 做健康探测——它对认证面的要求应与 R10 核对（本机未验证无 token 是否恒 200）。
5. **R12**：`install.sh` 的 systemd 分支（真机主路径：unit 生成/`enable --now`/无 systemd 回退）**无任何 CI 覆盖**——
   8 个调用 install.sh 的 e2e 腿全走 dind 的 `start_fleetlyd_background`（dind-smoke.sh:90 等）。若 R12 的 e2e 覆盖面结论
   要与 CI 对齐，这是最大的安装链空洞；升级矩阵虽调 install.sh 装旧版（dind-upgrade.sh:147-149），但换装本体是
   `pkill`+`docker cp`+`setsid` 手拼（dind-upgrade.sh:349-375），install.sh 的升级语义（M2）仍不被覆盖。
6. **R05**：`persistence.Open` 无跨进程锁（M7），若 R05 在 SQLite 并发面已有同类结论，建议合并成"单实例/单写者执法"一条，
   落点选在 `persistence.Open`（admin rewrap 也会受益）。
7. **R02**：`managed.go:38-41` 的 nil-proxy 早退是 H1 的"静默"所在；若 R02 计划给 Route 发布补"未装配即精确拒绝"，
   可与 H1 的建议合并为同一批（用户面错误优于静默成功）。

## 6. 测试与守卫质量评估

**能拦住的（有实证）**

- `e2eciaccount_test.go:50-103`：mise `e2e:*` 任务 ↔ `ci.yml` 调用双向对账，含防盲阈值（≥8/**≥5**）与白名单双向保鲜；
  当前 12 任务/12 调用、白名单为空，与 ci.yml 实况一致（我逐条数过）。
- `railpackpin_test.go` / `resticpin_test.go`：install.sh 的钉版字面量 ↔ Go 常量双向断言，改一处不改另一处即红——
  这正是 M3（无校验和）与 H1（缺 env）**没有**的那类守卫，说明范式在仓内是现成的，缺的是覆盖面。
- `shellguard_test.go`：进程执行面 args-array 红线（解释器名/`-c` 字面量），白名单零豁免且双向保鲜；
  与本模块的 relay/doctor 面吻合。
- `internal/config/config_test.go`：缺省语义、env-only 嵌套键（S3 五元组）、nil 链容忍——把 viper 的"嵌套 env 不通"坑钉死了，
  是 M4 之外配置面的有效防线。
- `internal/assembly/provides_test.go`：objectstore 选择谓词、runtime 选名 fail-fast、metrics 面 addr 门——装配决策面有 hermetic 锚。

**拦不住的（本模块的主要缺口）**

1. **没有生命周期测试**：`NewServices` 顺序、janitor/engine 的 Start 阻塞契约、Stop 是否 join 在途 step、
   预算与 `WithStopTimeout` 的关系，全部无测试（`provides_test.go` 只测纯函数与选名）。M5/M6 这类缺陷因此只能靠读框架源码发现。
2. **装配面"必需的 env 集合"没有守卫**：H1 的根因是 install.sh 与 Provider 工厂的 env 契约只存在于人脑与 runbook。
   现成的双向钉法（railpack/restic）完全可以推广到"工厂 `os.Getenv` 键集合"。
3. **生成物零漂移只在本地**（M1）——而 console 的同类门禁在 CI，说明架构上不是做不到，是 Go 侧没做。
4. **install.sh 只有 happy path 覆盖**：dind 内 bin-dir 模式（无 systemd、无下载、无升级、无校验和），
   真机 systemd 分支、下载通道、重跑升级三条完全无覆盖；`config-example.yaml`（L2）也没有"proto 覆盖"对账。
5. **装配层没有"配置面全景"测试**：`config.pb.go` 的字段 ↔ `WithDefaults` ↔ 访问器 ↔ 各 Provider 消费点之间没有机械对账，
   所以 M4（data.root 只进 env）与 M8（token 键名不同）都不会红。
6. 假阳性/假阴性风险：assembly 测试用 `metricsFaceApp`（嵌入 nil 接口）与全局 `capability.RegisterFactory`——
   后者会为同包其它测试留下注册项（`assembly-test-runtime-*`），目前无冲突，但属于全局状态污染面（L 级，未单列）。

## 7. 未决与置信度

**最不确定的 5 点**

1. **H1 的端到端现象**未在真机复验（禁止跑 install；本机无 docker）。四条代码链首尾相接、且全仓无其他 env 设置点，
   我判定"新装后 Route 不发布"成立；但"新装用户是否总会被 doctor 拦住"取决于用户是否跑 doctor。
2. **SQLite 库文件模式**（L5）无法在 Windows 判定；驱动无显式 chmod 是代码事实，"POSIX 下 0644"是经验推断 → 标待复核。
3. **M4 的触发概率**：全仓未找到 fleetlyd 的 YAML 全量配置示例（只有 15 行模板），若产品实际上只支持 env 形态，
   M4 应从 M 降为 L（"代码与注释的假设不一致"）。请 Lead 结合 R15 的文档面判断。
4. **M5 的实际超时频率**：引擎排空是否经常 >5s（构建 goroutine、baseline replay、provider 调用）无法测量；
   我按"风险 + 预算不变量缺失"计 M，而非"已观测故障"。
5. **M2 的 `install` 覆盖运行中二进制**行为（ETXTBSY vs unlink 重写）依赖 coreutils 版本，未能在 Linux 验证；
   但"不重启/不分代/无备份"三条与内核行为无关，结论不受影响。

**整体置信度：高**（H1、M1、M3、M4、M6、M7、M8、L1-L6 均有 `文件:行` + 原文/源码双证，且追到了框架与调用方；
M2/M5/L5 的"后果强度"为中置信，缺陷本身为高置信）。

**覆盖度自评**：R09 主审范围内 5.3k 行生产面全部读完（assembly 非 gateway 部分、config、cmd/fleetlyd、install.sh、
mise/CI/buf/golangci/go.work），另有 9 个 e2e 脚本与 4 个 providers 工厂的交叉抽样；未覆盖的是真机行为与 CI 历史运行记录。
