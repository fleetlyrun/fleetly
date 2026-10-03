# 时间语义

一条裁决定死三处：Schedule 存带时区 cron（IANA 时区名随 Schedule 持久化，跨夏令时由 cron 库按墙钟解释）；TTL 与 Owner Lease 落库为**绝对 deadline**（控制面重启后按墙钟续算，不依赖进程内计时器）；审计与 Event 时间戳一律 UTC RFC3339。

## 附录 A：Schedule 执行模型与三项裁决（F1.7 落地，2026-10-02）

正文裁决的执行细化。Schedule = 周期触发规则（CONTEXT.md 词条，Avoid:
cron 作实体名 / timer），到期拍从冻结 TaskSpec 模板铸一条 **one-shot
Task**，此后补足 / 观测 / TTL / 终态镜像全走 F1.5 既有链——Schedule 只
拥有"何时拍"，不另立第二套执行机制（Run 词条：Schedule 的执行也是 Run）。

### A.1 cron 库选型

**robfig/cron/v3（v3.0.1，MIT，零传递依赖）**。消费面刻意收窄：

- 只用 `Parser.Parse` + `Schedule.Next`（5 字段 + `CRON_TZ=` 前缀由
  Parser 原生处理）；**runner 不用**——ADR-0018 禁进程内计时器，到期
  判定由驱动环按 `next_fire_at` 绝对时刻墙钟比较；
- 时区名独立成列（proto `timezone` 字段 + 行 `timezone` 列），解析时经
  `CRON_TZ=` 前缀注入——表达式本体不含时区，人读/存储/API 面单一来源；
- 跨夏令时按墙钟解释（SpecSchedule.Next 在目标时区做墙钟算术）：春跳
  缺口内的拍点**当日不触发**（Vixie cron 同款，robfig 实测钉死在
  state 测试），秋跳重叠时刻取前一义、只触发一次；
- `@descriptor` 显式拒绝（5 字段解析器不启用 Descriptor 位）——`@every`
  是区间语义而非墙钟，两种时间模型并存会让"跨夏令时由 cron 库按墙钟
  解释"失去意义；
- `time/tzdata` 随 state/schedule 包嵌入（~450KB）：IANA 查表是正确性
  面，不得随部署环境（无系统 zoneinfo 的容器形态 / 裸 Windows）漂移。

### A.2 错过窗口：补跑一拍

控制面停机/重启跨过 `next_fire_at` 后，恢复的第一拍把错过的窗口**补跑
一次**（一拍恰好一个 Run），随后 `next_fire_at = Next(now)` 从当前时刻
续算——不按过期拍点追补多次（停机三天不会补 72 个 Run），也不静默跳过
（周期工作负载代表"本应发生"的意图：备份/清理/派发类晚跑一次好过漏跑；
要丢弃可停掉补跑 Run）。重启安全的机制基础：拍点状态只有 `next_fire_at`
绝对时刻一行，无进程内计时器状态可丢。

### A.3 重叠：skip（默认，可配置）

到期时上一拍铸出的 Task 仍有未终态 Run（pending/running/stopping——
**Run 状态是真源**，Task 终态镜像随驱动环有一拍延迟）→ 跳过本拍：落
`schedule.skipped` 事件（reason=overlap）、`next_fire_at` 照常推进。
手动触发（TriggerSchedule）同判定下诚实拒绝（E_CONFLICT，先停上一拍或
等其收口），且**不移动** `next_fire_at`——cron 节奏不被手动拍打乱。

策略默认 skip（最少惊异选项）；F1.9 治理批起开放旋钮
`FLEETLY_ENGINE_SCHEDULE_OVERLAP_POLICY`（AppConfig.Engine.
schedule_overlap_policy）：`skip` | `fire`（重叠时照常拍，允许并行拍），
无效值启动失败（裁决全文见 ADR-0017 附录 A.4）。旋钮只改**到期拍**行为：
手动触发恒诚实拒绝（显式动作给显式反馈——先停上一拍再触发）。第三选项
（queue/等待）仍不预支——torchwood 规模实证后再议。

### A.4 派生裁决

- **铸出的 Task 无名**（name 空）：tasks 活跃名唯一索引含终态行，固定名
  在第二拍即撞；导航走 `schedule.fired.last_task_id` 与事件流。
- **铸出的 Task 系统属主**（owner 空）：Owner Lease 是 resident 池的保温
  机制，Schedule 拍出的 one-shot 不需要属主在场。
- **DeleteSchedule 不停在途**：已铸 Task 跑完自然收口（Schedule 只拥有
  "何时拍"）——与删除 cron 不杀正在跑的作业同义。
- **拍点状态最小化**：行上只有 `next_fire_at` + `last_task_id` 两列运行
  态；`Fire` CAS 以 `next_fire_at` 旧值为前置（与 Task 落行同事务——
  双发回滚不落孤账）。

### 验收锚

- [x] 带时区 cron 解释：IANA 名随行持久化，Next 按墙钟算术（state 层
      测试钉死东京/纽约两例，含春跳缺口跳日行为）。
- [x] 绝对拍点：控制面重启后按 `next_fire_at` 续算，不依赖进程内计时器
      （引擎环 Kick-on-start；错过窗口补跑一拍有 engine 测试）。
- [x] 重叠 skip：上一拍 Run 未终态 → 到期跳过 + 事件；Run 终态后恢复
      （engine 测试）。
- [x] 手动触发：立即铸 Task、节奏不动、重叠诚实拒绝（engine + apitest）。
- [x] 全链：到期拍 → task.created → run.created → 观测终态（apitest；
      F1.5 机制复用的 API 面证据）。
- [x] 三链咬合：schedule.created/fired/skipped/deleted 事件入册
      eventcode + schemareg + golden。
- [x] 真机：跨 daemon 升级窗口（2026-10-03 收尾批真机：拍点间窗口
      `systemctl restart fleetlyd`，重启后下一拍恰一次（10:52/10:54 各一
      task）、next_fire_at 重算正确（10:56:00Z）、无漏拍无双发；用户池
      Workload 全程 running 零扰动。runbook 2026-10-03 节）。
- [ ] 真机：跨真实 DST 边界（Sydney 观察钟 2026-10-03 已植
      staging：*/20 Australia/Sydney 跨 2026-10-04 02:00→03:00 春令 =
      16:00Z 跳变；创建时 next fire 11:00Z=21:00 AEST 换算已实证，
      边界穿越核验随当日收尾批闭锚）。
- [x] 重叠策略旋钮：skip 默认行为不变；fire 允许并行拍；无效值启动红
      （F1.9 落地：TestScheduleOverlapPolicyFires / TestParseScheduleOverlap /
      TestNewEngineOverlapPolicyFailsFast）。
