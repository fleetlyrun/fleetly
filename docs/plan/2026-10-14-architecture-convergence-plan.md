# 架构收敛执行计划（2026-10-14）

真源：设计终稿见 `docs/design/2026-10-14-architecture-convergence-report.html`（v4 定稿；v1-v3 底稿随 /tmp 失效，v4 为唯一真源）；审查证据与执法手册（G1-G24）在其中 §二-§四。本文件是**执行与进度真源**：任务分解、验收锚、勾选状态。

前提（用户裁决，2026-10-14）：开发期**无向后兼容负担**；有硬理由可推翻既有约定；采用 Clean(DDD) 四环分层 + Repo 接口层 + 去方法论命名；六刀激进切法以"杀一整类 bug/一族样板 + design-it-twice 证据"为门槛。

## 验收线（Definition of Done）

- [ ] P0-P3 全部勾选；P4/P5 核心件落地（长跑项可挂账）
- [ ] 依赖规则守卫 = 5 行数据表全绿（domain 三红线 / application 只见 domain / infrastructure 实现 / interfaces 读写分离律 / assembly 组合根）
- [ ] 全量 `mise run test` + `lint` + `generate:verify` + `console:verify` 零漂移
- [ ] 三条 ADR 例外成文（贫血聚合集中 / admin 离线写 / 读写分离律）+ 分层立法 ADR + ADR-0070 适用范围修订（exec 会话 ID）
- [ ] e2e 矩阵绿

## 里程碑总表

| 阶段 | 主题 | 任务 | 预估 | 依赖 |
|---|---|---|---|---|
| P0 | 安全/并发/正确性止血 | P0-1…P0-7 | 2-3 天 | — |
| P1a | 四环搬移 + 立法 | P1a-1…P1a-5 | 2-3 天 | P0 |
| P1b | Repo 接口抽取 | P1b-1…P1b-3 | 3-5 天 | P1a |
| P2 | 事实发射 kernel | P2-1…P2-4 | 4-5 天 | P1b |
| P3 | engine 域化九批 | P3-0…P3-8 | 6-9 天 | P2 |
| P4 | 校验/错误三面收敛 | P4-1…P4-4 | 4-6 天 | P3（P4-1 可提前） |
| P5 | statekit + 测试长跑 | P5-1…P5-5 | 增量 | 随批 |

---

## P0 止血批（每件独立 commit；守卫/测试与修复同批）

### P0-1 exec 会话三修（S1+K1+K2）+ 首批单测 — [x] 
- **改动面**：`internal/engine/exec.go`、`internal/api/fleetlygrpc/exec.go`
- **S1**：StreamExecSession attach 前比对 `ExecSessionInfo.TeamID` 与调用方 Team（owner/platform 豁免口径对齐 authz.go 既有模式）；TeamID 判定下沉 engine 面（`AttachClient` 增 team 参数或 `SessionByID` 返回 info 由 handler 比对——以 handler 比对为最小面）
- **K1**：CreateSession 即装 hard TTL timer（不再等 AttachClient）；注释承诺与代码对齐
- **K2**：deliverRelayFrame 的 send 移入会话锁临界区并复查 closed；timer 装配竞态一并收口
- **首批单测**：`internal/engine/exec_test.go`（创建后不附着 → 推 TTL → 回收；finishSession×deliverRelayFrame 并发交错 -race；Team 比对正/负）
- **验收锚**：未附着会话在 hard TTL 后清出；他队 attach 得 E_NOT_FOUND/E_PERMISSION 口径；并发交错无 panic
- **验证**：`go test -race ./internal/engine/ ./internal/api/...` → 全量 `mise run test`
- **预估**：0.5 天 · **依赖**：无

### P0-2 events 面 Team 过滤最小修（S2 最小面）— [x]
- **改动面**：`internal/state/migrations/`（outbox +team_id 列，00032）、`internal/state/outbox/`、`internal/api/fleetlygrpc/contexts.go`、`eventtickets.go`
- ListEvents/StreamEvents/SSE 按 `team_id=? OR team_id=''` 过滤；写点（transition.go/writes kernel）补 team 铸入；票据 payload 绑 Team
- **验收锚**：他队 member 订阅只见本队+平台事件；apitest 双队矩阵；410 语义不变
- **验证**：apitest events 面 + golden（多队矩阵新增）
- **预估**：0.5-1 天 · **依赖**：无（刀 1 在 P2 会重写 kernel 面，本件是最小止血）

### P0-3 E_INTERNAL 三面统一（E0）— [x]
- **改动面**：`internal/api/apperr/`（error_id 铸造+脱敏下沉信封层）、`internal/assembly/errors.go`、gRPC 出口拦截器、`cmd/fleetly/cmd/render.go`
- **验收锚**：gRPC/CLI 面 E_INTERNAL message 不含 cause 文本；error_id 三面在场；服务端落日志；golden 断言（G15 雏形）
- **预估**：0.5-1 天

### P0-4 job 方言收口（含双重逃逸）— [x]
- **改动面**：`internal/composeintake/intake.go`（initJobFromService 换 rejectUnsupportedFields 专属白名单；删 :448 二次逃逸；清 :499-503 死代码；修注释错挂）
- **验收锚**：entrypoint/user/服务级 x- 键显式拒；job 面 `$` 与普通 service/顶层 job 同口径透传
- **验证**：composeintake 新负例+正例 + e2e first-boot 腿
- **预估**：0.5 天

### P0-5 k3s Watch 韧性三件 — [x]
- **改动面**：`internal/providers/k3s/runtime.go`（静默失败三处补 Warn；watchLoop recover+重开；退避 select+ctx.Done）
- **验收锚**：对齐 swarm 同款；断流可诊断；优雅退出真退出
- **预估**：0.5 天

### P0-6 state 写路径三件 — [x]
- **改动面**：`state/{secret,appvariable,sharedvariable}/repo.go`（唯一冲突归一 ErrAlreadyExists）；`backup/repo.go`（终态 CAS）；`run/repo.go`+`task/`（TerminalStateStrings 单源）
- **验收锚**：并发同名 PUT → 409 语义；二次软删幂等口径统一；终态不可覆写
- **验证**：statetest 新用例
- **预估**：0.5-1 天

### P0-7 杂项止血 — [x]
- deployVarLookup 吞错上抛（delivery.go:218-227）；registry"未配置"族入册 503（registrycontent.go + errcode + golden）；api_base 私网拒绝单源（githubapps validateAPIBase）；Q1 节点条件写（nodes.go 值变化才写）；Q2 按名点查（dokploy/quickstart GetByName）；dokploy 文案失真修正
- **预估**：0.5-1 天

---

## P1a 四环搬移 + 立法（2-3 天）

### P1a-1 搬移主体 — [x]
`git mv` 重组为四环 + 八上下文（对齐 proto 服务族；映射表开工时与 proto 目录对照定稿）；全仓 import 重写；wire 再生成；三 module 编译绿。**移动不改名**（composeintake/state→persistence 例外）。
### P1a-2 立法 — [x]
§2 重写为四环树 + 5 行依赖规则 + 三条例外；新 ADR（分层立法 + XxxKernel 废除与命名法则 + 例外三条）；CONTEXT.md Audit 词条入册 + Event/Audit 双轴注记。
### P1a-3 守卫换装 — [x]
dep_matrix 数据表 5 行化；domain 三红线守卫（无 I/O/genproto/框架）；capability 纯度守卫；scanModulePrefixes/sizeguard/wording/statefacade/entityid 路径面修缮；apperr 纯化（status 构建器移 interfaces/grpc）；material 拆分（keyring 入 infrastructure）。
### P1a-4 e2e 扫面 — [x] scanModulePrefixes + e2e/。
### P1a-5 零漂移验证 — [x] golden 全集零漂移 + mise 全套。

## P1b Repo 接口抽取（3-5 天，按聚合增量）

### P1b-1 模板立定 — [x]
每聚合循环模板：`domain/<ctx>/<agg>/`（行类型+`Repo` 接口）→ `infrastructure/persistence/<agg>/`（不导出实现+New）→ 消费面改接口 → wire.Bind → statertest 返接口。每聚合一 commit，golden 零漂移为界。
### P1b-2 高频十聚合先行 — [x] deployment/task/app/project/revision/run/secret/config/build/audit/outbox 十一聚合已上移（c5c3108/74277a5/批二）；根包契约面已迁 domain/shared/state、domain→persistence 豁免行已删、writes 豁免行已去冷删行。
### P1b-3 其余聚合 + 守卫收紧 — [x] 37/37 聚合上移；DB 契约（state.DB 窄面）+ NextSeq 方法化；构造上提 assembly（engine.Repos 26 字段组结构注入）；application 行收紧 domain only、interfaces 行 application+domain；豁免全集 5 行（github/upload×4 带 P3 锚 + admin 离线 ADR-0075 例外②）；红灯实验双份自证（代理+主会话）。

## P2 事实发射 kernel（4-5 天）
P2-1 outbox team_id 全量铸入 + Runner 端口 — [x]（P0-2 奠基；domain/shared/state.Runner 承载无需另立；ownerTeam 三态分派补齐余量）
P2-2 kernel 本体 — [x]（e647304）
P2-3 A 化 + 四族事件 — [x]（0fb54ce + 批三收尾：acceptance 62 处 + engine 前门全量单声明；9 新事件按实际 RPC 面定数；templates.refreshed→template.refreshed 归一）
P2-4 守卫 — [x]（factappend 白名单 + factdual 双真纪律；红灯双份自证）

## P3 engine 域化九批（6-9 天）
P3-0 守卫前置（transitionguard 接收者泛化 + 域 struct 禁回引 Engine）— [ ]
P3-1 exec（P0-1 修复正式收编 + 会话 ID 随机化 + ADR-0070 修订 + 单测扩充）— [ ]
P3-2 observ（metrics 拆 exposition/alerting/channelnotify）→ P3-3 task/run（动词拆 taskverbs）→ P3-4 data → P3-5 managed → P3-6 build/material — [ ]
P3-7 deploy 域 + SubmitSpec（v2 §4 九条不变式逐条 apitest 断言；ADR-0072/0074 追记咽喉新址）— [ ]
P3-8 browse + 壳收口（engine.go <300 行 + Options 按域切片 + config 面同批）— [ ]

## P4 校验/错误三面（4-6 天）
P4-1 protovalidate 引入 + 71 处手写退役 + config_refs 删除（buf breaking 开发期基线追认）— [ ]
P4-2 CLI descriptor 客户端校验（deploy 互斥手拷贝 55 行退役）+ runRPC/pageFlags/connFlags 骨架 — [ ]
P4-3 "未配置"族码律收尾 + G17 守卫 — [ ]
P4-4 SDK FromError + apitest builder 三件 — [ ]

## P5 statekit + 测试长跑（增量）
P5-0 夹具去重：internal/state/statertest 与 statetest 一字母双包合并（审查中新发现，10-12 事故复制）
P5-1 statekit 共享件（TransitWhere/ClampLimit/Placeholders/软删幂等/冲突归一）+ G7/G8 — [ ]
P5-2 升级前进测试（旧 schema 快照→Up）+ 换装 hermetic 测试 — [ ]
P5-3 G19（state 聚合带测试，volume/run/user/audit 先补）+ G20（e2e↔CI 对账 + coverageguard 补 k3s）— [ ]
P5-4 中继退出路径族（writeLoop done 通道/resize close/k3s logs cancel-同伴/metricsDom 加锁）— [ ]
P5-5 G5/G13 + 性能阈值锚注释（SSE 50 并发/pollTasks 500 task/spec blob 3k task）— [ ]

## 风险与止损
- **P1a 搬移遗漏守卫路径** → 守卫自检"扫零文件即红"兜底；`mise run lint` 全套跑
- **P1b 行类型上移冲突** → 单聚合回退不影响他聚合
- **P2 A 化行为漂移** → 该域暂留双声明形，kernel 双形态共存
- **P3 一批跨域** → 回退该批按耦合登记表重切；ensureMu/maintenanceMu 锁粒度不得顺手拆
- **并行会话** → 每次提交前 `git status` 复核暂存面；换装/构建必须干净 HEAD

## 纪律
小步提交（任务 ID 入 commit 头）；守卫先行先红后绿；golden 与用户可见变更同 commit；门禁必跑全套。
