# ADR-0033: firstBootJobs intake——compose 扩展键 + command/environment list 形态伴随扩宽

| 状态 | 日期 | 关联 |
|---|---|
| Accepted | 2026-10-02 | ADR-0030（firstBootJobs 部署链，决策 8 intake 挂账）、ADR-0014（Secret 材料文件面）、ADR-0009（git 源 webhook 面）、ADR-0032（builder 面 upload 专属先例） |

## 背景

ADR-0030 落地了 firstBootJobs 的引擎链线与校验面，intake 面挂账："compose
扩展键 / spec_file 裸 AppSpec 面随后续 API 扩展批"。F1.15 dogfooding 前置
评估结论：**torchwood 栈需要部署期迁移**（golang-migrate 13 个 SQL 迁移 +
roles 签名密钥落库的部署时序链），ADR-0030 明文规定该场景先落 intake 面；
messageloop 栈无迁移（数据面是栈内 Redis AOF），不依赖本批。本 ADR 收口
intake 形态裁决。

## 决策

1. **形态 = compose 顶层扩展键 `x-fleetly-first-boot-jobs`**（值为 job
   mapping 列表）。spec_file 裸 AppSpec 面**维持延后**——现有消费者
   （torchwood/messageloop 均以 compose 文件为栈声明单一真源）无整 spec
   投放需求；spec_file 会让全部 AppSpec 字段成为 API 可写面（校验爆炸半径），
   无真实需求前不开。
2. **job 字段白名单**（复用 compose 服务词汇，最小面）：
   - `name`（必填，spec 内唯一——ValidateApp 既有执法）；
   - `image`（必填；compose intake 的 App 无 Build 声明，`from_build` 在此面
     不可达——git 源构建批再议）；
   - `command`（string 形态 → Fields 切分，语义与服务相同；**list 形态 →
     字面 argv**，见决策 4）；
   - `environment`（map 形态 / list `"K=V"` 形态，见决策 4）；
   - `secrets`（与服务同款短语法 `[name]` / 长语法 `{source: name}` →
     secret_refs；Task 域材料面与 App 域同一条解析通道——migrate job 经
     `/run/secrets/database:<name>` 文件拿连接 URL 是 canonical 用法）；
   - `ttl`（必填，Go duration 字面量；值域 (0, 86400s] 由 ValidateJob 既有
     执法）。
   未知键拒绝理由精确；**禁面键在 compose 键名层即拒**（不待 IR 层）：
   `volumes`/`configs`/`ports`/`healthcheck`/`placement`/`replicas` 的拒绝
   理由与 ValidateJob 同口径（ADR-0030 决策 8），`networks` 的拒绝理由指向
   缺省（job 缺省挂靠项目全部活跃网络——连 `db-<id>` 已覆盖；收窄需求出现
   再按白名单只增纪律扩）。
3. **执行语义零新裁决**：ADR-0030 全链不动（releasing 前半串行、行游标、
   回滚永不重跑、网络铸造时解析、ttl 必填）。本批只加喂食面。
4. **伴随扩宽（服务面同享）**：
   - `command` 支持 **list 形态**（字面 argv）。动机：Secret 注入是文件面
     而非 env 插值（ADR-0014），"读 secret 文件 → export → exec"的
     `["sh", "-c", "…"]` 包装是 compose 原生惯用法；string 的 Fields 切分
     无法表达带引号/带空格的 argv。string 形态语义不变——存量 compose
     零漂移。
   - `environment` 支持 **list 形态**（`["K=V", …]`，compose 原生第二形态）
     并**修复 map 断言失败时的静默丢弃**（list 形态此前被静默吞掉——
     静默丢弃即设计缺陷，N0.1 P2-3 同款口径）。非法元素（无 `=`/空键）
     显式拒绝。
5. **proto/CLI/webhook 零改动**：扩展键骑在 `DeployRequest.compose_yaml`
   里（F1.10 既有面）；`deploy --compose-file` 直读文件内容；webhook 面
   （git 源）不涉 compose。与 ADR-0032 的"旗标面 upload 专属"同款纪律：
   intake 面不新增 RPC 字段。
6. **零新 errcode/eventcode**：校验失败 `E_INVALID_ARGUMENT` 既有
   （ValidationError → apperr 通道）；job 生命周期观测沿用
   `deployment.first_boot_job` + `task.*`/`run.*`（ADR-0030 决策 9）。

## 后果

- compose 白名单只增纪律延续：顶层白名单 +`x-fleetly-first-boot-jobs`。
- ADR-0030 的开放锚"protojson golden 随 intake 面批补"（Deployment 消息
  `first_boot_task_id` 在 intake 缺位下无 API 可达夹具能置起）在本批闭：
  apitest 经 compose intake 驱动带 job 部署全链，golden 钉
  `first_boot_task_id` 可见。
- torchwood/messageloop 仓的 `docker/fleetly/` 形态按本批面重写（其现存
  README/runbook 是归档仓 v0.1 的面：`fleetly.job: init` label / `fleetly
  env set` / `percona-postgresql-18` 模板等在新平台均不存在——F1.15
  dogfooding 批处理，非本 ADR 票面）。
- spec_file 面若未来出现真实需求（整 spec 投放、from_build job 声明），
   按新 ADR 开，不回头改本裁决。

## 验收锚

- [x] 翻译：`x-fleetly-first-boot-jobs` 两 job（command list 形态 +
  secrets + environment 两形态）→ `AppSpec.first_boot_jobs` 逐字段断言
  （TestComposeFirstBootJobs）
- [x] 服务面伴随扩宽：command list 形态 → 字面 argv；environment list
  形态 → map；string/map 形态语义不变（存量零漂移）；list 元素非法
  （无 `=`/空键）显式拒绝；environment 此前的 list 静默丢弃不再发生
  （TestComposeCommandEnvListForms / TestComposeCommandEnvSecretRejections）
- [x] 拒绝面：未知 job 键 / 缺 name / 缺 image / 缺 ttl / 禁面键
  （volumes/configs/ports/healthcheck/placement/replicas/networks）
  各拒绝且理由精确（TestComposeFirstBootJobRejections 十六形态）
- [x] apitest 全链：compose 带 first_boot_jobs 部署 → succeeded，Deployment
  消息 `first_boot_task_id` 可见（TestComposeFirstBootDeployEndToEnd：
  job Run 的 task 域 Ensure 带 secret 文件注入 + list 形态 command 字面
  argv；golden 三处 = deploy-compose-jobs / deployments-list-first-boot
  双形态——ADR-0030 开放锚闭）
- [x] 全门禁绿（mise test 三 module / lint 0 issues / generate:verify
  清洁树）
