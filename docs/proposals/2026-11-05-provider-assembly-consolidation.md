# fleetly Provider 机制与装配面收敛提案（2026-11-05）

> 状态：**已裁决（2026-11-05 用户四项裁定）**：D=维持+登记重评触发器（§2.5）；C1-0=立即开工（C5/C6 同批）；**C1 主体批=先于 managed-data B1-B7 执行**（用户裁定——新引擎家族直接落在干净签名上，避免二次返工；§6 已重排）；C7=邻 C1 窗口。v2 稿含双独立子代理评审回填（A 侧证据核查 12 发现、B 侧方案风险 12 发现，裁决表见 §9）。
> 核心问题：**provider 机制（注册表 + FacesOf + 七端口）是否需要重造**。结论先行：**骨架维持（双侧对抗评审均未推翻）；唯一结构级收拢是配置载面五通道归一（C1，需 ADR-0036 修订）；守卫前置件批（C1-0）可立即落地；三处轻伤随主体批（C2-C4）；两件小刀任意班车（C5/C6）；组合根清单收敛因 ADR-0088 P5 已收官改为独立排批（C7）；注册表自身一个便宜守卫缺口收编为 C8。本提案不新增任何机制，只消灭平行机制。**
> 载荷澄清：本提案只覆盖「机制形态」裁决，不改变任何端口语义、API 契约或运行时行为；全部变更是装配期，升级零扰动是硬约束（staging 现役 53b2863a，ADR-0092 flag-day 后）。
> 关联：ADR-0003（编译期注册表）、ADR-0004（受管 reconciler）、ADR-0036（绑址可配置——C1 的修订对象）、ADR-0075（Clean 四环）、ADR-0080（UoW——C6 相关）、ADR-0087/0088（engine 拆分——**P4/P5 已于 2026-11-01 收官**，本提案时序据此修订）、docs/proposals/2026-11-04-managed-data-services.md（在途——经核**复用既有端口，不新增 capability 面**）、架构文档 §0（每段只许有一份）/§2/§3/§8。

## 0. 提案总表

| # | 提案 | 重量 | 定性 | 建议批次 | 裁决 |
|---|---|---|---|---|---|
| D | **骨架维持裁决**：注册表 + FacesOf + 七端口 + domain 归属不动（§2；B 侧对抗核验：并发时序/relay 进程形态/空名直取三线质疑全部排除） | 决策 | 维持现状 | — | **✅ 维持 + 登记重评触发器（§2.5）** |
| C1-0 | **守卫前置件批**：providers os.Getenv 站点级白名单守卫 + `"./data"` 字面量清零（DataRoot 经装配 ctx 临时通道下传，C1 退役）+ blank import ↔ providers 子目录双向对账（=C8） | 小 | 堵漏·先行 | 立即 | **✅ 立即开工（C5/C6 同批）** |
| C1 | **配置载面归一主体**：`Factory(ctx, FactoryParams)` 显式参数（七端口子结构）取代 ctx 载值与工厂 env 直读 + traefik 三键入 config（additive + 旧拼写兜底）+ env 读点收敛至 assembly/relay 两处清单 + ADR-0036 修订节 + installenv 改针 | 大 | 结构级·唯一必修 | — | **✅ 先于 managed-data B1-B7 执行（用户裁定）** |
| C2 | **泛型 `Build[T]`**：端口类型编译期钉死，装配断言样板删除（8 调用点含 relay） | 小 | 重构 | 搭 C1 | **✅ 搭 C1** |
| C3 | **Close 探测均匀化**：`closeProvider` helper 七处统一（现 2/7） | 小 | 缺陷堵漏 | 搭 C1 | **✅ 搭 C1** |
| C4 | **N 选一承诺诚实化**：报错文本按 kind 参数化（Runtime 有旋钮不得误称无）；旋钮 YAGNI 推迟 | 小 | 缺陷堵漏 | 搭 C1 | **✅ 搭 C1** |
| C5 | **FacesOf 幸存断言收编**：`componentrestart.go:44` 改走 FacesOf（B 侧核验：行为逐位等价零风险） | 微 | 风格统一 | 搭 C1-0 | **✅ 搭 C1-0** |
| C6 | **「唯一事务门面」守卫**：`.Tx(` 圈禁 + `transact.NewTx(` 限 assembly + 站点白名单 | 小 | 措辞/守卫 | 搭 C1-0 | **✅ 搭 C1-0** |
| C7 | **组合根五份 repo 清单收敛**（NewBinder/NewAPIDeps/anchorRepos/NewAuthnRepos/**NewSeedRepos**）：ADR-0088 P5 已收官，无冲突面，**独立排批**（不再移交/等待） | 中 | 独立批次 | C1 邻窗 | **✅ 邻 C1 窗口合并** |
| C8 | **blank import 对账守卫**：main.go import 集 ↔ providers/ 子目录树双向保鲜（漏挂 = 可选能力静默停用，今日无守卫） | 微 | 堵漏 | 搭 C1-0 | **✅ 搭 C1-0** |

## 1. 评估程序与证据基线（2026-11-05，四轮 + 评审复核）

方法：全量代码证据核（非抽样）——11 个 provider 工厂 init 体逐个读、FacesOf 全部 31 个非测试消费点清点（16 文件）、守卫五件（importguard/dep_matrix/uowguard/providerfaces/**installenv**）法条核对、`go list -deps` 依赖面实测、git 历史与 ADR 状态核查。关键事实（A 侧逐条复核判定：18 条中 12 条逐位属实、6 条计数/措辞出入已按下表修正；F19-F23 为评审后新增）：

| # | 事实 | 锚点 |
|---|---|---|
| F1 | 注册表 3 个消费文件、8 个调用点，跨两进程形态（daemon + relay 载体容器独立进程，`--runtime` 旗标恒非空缺省 swarm） | provides.go×6 + proxyconfig.go:126 + relay.go:64-71 |
| F2 | importguard 只放行 cmd/**（与 providers 内部互引——现仓零使用）import providers；providers 内部只准 capability/spec/model；AGENTS.md「cmd/assembly」措辞比守卫宽（assembly 实测零 provider import） | guards/importguard_test.go:97-129 |
| F3 | k3s 第二 Runtime 29 个 .go 文件落地、Provider 本体 commit（5882e3b3）engine 零改动 | providers/k3s/；git 核验 |
| F4 | capability 包纯标准库依赖，零 fleetly 外向、零第三方 | `go list -deps` 实测 |
| F5 | FacesOf 31 个非测试消费点全走单点（engine 17 + fleetlygrpc 4 + manageds 5 + logpipes 2 + intake 1 + assembly 4——此处 4 含 provides 3/proxyconfig 1，与 F1 计数口径不同：F1 数 Build，此处数 FacesOf）；幸存直断言仅 componentrestart.go:44 且语义正确（nil → ErrRestartUnsupported）；faces_test 三形态冻结 | 各文件；faces_test.go |
| F6 | 测试故事与注册表解耦：三层假件（engine fakeRuntime/apitest FakeRuntime/testruntime 共享核）；ResetFactories 全仓唯一调用点 capability_test.go | testsupport/testruntime/ |
| F7 | 配置进 provider 五通道：ctx 载值（7 对 With*/FromContext）／裸 env 唯一源（traefik 三键，proto 无对应键——ProxyConfig 仅 addr/auth_token）／约定 env（DOCKER_HOST×4、FLEETLY_DATA_ROOT×4）／硬编码 `"./data"`×4 与 config.DefaultDataRoot 平行复制／构造后 setter（NodeIDMinter） | ports.go:258-374,500；traefik/provider.go:212-219；zot:627 等；provides.go:494 |
| F8 | traefik 工厂混装：token 走 ctx、endpoint/acme_email/ca_server 三参数走 env | traefik/provider.go:212-219 |
| F9 | ADR-0036 验收锚钉死 ctx 注入形态（L35 勾选锚 + L29 后果节原文）——C1 需修订该 ADR | docs/adr/0036 |
| F10 | Factory 类型擦除：Build 返基接口 + 七处端口断言 + 手写报错 | provides.go:258 等 |
| F11 | Close 探测 2/7：仅 Runtime（263-267）/Builder（326-332）挂探测；余五处空 cleanup；providers 实测有 Close 的仅 swarm/k3s/builders×3（k3s 为 no-op） | provides.go；providers 各文件 |
| F12 | N 选一 1/7 兑现：仅 Runtime 有旋钮（RuntimeProvider()，缺省 "swarm" 恒非空）；四 kind 空名直取，多注册时报错「configuration must select one」对无旋钮 kind 指向不存在的键 | capability.go:97；config/defaults.go:272-278 |
| F13 | 装配注释自称「唯一事务门面」（provides.go:177）；直调 db.Tx 恰两处（janitor provides.go:834、admin rewrap.go:176）；uowguard 真实法条是执行器词汇圈禁，直调不红 | guards/uowguard_test.go |
| F14 | §0 循环法为包域（engine 内 ticker 只许 loop.go）；provider watch/SSE ticker 是不同工作，且 providers 结构上不能 import engine | engine/loop.go:8 |
| F15 | REST 是 proto 派生面（grpc-gateway 生成 + 挂载对账守卫 P1-13/A-9）；原生挂法 5 家族（webhook/uploads/SSE/exec/browse）各带理由且委托 engine 核——非平行实现 | gateway.go:102-120 |
| F16 | compose 解析单轨（composeintake 唯一；apptemplate 的 yaml 是模板清单另一格式） | composeintake/intake.go |
| F17 | SOLID 审计：D 最强、I 强、L 靠行为契约、O 被 k3s 实证；S 唯一真违反 = engine 包 24,031 行（**含测试**；非测试 11,494 行）——已由 ADR-0087/0088 立案且拆分主体已收官（F19） | §2.3 |
| F18 | 组合根五份手写 repo 清单（NewBinder:123-170/NewAPIDeps:614-682/anchorRepos:208-225/NewAuthnRepos:183-192/**NewSeedRepos:196-204**），加聚合触 2-5 份 | provides.go |
| F19 | **（评审新增）ADR-0088 P4/P5 已于 2026-11-01 收官**（八卫星拆包落地：intake/backups/logpipes/metrics/execs/manageds/databases/browses/diagnosis，九星对账 9/9）；在途仅 ADR-0087 P3a 规则回填残项，不动 assembly repo 清单面 | docs/adr/0088 头部 |
| F20 | **（评审新增）installenv 守卫双向钉死 traefik env 读点**：断言 traefik/provider.go 必须出现 `os.Getenv("FLEETLY_PROXY_CONFIG_ENDPOINT")`——C1 落地即红，必须同批改针 | guards/installenv_test.go:41-66 |
| F21 | **（评审新增）env 拼写矩阵**：lynx AutomaticEnv 把 config 键映射为 `FLEETLY_SERVER_PROXY_CONFIG_AUTH_TOKEN` 形，与旧通道键 `FLEETLY_PROXY_AUTH_TOKEN` 等**不同拼写**；install.sh 物化旧拼写且被 installenv 钉死——「逐位不变」必须为旧拼写保留手写兜底；DataRoot 无虞（同形映射） | internal/config/bind.go:17-24；install.sh |
| F22 | **（评审新增）managed-data 三家族复用既有 capability.Workload + Runtime.Ensure**，新模板在 domain/services 侧——不新增 capability 端口或工厂 | docs/design/2026-11-04-managed-data-services.md §L1 |
| F23 | **（评审新增）blank import 遗漏 = 可选能力静默停用**：四个可选 New* 在零在册时返 nil 无告警；忘挂 import 时 Registry/Logging 面静默消失，importguard 只执法方向不钉清单 | provides.go:340-412；proxyconfig.go:120-129 |

## 2. 骨架维持裁决（D）——不做什么

### 2.1 注册表 vs 显式 switch：维持注册表

四块证据（B 侧对抗核验通过：map 并发安全成立——11 处注册全在 init、Go init→main happens-before；relay 是独立进程不与 daemon 同进程；空名直取路径 relay 恒不触发）：

1. **relay 第三消费文件**（F1）：switch 放 assembly 撞 importguard（F2）；relay 直接 import providers 虽守卫合法，但把载体容器第二进程形态钉死在具体 Provider 上（换 Runtime 要改 relay），复制选择逻辑则漂移——注册表是唯一两全形态。
2. **守卫比 AGENTS 更严**（F2）：「switch in assembly」是要弱化 importguard 的方案，非中性替代。
3. **装配判定的单一数据源**：`RegisteredFactories()` 供 Builder 全家族枚举与四个可选能力的在册判定（assembly 5 处消费）——手写 manifest 是新漂移面（评审勘正：GetStatus 走已构造实例、doctor 与能力发现端点今日不消费该函数，此块证据以现役消费面为准）。
4. **assembly 保持零 provider import**：hermetic 测试面的地基。

### 2.2 FacesOf：维持单点协商

31 消费点单点收敛（F5）、faces_test 行为冻结、providerfaces 守卫钉死双 Provider 契约。加子面要改 FacesOf+Offered 是记录在案的刻意取舍。

### 2.3 SOLID 对照结论（不新增工作项）

D 教科书级；I 强；L 靠行为契约；O 被实证。S 唯一真违反是 engine 体量——**ADR-0087/0088 已立案且 P4/P5 已收官**（F19），本提案不重复立案。DIP 唯一褶皱即 F7，归 C1。

### 2.4 刻意双轨清单（带裁决记录，不动）

zot env 旧通道（C1 后收敛为 assembly 单点读）；wire+registry 分工（静态图 vs N 选一，缝干净）；读写分离（ADR-0085/0075 法律）；日志双径（检索 vs 实时，两问题）；双假件（同构核已收编）。嫌疑排除：REST 派生面（F15）、compose 单轨（F16）、包域循环法（F14）、Health vs doctor（两问题）。

### 2.5 重评触发器（2026-11-05 用户裁决：维持 + 登记）

「维持」不固化为不可挑战。下列任一触发即重开骨架评估（届时以本提案 §9 评审记录为基线增量再审）：

1. **第三 Runtime 立项**——三实现并存时注册表选择面/旋钮设计（C4 推迟项）强制兑现，形态重估；
2. **进程外插件需求真实出现**（ADR-0003 预留的演进线）——契约类型已全在 proto，抽进程外是机械翻译，但注册表/FacesOf 的进程内假设需重审；
3. **Registry/Logging 等可选 kind 出现第二实现**——空名直取形态终结，N 选一全面兑现。

## 3. C1 配置载面归一（结构级）

### 3.1 目标形态（评审 B-3 修订版）

**一个问题一条通道：配置值经显式参数结构进工厂；env 读点收敛为 assembly + relay 两处清单；provider 面零 `os.Getenv`（站点级白名单制）。**

```go
// ctx 留函数首参（Go 惯例，不进结构体——评审 B-3）；
// FactoryParams 按端口分七个子结构（窄面文化自洽——traefik 看不见 S3 凭证）：
type FactoryParams struct {
    Registry  RegistryParams  // Addr
    Logging   LoggingParams   // Addr, RetentionDays
    Metrics   MetricsParams   // Addr, RetentionDays
    ObjectStore ObjectStoreParams // S3 *ObjectStoreS3Config
    Proxy     ProxyParams     // AuthToken, ConfigEndpoint, AcmeEmail, AcmeCAServer
    Runtime   RuntimeParams   // DockerHost, K3sKubeconfig（moby client 无 FromEnv 不自读 DOCKER_HOST——显式化必要，评审 B-3 核验）
    Platform  PlatformParams  // DataRoot（灭 "./data"×4）
}
type Factory func(ctx context.Context, p FactoryParams) (Provider, error)
```

**装配期 env 读点终态清单**（旧拼写手写兜底，各带退役条件；新 config 键的 lynx 自动映射拼写不同形 F21，不可依赖）：

| 读点 | 键 | 存在理由 | 退役条件 |
|---|---|---|---|
| assembly | FLEETLY_PROXY_AUTH_TOKEN / _CONFIG_ENDPOINT / _ACME_EMAIL / _ACME_CA_SERVER | install.sh 物化旧拼写（installenv 钉死） | install.sh 改物化 config 文件 + 一个大版本 |
| assembly | FLEETLY_REGISTRY_ADDR | ADR-0036 env 旧通道 | 同上 |
| assembly + relay | DOCKER_HOST | docker 生态约定键 | 不退役（本义使用） |
| assembly | FLEETLY_RUNTIME_K3S_KUBECONFIG | lynx env 绑定同键（config 值优先语义） | 不退役 |
| relay | DOCKER_HOST（同上） | relay 进程独立读 env 填 params | 不退役 |

NodeIDMinter setter 不归入本批：是能力注入面非配置（ADR-0070 平台铸造权语义），保留并记档。

### 3.2 traefik 三键入 config

`server.proxy_config.acme_email` / `acme_ca_server` / `config_endpoint` 三键 additive 入 config.proto（现役消息为 `ProxyConfig{addr, auth_token}`；评审勘正：无 `edge_config` 键——那是 ADR-0036 行文旧名）。**不做同源派生**（评审 B-9：endpoint 是按广播地址派生的完整 URL，监听键常为通配，config 无 advertise host 信息——派生是伪选项）；旧拼写兜底按 §3.1 清单。**installenv 守卫同批改针**：readerFile 断言重指 assembly 新读点，install.sh 侧物化断言不动（F20）。

### 3.3 新守卫（C1-0 先行版 + C1 终态）

- **扫描口径**：providers/** 生产 .go（跳过 _test，uowguard walkRepoGoFiles 同款）零 `os.Getenv(`；**站点级白名单**（键→文件→理由，wording 机制原样）——新文件抄旧键同样红（评审 B-7：按键白名单挡不住「照抄 FLEETLY_DATA_ROOT+`./data`」这一最可能的生长向量）。
- **过渡白名单（C1-0 落地时枚举 9 站点）**：DOCKER_HOST×4（builders×3/swarm——生态约定，终态保留但读点上移后从 providers 白名单退役）、FLEETLY_DATA_ROOT×4、FLEETLY_REGISTRY_ADDR、FLEETLY_RUNTIME_K3S_KUBECONFIG、FLEETLY_PROXY_AUTH_TOKEN/_CONFIG_ENDPOINT/_ACME_EMAIL/_ACME_CA_SERVER；**C1 落地后收敛为终态白名单**（预计仅 FLEETLY_RAILPACK_BIN 钉版路径覆盖一条，若它在生产文件）。
- **`"./data"` 字面量清零**随 C1-0（DataRoot 参数化不需要等签名批——各工厂加一个参数即可，先于 FactoryParams 整批）。
- C8 对账守卫（§0）：main.go blank import 集 ↔ providers/ 子目录树双向保鲜（F23）。

### 3.4 ADR-0036 修订节

以追加「载体收拢」节承载（先例：N2 兑现节），不占新号：验收锚「显式值经装配 ctx 注入」→「显式值经 FactoryParams 注入」；后果节「zot 工厂签名不变（ctx 注入）」勘正；relay 进程的 env 读清单记档（§3.1）。

### 3.5 变更面与风险

触碰：capability（签名+助手段落）、11 工厂 init、assembly 七处 New*、relay.go、config.proto 三键、ADR-0036、installenv 改针、新守卫两件。不触碰：端口语义、engine 内部（construct.go 消费端口接口非工厂——评审 B-4 核验，零交集）、API 服务面、install.sh。golden 影响经核：doctor 无 RegisteredFactories 输出面（A 侧核验），CLI golden 零重生成；`mise run generate:all` 随批。

## 4. C2-C6 小刀集

| # | 现状锚点 | 修法 | 验收锚 |
|---|---|---|---|
| C2 | F10 | `func Build[T Provider](ctx context.Context, p FactoryParams, k Kind, name string) (T, error)`——断言收进库内，调用侧编译期钉型；8 调用点含 relay 同批 | 断言样板删除；失败报错仍列在册候选 |
| C3 | F11 | `closeProvider` helper 七处统一挂 wire cleanup | helper 单测；七处全走 |
| C4 | F12 | 报错文本**按 kind 参数化**：「kind %s 无选择旋钮，同期只许编译进一个（registered: …）」；Runtime 臂注明旋钮名 `runtime.provider`（评审 B-11：Runtime 有旋钮不得误称无） | 单测钉双臂文本 |
| C5 | F5 | componentrestart.go:44 改 FacesOf（B 侧核验零风险） | 行为零变化 |
| C6 | F13 | 守卫：`.Tx(` 圈禁 transact/persistence + 站点白名单（rewrap/sweepUploads 带理由）；**加** `transact.NewTx(` 限 internal/assembly（评审 B-8：门面旁路抓不到则守卫虚设）；完整读表达式锚防注释误报（installenv 同款） | 红绿用例各一 |

## 5. C7 组合根清单收敛（独立排批）

五份手写清单（F18）收敛为单源构造 + 子集投影（NewReadRepos 形态）。**v1 版「移交 ADR-0088 P5」前提已失效**——P4/P5 已收官（F19），卫星形态已定，本项无冲突面、无等待对象，独立排批即可；与 C1 建议先后相邻（同触 provides.go，合并窗口可省一次评审）。

**已落地（2026-11-05，commit 11501c05，子代理实施）**：newStoreSet 唯一构造源（36 repo + NextSeq + anchor 投影造 Anchor 服务，anchorRepos 删除）；五消费面全投影（NewBinder/NewAnchor/NewAuthnRepos/NewSeedRepos/NewAPIDeps，wire 签名零变化 wire_gen 零漂移）；顺带收编 proxyconfig 一处 teamrepo 直构；安全网 = 反射完整性测试（38/38 字段非 nil，豁免清单空）+ 构造点守卫（assembly 内 *repo.New 只许 provides.go，双向保鲜）。加聚合触点从 2-5 份清单降为单源+投影。

## 6. 批次划分与时序（2026-11-05 用户裁定版）

```
C1-0 守卫前置件批（立即开工；C5/C6 同批）
  └─→ C1 主体批（FactoryParams + traefik 三键 + installenv 改针 + C2/C3/C4 搭车）
        └─→ C7（相邻窗口合并省评审）
              └─→ managed-data B1-B7（新引擎家族落在干净签名上，避免二次返工）
```

时序依据（用户裁定 + 评审 B-4 事实核）：engine 拆分主体已收官（F19）、managed-data 复用既有端口（F22）——各批间零函数冲突；C1 提前到 managed-data 之前的收益是**新引擎家族（pubtemplate/buckettemplate 及其 provider 面）直接落在 FactoryParams 干净签名上**，五通道债不再被在途程序放大。

## 7. 验收锚

C1-0 批（2026-11-05 已落地，工作树待提交）：
- [x] providers 生产代码 os.Getenv 命中 = 过渡白名单 **11 站点**（站点级，条目带理由与退役批次；新文件用旧键即红）——实存清单：DOCKER_HOST×4、FLEETLY_RAILPACK_BIN×1（终态保留候选）、FLEETLY_RUNTIME_K3S_KUBECONFIG×1、FLEETLY_REGISTRY_ADDR×1、traefik 旧拼写×4
- [x] `"./data"` 字面量与 FLEETLY_DATA_ROOT 直读在 providers 清零（DataRoot 经装配 ctx 临时通道下传，C1 主体批随 FactoryParams 退役；未注入 = 精确失败不回退平行缺省；zot 工厂三态测试平移 + fail-closed 态钉测）
- [x] main.go blank import 集 ↔ providers/ 子目录对账守卫（C8，双向）
- [x] C5 componentrestart 断言收编 FacesOf；C6 事务门面守卫（AST 执法：`.Tx(` 圈禁 persistence/transact + 白名单两站点、`transact.NewTx(` 限 assembly/testsupport）
- [x] 门禁：mise run test 三 module -race 全绿；lint 仅存量红（apitest gosec×5 在案，与本批零文件重叠；本批包面 golangci 0 issues）；lint:proto 绿

C1 主体批（2026-11-05 已落地，工作树待提交；子代理三波：config 半件/ADR 半件/签名链主体）：
- [x] FactoryParams 七子结构落地；ctx 留函数首参；providers 生产 os.Getenv 终态白名单 **1 条**（FLEETLY_RAILPACK_BIN）；8 对 With*/FromContext 全删（含 C1-0 临时 WithDataRoot）
- [x] traefik 三键 config additive（ProxyConfig 字段 3/4/5）+ 访问器三态测试 + 装配侧**四键**×三态兜底矩阵测试（config 显式胜/仅旧拼写 env/双空——TestProxyProviderLegacyEnvFallbackMatrix）；拼写矩阵实证：同形五键 config 层吸收零手读、proxy 四键手写兜底（bind.go 核验）
- [x] 配置优先级逐位不变：config > 旧拼写 env > 缺省（zot 工厂测试改参数形态；同形键解析序上移 config 层由既有 env 测试背书）
- [x] installenv readerFile 重指 internal/assembly/proxyconfig.go；install.sh 物化断言不动
- [x] `mise run test` 三 module -race 全绿；`lint:proto` 绿；golangci 触碰面包+cmd **0 issues**（全仓 lint 仅存量红 apitest gosec×5，零重叠）；`generate:verify` 为提交门禁（未提交工作树预期红，wire_gen 零漂移、config.pb.go 仅三键增量）；**挂账=staging 换装窗验证零扰动 + relay exec_script golden 不变（只钉 argv，agent 批内已跑 cmd 测试绿）**
- [x] C2（Build[T] 断言入库，8 调用点样板删）/C3（closeProvider 七处统一）/C4（报错按 kind 双臂参数化+测试）搭车落地；ADR-0036 修订节入册（含 env 终态表同形吸收口径修正）

## 8. 风险与退路

- **时序空窗已用 C1-0 封堵**：站点级白名单 + `"./data"` 清零先落地，最可能的生长向量（照抄旧键模板）即红；C1 主体晚到只延迟签名收拢，不延迟执法。
- **最大残余风险：拼写矩阵遗漏**。F21 的旧拼写兜底若漏一键，升级即行为变化——三态测试把每个旧拼写键钉进用例是硬锚。
- 退路：FactoryParams 若与后续程序冲突，可降级为「traefik 三键 + DataRoot 参数化 + 守卫终态」半批（C1-0 已含其大半），ctx 载值保留记账。

## 9. 评审记录与裁决表（双独立子代理，2026-11-05）

评审 A（证据核查侧，agent_9f4023d6）：12 发现——P0×1、P1×3、P2×8；F1-F18 判定：属实 12 / 部分属实 6 / 有误 0；总评「事实基线可信度高，动机性事实全部铁证」。
评审 B（方案风险侧，agent_9c6c0f7d）：12 发现——P1×4、P2×8；骨架维持裁决对抗核验通过（并发/进程形态/空名三线排除）；总评「方向正确，4 条 P1 修订后交付裁决」。

| 发现 | 内容 | 裁决 | 落点 |
|---|---|---|---|
| A-P0 | ADR-0088 P4/P5 已收官，C7 移交与 §6 时序前提过时 | **接受**（与 B-12 的「须等 P5」冲突，以 ADR 头部实文为准裁 A） | F19/§5/§6 重写 |
| A-P1a | `server.edge_config.addr` 键不存在，实为 proxy_config | 接受 | F7/§3.2 勘正 |
| A-P1b | §2.1 relay 撞 importguard 论证与 F2 自相矛盾（relay 在 cmd 下合法） | 接受 | §2.1 论据 1 重写 |
| A-P1c | FacesOf 30→31 | 接受 | F5/§1/§2.2 |
| A-P2×5 | k3s 29 文件；端点 5 家族；清单 5 份；providers 自引放行；行数口径含测试 | 全接受 | F3/F15/F18/F2/F17 |
| A-P2 | RegisteredFactories 消费面夸大（doctor/能力发现不消费） | 接受（与 B-5 同源） | §2.1 块 3/§3.5 去水 |
| B-P1a | installenv 守卫钉死 traefik env 读点，C1 落地即红——硬漏项 | **接受** | C1 触碰面 + §3.2 + 验收锚 |
| B-P1b | 新守卫口径自相矛盾（TEST 键在测试文件、生产扫描永不命中）+ 过渡白名单未枚举 | 接受 | §3.3 重写：生产面扫描/站点级/9 站点枚举 |
| B-P1c | FactoryParams.Ctx 入结构是回头路；平铺 god-struct 违窄面文化；relay 第二读点 | 接受 | §3.1 重写（ctx 首参/七子结构/两读点清单） |
| B-P1d | §6 两条技术理由与代码事实不符（P5 已收官/managed-data 不动端口） | 接受 | §6 重写：队列纪律表述 + C1-0 前置 |
| B-P2 | blank import 遗漏静默停用无守卫 | **接受，立项 C8** | §0/§3.3 |
| B-P2 | 按键白名单挡不住照抄旧键；`"./data"` 清零可先行 | 接受 | 站点级白名单 + C1-0 |
| B-P2 | C6 漏 `transact.NewTx(` 旁路；需完整表达式锚防注释误报 | 接受 | §4 C6 |
| B-P2 | 同源派生是伪选项（无 advertise host） | 接受 | §3.2 删选项 |
| B-P2 | env 拼写矩阵未成文（新旧不同形）；三态测试须含旧拼写态 | 接受 | F21/§3.1 表/§7 锚 |
| B-P2 | C4 文案对 Runtime 不成立 | 接受 | §4 C4 按 kind 参数化 |
| B-P2 | 验收锚漏三处 | 接受 | §7 补齐 |
| B-无争议确认 | C5 零风险/C3 现状核实/golangci/coverageguard/wording 无交互冲突；DockerHost 显式化必要性（moby 无 FromEnv） | 采纳为依据 | §3.1/§4 |
