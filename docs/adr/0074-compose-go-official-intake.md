# ADR-0074: compose 受理面切换 compose-go 官方实现——官方 schema/external/插值机制 + x-fleetly 扩展

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-13 | compose-spec（11-extension：x- 是唯一静默忽略通道）、ADR-0033（x-fleetly-first-boot-jobs 先例）、ADR-0048（策略语义收编——扩展键只换拼写）、ADR-0072（spec 所有权——spec_file 面保留依据）、ADR-0007（词汇冻结——Compose Intake 词条入册）、架构 §4（归一化产物即 Revision 冻结体——定律不破） |

## 背景

compose 受理现状是 `internal/spec/normalize.go` 手写 map 白名单走查
（~550 行）：`yaml.Unmarshal` 进 `map[string]any` 后逐键断言。该形态已实证
三个静默洞（同族于 N0.1 批修复的"断言失败即跳过"）：

1. **端口截断**：`composePort` 用 `Sscanf("%d")` 只取前缀——`"8080:80"`
   静默解析成 8080，`"127.0.0.1:8080:80"` 变成 127；
2. **ports 长语法静默丢弃**：map 形态（`{target, published}`）类型断言
   失败，整个 ports 键消失（连带 Route-facing 挂网裁决）；
3. **networks 长语法静默丢弃**：map 形态（带 aliases）同款。

平台处于开发期（无向后兼容负担）。裁决原则（用户指令）：**尽量使用官方
机制**。生态论证（2026-10-13 调研）：compose-spec 官方实现 compose-go 是
Docker Compose CLI 同源解析器；其自我定位即"平台无关意图描述，实现各自
映射"——与平台"受理方言 → AppSpec IR"的既有架构同构。附带红利：官方
JSON Schema 编辑器生态、Renovate docker-compose manager 免费镜像升级
供应链、LLM 语料覆盖密度（agent 生成质量）、标准字段随上游演进。

## 决策

1. **解析器 = compose-go（官方实现）**。新包 `internal/composeintake`
   （import {compose-go, internal/spec}，被 api/apptemplate 消费；
   importguard 既有注释已豁免 compose-go——它是文件格式解析器非编排器
   SDK）。`spec` 叶子纯度不破（compose-go 不进叶子）。签名
   `FromYAML(body, appID, projectID, varLookup)`：官方 loader → 官方
   schema 校验 → 语义白名单执法 → x-fleetly 扩展 typed 映射 →
   `spec.ValidateApp` 收尾。**IR/proto/API 契约零变化**：归一化产物仍是
   AppSpec，冻结仍是 protojson Revision（架构 §4 定律不破）；存量
   Revision 兼容，无 state flag-day。
2. **官方 schema 校验执法**：非 x- 未知键由官方 schema 拒（x- 是唯一
   扩展通道，compose-spec 11-extension 原文）。已验证 compose-go 在
   Service/DeployConfig/HealthCheckConfig 均带 Extensions 字段保留
   嵌套 x- 键。
3. **扩展键拼写全落官方 x- 机制**（旧拼写废除，开发期无兼容）：
   - `deploy.x-fleetly-strategy`（rolling|blue-green；strategy 非
     compose-spec 标准键故必须走 x-）；
   - `healthcheck.x-fleetly-http-path` / `healthcheck.x-fleetly-tcp-port`
     （与标准 `test` 三选一互斥，跨两源执法；节律键用标准面）；
   - `x-fleetly-first-boot-jobs`（顶层，ADR-0033 形态不动）。
4. **external 声明律（官方机制）**：service 引用的 secrets/volumes 必须
   顶层显式 `external: true` 声明——官方语义"资源存在于 compose 之外"
   恰是平台实体（Secret/Volume 由平台管理）。名即平台实体名；**不做**
   自动补铸（自定义预处理让渡官方声明面）。first-boot-jobs 的 secrets
   引用同律。
5. **官方插值律**：compose-go 官方插值开启，插值环境 = Project
   SharedVariable ∪ AppVariable（App 胜出），经官方 LookupValue 扩展点
   注入；**未消解 `${VAR}` = 受理错误**（fail-closed）。语义变化注记：
   原"落字面值"口径废除——该口径下同键 SharedVariable 会被字面量
   `${...}` 屏蔽（分层 Shared < source env 使字面量胜出，坏设计）；
   变量在受理期消解后冻结进 Revision。dokploy 迁移路径不变（客户端
   先替换 env 面再投递，到达时已无孔）。
6. **语义白名单在 parsed types 上执法**（官方解析后、映射前；理由
   精确）：拒 build（git 批次）、ports 的 published/host 映射
   （**target-only**——长语法 `target: N` 收）、bind/匿名卷（卷必须是
   external 声明的名引用短语法 `name:/target[:ro|rw]`）、secrets 带
   `file:`/`environment:` 值（材料值永不进 compose，ADR-0014）、
   networks map 形态带 aliases（只收名单）、`deploy.restart_policy/
   update_config/rollback_config/placement/labels/mode`（受管字段，
   理由照旧）、`depends_on/profiles/include/env_file/configs/extends/
   container_name` 等未开放官方面（文案"official compose feature, not
   enabled on fleetly"）。白名单只增纪律延续。
7. **Revision 原文冻结**：迁移 `00031` 加 `revisions.source_text`
   （compose 源原文，其他源 NULL）；GetRevision 读面透出；console
   Spec tab 渲染用户亲笔文件（收 W-9 挂账——console 看不到部署声明的
   可读形态）。
8. **方言 schema 发布**：`docs/reference/compose-dialect.schema.json`
   （官方 schema + x-fleetly 扩展定义）；SchemaStore 社区提交为外部
   挂账。
9. **受理喉唯一**：`delivery.go normalizeDeploySource` 换
   `composeintake.FromYAML`（模板实例化回流 Deploy、dokploy、console、
   CLI `--compose-file` 自动全覆盖）；apptemplate 保持 map 操作 +
   yaml.Marshal 回流（不依赖 compose-go），仅 `dryRender` 预校验换
   新包。normalize.go compose 半边删除；ImageDeploy/UploadDeploy/
   ParseSpecFile 留 spec 包。

## 后果

- 手写走查三静默洞连根死掉（截断/跳过形态由官方解析消灭，published
  等不支持形态转为显式拒绝）。
- 拒绝文案与插值语义是用户可见行为变更：apitest/CLI golden 断言同批
  重写（golden 同 commit 铁律）；e2e/模板/文档/console/skills 方言
  迁移同批（grafana 模板补 external 声明）。
- compose-go 依赖入根 module（genproto/sdk 纯度不动）；版本钉 go.mod。
- 受理期消解变量意味着 SharedVariable 变更后 compose 应用需重新部署
  才进新值（原口径同样需要——字面量屏蔽下更糟；诚实边界入文档）。
- 官方面（profiles/include/env_file 等）开放走白名单只增，逐个按需
  评审，不在本批。
- spec_file（protojson）面保留（ADR-0072 所有权分裂不动）：git 源 +
  railpack/static builder 形态 compose 表达不了；console 编辑重发动线
  依赖它。

## 验收锚

- [x] compose-go 是唯一 compose 解析器（spec 包无 compose 手走查残留；
  normalize.go compose 半边删除，~500 行）
- [x] 官方 schema 执法：非 x- 自定义键被拒（deploy.strategy/http_path 旧
  拼法由官方 schema 拒，测试钉死）
- [x] 引用必经官方 external 声明（未声明拒；渲染管线声明铸位——grafana
  模板体零改动过 dryRender）
- [x] 官方插值：`${VAR}` 经平台变量消解、未消解即错（正/负例钉死；
  LookupValue=平台变量 + Substitute 包 fail-closed 预检）
- [x] ports published/host 形态显式拒；target-only 长语法收（截断/跳过
  两洞死掉；路由协议缺省 http、app_protocol 显式声明）
- [x] networks map+aliases 显式拒（名单收）
- [x] `deploy.x-fleetly-strategy` / `healthcheck.x-fleetly-http-path` /
  `x-fleetly-tcp-port` golden 双形态钉死（deploy-compose-strategy 族 +
  apitest REST 面三例）
- [x] Revision 原文冻结 + GetRevision 读面 + console 渲染（00031 迁移；
  apitest REST 锚 + Spec tab Compose Source 卡，收 W-9）
- [x] 双 verify 零漂移（generate:verify + console:verify）
- [x] e2e:bluegreen 全绿（新方言全链腿：零 5xx 切换/双代窗/L1 失败隔离/
  rollback/supersede/双节点 Placement/P16 双别名——x-fleetly-strategy +
  external 声明在真 swarm 端到端）；dind-smoke 段全绿至受管域自举
  （VM 外网拉取挂 = 当日本机网络劣化，railpack/restic 同轮下载失败
  为证）；两处 e2e 环境加固随批（宿主 Tailscale 搜索域泄进容器致裸名
  假红 → 尾点绝对查询）
- [ ] staging 上站（挂账：runbook 升级序换装；dogfood 仓 compose 迁新
  方言同批——torchwood/messageloop 的 stack 声明补 external 声明与新
  扩展键拼写）
