# ADR-0091：控制面自身日志的 CLI 可达面（自轮转镜像文件 + 服务端直读 RPC）

日期：2026-11-03 ｜ 状态：已实施（`ls docs/adr/` 复核 0091 空闲）

## 背景

torchwood 事故盘查（2026-11-03，hook-pull compose 通道出生即坏）实证了 CLI 排障面的两个断头：

1. **错误消息吞底层 err**：受理错误在 `os.ReadFile` 失败分支只给结论不给 errno，`WithCause` 又是服务端日志专属——操作者拿着 CLI 报错与 journal 都无从定位（最终靠 strace 服务端进程收口）。
2. **daemon 自身日志无 CLI 读径**：fleetlyd 的 zap JSON 行（含 lynx 拦截器的 `gRPC request failed` 行：method/duration/error，与 `E_INTERNAL` 出口的 cause 全文 + `error_id` 关联）只落 stdout→journald。VictoriaLogs 只收 `runtime`/`build` 两类用户面日志，`fleetly logs --text` 看不见控制面自己。AGENTS.md 已裁定「fleetly CLI 一律本地执行」——SSH 到控制面主机仅留给 CLI 覆盖不到的面，而控制面日志恰是最大的这样一面。

**用户裁决（2026-11-03）**：诊断通道不得依赖 VictoriaLogs——VL 自身是受管组件（RestartComponent 面的排障对象），被诊断面与诊断通道共命运不可接受；VL 挂 = 无法盘查。方案 = daemon 把自身日志镜像到**数据根下自轮转日志文件**，新 RPC 从服务端直读该文件。

## 决策

1. **自轮转镜像文件，不碰 VL**：`<DataRoot>/logs/fleetlyd.log`，常量轮转界 **16MB × 4 份**（.log + .log.1~.log.3，共 ~64MB 上界）。写入侧 = slog tee（见决策 2），读取侧 = 新 RPC 直读文件（见决策 4）。不扩 `LogKind` 词汇（`runtime`/`build` 二值冻结），不进 VL 入库批处理器——**控制面日志长期留存列为后续可选批**（若未来接入，走独立通道决策，不回头改本面）。
2. **tee 镜像 best-effort，主日志径零风险**：logger provider（lynx `WithLoggerProvider` 是普通 `func(AppContext) (*slog.Logger, error)`，零上游改动）包一层 tee handler——inner = lynxzap 原始 handler（stdout/journald 行为零变化），side = 镜像文件追加写。side 侧纪律：**异步单 goroutine 消费有界队列**（1024 行）；队列满/写失败/盘满 = 丢弃计数（日志镜像永不阻塞、永不 panic 主进程）；递归在结构上不可能（镜像写径不经 slog）。**无 boot 预挂缓冲必要**：provider 是 logger 创建点，先于一切日志行存在（OpenMirror 在 provider 内同步完成）。镜像级别面自持 Info+（不委托主 handler——主径整面停用形态〔测试夹具 DiscardHandler〕会让镜像一并失明，apitest 实咬）；身份字段（service.name/version/id）由 provider 从 lynx.Meta 注入固定前缀，每行随行。
3. **读取面语义**：tail N（0 = 窗内全量）/ follow（文件增长尾随，轮转感知）/ since-until 时间窗（解析 JSON 行 `ts` 过滤）/ text 子串过滤（大小写敏感，logs 面检索路径同口径）。响应帧 = `time`/`level`/`msg` 投影 + `line` 原始 JSON 字节（零改写——method/duration/error/error_id 全量字段保留，CLI `--json` 原样透传）。**`error_id` 与 `E_INTERNAL` 出口闭环**：CLI 报错打印 error_id → `platform logs --text <error_id>` 直达 cause 全文。
4. **RPC 与授权**：`PlatformService.StreamPlatformLogs`（`fleetly.system.v1`），server-streaming。授权双门 = `platform:read` 注解（authn 拦截器）+ `RequirePlatformAdmin` 腰带（handler 首行，403 先于一切副作用与 404/503——跨队可见面，ListPlatformBackups 与 `ListPlatformIncidents` 蓝图同口径：显式不采用 GetStatus 的已认证即见口径）。镜像文件缺失（数据根无 logs/）= E_LOG_STORE_UNAVAILABLE 诚实失败，不与「无日志行」混淆。
5. **错误面补强（同批根修）**：受理/映射错误在底层 err 在手时不得丢弃——`webhook.go` compose 读取分支嵌 `%v` + `WithCause`（staging 实咬点）；authn 拦截器 `roles.Get` 两处做存储故障分诊（Q-24 同口径：DB 瞬断不再伪装成「角色不可用」的认证拒绝/引用损坏）；token/nonce 生成家族七处对齐 `WithCause`。这是本 ADR 的**前置同类根修**：日志可达后，若错误消息仍吞 cause，盘查闭环依然缺一角。
6. **诚实边界（本面不做到的，防回头重议）**：本面覆盖「daemon 在跑但内部失准」的盘查；**daemon 起不来的 boot 级故障仍归 SSH/journald 面**（自己读不了自己的日志是结构性边界，GetStatus 匿名面是另一条腿）；日志镜像非审计面（有丢弃语义，best-effort）；多节点控制面（当前单节点）不假设聚合——文件是本机视角。

## 后果

- 控制面排障动线从「SSH + journalctl + strace」收敛为 `fleetly platform logs`（本地执行，AGENTS.md 律内）；错误三件套（错误消息带 cause、error_id、`platform logs` 直查）构成闭环。
- daemon 主日志径（stdout/journald）行为零变化；镜像侧任何故障只损失镜像自身。
- 数据根新增 `logs/` 目录：平台备份/restic 面是否收录该目录**排除**（日志是易失诊断面，非状态——Platform Backup 语义不因本 ADR 扩容）。
- 守卫/测试连坐（同批）：writeclassguard READ 行 + 台账计数 bump + ADR-0081 附录行；scopeguard Stream* readVerbs 核对；apitest 授权矩阵（member/他队 admin 403、default 队 owner 通过，403-before-503）+ 端到端锚（触发必败 RPC → 流回含 method 与 error 的失败行）；CLI golden 双形态；`fleetlyd` 重启后 tail 仍见重启前行（文件存活性断言）。

## 验收锚（本地已验勾选）

- [x] tee 镜像字段形态（ts/level/msg 小写、duration 秒浮点、fixed 身份字段、error → 文本）+ 队列满丢弃不阻塞（Drops 计数断言）+ Close 排空落盘（mirror_test 5 用例；boot 无预挂缓冲必要——provider 是 logger 创建点，先于一切日志行）。
- [x] 轮转：字节界触发、4 份上界（.log.4 不出现）、最近 MaxFiles 份保留语义（最老顶出）、跨轮转行数守恒（mirror_test 小界加速）。
- [x] reader：tail/since/until/text 组合 + 半截行容忍 + 跨轮转拼接旧→新（read_test）；follow 新增投递、轮转感知续读、ctx 取消（read_test）。
- [x] RPC：镜像缺失 = E_LOG_STORE_UNAVAILABLE（与空流分立，apitest 钉 Recv 形态）；授权矩阵 403-before-404（三类非平台身份，镜像目录已删仍 403）；端到端锚（标记者行 → tee → RPC text 过滤取回，error_id 保真）。
- [x] 守卫四件连坐（writeclassguard 77 READ + ADR-0081 附录/统计 160 同批 / AssertAllRegisteredHavePolicy / scopeguard Stream 前缀既有）。
- [x] CLI `platform logs` 双形态 golden（零帧 NDJSON 豁免登记，行渲染由 apitest 端到端锚钉——镜像行带真实时间戳不可逐字钉）；`--json` = 原始镜像行 NDJSON 原样透传（protojson 会 base64 包 bytes，弃用）；`--error-id` 糖 → `--text`（错误三件套闭环）。
- [x] 真进程端到端实跑：本地 fleetlyd 起服 → 镜像即捕 boot 日志（身份字段随行）→ `hooks refresh` 必败 → `platform logs --text "request failed" --tail 1 --json` 直达失败行（method=/fleetly.delivery.v1.HooksService/RefreshGitHook + duration + error 全量）。
- [ ] `fleetlyd` 重启后 tail 仍见重启前行（文件存活性由文件形态结构性保证——重启不丢是文件 vs 内存 ring 的决定性差异；dind 断言随 staging 换装批补）。
