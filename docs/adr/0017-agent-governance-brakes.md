# Agent 治理刹车：配额、速率、change freeze 与属主吊销处置

持写 Scope 的 Token 是自动化地基，也是失控面（失控 Agent 可无限 CreateTask 打爆集群）。决定四件刹车：①per-Project 的 Task/Workload 数量配额；②per-Token 创建速率限制；③change freeze——API 级变更冻结窗（按资源/动作/条件封禁，命中返回带原因的拒绝，继承 tsuru Block 语义）；④属主 Token 吊销时，名下 Task 默认宽限排空（可配置为跑完 TTL），处置进审计。Scope 语义维持"读默认开放、写显式授权"（write 蕴含 read 不变）。

## 附录 A：四刹车的执行裁决（F1.9 落地，2026-10-02）

正文四件刹车中 ④（属主吊销宽限排空）已随 F1.6 落地（engine Options
`TaskOwnerRevokedRunToTTL`，默认宽限排空）。本附录裁决 ①②③ 的执行形态；
Schedule 重叠策略旋钮（F1.7 留给本批的开放锚）随批挂入，裁决修订记在
ADR-0018 附录 A.3。

### A.1 per-Project Task/Workload 数量配额

- 两枚 Task 轴配额 + 一枚同族收口：
  - `MaxTasksPerProject = 100`：非终态 Task 行数（active/draining 计；
    终态与 tombstone 不占位）。
  - `MaxTaskConcurrencyPerProject = 200`：非终态 Task 的
    desired_concurrency 之和——期望 Run 总量是 Workload 数量的真源口径
    （one-shot 归一后恒折 1）。
  - `maxAppsPerProject = 50`：活跃 App 行数（Workload 的另一来源面，
    同一失控向量，随本批附带）。
- 执法点全部**事务内读**（SQLite 单写连接串行——ADR-0024"所见即受理
  终局"同款无 TOCTOU 保证）：
  - CreateTask / CreateApp：受理位检查（configQuota 范式，acceptance.go）；
  - ScaleTask：engine 写事务内**增量口径**检查（sum − 现值 + 新值 ≤ 上限）；
  - Schedule 到期拍：spawn 事务内检查，超限 → skip（`schedule.skipped`
    reason=`quota_exceeded`、next_fire_at 照常推进——与重叠 skip 同形：
    不静默丢拍、不追补、事件可观测）；手动拍超限 → 诚实拒绝。
- F1.5 挂账的 desired_concurrency sanity 上限 100 就此收口：per-Task
  上限由项目并发总量配额承载（空项目单 Task desired > 200 亦过不了
  受理位），E_INVALID_ARGUMENT 只保留非负检查。
- 错误码复用 E_QUOTA_EXCEEDED（不新增）。缺省为常量（maxConfigsPerProject
  先例：小团队口径保守缺省，配置面接入后可覆盖）。

### A.2 per-Token 创建速率限制

- **计数器住内存**（进程内 per-Token 桶，固定窗 fixed window，缺省 60s
  窗 / 120 次）。控制面单进程是 v1 部署事实（SQLite 单写者、单实例），
  无跨进程协同需求；单表持久化是对该事实的过度设计，且每次创建多一条
  写放大。
- **动词面 = 幂等执法面同源**（`idem.EnforcedMethods` 单一清单，编译期
  引用）：创建型动词单一真源已在 ADR-0024 建立，速率面复用之；豁免联动
  （某动词豁免幂等即同时豁免速率，理由随豁免条目走）。
- **计数口径**：仅"实际执行的创建型动词"计数——幂等重放不消耗预算
  （拦截器位于幂等执法器之后）、被拒请求不消耗预算、无 Token 身份的
  调用不计数（创建型动词全部 SERVER 面，实际恒有 Token；webhook 接收
  面无 Token 身份，天然不计数）。
- **重启行为诚实边界**：重启计数清零（= 窗口重置）。重启是运维权力、
  非攻击可达面；刹车目标是分钟级失控的 Agent 循环，不是对抗持久化绕过。
- 错误码 **E_RATE_LIMITED** 新入册（gRPC ResourceExhausted / REST 429），
  信封带 `retry_after_seconds` 上下文。
- **拒绝不落事件/审计**：被刹住的调用方重试风暴不得经"刹车命中记录"
  反向打爆平台写面——错误信封即调用方观测面，平台侧命中计量挂 N2
  metrics 面。

### A.3 change freeze（变更冻结窗）

- **载体**：单表 `change_freezes`（id、team_id、reason、created_by、
  created_at、lifted_at）；`team_id=''` 是全局冻结行；每 scope 至多一条
  活跃行（部分唯一索引，Set 命中活跃冻结 → E_CONFLICT，先 lift 再 set）；
  lift = 落 lifted_at（幂等：已 lift 再 lift 成功）。
- **判定轴 = 资源所属 Team**（ADR-0028 Team 轴）：命中活跃冻结
  （team_id ∈ {'', 资源所属 Team}）即拒。调用方所属 Team 不参与判定
  ——跨 Team 管理 Token 不得借道自家 Team 绕过目标 Team 的冻结。
- **封禁面**（按资源/动作枚举；继承 tsuru Block"拒绝带原因"）：Workload
  与结构变更族——structure 全部变更动词；delivery 的 Deploy / Rollback /
  SetGitHook / RotateHookToken / **ReceiveWebhook**（webhook 是无 Token 的
  部署触发面，漏它 = 冻结漏 GitHub push）；automation 的 CreateTask /
  ScaleTask / DeleteTask / CreateSchedule / DeleteSchedule /
  TriggerSchedule；edge 的 CreateRoute / DeleteRoute。
- **豁免面**（带理由，冻结面反扫守卫在册）：停止族（StopTask / StopRun /
  CancelDeployment——冻结期间安全收口必须可用）；租约保温（RenewTask
  ——冻结不杀保温）；runtime 运维面（drain/cordon/uncordon/enroll——
  集群运维非变更控制）；identity 全部变更（账号/Token 管理非变更控制，
  且冻结解除依赖这些面可用）；GovernanceService 自身。
- **执法形态**：拦截器（authn 之后、幂等执法器之前——冻结拒绝不占幂等
  记录）。Team 解析自请求字段逐级回行（project_id 直取；app_id / task
  id / schedule id / network_id / peer id / route id / hook token 回行）；
  行不存在等解析失败放行——受理位自会给出诚实拒绝。冻结进出与在途写的
  毫秒级竞态接受为良性（tsuru 同款）。
- 错误码 **E_CHANGE_FROZEN** 新入册（gRPC FailedPrecondition / REST
  409——与 E_CONFLICT 族同映射面），信封带冻结 reason 与 freeze id。
  拒绝不落事件/审计（A.2 同款自放大防护）。
- **事件**：freeze.set / freeze.lifted（管理动作有界，eventcode 三链
  入册）；**管理面**：GovernanceService（system 包，platform:write/read）
  SetChangeFreeze / LiftChangeFreeze / ListChangeFreezes（after_* 惯例）；
  SetChangeFreeze 进幂等执法面（Set 前缀创建型动词）。CLI
  `fleetly freeze set|lift|list`。

### A.4 Schedule 重叠策略旋钮（修订 ADR-0018 A.3）

重叠策略从固定 skip 改为可配置：`FLEETLY_ENGINE_SCHEDULE_OVERLAP_POLICY`
（AppConfig.Engine.schedule_overlap_policy）取 `skip`（默认，行为不变）或
`fire`（重叠时照常拍，允许并行拍）。无效值启动失败（fail-fast，不静默
回退 skip）。第三选项（queue/等待）仍不预支——torchwood 规模实证（F1.15）
后再议。

### 验收锚（F1.9，2026-10-02 落地批次勾验）

- [ ] 配额：CreateTask / CreateApp / ScaleTask / 到期拍 / 手动拍五面执法，
      并发创建不超限（engine + apitest）。
- [ ] 速率：预算耗尽 → E_RATE_LIMITED（429/ResourceExhausted）；
      窗口翻转恢复；幂等重放不消耗预算（单测 + apitest）。
- [ ] 冻结：set → 封禁面拒绝（含 webhook push 与跨 Team 资源轴）→ lift
      恢复；停止族豁免在冻结期可用；全局与 per-Team 两形态；freeze.set /
      freeze.lifted 三链入册。
- [ ] 冻结面反扫守卫：proto 变更型动词 ↔ 冻结表/豁免清单双向保鲜
      （freezeguard）。
- [ ] 旋钮：skip 默认行为不变；fire 允许并行拍；无效值启动红
      （engine + assembly）。
- [ ] CLI freeze 动词组双形态 golden。
