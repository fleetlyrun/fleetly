# ADR-0023: App 删除语义——收口拆载体、撤路由、活跃部署拒绝删除

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-01 | ADR-0016（admission 显式语义）、ADR-0005（Ensure/Remove 动词面）、CONTEXT.md Orphan 词条 |

## 背景

N0 的 DeleteApp 只落 tombstone：载体继续跑、Route 继续服务、`Runtime.Remove`
自落地以来无调用方（死代码）。删除语义失真——"删了但还在跑"。

## 决策

1. **DeleteApp = 收口动作**，四步一序：
   ① **活跃部署拒绝**：App 有 queued/在途/回滚中部署时返回 E_CONFLICT
   （提示先 cancel 或等终态）。与 admission 的显式语义对称（supersede 必须
   显式，删除更必须）——不引入隐式取消。
   ② **engine 收口**：`Engine.TeardownApp` 调 `Runtime.Remove`（幂等拆域内
   全部载体——Remove 自此有唯一调用方）并清归属/期望/drift 缓存。
   ③ **撤路由**：软删引用该 App 的全部 Route 并即时触发 Edge 全量发布。
   ④ **落 tombstone + 审计**（四件一拍的结构面）。
2. **孤儿原则维持**（CONTEXT.md Orphan）：非平台管辖载体只登记永不自动删；
   Remove 只拆 fleetly 标记域。
3. **顺序与失败语义**：②在③④前（先停流量面再拆数据面记账——与
   RuntimeAdmin 同款"先变更后留痕"：副作用不可与审计同事务）。②失败即
   整体失败（tombstone 不落——App 保持可操作，可重试删除）。
4. **ID 永不复用**（D-MN-8 同族）：tombstone 后同 Project 同名可新建（新
   ULID），旧 ID 的部署/审计历史永续。

## 后果

- CLI `fleetly apps delete --app APP_ID`（双形态 golden）。
- Revision/Deployment 行永不删（审计单位）——App 删除不级联清历史。
- 卷/网络等材料不动（Project 级资产，随各自生命周期）。
