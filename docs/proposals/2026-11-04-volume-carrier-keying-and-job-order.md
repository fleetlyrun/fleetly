# 卷载体键位与 pre-deploy job 序机制缺口（重播批咬出，2026-11-04）

> 状态：登记待裁决。三笔均为机制补全（非补丁）：载体名/卷键/作业序都是平台语义，不应由操作者绕。

1. **app 卷跨项目共键（隔离缺陷，最高优先）**：数据库自动卷与 app 声明卷的运行侧键 = `fleetly-vol-<volumeID>`，而 VolumeAttachment.VolumeID 携带的是**卷名**（项目内唯一、全局可撞）——同名卷跨项目在共享 Runtime 上共用同一 docker volume。修法：载体键换**卷行 ULID**（A1 后已有，`fleetly-vol-<volULID>`）；需小 ADR 定存量卷 adoption（docker 不支持改名：ensure 期按「旧行在场+新键缺席」做一次性数据搬移或显式拒绝+迁移动词）。
2. **数据库自动卷的异源检测（fail-closed）**：flag-day 后同名陈旧卷致新凭证对旧 pgdata 失配且 rotate 不能自愈（表现是隐蔽认证失败）。修法：库模板首启在卷内落实例标记（instance id），provision/ensure 发现异源标记即显式拒绝（E_CONFLICT+指引），删除保留义不变。
3. **pre-deploy job 序语义**：jobs 按字母序串行执行，依赖角色后建的 job 在处女库上死锁。修法方向二选一：受理面保持**声明序**（现状按什么排序待查——若 composeintake 排序则修 intake）或 spec 显式依赖字段；拒绝面先加「等待环」受理期可检子集。
4. 换装序补丁位：runbook 重置序补「运行侧同名卷清理步」属流程面，机制位由 1/2 承接后此步退化为防御性检查。

建议批序：B1 卷键 ULID 化+异源检测（同批 ADR+守卫）；B2 job 序语义（spec proto 面，裁决声明序 vs 依赖字段）；B3 重置序工具化。
