# ADR-0084：grpcapi 升 v0.4.0（policy.proto 声明面 + 自持 guard 执行面）

- 状态：已接受（2026-10-26）
- 关联：ADR-0021（技术栈基线）、ADR-0035（行级授权）、ADR-0038（Token 实时收窄）、ADR-0039/0081（引用）

## 背景

fleetly 自 F0.5 起消费 lynx-go/grpcapi（v0.1.0 时代形态）：proto 侧
`grpcapi/v1/authz.proto` 注解（`service_auth`/`method_auth` 五档访问模型），
Go 侧 `authz.Build` 收集 + 自持 authn 拦截器执法。上游经 v2→v3 重写
（v0.2.0~v0.4.0）后旧 `authz` 包与 `authz.proto` 已删除；torchwood 已完成
迁移（`policy.Build + protopolicy` 收集、库 authz Decider 执行）。本批把
fleetly 升到 v0.4.0（openapi 包 + CORS/bridge 等能力入仓可用），proto 依赖
按用户裁定继续走 BSR `buf.build/lynx-go/grpcapi`（不 vendored，与 torchwood
的 vendor 形态分立）。

版本组合：`github.com/lynx-go/grpcapi v0.4.0` + `github.com/lynx-go/grpcapi/
genproto v0.3.0`（v0.4.0 无 proto 变更，上游未重打 genproto tag）。

## 决策

1. **声明面整体迁 `policy.proto`（路径 B codemod）**。fleetly 全量注解
   仅两形态，机械映射：

   | 旧（authz.proto） | 新（policy.proto） |
   |---|---|
   | `service_auth { default_access: PUBLIC }` | `service { default_policy: { anonymous: true } default_exposure: EXPOSURE_PUBLIC }` |
   | `service_auth { default_access: SERVER }` | `service { default_policy: { authenticated: true } }`（绊线，见决策 3） |
   | `method_auth { access: PUBLIC }` | `method { policy: { anonymous: true } exposure: … label: "PUBLIC" }` |
   | `method_auth { access: SERVER, api_key_scope { res, op } }` | `method { policy: { require: [{ kind: "scope" values: ["res:op"] }] } exposure: … label: "SERVER" }` |

   exposure 逐方法按「有无 `google.api.http` 注解」派生——与旧库 hasHTTP
   推导等价；两个 client-streaming 方法（UploadSource/StreamExecSession）均
   无 http 注解 → INTERNAL，不触发 v3「client/bidi 流不得 PUBLIC」Build 禁令。

2. **收集面换 `policy.Build + protopolicy`**（torchwood 同款）：Registry 注册
   库内置 kind + `scope.Kind`（Claim="scopes"、词表域 "scope"、
   RequireVocabulary），Vocabulary 单源仍是 `assembly.ScopeResources()`
   （CONTEXT.md Scope 词条，资源声明出词表 = Build 失败，fail-closed 强度
   不变）。proto 无策略/无 exposure = Build 违例，比旧 service_auth 缺省
   继承更强（新方法漏注解 = 启动红）。

3. **执行面保留 fleetly 自持 guard（不采库 authz Decider），经 View 消费
   规范树**。`authn.ViewRules` 是全仓唯一策略投影：把
   `policy: {anonymous}` / `policy: {require[scope(res:op)]]}` 两种规范树
   识别回执法视图（`MethodRule{Public | Scope}`），非规范树形即构造失败
   （fail-closed：服务级 `authenticated` 绊线解析出的树形会被此拒——新
   方法缺方法注解 = 启动红，旧「SERVER 无 scope 走运行期 E_INTERNAL」的
   静默面消灭）。不采 Decider 的理由：fleetly 的拒绝契约（apperr 信封 +
   token 名/required_scope/建议文案 + PUBLIC 面无效凭证降级匿名）与身份
   解析（收窄/时效/审计节流）熔接在 guard，换 Decider 会改写 CLI/REST/
   golden 三面用户可见错误体；单 principal kind（platform token）也无
   torchwood 五档主体树的需求。采纳与否留待独立批次裁决。

4. **scope 语言统一 colon 单形态**。v3 库以 `resource:op`（colon）为规范
   （dot 仅兼容）；identity 域的 `DotForm` 转译退役，`ScopeSet` 直出 colon
   token、蕴含物化保留（库 `Satisfies` 是精确匹配，admin⇒write⇒read 仍须
   展开物化——语义不变）。`authz.ScopeOp` 枚举退役，方向常量落
   `identity.OpRead/OpWrite/OpAdmin`（string）。

5. **拦截器链换 Stage 形态**（`ChainItem{Slot}` → `Stage{Name, Order}`；
   clientinfo/authn/validate 三段链序不变）；gateway 错误出口换
   `ErrorEncoder` 协议（`MuxOptions.ErrorHandler` 字段已死）：信封/errcode
   →HTTP 映射/Retry-After/error_id 头语义逐项保留，原生挂法（webhook/
   SSE/uploads）经库 `gateway.WriteError` 复用同一编码器；REST 错误体字节
   形态不变（protojson snake_case + 尾换行）。

## 验收锚

- [x] proto 全量 codemod 零残留（`method_auth`/`service_auth`/
      `ACCESS_LEVEL`/`SCOPE_OP` 全仓无命中）；buf lint 绿、buf breaking
      （对 origin/main）绿——注解属 option 变更，非契约破坏。
- [x] `generate:all` 再生成：genproto 仅 14 个 .pb.go 原始描述符字节变
      化；`*.swagger.json` 字节不变（openapiv2 不读 grpcapi 扩展）→
      console:gen 零漂移、console 产物零变化。
- [x] ViewRules 规范树形构造期校验：非两形态树（含服务级 authenticated
      绊线）= 启动失败（apitest 夹具同链验证）。
- [x] scopeguard/writeclassguard 消费 ViewRules 面（PUBLIC/SERVER+scope
      语义等价，豁免表全量续命中）；policy_test 投影断言迁移。
- [x] guard 拒绝路径错误体逐字节不变（lacks required scope 文案、
      E_UNAUTHENTICATED 登录提示、PUBLIC 面脏凭证降级）——apitest 既有
      断言全绿即证。
- [x] buf.lock 钉 BSR `buf.build/lynx-go/grpcapi` 最新 commit（368cbc01，
      policy.proto 面）。

## 后果

- 上游 v0.4.0 新能力（openapi 包/server 免费挂载、CORS、bridge、
  ratelimit/audit/usage 参考实现、自有 runner 集成）在本批仅「可用未采」
  ——各面引入另立批次。
- grpcapi facade 冻结承诺（v0.3.0 起消费者已采核心面 additive-only）使
  后续升级低成本；v1.0 的执行侧迁移硬验收（torchwood 面板）不约束
  fleetly（fleetly 无 legacy 双投影）。
- genproto 版本（v0.3.0）落后主库（v0.4.0）是上游发版节奏事实，升级
  genproto 需等其重打 tag。
