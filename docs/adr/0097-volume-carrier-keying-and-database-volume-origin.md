# ADR-0097：卷载体键换行 ULID + 数据库卷异源 fail-closed

日期：2026-11-04 立法（提案与用户裁定见 `docs/proposals/2026-11-04-volume-carrier-keying-and-job-order.md` §1/§2：机制补全不打补丁；staging 现役是 flag-day 后干净态，无旧名卷残留）｜ 状态：实施中（B1-1 载体键、B1-2 异源门同批）。占号复核：0093-0096 已被 F5 托管数据服务立项预留（并行会话编号复核纪律）。

## 背景

重播批（2026-10-31 staging 应用栈重播）咬出两处机制缺口：

1. **卷载体键携带卷名**：`VolumeAttachment.VolumeId` 自受理起携带的是卷名——项目内唯一、全局可撞。运行侧载体键公式 `fleetly-vol-<volumeID>`（swarm docker volume 与 k3s PVC 同构）在共享 Runtime 上把**跨项目同名卷折叠到同一载体**：两项目各建卷 `data`，在同一 swarm 上共用 docker volume `fleetly-vol-data`——隔离缺陷，重播批实咬。
2. **数据库自动卷的异源静默复用**：删除库不删卷（备份保留义，重建同名库复用卷、覆写凭证）。重建同名库 = 新凭证 + 旧 pgdata：首启密码不进旧数据面 → 隐蔽认证失败且 rotate 不能自愈（旧密码才能认证，改密链也以新凭证敲门）。flag-day 平台库重置后同名陈旧卷是同一病灶的批量形态。

ADR-0092 已裁定 id 铸造单源（26 位小写 ULID）与「name 在边界解析，id 在内核与存储」边界解析律——本 ADR 把该律延伸到卷附件面。

## 决策

1. **附件携带卷行 ULID，载体公式不变**：`VolumeAttachment.VolumeId` / `capability.VolumeMount.VolumeID` 的值语义 = **卷行 id**（`fleetly-vol-<volumeID>` 公式两 Provider 原样，`sanitizeNamePart` 对 ULID 恒等——ULID 字符集天然全落载体名字符集）。运行时零 name：name 只活在受理输入与卷表行。
2. **三个解析点（ADR-0092 边界解析律同款）**：
   - **受理咽喉**（`engine.SubmitSpec` → `composeSpec` 合并+变量合成之后、冻结之前）：spec 全部卷附件 name → 行 id 就地改写后进冻结体——四源（image/compose/upload/spec_file）与 Deploy/Preview 同一咽喉收敛；卷名不在册 = 受理失败（`spec.ValidationError`，E_INVALID_ARGUMENT 带指向文案）。
   - **投影前段**（`projectRevision` 等冻结体装载点）：对**存量冻结体**（本 ADR 之前冻结、附件携带 name）做双形解析——`entityid.LooksMinted` 直通、否则按名解析改写（内存态，不改冻结字节）。新冻结体恒为 id 形，双形判据幂等。name 与 26 位 ULID 字符集重叠：形如 ULID 的卷名按 id 直通——与旧名键行为逐字节一致，无回归面（ADR-0092 决策 3 同款判据与豁免）。
   - **数据库自动卷**（库名确定性公式处）：`ensureDatabaseVolume` 补建/复用卷行后**返回行**，投影以行 id 为载体键；出生序不变（收敛环幂等补建，API 侧无预建约定）。
   - 连坐改形：`PinVolumes`/`ApplyVolumePinning` 按行 id `Get` 解析钉住（原按名 `GetByName`）；`NamespaceRef.DatabaseName` 仍承载数据库名（载体名 `fleetly-db-<name>-<id>` 可读段，ADR-0092 不动）。
3. **存量 adoption：显式拒绝**（用户裁定 2026-11-04）：docker 卷不可改名，ensure 期检测「**旧名载体在场且新键缺席**」→ 显式拒绝（哨兵错误，app 面 = 部署失败带迁移指引文案；库面 = 收敛环拒绝确保 + 受理面前置读拒），**v1 不做同批数据搬移动词**（留后续；staging 现役 flag-day 后干净态，无存量面）。检测经新可选子面 `RuntimeVolumeInspection.VolumeCarrierExists(ctx, ns, volumeKey)`——平台以卷键（name 历史形/id 现行形）问载体在否，载体名公式仍是 Provider 私有（架构 §5「平台永不解析载体命名」不破）；面缺席时守卫跳过（Inspector 降级文化；两在册 Runtime 均实现）。
4. **库卷实例标记（fail-closed）**：dbtemplate 拥有标记契约——数据目录根 `fleetly-instance.json` = `{"database_id","minted_at"}`（JSON 常量形，读写脚本为平台常量 `sh -c` 串，redis 恢复脚本先例，零用户数据插值）。异源检测放平台侧（用户裁定）：databases 域 ensure 链在 Ensure 前经 RuntimeUtility 挂卷执行「在场即读、缺席即铸」（claim）——标记在场且 `database_id` 不等 → 哨兵拒绝，受理面（CreateDatabase，卷行已在 = 同名重建形态）前置读映射 **E_CONFLICT**，文案指「同名陈旧卷：清理运行侧卷或换名重建」。已制实例（标记同源）与无标记存量卷（含预建卷）启动不受影响（无标记 = 铸记收编）。
5. **节点拓扑边界（诚实声明）**：swarm 工具容器恒在控制面 daemon 跑，docker 卷 node-local——标记门仅在**卷未钉住或钉住控制面节点**时执行（N0 单控制面口径，PinVolumes 同判据），钉住他节点的卷跳过检测并记日志（fail-open 覆盖缺口，多节点 swarm + 库卷钉离控制面是 v1 外形态；k3s 工具容器挂 PVC 由调度器回 PV 节点，天然节点正确）。后续手：service 形探测容器（随首个多节点库卷场景）。
6. **守卫钉死「载体键值 = entityid 形态」**：`internal/guards` 新增 volumekey 守卫——生产代码中 `VolumeId`/`VolumeID` 赋值点白名单（带理由注释，双向保鲜：合法点消失即红、新增点不在册即红）；swarm/k3s translate 测试钉 `fleetly-vol-<26 位 ULID>` 载体名；受理咽喉测试钉冻结体携带 LooksMinted id（防 name 回流）。

## 后果

- 跨项目同名卷隔离由构造保证（ULID 全局唯一）；`docker volume ls`/`kubectl get pvc` 的卷载体一眼可对卷行（id 可复制锚，ADR-0092 决策 6 延伸）。
- 卷删除重建（同名）= 新行新 id 新载体：never-reuse 从「行约束」升为「物理载体事实」；引用旧冻结体部署在旧卷行消失时受理/物化精确失败（原先在 pinVolumes 才炸）。
- 存量升级面（e2e upgrade 矩阵 HEAD~1→HEAD 首配对）：旧名载体在场 → 库收敛环拒绝确保（载体保持现状、零滚动、状态不翻），e2e 补「按指引清理运行侧卷 → 新键重收敛 → 活体复验」迁移步钉死裁定行为；app 面同理（部署失败带指引）。
- 受理面行为变化：卷名不在册从「部署期 pinVolumes 失败」提前到「受理失败」（compose/spec_file 面）；库同名重建从「隐蔽认证失败」变为「受理 E_CONFLICT」。
- 债与后手：数据搬移动词（旧名载体 → 新键同批迁移）留后续需求触发；多节点 swarm 库卷标记覆盖随 service 形探测容器补。

## 验收锚

- [x] B1-1：受理咽喉测试（name→id 冻结、未在册名拒）；存量冻结体双形解析测试；databases 投影测试（VolumeID=行 id、DatabaseName=库名）；PinVolumes/ApplyVolumePinning 按 id 解析测试；swarm/k3s `VolumeCarrierExists` 实现 + ULID 形载体名测试；volumekey 守卫绿；golden/apitest/e2e 卷名形态断言全刷；`mise run test` 三 module -race 全绿（触包 golangci 零新增）。
- [x] B1-2：dbtemplate 标记读写单测（铸/读/坏形态拒/字符集闸）；卫星多态测试（同源过+幂等/异源拒且不 Ensure/坏形态拒/无 utility 面 fail-closed/受理前置读两态）；apitest 两态（异源 E_CONFLICT / 无标记重建过+新键收敛）；errcode E_CONFLICT Source 锚与 golden 同步；e2e upgrade 迁移步实装（同键配对自动跳过）。
