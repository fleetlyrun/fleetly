# ADR-0071: 部署配置可观测面——冻结 Spec 全文 / Workload 投影 / 在服观测

日期：2026-10-13；状态：已采纳（同批落地：delivery/runtime 契约 + engine + CLI + console）。

## 背景

Console 用户对"当前 deploy 到底跑的什么配置"没有任何可观测面：

- 冻结 Spec 只在 Processes/Variables tab 被投影成派生字段（replicas/strategy/ports 计数/env），image/command/healthcheck/resources/volumes 等字段拉到不渲染；无"查看原文"入口（CLI 有 `apps spec`，console 未对齐）。
- `GetAppSpec` 只回"最新冻结 Revision"且不带 seq/digest 锚——部署失败或回滚后"最新冻结"≠"实际在服"，没有 API 能回答"当前生效部署的 Spec 是哪份、内容是什么"（Revision 有 digest 无全文，`GetRevision` 端点不存在）。
- "实际生效的 compose/k8s 文件"在平台不存在：部署链是 AppSpec →（Project 投影）→ `capability.Workload` → Provider 直发编排器 API（swarm ServiceSpec / k3s typed 对象），从不生成 compose 文件或 manifest；投影产物不可寻址读取（ADR-0064 的 canonical JSON 只活在 Provider 内存账本，重启即冷且无 Revision 关联）。
- 在服实况只有引擎 drift 扫描内部消费（`InspectWorkloads`），无 API 透出；ADR-0067 实例面是个体级（instance/node），载体级配置观测缺失。

## 决策

1. **三层真源各配只读 API，Revision 寻址**：
   - **冻结层**：`RevisionsService.GetRevision`（`(app_id, seq)` 复合寻址，复刻 GetConfig 复合路径段先例）回 Revision 行 + AppSpec 全文；`GetAppSpecResponse` 加 `revision_seq`/`revision_digest` 锚（语义保留"最新冻结"——Redeploy 依赖，ADR-0069）。
   - **投影层**：`RevisionsService.GetAppWorkloads`（`GET /v1/apps/{app_id}/workloads?seq=`）——按 Revision 只读重放 `engine.Project` 投影，返回用户面 Workload 集。这是"平台会下发什么"的正式答案。
   - **观测层**：`WorkloadsService.GetAppWorkloadObservations`（runtime 域，`GET /v1/apps/{app_id}/workloads/live`）——透出 `InspectWorkloads` 的载体级在服观测（workload/generation/image/replicas/state/rollout）。
2. **"当前生效"寻址 = LatestSucceeded 的 to_revision**：`GetAppWorkloads` 缺省 seq=0 经 `Deployments.LatestSucceeded` 解析（无成功部署 → E_NOT_FOUND）；"最新冻结 ≠ 当前生效"由 Console 提示条显形。生效语义从此有单一权威解法，不再与"最新冻结"混用。
3. **渲染与部署路径单源**：抽取 `projectRevision` 共享装配（loadSpec → appTeam → resolvePeerRefs → buildDigests → egressNetworkMap → `Project`），materialize 与 render 同源——视图即部署管线的只读半边：isolate=true（与基线重放/drift 收敛同形态，view 永不因撤销中的 peer 报错）；`applyVolumePinning` 只读并入；**不** pinVolumes（零写库）、不 stamp 代戳、不 resolveMaterials、不 Ensure、不 recordEnsured。
4. **近似语义如实披露**：历史 Revision 重放使用当前侧表（team/name、peer 批准态、egress、卷钉住、builds 行）——语义是"现在重放会下发什么"而非考古；当前生效 Revision 与在服实况一致（撤销进行中的收敛态除外）。变量类值（SharedVariable 合成 env）在冻结体内，不受侧表影响。
5. **载体原生形态不透出**（swarm ServiceSpec / k8s 对象 JSON）：Workload 词条"平台不感知载体形态"的执法延续；且载体 spec 混入环境性 ID（daemon 网络 ID/SecretID）不可按 Revision 重放。proto `Workload` 镜像消息只含用户面字段；**Materials（拉取凭证/secret 值）永不进任何视图**（ADR-0014 值不落日志纪律适用于展示面）；**Generation 不进投影视图**（渲染非下发，代戳语义属 ADR-0064——观测层才有真实 wire-gen）。
6. **逐 Revision provenance**：`Revision.source_kind`（git|image|upload）从冻结体 `source` oneof 派生，零迁移——材料来源在 Revision 面可见（git push 覆盖历史从此可读；与 ADR-0072 写面配对）。
7. **授权与词汇**：三 RPC 全 `Get` 前缀（freezeguard 读桶 + scopeguard readVerbs 零豁免）；scope resource `revisions` READ ×2 + 新增 `workloads` READ ×1；CONTEXT.md Workload 词条补可观测面句，零新增 Avoid、零守卫分诊。

## Consequences

- Console Spec tab 三段式（冻结 Spec / 投影 Workloads / 在服观测并置，drift 一眼可见）+ Deployment 详情 View spec 链接 + Processes 行展开补齐已拉未渲染字段；CLI `revisions get` / `apps workloads` 对齐，Agent 面随 skills 散文同步。
- 渲染层是"期望态的确定性重放"（近似真源，第 4 条披露）；排障问"集群真跑什么"以观测层为准——两层分立不冒充。
- 三个新面全部 only-additive，buf breaking 自然绿零豁免。
- 写面（git 应用配置权责、Spec 所有权分裂、变量三级合成）随 ADR-0072。

## 验收锚

- [ ] engine 单测：render 与 materialize 投影一致性（共享装配单源 + 用户面字段显式断言、Generation 排除）；seq=0 无成功部署负例；isolate 剥离语义例（`internal/engine/render_test.go`）。
- [ ] apitest 全链：GetRevision（含未知 seq E_NOT_FOUND）；GetAppWorkloads（缺省 = 生效锚、历史 seq 重放、**render 输出与 FakeRuntime Ensure 录制面用户字段逐一致**、无成功部署 E_NOT_FOUND）；GetAppWorkloadObservations（FakeRuntime inspect 录制）。
- [ ] CLI 双形态 golden ×2 动词 + 负例；`apps spec --json` golden 随锚字段刷新；coverage 守卫自动对账。
- [ ] 守卫全绿（freezeguard/scopeguard/grpcguard/gatewayguard/wording/实体域）；`generate:verify`/`console:verify` 零漂移。
