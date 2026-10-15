# ADR-0072: Spec 所有权分裂与 App 级变量——git 应用与 console 的配置权责

日期：2026-10-13；状态：已采纳（同批落地：spec 纯函数 + AppVariable 聚合 + freeze 咽喉 + webhook 合并 + Deploy 闸门 + CLI + console）。

## 背景

三个相互咬合的缺口（用户裁决 2026-10-13）：

1. **push 静默覆盖**：webhook 每次 push 从 hook 配置全新合成 AppSpec（单 web 进程 from_build，不读当前冻结 Spec）。git 应用在 console/API 侧做的任何 spec 改动（env/replicas/ports/额外进程）被下一次 push **整体静默回退，零冲突信号**；反向地，console 重发的 spec_file 携带旧 `source.git.ref`，触发构建会 clone 旧 commit。
2. **env 写死在配置文件**：变量只有两级（SharedVariable 项目级 freeze 时合成、源声明 env 烧在 intake），App 级没有平台侧变量存储——git 应用（webhook 合成体无 env）与 compose 应用（env 烧在文件里）都没有"配置在平台、制品不含值"的 env 之家。
3. **provenance 缺位**：Revision 无来源列，audit 的 source 不在 API 投影，`deployments.commit_sha` 调用方自填可伪造——平台无法一等回答"这个 App 的配置真源在哪、谁改的算数"。（读面已随 ADR-0071 补齐：`Revision.source_kind` 逐条材料来源。）

## 决策

1. **Spec 所有权分裂**：材料半边（`source`、`build`）归部署来源所有；运行时半边（processes 的 replicas/env/secret_refs/ports/healthcheck/resources/placement/volumes/networks/strategy、额外进程、first_boot_jobs）归声明方所有。判定信号 = **`app_hooks` 行存在**（单一信号，API 与 console 共用——hook 在即 push 会合成，无论最近一次部署来自谁）。
2. **push 合并**：webhook 冻结 = `MergeHookOwned(hook 材料, 冻结运行时)`——新材料（source.git@新SHA + build）+ 当前冻结 Spec 的运行时半边 → 新 Revision。console 声明的运行时配置跨 push 存活；首次 push（无冻结 Spec）= 纯合成照旧。
3. **hook 应用的 Deploy(spec_file) = 运行时 overlay**：冻结材料 + 提交运行时 → 新 Revision；**提交件的 source 一律忽略**（防旧 ref 构建旧 commit）。这是 hook 应用运行时配置的写面——Variables/Processes/Spec 编辑继续走 spec_file 零改动即安全。（对"拒收 spec_file"候选的精确化：拒了就没有运行时写面；full-replace 则把旧 ref 与静默回退都放进来；只有 overlay 让"材料归 git、运行时归声明方"两个裁决自洽。）
4. **材料源硬闸门（API 级）**：hook 应用的 Deploy 携带 image/compose/upload 源 → `E_APP_HOOK_OWNED` 拒收；`DeployRequest.override_hook: true` 显式一次性全量接管（regular 路径 = 先删 hook，错误 suggestion 明示）。console 不暴露 override（un-git 走 Settings 删 hook）。非 hook 应用一切照旧（full-replace，不分裂）。
5. **App 级变量三级合成（env 不写死在源）**：AppVariable 升格为一等平台聚合（表/聚合/API 完全镜像 SharedVariable 先例，App 轴）；合成优先级 **SharedVariable（底）< 源声明 env（中）< AppVariable（顶）**，在 freezeRevision 咽喉执行（`MergeSharedEnv` 扩展为三层）——push 合并与 overlay 都过咽喉，store 值每次重合成永不丢。git 应用的 env 之家 = AppVariable；console 改 env = 改 store + Apply（PUT/DELETE 变更键 + 重发冻结 Spec——新 store 顶层覆盖产生新 digest，ADR-0064 内容寻址按差异滚动）。变量轮换需显式部署生效（与共享变量现状一致）。明文非敏感契约同 SharedVariable（敏感值走 secrets age 信封，不进 AppVariable）；secret_refs 仍走 spec/overlay（聚合保持干净）。
6. **边界披露**：Replay（回滚）重放旧 Revision 连材料一起回旧（下次 push 再合并回来——回滚的语义本就如此）；image→git 迁移场景运行时半边的显式 image 进程被保留（adopt git 材料 = 进程改 from_build）；运行时半边的 env 条目是"中间层"——AppVariable 同键覆盖之，删除 store 键则回落 intake 值。

## 与既有裁决的关系

- ADR-0002（Spec IR 与 Revision）："部署基线以 Revision 为准，不回读源文件"不变——所有权分裂是 Revision 之间的合并语义，不是回读源。
- ADR-0009（webhook pull-only）"待重估"注记：本 ADR 补的是合成侧（hook 配置 + 冻结运行时）；"从仓库读 spec/compose 文件"的完整 pull 语义仍留待重估，方向兼容（仓库 spec 文件届时作为运行时半边的另一来源进合并）。
- ADR-0043（两级变量合成）：三级化是它的直接扩展，EnvNamePattern 白名单与 freeze 咽喉位置不动。
- ADR-0060 决策 5（console 暂存-部署 = 回读冻结 Spec + 改动组装 spec_file）：机制保留，hook 应用的 spec_file 语义升级为 overlay（console 侧零改动）。

## Consequences

- git 应用的完整配置权责：材料（source/build）随 push 走，运行时形状走 console overlay，env 走 AppVariable——三面互不覆盖。
- 非 hook 应用行为零变化（full-replace + 两级合成的既有语义由三级合成向后兼容承载：无 AppVariable 行时中间层即终态）。
- webhook 冻结的 digest 覆盖合并后的运行时半边 + 三级合成 env——内容寻址去重语义不变（同内容复用同一 Revision）。
- 审计修 bug 同批：webhook 路径 `WithAuditOverride` 提前到 freezeRevision 之前（`revision.create` 审计行的 source 此前错标为 api）。

## 验收锚

- [x] spec 纯函数表驱动单测：MergeHookOwned 所有权字段表（材料/运行时逐字段）、三层合成同键覆盖序、image→git 迁移保留显式 image、from_build 保留、first_boot_jobs 归运行时半边（internal/spec）。
- [x] apitest：push 合并保留运行时改动（console 改 replicas/env → 模拟 push → 断言存活 + ref 更新）；spec_file overlay 只动运行时（提交件 source 被忽略）；材料源闸门 E_APP_HOOK_OWNED + override_hook 全量接管；非 hook 应用零影响；AppVariable 合成优先级（三级同键）与轮换-重发产生新 Revision；digest 去重仍成立。
- [x] CLI 双形态 golden：`apps vars`（list/set/unset）+ `deploy --override-hook`；skills 散文同批。
- [x] 守卫全绿；`generate:verify`/`console:verify` 零漂移；迁移带 goose 注记。

## 追记：咽喉新址（2026-10-15，P3 架构收敛批 7）

冻结咽喉自落地时的 api 层 `freezeRevision` 下沉为 `engine.SubmitSpec`
（application 单咽喉）：所有权合并按 `MergeMode` 方向参数化（push =
MergeMaterial 新材料 + 冻结运行时；overlay = MergeOverlay 冻结材料 +
提交件运行时），三级变量合成、内容寻址复用冻结与 admission 随行；webhook
摄取核与 Deploy 全部切换。职责分野不变：请求形状执法、四源旗标互斥、
hook 闸门（E_APP_HOOK_OWNED/override_hook/spec_file 判向）与
deployVarLookup 留在 api；webhook 的 delivery 台账 Tx 留在 api
（RecordDelivery + hook 事件一拍，ADR-0072 语义不受迁移影响）。审计语义
随 ctx 走：WithAuditOverride 先于 SubmitSpec 注入，revision.create 与
deployment.create 审计行 actor/source 逐字节同形。九条受理不变式由
apitest（submit_spec_invariants_test）逐条钉死。
