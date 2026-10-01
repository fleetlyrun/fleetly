# ADR-0023: App 删除语义——收口拆载体、撤路由、活跃部署拒绝删除

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-01 | ADR-0016（admission 显式语义）、ADR-0005（Ensure/Remove 动词面）、CONTEXT.md Orphan 词条 |

## 背景

N0 的 DeleteApp 只落 tombstone：载体继续跑、Route 继续服务、`Runtime.Remove`
自落地以来无调用方（死代码）。删除语义失真——"删了但还在跑"。

## 决策

1. **DeleteApp = 收口动作**，三步一序（2026-10-01 修订：撤路由并入落账
   事务）：
   ① **活跃部署拒绝**：App 有 queued/在途/回滚中部署时返回 E_CONFLICT
   （提示先 cancel 或等终态）。与 admission 的显式语义对称（supersede 必须
   显式，删除更必须）——不引入隐式取消。拒绝由三道复查承载：
   **无锁预检**（常规拒绝在零副作用阶段）→ **TeardownApp 锁内预检**
   （与 Submit 共享 appMu，拆载体前所见即受理终局）→ **落账事务内复查**
   （与 tombstone 同生共死，见 ③）。任一命中即拒绝，App 保持可操作。
   ② **engine 收口**：`Engine.TeardownApp` 调 `Runtime.Remove`（幂等拆域内
   全部载体——Remove 自此有唯一调用方）并清归属/期望/drift 缓存。
   ③ **落账一事务**：App tombstone + **撤路由**（软删引用该 App 的全部
   Route）+ `app.deleted` 事件 + 审计同生共死——复查见活跃部署则整单回滚
   （无 tombstone、无路由删除）。**路由消失与 App 删除原子**：拒绝路径
   永不撤路由，Edge 周期发布只可能见到"App 与路由同逝"的一致状态。
2. **拒删不变式：App 存活 ⇒ 路由不得消失**（修订动机，复审实证缺陷：
   旧序"先撤路由后复查"在收口后复查命中活跃部署时，返回 E_CONFLICT 而
   路由已撤——App 存活、部署在途重建载体、流量静默丢失、无事件）。残余
   面（收口后、落账前受理的部署）：载体已被拆——**不静默**：以
   `app.teardown_aborted` 事件 + 审计（`app.teardown_abort`）留痕后按
   E_CONFLICT 拒绝；在途部署的 Ensure/回滚重放自愈重建载体，路由因整单
   回滚未被触碰。留痕失败即整体失败（不做静默的 E_CONFLICT）。
3. **孤儿原则维持**（CONTEXT.md Orphan）：非平台管辖载体只登记永不自动删；
   Remove 只拆 fleetly 标记域。
4. **swarm secret 载体不随 App 删除**（现状即设计，N0.1 P2-4 复审澄清）：
   secret 载体按"名+指纹"命名（`ensureSecrets`），同值 Secret 被多个 App
   引用时共享同一载体——Remove 无法按 App 边界拆除。故 Remove 只拆域内
   service；secret 载体随最后引用者消失成为无引用载体，其回收走 Project
   级 Secret 删除触发的 GC 路径（N1 材料批次，届时以引用计数落地——
   本 ADR 不预设实现）。
5. **顺序与失败语义**：②在③前（先停流量面再拆数据面记账——与
   RuntimeAdmin 同款"先变更后留痕"：副作用不可与审计同事务）。②失败或
   ③复查命中即整体失败（tombstone 不落——App 保持可操作，可重试删除）。
6. **ID 永不复用**（D-MN-8 同族）：tombstone 后同 Project 同名可新建（新
   ULID），旧 ID 的部署/审计历史永续。

## 后果

- CLI `fleetly apps delete --app APP_ID`（双形态 golden）。
- Revision/Deployment 行永不删（审计单位）——App 删除不级联清历史。
- 卷/网络等材料不动（Project 级资产，随各自生命周期）。
- `app.teardown_aborted` 事件入册（eventcode 只增）：订户可据其告警
  "删除与部署赛跑、载体短暂缺位将由在途部署补齐"。
