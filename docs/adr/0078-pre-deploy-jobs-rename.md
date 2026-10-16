# ADR-0078: pre_deploy_jobs 改名——first_boot_jobs 整族更名,无向后兼容

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-16 | CONTEXT.md Task/DeploySpec 词条、ADR-0007（词汇冻结：更名走显式 ADR）、ADR-0030（部署链接线）、ADR-0033（compose 受理）、ADR-0072（Spec 所有权分裂）、ADR-0074（compose-go 官方受理） |

## 背景

`first_boot_jobs`（部署期 init job，数据库迁移等）的命名经评估判定为真实词汇缺陷，非口味问题：

1. **语义误读**："first boot" 在业界有固定含义——设备生命周期内仅一次的初始化（cloud-init/OEM 首启）。fleetly 的实际语义是**每次部署都执行**（同 Revision 再部署 = 重跑，回滚永不重跑，ADR-0030 决策 5）。误读方向危险：读者可能以为迁移只在首次部署跑，从而漏配幂等或画蛇添足补重跑逻辑。fleetly 把 AI Agent 当一等用户，标识符自解释是产品面。
2. **业界命名两大家族，现名哪个都不属于**：release 系（Heroku Release Phase、Fly.io `release_command`）与 pre-deploy 系（Render Pre-Deploy Command、DigitalOcean App Platform `kind: PRE_DEPLOY`、Coolify、Kamal `pre-deploy`）。
3. **词汇纪律对候选空间的裁决**：Deployment 词条 Avoid 表禁 `release` → release 系封死；`hook` 已被 webhook（通知通道实体）/git hook 占用；`init` 是 K8s initContainer 的 pod 级另一层概念。`pre_deploy_jobs` 由在册词复合（Deploy 词条 + 既有 "deploy-time job" 校验措辞），零新名词，且消除误读。

用户裁决（2026-10-16）：**改名 `pre_deploy_jobs`，不留向后兼容。**

## 决策

1. **新名族**（旧 → 新）：

| 面 | 旧 | 新 |
|---|---|---|
| Spec 字段（spec.proto 号位 6 不变） | `AppSpec.first_boot_jobs` | `AppSpec.pre_deploy_jobs` |
| API 字段（delivery.proto 号位 15 不变） | `first_boot_task_id` | `pre_deploy_task_id` |
| DB 列（deployments，迁移 00015 引入） | `first_boot` | `pre_deploy`（迁移 00033 RENAME COLUMN） |
| compose 扩展键 | `x-fleetly-first-boot-jobs` | `x-fleetly-pre-deploy-jobs` |
| eventcode | `deployment.first_boot_job` | `deployment.pre_deploy_job` |
| 引擎选项 | `Deploy.FirstBootWaitGrace` | `Deploy.PreDeployWaitGrace` |
| 域字段/方法/常量 | `Deployment.FirstBoot` / `FirstBootAnchor` / `FirstBootDone` | `PreDeploy` / `PreDeployAnchor` / `PreDeployDone` |
| 引擎文件 | `engine/firstboot.go` | `engine/predeploy.go` |
| 错误哨兵 | `ErrFirstBootNetworkUnknown` | `ErrPreDeployNetworkUnknown` |

   **不动的**：`JobSpec` 消息名与 "deploy-time job" 校验措辞（最小改名面）；词条归属不变——部署期一次性工作负载仍是 Task 的部署期特例，不另立实体（ADR-0007 冻结裁决）。
2. **无向后兼容**：旧 compose 键不再受理（未知 `x-fleetly-*` 键 fail-closed 既有语义自然覆盖）；旧 proto JSON 名不保留（protojson 名随字段名切换）；buf breaking 对 origin/main 恰 FILE 红 = 开发期基线追认（`config_refs` reserved 先例、JobSpec C-13 整删先例，上 main 后自愈）。
3. **eventcode 注册表只增纪律的显式例外**：`deployment.first_boot_job` → `deployment.pre_deploy_job`，用户裁决改名且无兼容负担；golden 与 usage 反扫同批更新，Source 锚点随文件改名同步。
4. **历史记录不改写**：ADR-0030/0033 正文与文件名（含 "first-boot-jobs" 字样）是当时裁决的忠实记录，只在头部加修订注指向本 ADR；docs/plan、docs/reviews 的历史条目同理不动。
5. **读法注记随名落地**："每次部署执行，非仅首次" 写进 spec.proto 字段注释，防新读者再造旧误读。

## 后果

- proto/genproto/swagger/console 生成链同批再生成；dist 是入仓生成物，`console:gen && console:build` 与变更同 commit。
- golden 全量再生成（CLI verbs golden、assembly selfdescription、composeintake/spec 归一化 golden）与变更同 commit。
- e2e 套件零改动（不触该键；install.sh 与 skills/database-provision 的 "first boot" 是数据库 initdb/bootstrap token 的另一含义，不在改名面）。
- staging 现役不受影响（列改名走迁移，游标值域不变；`''`/`<idx>:<taskID>`/`done` 三态语义零变化）。

## 验收锚

- [x] spec/delivery proto 字段改名 + `mise run generate:all` 基线通道绿（genproto/config/wire 零漂移断言）
- [x] 迁移 00033 带 goose Up/Down 注记，列改名保持游标值域
- [x] Go 面全族改名（engine/composeintake/apptemplate/persistence/spec/mapping/CLI）+ `mise run test` 三 module 全绿
- [x] golden 再生成与变更同 commit（-update 通道）
- [x] compose 键 `x-fleetly-pre-deploy-jobs` 受理 + 参考文档（compose-dialect.md/schema.json）同步 + 旧键 fail-closed
- [x] console:gen && console:build，dist 同 commit；UI 文案同步
- [x] 活文档同步：CONTEXT.md 两词条 + ADR-0030/0033 修订注 + skills/deploy-diagnose + runbook 活参考行
- [x] `mise run lint` 除 buf breaking FILE 红（预期追认）外全绿
