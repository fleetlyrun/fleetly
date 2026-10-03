# ADR-0038: Token 实时收窄与写面 scope fail-safe 门

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-04 | P6（docs/design/2026-10-03-optimization-proposals.md §P6）、ADR-0035（行级授权）、ADR-0024（幂等执法面与守卫反扫范式）、CONTEXT.md Token/Role 词条 |

## 背景

zane 三维修权中两项是 fleetly 尚未显式承诺的语义：Token 授权永不超 creator、
实时收窄；写面 RPC 忘声明 scope 时的 fail-safe 门。fleetly 现状（2026-10-03
盘点）：Token 的 scope 经 role 行间接持有，resolve 逐请求读 role——**role 行
编辑已实时生效**；但 creator（tokens.user_id）的授权变化不约束 Token：

- creator 被移出 Team（membership 行删除）→ 属主 Token 照常工作；
- creator 的 membership 角色降档 → 高档声明的 Token（如 owner `*`）不降；
- 给无 membership 的用户铸属主 Token 照铸（CreateToken 只查 userExists）。

creator 被删除已由 FK 结构性盖住（tokens.user_id REFERENCES users，删用户
须先吊销名下 Token）——本 ADR 补防御分支而非改删除面。

## 决策

1. **T1 实时收窄**：有属主 Token 每次请求的有效授权 =
   **min(Token 声明, creator 当前授权)**。
   - creator 当前授权 = creator 在 **Token 所在 Team** 的 membership 角色
     （memberships 是唯一真源；Token 单 Team 绑定，无"Project 绑定集"求交面）。
   - 求交算子 `identity.Meet`（域纯函数）：同资源按蕴含阶梯取低档
     （声明 apps:admin × creator apps:write → apps:write）；creator 无该
     资源即丢弃；声明 `*` 收敛为 creator 完整授权集；与输入序无关。
   - 全失（membership 消失 / 交集为空 / creator 行缺失）→ **403
     E_FORBIDDEN 带原因**（`reason` context ∈ creator_deleted /
     creator_not_in_team / empty_scope_intersection）——Agent 可判定
     "找管理员"而非"重新认证"（P6 裁决 2026-10-03；E_UNAUTHENTICATED 会
     误导重认证）。空交集不是部分收窄：单 Team 绑定下零交集 = 全失。
   - 全失时落审计行 `token.narrowed_denied`（节流 60s/Token——重试循环
     不刷屏）；非空收窄只收 scope 面，不落审计（Info 日志一次）。
   - 无属主 Token（bootstrap/Agent/CI）不参与收窄——声明即有效（无
     creator 可言）；bootstrap 是平台 onboarding 起点，语义不变。
   - 同角色快路径：membership 角色 = Token 角色时免二次读角色行（求交
     恒等）。性能面：属主 Token 每请求多两次点查（user + membership）。
2. **PUBLIC 面不浮出收窄**：WhoAmI/status 面对无效凭证按匿名放行的既有
   语义不变（status 不被脏凭证阻塞，F0.6 裁决沿用）——收窄 403 只在
   SERVER 面浮出。Agent 判定 Token 健康度走任意 SERVER 调用。
3. **T2 写面 scope fail-safe 门**：proto 注解（method_auth + api_key_scope）
   是 scope 声明表，authz.Build 收集 + AssertAllRegisteredHavePolicy 启动
   断言已盖"access 缺声明"与"SERVER 缺 scope"；本 ADR 补"**方向误标**"
   反扫（守卫 `TestScopeDeclarationsMatchVerbs`，与 idem 覆盖反扫同构）：
   - 变更型动词（Create/Delete/Put/Deploy/Rollback/Revoke/Enroll/Drain/
     Cordon/Uncordon/Scale/Stop/Cancel/Renew/Trigger/Rotate/Upload/Set/
     Lift/Approve/Accept/Declare）声明 READ op 即红——变更面挂读档 =
     fail-open 静默洞（zane `required_scopes` 防线的 fleetly 形态）；
   - 读型动词（Get/List/Stream/Wait/Diff/Explain/WhoAmI/Follow/Watch）
     声明 WRITE/ADMIN 即红（声明与动词漂移即误标）；
   - **动词表完备性双向把守**：未知动词两侧都不命中即红——新 RPC 动词
     必须显式入表，杜绝绕过分类的静默通道；
   - 例外通道：豁免表带理由（现一条：IssueEventTicket——铸只读 SSE 短票，
     events:read 对应被交换能力而非铸造动作），不再命中即红（双向保鲜）。
4. **铸造位收口**：CreateToken 给 user_id 显式目标时，受理位要求该 user
   在目标 Team 持 membership（E_CONFLICT 拒绝铸出即死的 Token）。无 owner
   豁免——给无 membership 的用户铸跨队 Token 从来不是合法运维。
5. **T3 三级 ability 否决**（同日裁决）：敏感可见面被 Secret 模型结构性
   消除（值永不回显、只回指纹），不引入 read/write/write:sensitive 粒度。

## 后果

- `fleetly init` 链零漂移：CreateUser 同事务建 membership，随后铸的属主
  Token 求交恒等（同角色快路径）；单 Team 部署行为不变。
- 属主 Token 的授权语义从"铸造时快照+role 行实时"收紧为"role 行实时 +
  creator 面实时"；已存在的属主 Token 若 creator 已无 membership 将立即
  失效（正确行为，非回归）。
- WhoAmI 的 scopes 展示的是**有效面**（收窄后）——Token 声明面经
  GetToken/role 行仍可审计。
- 新写面 RPC 的流程固化：proto 注解（方向正确）→ scopeguard 动词表（新
  动词入表）→ 守卫绿；忘任何一步 CI 红。
- v1 API 面尚无 membership 移除/降档 RPC（DeleteUser 被 FK 挡住整链）——
  收窄的现实触发面是 DB 直改与未来 membership 管理 RPC；语义先于面落地。

## 验收锚

- [x] creator 降权后 Token 立即失去对应面（apitest：WhoAmI 面收窄 +
  member 档外的写面 403；authn 单测：阶梯降档/原样通过矩阵）
- [x] creator 被移出 Team → 同 Token 下一次 SERVER 请求 403 带原因
  （reason=creator_not_in_team）+ 审计行恰一条（60s 节流）
- [x] creator 行缺失（FK 兜底分支）→ 403 reason=creator_deleted（authn
  单测钉 PRAGMA foreign_keys=OFF 制造现场）
- [x] 空交集 → 403 reason=empty_scope_intersection + 审计行
- [x] scope 方向守卫入 CI（TestScopeDeclarationsMatchVerbs：变更动词×
  READ 红 / 读动词×WRITE 红 / 未知动词红 / 豁免双向保鲜；首跑即逮到
  DeclareNetworkPeer 漏分类——完备性把守实证）
- [x] CreateToken 属主目标无 membership → E_CONFLICT（apitest）
- [x] identity.Meet 纯函数性质：重复调用稳定 + 输入序无关（100 次迭代钉）
- [x] 无属主 Token（bootstrap/CI）零漂移（authn 单测 + 全量 golden 无漂移）
