# ADR-0050: App Template 模板库——目录文档、服务端实例化、目录刷新与迁移钩子

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-06 | F3.3（checklist）、CONTEXT.md Template 词条、ADR-0007（词汇冻结）、ADR-0014（Secret 材料分发）、ADR-0016（部署受理）、ADR-0021（钉版口径）、ADR-0024（受理位）、ADR-0026（API 惯例）、ADR-0029（dbtemplate 与 database: 命名先例）、ADR-0033（compose 扩展键先例）、ADR-0035（行级授权）、ADR-0043（变量合成咽喉）、ADR-0044（Console 最小面）、ADR-0045（digest 纪律与冷启动诚实边界） |

## 背景

F3.3 要求模板库四面：模板 schema（语义变量/自动生成密码/域名）、一键部署、CDN 热更新、竞品迁移钩子（`create-from-dokploy` 形态）。现状事实：

- **Deploy 已有四源**（image 直投 / compose 受控子集 / upload / spec_file，F3.5）互斥单入口；
  freezeRevision 是共享变量合成与内容寻址复用的唯一咽喉——任何新部署形态都应走到这条
  喉，不另起部署轨。
- **quickstart 是客户端编排先例**（CLI 与 Console 各一份同序幂等链，零模板）；compose
  intake 有受控子集白名单与 `x-fleetly-first-boot-jobs` 扩展键先例（ADR-0033）；
  dbtemplate 是"Database 模板"= 引擎钉版知识（ADR-0029/0045），与本批"App 模板"是
  两个实体。
- **词汇面**：Spec 词条 _Avoid_ 含 template（作 Spec 同义词禁）；wording 守卫
  skippedTokens 现理由只覆盖 dbtemplate——Template 升格为在册实体需词条与守卫理由
  同步（ADR-0007 双向保鲜）。
- **dokploy 事实**：无官方全量导出格式（GitHub issue #3420 是导出格式的特性请求）；
  其 API 数据模型是分立资源（applications / compose / domains / databases）——迁移
  钩子的输入契约必须钉在可核对的数据模型字段面上，诚实边界显式。

## 决策

1. **Template = 平台全局内容寻址目录文档，非资源行**：
   - 模板无 per-Team/per-Project 行、无状态机、不可单条写。目录两源——**内嵌目录**
     （`internal/apptemplate` 内 go:embed YAML，冷启动/离线即全部——ADR-0045"随二进制"
     纪律同款；console dist 的 embed 载体先例）+ **刷新快照**（state 单行，
     RefreshTemplates 成功后原子覆盖）。解析序 = 刷新快照在场优先，内嵌目录兜底。
   - 词条入册 CONTEXT.md：**Template**（一键部署蓝图：变量声明 + compose 受控子集 +
     平台扩展键；实例化产出 App 部署与伴生 Secret/Database/Route）。与 Database 模板
     （dbtemplate，引擎钉版知识）、Spec（规范化 IR）三面分立。_Avoid_: blueprint,
     recipe。
   - 目录条目 = name（目录键，`[a-z][a-z0-9-]*`）+ version（模板自身 semver，回显/
     审计面）+ digest（模板体 sha256，刷新核对与回显锚）+ body（模板 YAML 全文）。
     同 name 单版本（v1 目录无多版本并存；版本演进 = 刷新整目录替换）。

2. **模板文档形态 = compose 超集 + 两个平台扩展键（单文件）**：
   - `x-fleetly-template`（name/version/description/variables/routes）+
     `x-fleetly-databases`（`[{name, engine}]`，ADR-0033 扩展键先例）+ compose 受控
     子集（services/volumes/x-fleetly-first-boot-jobs 白名单不变）。
   - **语义变量三型**（variables[].type，声明面 = name/type/description/default/
     required）：
     - `string`：文本值；`${name}` 插值进 compose 文本（environment 值等）。
     - `secret`：**自动生成密码**——实例化时平台铸造随机值（hex 48，dbtemplate 同
       强度）落 Project Secret `template:<app>:<var>`（ADR-0029 `database:<name>`
       命名先例）；服务 `secrets:` 列表中等于变量名的条目重写为该真名（进
       secret_refs → 文件注入）；compose 文本中 `${name}` 插值为**文件路径**
       `/run/secrets/template:<app>:<var>`——secret 值永不进渲染产物/Spec/env
       （ADR-0014 红线，模板体与手写部署同一条）。已有同名 Secret（前次实例化/用户
       预置）→ 复用不重铸（**重试不旋转密码**）。
     - `domain`：**域名**——值 = host（用户供；CLI 可 sslip 缺省拼，服务端无缺省、
       required 语义由声明承载）；与 `x-fleetly-template.routes`
       （`{var, process, port, protocol?}`，protocol 缺省 http）联动，实例化创建/复用
       Route（同 host 复用，quickstart 同款）。
   - **渲染链 fail-closed**（`internal/apptemplate` 叶子纪律，dbtemplate 同款）：
     解析拒绝扩展键内未知字段；变量声明校验（未知类型/重名/secret 带 default 拒）；
     values 校验（未知键/缺必填/secret 型携带值即拒）；插值后残余 `${` 拒（未消费
     变量即红）；渲染产物剥离两个平台扩展键后**经 NormalizeCompose 白名单喉**（与
     手写 compose 同一条执法——漏剥的平台键被白名单自然拒绝）；routes 引用的
     process/port 必须在 compose 服务声明面在场。
   - **诚实边界**：Database 网内 DNS 名（`db-<id>`）创建时才知道——v1 不设隐式变量；
     连库 App 从 `database:<name>` secret 文件读完整连接 URL 自行解析（12-factor
     文件惯例）。需要 host 直配的模板形态（adminer 类）显式挂账，首个真实模板需求
     出现时再裁。镜像钉版是模板作者的责任（tag 粒度）；目录刷新预校验不解析镜像
     digest（registry 核对超射程——ADR-0045 决策 3 同款否决理由）。

3. **一键部署 = 服务端 InstantiateTemplate 动词（非 Deploy 第五源）**：
   - 新 proto 文件 `proto/fleetly/delivery/v1/templates.proto`（package
     fleetly.delivery.v1；exec.proto 同上下文第二文件先例）；TemplatesService 四
     RPC：ListTemplates/GetTemplate（读面，平台全局目录无行级归属，scope
     `templates` read）、InstantiateTemplate（scope `templates` write + 行级 project
     授权，ADR-0035）、RefreshTemplates（scope `platform` write——改写全平台目录
     内容是集群面权力，C3 同口径）。
   - **InstantiateTemplate(project_id, template, app_name, values,
     idempotency_key?)** 服务端编排（api 层跨聚合组合，projects delete 级联同款
     定位）：
     1. 解析模板（快照 > 内嵌）+ values 校验（决策 2）；
     2. App create-or-reuse（按名，quickstart 同款）；
     3. 模板声明 databases 时确保项目 default 网在场（CreateDatabase 受理位要求
        活跃网络）；
     4. secret 变量铸造/复用（Upsert；重试不旋转）；
     5. databases create-or-reuse（按名，凭证单真源链复用——ADR-0029 决策 6 原样）；
     6. 渲染 → NormalizeCompose → **既有 Deploy 链内部复用**（normalize/
        freezeRevision/Engine.Submit 因子化为包内共享函数——admission/审计/幂等/
        事件/共享变量合成全复用，零新部署轨）；
     7. routes create-or-reuse（同 host 复用）。
   - 响应逐资源报 created|reused（kind/id/name/reused）；部署以 Deployment 引用
     返回，等待是调用方的事（CLI wait 流 / Console DeploymentDetail 既有面）。
   - **部分失败诚实语义**：顺序步骤各自幂等，失败即停带精确错误（已创建面在响应/
     错误文案可见）；重跑按 create-or-reuse 收敛——projects delete 级联同款口径，
     无回滚事务（跨聚合半事务态无诚实承载）。
   - 事件 `template.instantiated`（aggregate=app；payload 模板 name@version +
     deployment id）；审计 `template.instantiate`（resource=app/<id>）。部署自身的
     deployment.* 事件族随 Submit 自然发射。
   - **quickstart 不收编**：零模板引导路径（样例镜像直投）与模板目录是两个面，
     并存；Console Quickstart 页维持。
   - errcode +2：`E_TEMPLATE_INVALID`（模板文档解析/渲染/校验失败——远端目录坏
     条目或模板缺陷；建议报模板 name@version）；`E_CATALOG_UNAVAILABLE`（刷新面
     失败——拉取/digest 不符/预校验拒；建议核对 catalog URL 与清单）。实例化面的
     values 错误复用 E_INVALID_ARGUMENT（精确字段文案，列合法值域）。

4. **目录热更新 = 显式刷新动词，非后台轮询（"CDN"的 fleetly 形态）**：
   - config 新增 `server.templates_catalog_url`（空 = 刷新停用，内嵌目录即全部；
     env 形态 `FLEETLY_SERVER_TEMPLATES_CATALOG_URL`）。
   - **RefreshTemplates**：GET `<url>/catalog.json`（清单：version + templates[]，
     每条 name/version/digest/body）→ 逐条 digest 核对（sha256(body)）→ **全量
     预校验**（每条解析 + 变量声明 + routes 声明 + compose 白名单 dry 校验，占位
     AppRef 形态）→ 全部通过才落快照行（fail-closed：坏目录不换好目录；任一条目
     坏 = 整次刷新拒绝）。响应报新旧 digest 与条目数。
   - 解析序与持久面：快照行（state 新聚合 catalog：单行 id=1，digest+body+
     fetched_at，upsert）在场优先；无快照 = 内嵌目录。刷新不清已部署引用
     （Revision 冻结体与模板目录解耦——部署基线永远以 Revision 为准）。
   - **诚实边界**（ADR-0045 回声）：刷新是操作员显式动作，无自动 re-fetch/无定时器
     （ADR-0018 禁进程内计时器同口径）；digest 在拉取时核对，远程事后删除/篡改不
     防——快照在手即继续可用；fleetly 不运营自有 CDN——"CDN 热更新"落地为
     **可配置 base URL + 内容寻址清单 + 显式刷新动词**，首版默认关闭（内嵌目录即
     开箱全部）。

5. **竞品迁移钩子 = 客户端解析转计划（CLI 动词 `create-from-dokploy`），非向导、
   零新 proto**：
   - `fleetly create-from-dokploy --file <export.json> --project-id <p> [--dry-run]`：
     解析器 = `internal/spec/dokploy.go` 叶子（纯 JSON→计划，零 IO）；计划 = 逐 App
     的 {name、部署形态（image 直投/compose）、env、routes、databases} + skip 清单
     （逐条带原因）。apply 走既有 API（project/app/network/database/deploy/route
     与 quickstart 同族动词）；`--dry-run` 只报计划零调用。
   - 输入契约钉 dokploy API 数据模型字段（docs.dokploy.com/docs/api 锚）：
     `applications[]`（name/buildType/dockerImage/env）、`compose[]`（name/
     composeContent）、`domains[]`（host/port/applicationId/composeId/serviceName）、
     `databases[]`（name/type）。映射：dockerImage→image 直投；composeContent→
     compose 受控子集（白名单外键诚实拒）；domains→Route（serviceName→process）；
     databases→引擎映射（postgres/mysql/mongo/redis 同名直映）。
   - **诚实边界**：dokploy 无官方导出格式——接受手组/截取自其 API 的 JSON；不可
     映射项（git 源构建 app、mounts、redirects、webhooks、cron schedules、mariadb
     等未在册引擎）逐条 skip 进报告**不静默丢**；迁移不搬数据（Volume/数据库内容
     仍在原地——数据面迁移走 Backup/Restore 面）。
   - 形态裁决：**解析转计划 + 报告**（自动、Agent 友好、可 dry-run），否决向导式
     半自动（Console 问答形态复用价值低，且 Console 写面已有部署表单）。

6. **Console：模板目录页 + 实例化向导（ADR-0044 最小面）**：
   - Templates 页：目录列表（name/version/description）→ 详情（变量声明表 +
     compose 体只读）→ 实例化表单（project/app_name/values——secret 型变量显示
     platform-generated、domain 型 host 输入）→ 提交后跳 DeploymentDetail wait
     流既有面。delivery 消费类型经既有 console:gen 通道（同 package 的 swagger）。

7. **守卫与执法**：
   - wording 守卫：skippedTokens["template"] 理由更新为三面（App Template 在册 /
     dbtemplate 契约语汇 / Spec 同义词语境人工评审）；新 _Avoid_ 词条 blueprint/
     recipe 进 bannedPatterns（机械无歧义，预期扫面零命中）。
   - 无新守卫包：渲染链纪律（secret 值不出渲染产物/平台键必剥/白名单喉）由
     apptemplate 单测 + apitest 契约承载——NormalizeCompose 白名单即静态执法，
     漏剥自然红。
   - scope 词表 +`templates`（assembly/policy.go；builtin 角色自动吸收，存量 token
     无需重铸——F3.2 同款）。

## 后果

- proto/API 面：delivery 上下文 +1 服务（TemplatesService 四 RPC）；config +1 键；
  errcode +2；eventcode +2（template.instantiated / templates.refreshed）。
- state +1 聚合（catalog 单行）+ 迁移 00026。
- CLI：`templates` 命令组（list/show/instantiate）+ 顶层动词 `create-from-dokploy`；
  golden 双形态钉死。
- Console +1 页（Templates）；Quickstart/部署表单零改动。
- e2e +1 腿（模板实例化全链：instantiate → succeeded → Route 200）。
- 引擎零改动：模板渲染与实例化编排都在 api/叶子层，Deploy 链只是被内部复用
  （因子化不改行为，既有 Deploy apitest 不动即证）。
- 挂账：db host 隐式变量（adminer 类模板需求出现时再裁）；目录多版本并存（真实
  需求前不设）。

## 验收锚

- [x] 三型变量各得其所：string 插值、secret 铸造 + secret_refs 文件注入 + 值零
      出现于渲染产物与 AppSpec（反扫断言）、domain 变量驱动 Route 创建
      （internal/apptest/templates_test.go TestInstantiateTemplateSecretChain——
      Ensure Materials.SecretFiles 形态值面 + env 路径引用面；staging grafana
      真机复证，2026-10-06）
- [x] fail-closed 渲染：未知变量/缺必填/secret 携值/残余插值/平台键漏剥/未知扩展
      字段 全拒（信封文案断言）
      （internal/apptemplate/apptemplate_test.go TestParseRejectsUnknownFields +
      TestRenderFailClosed）
- [x] 实例化幂等：重跑同 instantiate 不旋转密码、不重建 Database/Route
      （created→reused 报告面可见）
      （apitest 指纹不变断言 + CLI golden rerun；staging 真机 reused 报告一致）
- [x] 目录刷新 fail-closed：digest 不符/预校验拒的清单不覆盖快照（apitest）
      （TestRefreshTemplatesFailClosed；e2e dind 篡改清单反锚——digest reason +
      快照存活，2026-10-06 本地 dind 两腿全绿）
- [x] 内嵌目录冷启动：无目录源环境 list/instantiate 全可用
      （e2e dind-template.sh 腿 1 无目录源配置；staging 常态即此形态）
- [x] create-from-dokploy：解析映射 + skip 报告诚实（不可映射项逐条可见），
      --dry-run 零调用
      （cmd/fleetly/cmd/templates_golden_test.go TestGoldenCreateFromDokploy——
      git 源 app/插值孔 compose/mariadb 三类 skip 逐条钉板）
- [x] CLI golden 双形态：templates list/show/instantiate + create-from-dokploy
      （TestGoldenTemplatesCatalog/…InstantiateWaitsToSucceeded/…NoWaitJSON/
      TestGoldenCreateFromDokploy，含组与 refresh 的动词级双形态契约）
- [x] e2e 一腿全链：instantiate → deployment succeeded → Route 200（本地 dind）
      （e2e/dind-template.sh 两腿：内嵌目录 + 目录热更新，TEMPLATE E2E PASSED）
- [x] Console：Templates 页目录/详情/实例化（tsc+vitest；dist 同 commit）
      （console/src/pages/Templates.tsx + lib/templateForm.ts/vitest 28 用例，
      console:verify 零漂移）
- [x] 门禁全套绿：mise run test + lint、guards -count=1、动 proto 后
      generate:verify、console:verify
      （2026-10-07 收官批全绿；守卫咬出并同批修复：dbtemplate 引擎词例外
      机制重构 + 禁词 apply/dump/sink 措辞面）
- [x] staging 走查：目录读面 + 实例化全链 + 事件/审计（HTTP/消费契约级）
      （docs/reviews/2026-10-06-template-walkthrough.md PASS；runbook 记录·十二）
