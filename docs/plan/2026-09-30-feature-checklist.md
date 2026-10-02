# fleetly v1 功能清单（2026-09-30）

实现契约：每项带验收口径；批次验收 = 该批全部项打勾。上游依据：两份设计书、ADR 0001~0020、ADR-0012 dogfooding 能力清单。

**本期裁定（2026-09-30）**：①账号体系完整 Team/Role 从 N0 就有；②Git 集成 v1 = 通用部署 Token + GitHub 原生 webhook；③dogfooding 接管现有 staging 双节点（归档版退役，不做数据迁移）；④Console N2 最小只读观察面、N3 全功能。

**N0 收官（2026-09-30）**：F0.1~F0.25 全部 ✓（末三项 F0.12/18/19 于 N0 完结真机批勾选，真机证据见 docs/runbooks/staging-fleetly.md）。staging 双节点现役新 fleetly（F1.15 前哨已立）。

**N0 修复批（2026-10-01）**：验收 A/B/C 全清——5 真 bug（A1 实测不成立只补回归）+ 挂网/声明面/删除语义/漂移口径/EnrollNode 安全全落地；staging HTTPS 200（双视角）、e2e 三件套（dind-smoke/e2e:h2c/e2e:twonode）全绿、lint 0 issues + race 全过。**N0 判定翻"通过"**。下一步 N1 F1.1 幂等键执法批（E_IDEMPOTENCY_KEY_CONFLICT 届时入册）。

---

## N0 心脏（单节点可用 + 完整账号体系）

**安装与引导**
- F0.1 [x] 一行安装：`curl | bash` 检测/安装 Docker → 单节点 swarm init → fleetlyd（容器形态优先）→ 数据根初始化。验收：全新 Ubuntu VPS 一条命令到 fleetlyd healthy。
- F0.2 [x] 首启引导：Bootstrap Token 落盘（journal+文件，可吊销）+ 管理员初始化**全 CLI 可完成**（`fleetly init`）。验收：无浏览器环境可完成全部初始化。
- F0.3 [x] `fleetly quickstart`：样例应用 + sslip.io 零 DNS 域名 + 自动 TLS。验收：安装完成后 2 分钟内公网 HTTPS 可访问。〔2026-10-01 N0 修复批真机收口：B1 挂网（受管 traefik 挂全部活跃 Project 网络）+ quickstart 建网/挂网/路由幂等——staging 重装后 HTTPS 200（manager 与 node2 双视角，LE staging CA）；dind e2e（e2e:h2c）同链路钉死。N0.1 更正：该 e2e 的 h2c 腿在修复批是假阳性（断言被客户端自身 PROTO 行满足）——N0.1 换真 h2c 后端（e2e/h2cserver）+ 先行知识客户端后两跳锚真实成立，见 a182b2c〕
- F0.4 [x] `fleetly doctor`：Docker 版本/端口/磁盘/时间同步诊断，输出处置建议。

**账号与权限（完整 Team/Role）**
- F0.5 [x] User/Team/Role/Token 全模型：多用户、内置角色（owner/admin/member）+ 自定义 Role（Scope 集合）、邀请流（一次性链接）。验收：双用户双角色权限差异 e2e。
- F0.6 [x] Token 全套：创建/列出/吊销（含 Bootstrap Token）；`resource:action` Scope、write⇒read；前缀化随机串、sha256 存储、last_used_at 节流记录。验收：吊销后进行中请求的下一个调用即 401。
- F0.7 [x] 审计：全部写操作留痕（操作者/来源枚举 manual|api|cli|webhook|schedule/前后值指纹）。

**应用与部署**
- F0.8 [x] Project/App CRUD（API+CLI）；Spec 归一化两源：Compose 受控子集（白名单+受管字段显式拒绝+拒绝原因精确）与镜像直部署。
- F0.9 [x] Build（dockerfile 构建器）：BuildKit、控制面节点执行、并发上限可配、缓存本机、构建日志实时流。
- F0.10 [x] Deployment 状态机（preparing→building→releasing→observing→succeeded|failed|rolling-back→superseded）+ admission 队列：同幂等键/commit 去重、latest-wins、显式 supersede、queue_full 反馈、排队与在途可取消。
- F0.11 [x] 回滚 = Revision Replay 一等动词（`fleetly rollback`）+ Revision diff（`fleetly revisions diff R1 R2`）。
- F0.12 [x] 健康门与优雅退出：http/tcp/exec 探针 + L1 门 + L2 看门狗 + L3 观察窗（60s 默认）；SIGTERM 宽限；被杀后按 Generation 幂等重放（场景 1/2 回归）。
- F0.13 [x] Git 触发：per-App 通用部署 Token URL（可再生成、token 不进 URL 路径段日志）+ GitHub 原生 webhook（push 自动部署、`[skip deploy]`、watchPaths monorepo 过滤、HMAC 验签+delivery 去重）。

**网络与路由**
- F0.14 [x] per-Project overlay；跨 Project 默认隔离；`egress:none` 声明（swarm v1 弱隔离，明示）。
- F0.15 [x] Edge：traefik 受管自宿（通用 ManagedProvider reconciler 首个实例）+ Route（host/path/port + protocol http|h2c|tcp + TLS 模式）+ LE HTTP-01 自动证书 + 默认 sslip.io 域名。验收：h2c 后端路由可通（messageloop 形态）。〔2026-09-30 staging 真机补验：LE staging CA 经 HTTP-01 签出 n0.dev.fleetly.run；2026-10-01 修复批：h2c 端到端在 dind e2e 钉死（客户端协商 h2c + 后端收到 HTTP/2.0 双证明）；生产 CA 轮换另批〕
- F0.16 [x] Volume：受控子集 + 默认钉住节点（Placement 以平台节点 ID 为锚）。

**材料与安全**
- F0.17 [x] Secret/Config：age 信封加密（KEK 数据根、轮换 runbook）、值永不回显回指纹、注入 App/Database；Config 版本化可回读、配额。
- F0.18 [x] 镜像凭证分发：私有 registry 凭证存 Secret，Ensure 解析后按节点分发，不落 label/明文 env。验收：私有镜像双节点拉取成功（场景 13）。〔2026-09-30 staging 真机：zot+Secret 双节点 Running、node2 镜像落位、载体零凭证〕

**集群（Runtime）**
- F0.19 [x] swarm Provider 全契约：Ensure/Remove/Watch/Addresses/DescribeCluster/Enrollment + 节点身份锚定（铸造/写回/`node.joined`）+ RuntimeLogs 子面 + RuntimeAdmin（drain/cordon）。〔2026-09-30 staging 真机：RuntimeAdmin 面（drain 实迁移/cordon/uncordon+审计）+ 双节点 e2e（场景 13 部署、enroll 重组链、node.joined/left 事件）〕
- F0.20 [x] 多节点就绪：`fleetly nodes enroll` 输出加入材料。验收：双节点部署同一 App、卷钉住正确（双节点 e2e 随 dind 套件批回归）。〔2026-10-01 修复批：e2e/dind-two-node.sh 在册——nodes enroll 材料真实 join、双 process 部署跨双节点、卷钉住进程全落钉住节点且约束锚平台节点 ID；mise 任务 e2e:twonode〕

**API/CLI 面**
- F0.21 [x] proto 单源：gRPC + REST gateway + OpenAPI 生成；buf breaking 门禁进 CI。
- F0.22 [x] CLI 核心：init/login/projects/apps/deploy/rollback/logs/events/secrets/configs/routes/nodes/tokens/audit——全命令 `--json`（golden 双形态钉死）+ 稳定退出码 + 错误信封（errcode+处置提示）。
- F0.23 [x] 事件地基：Outbox+seq 落库、`fleetly events list`；流式 follow 在 N1。

**工程守卫（先行）**
- F0.24 [x] CI 守卫全套：编排器 SDK 仅限 providers/、model/spec 叶子纯度、禁词扫描（ADR-0007 清单）、errcode/eventcode 注册表三链咬合、CLI golden、e2e dind smoke。

**可观测最小**
- F0.25 [x] `fleetly logs`：运行/构建日志经 RuntimeLogs 直读（时间窗/tail/容器过滤）；持久化检索 N2 前诚实标注"仅实时+最近缓冲"。

## N1 Agent 面 + torchwood 线（验收 = ADR-0012 能力清单全绿）

- F1.1 [x] 幂等键：创建型 RPC + CLI `--idempotency-key`；同键同体重放、异体 409、24h 保留。〔2026-10-01 按 ADR-0024 三段落地：①Idempotency-Key 头 + 单表（key→指纹→响应引用→24h）+ 拦截器一份实现覆盖全部 15 个创建型 RPC + janitor（engine.NewLoop 骨架）；DeployRequest.idempotency_key 降级部署专锚（双源不一致 409）；CLI 14 动词 --idempotency-key 透传；守卫 TestIdempotencyCoversCreateVerbs 反扫（红灯实验过）②webhook 去重收口（Q-21：gateway 按 delivery 派生键重放；台账随效果同事务；duplicate 状态退役）③受理位 + 统一写原语（Services.commit：受理检查→聚合写→事件→审计一事务唯一拥有者；29 处手写编排归零；配额读入事务收口 TOCTOU；守卫 TestAcceptanceWritePathsGoThroughCommit + TestNoHandRolledTxChoreography）。附带修一隐性破损：grpc_test.go 自 6d8ef07 起拼错 statertest 从未被净编译（热 build cache 掩盖），本批 go clean -cache 炸出后修复〕
- F1.2 [x] 事件流 follow：SSE + gRPC stream + `fleetly events follow --json`；断档 410 + 快照重同步。〔2026-10-01 按 ADR-0026 两段落地：①outbox 保留窗契约（EarliestSeq+TrimBefore 7d，retention janitor 并入唯一 Loop 骨架；seq AUTOINCREMENT 不复用）+ E_EVENTS_GONE（REST 410；窗清空=任何正游标判档）+ GetEventStatus 重同步基准 + StreamEvents（重放+follow 250ms 轮询）+ CLI events follow（--replay 有界形态进 golden）②SSE 原生入口 /v1/events/follow（EventStreamSource 与 gRPC 面同源同口径；IssueEventTicket 60s 单用途票据；断档预检先于 200 出 410 信封；15s keepalive 帧喂代理）。残留真机项：lynx 优雅关停超时对流式长流的行为（staging 实证后闭 ADR-0026 锚）〕
- F1.3 [x] Wait 原语：WaitDeployment/WaitBuild/WaitRun + CLI `--wait`。〔2026-10-01 落地：WaitDeployment/WaitBuild 为 server-streaming 状态快照帧（事件流过滤实现——复用 F1.2 的 subscribeEvents 单一订阅核心，outbox 事件只是信号、行重读是真源；首帧=当前行快照，终态帧后收流；流面豁免 unary 超时）；CLI deploy/rollback --wait（帧渲染 + 非 succeeded 终态非零退出）；quickstart 私有轮询收编（F-15：120×1s 轮询→等待原语，且帧跟自己那条部署而非"App 最新一条"）。WaitRun 随 Run 实体（F1.5/F1.6）落地，不预发空面〕
- F1.4 [x] 能力自描述：`fleetly explain <资源>` / `fleetly schema`（JSON Schema 反射生成）。〔2026-10-01 落地：internal/schema 叶子包（Go 类型反射 → draft-07 子集；事件 payload 走 json-tag 反射 + Spec 走 protoreflect——字段名与 protojson UseProtoNames 同一 snake_case 面；注册表"扩展面各自注入合并"= capability.RegisterFactory 同款 init 自注册）+ SystemService.GetSchema/Explain（PUBLIC 零文档发现面；schema_json=canonical 紧凑 JSON）+ CLI schema/explain 双形态 golden。ADR-0026 两锚闭：assembly golden 漂移门（selfdescription.golden.json，payload 字段改名/删除 CI 红）+ eventcode 完备性双向对账（新事件入册漏 schema 即红）。identity 面 map payload 具名化（wire 字节不变）。apitest 夹具补挂 SystemService（与生产 NewGRPCServer 对齐）〕
- F1.5 [x] Task API：one-shot/resident 创建（镜像直部署、Variable+secretRefs、TTL、资源上限）、期望并发数、排空停止、列表/watch、per-Task + per-Run 双级稳定 DNS。〔2026-10-01 按 ADR-0025 四段落地：①契约加宽（NamespaceRef Task 轴、Workload Restart/StopGrace/Addressing、WorkloadEvent ExitCode/Reason/Instance、completed/failed 终态、TaskSpec.form、JobSpec 重塑嵌 ProcessSpec——旧标量字段 buf breaking FILE 档下标弃用保留、taskGroup 翻译责任归 engine 投影层、DNS 铸名公式住 engine TaskDNSName/RunDNSName、swarm never→none/别名映射/Task 域命名 fleetly-run-<id>/终态任务观测可见）②state 层（00008 迁移：tasks/runs 表 + CAS 状态机 + 七枚举停止原因 + 绝对 deadline）③engine 驱动环（补足/排空/TTL janitor/停止兜底/属主吊销拉式排空 P1-8/Ensure 签名跳过 + 周期重放/观测两轨 C3+P1-7 独立缓存组/one-shot 终态镜像/DeleteTask 载体收口）④API+CLI（automation 上下文 proto：Tasks/Runs 服务 + WaitRun 收口 F1.3 尾巴 + after_* 游标 ADR-0026 首个新 List 面 + 幂等覆盖 + tasks scope 入册；CLI tasks/runs 动词组双形态 golden）。列表/watch 的 watch = 事件订阅面（task.*/run.*/lease.* 15 事件入册 + schema 咬合）。Variable 本批为 env 直传（两级变量实体合成随 ADR-0027 专属批）；firstBootJobs 解锁接线随部署链批（执行机制已备）〕
- F1.6 [x] Owner Lease：`fleetly task renew`（RenewTask）+ 宽限；lease_expired 排空并补足；属主吊销 → 名下 Task 宽限排空（可配置跑完 TTL）。〔2026-10-01 随 F1.5 同批落地：RenewTask 推进绝对 deadline（ADR-0018 墙钟续算，默认步长 30s）+ 宽限（默认 90s，超宽限 drainTask：停补足 + 存量 Run stopping/lease_expired + lease.expired 事件）+ 复活语义（draining/drained → active，补足随 active 恢复——"排空并补足"）；属主吊销走 task 环周期扫 revoked Token 拉式排空（重启安全；Task 行记 owner_token_id 引用非明文），TaskOwnerRevokedRunToTTL 选项切"跑完 TTL"模式（ADR-0017 默认宽限排空）；CLI `fleetly tasks renew`〕
- F1.7 [x] Schedule：带时区 cron（ADR-0018）、手动触发、重叠 skip 策略。〔2026-10-02 按五段落地（393b410→ce3224a 五 commit，全门禁绿）：①state（00009 迁移 schedules 表：cron_expr + IANA timezone 随行 + 冻结 TaskSpec 模板 + next_fire_at 绝对时刻 + last_task_id 重叠锚；Fire CAS 双发防御；robfig/cron/v3 选型——只用 Parser+Next 不用 runner（ADR-0018 禁进程内计时器），@descriptor 拒绝、time/tzdata 嵌入防环境漂移）②engine（scheduleStep 单写者环：到期拍从模板铸 one-shot Task——补足/观测/TTL/终态镜像全复用 F1.5 链，Schedule 只拥有"何时拍"；三项裁决入 ADR-0018 附录 A：错过窗口补跑一拍不追补、重叠 skip 固定策略（Run 状态为真源）+schedule.skipped 事件、手动触发不移节奏 + 重叠诚实拒绝；铸出 Task 无名/系统属主）③API（automation 上下文 SchedulesService 5 RPC：Create 走受理位 + ValidateTaskTemplate（新 spec helper：task ref 缺席由行提供身份锚）+ 首拍按注入时钟铸出；scope 复用 tasks 资源（Runs 先例）；幂等覆盖 CreateSchedule；ListSchedules after_* 惯例；Delete 幂等 tombstone 不停已铸 Task）④CLI（schedules create/list/get/trigger/delete 双形态 golden——时区换算在 golden 可见：东京 12:00=03:00Z）⑤事件三链（schedule.created/fired/skipped/deleted：eventcode + schemareg + 四 golden 同 commit）。开放锚：长周期真机昼夜观察随 F1.15〕
- F1.8 [x] 互通：Task Network Group（创建时刻挂靠）+ App Process 跨挂（`taskGroup:<name>`）+ 跨 Project 双向声明/批准/即时隔离。〔2026-10-02 按六 commit 落地（466f070→CLI 批，全门禁绿）：前置批 = ADR-0028 四项——Team 轴接实（appTeam/taskTeam/resolveBackend/activeProjectNetworks 全部从 Project 行实取 team_id，projectTeam 单一真源；守卫 TestEngineAssemblyNoDefaultTeamLiteral AST 级反扫；engine 测试夹具补播 Project 行）+ idx_projects_name → (team_id,name) 加法迁移（先建后删同事务）+ Q-16 roleInTeam 受理检查（CreateUser/CreateToken/CreateInvitation 三面；内置角色平台级任意 Team 可授）+ user repo FK RESTRICT → ErrConflict 归一；本体 = ADR-0013 附录 A 四裁决——A.1 载体 NetworkPeer 独立表（network_peers，pending→approved→revoked，非 revoked 行间 (network,peer) 唯一，撤销后新声明进新审批环）+ NetworksService 5 RPC（scope 复用 networks 资源，Schedules 先例）+ 双方审计（每拍网络侧+挂靠项目侧两行）；A.2 引用形态 `project:<id>/<name>`（spec 叶子只校验形态：26 位大写平台 ID 段 + 非空名段）；A.3 投影 strict/isolate 双模式（strict：Submit 受理预检 + prepare 双层 fail-closed；isolate：未批准引用剥离）+ NetworkRefs Namespace.Team 从目标 Project 行实取；A.4 撤销即时隔离 = 断存量（RevokeNetworkPeer 落账后引擎 isolate 重收敛剥离附件——swarm service update 全量替换断存量连接；在途部署 App 不抢 Ensure（驱动器 fail-closed 至终态）；漂移扫描拍隔离不变式自愈兜底 + 重启基线重放 isolate 模式）；事件三拍 network.peer_declared/approved/revoked 三链咬合；CLI networks declare/approve/revoke/peers 双形态 golden（declare 幂等键 --json 轮重放同响应）。真机锚：swarm 跨服务 alias DNS RR 随 F1.15 dogfooding 实证〕
- F1.9 [x] 治理刹车：per-Project Task/Workload 数量配额、per-Token 创建速率、change freeze（按资源/动作封禁，拒绝带原因）。〔2026-10-02 按五 commit 落地（65da953→旋钮批，全门禁绿）：裁决 = ADR-0017 附录 A（④属主吊销宽限排空已随 F1.6 落地不重做；④'=schedule 重叠旋钮修订 ADR-0018 A.3）。①配额——engine.MaxTasksPerProject=100/MaxTaskConcurrencyPerProject=200（受理位 CreateTask + ScaleTask 事务内增量 + 到期拍 spawn 事务内三面执法；超限到期拍 skip reason=quota_exceeded、手动拍诚实拒绝）+ maxAppsPerProject=50 + task repo StatsByProject/app CountByProject；F1.5 挂账的 desired_concurrency sanity 100 收口为项目总量配额。②速率——internal/governance RateLimiter：内存固定窗 60s/120 次 per-Token，动词面=idem.EnforcedMethods 编译期同源（豁免联动），仅实际执行计数（拦截器位于幂等之后，重放不耗预算），E_RATE_LIMITED 新码（429 + RetryInfo detail→Retry-After 头，apperr 新 WithRetryAfter）；拒绝不落事件/审计（重试风暴自放大防护）。③冻结——change_freezes 表 00012（team_id=''=全局行，每 scope 一活跃行部分唯一）+ GovernanceService（system 包 governance.proto，platform:write/read，Set 进幂等面）+ governance 拦截器（authn 后幂等前；Team 解析自请求字段逐级回行——project/app/task/schedule/network/peer/route/hook token 八链，ReceiveWebhook 纳入封禁面否则冻结漏 GitHub push；行不存在放行交受理位）；豁免=停止族/RenewTask/runtime 运维/identity 全部（冻结不得把自己锁门外）；E_CHANGE_FROZEN 新码（409/FailedPrecondition）+ freeze.set/lifted 事件三链 + freezeguard 全 proto 方法三桶分类反扫（frozen/exempt-with-reason/read 前缀）+ CLI freeze set/lift/list 双形态 golden（set 幂等键让 --json 轮重放）。④'旋钮——AppConfig.Engine.schedule_overlap_policy（skip 默认|fire 并行拍，ParseScheduleOverlap fail-fast 无效值启动红，手动触发恒诚实拒绝）+ config-example 文档。附带：authn.WithIdentity 导出（拦截器链下游注入面）〕
- F1.10 [x] build-from-upload：流式 tar + 大小上限。〔2026-10-02 落地（全门禁绿），裁决 = ADR-0019 附录 A：①契约——BuildsService/UploadSource 是仓内首个 client-streaming 写面（首帧 meta.project_id + 余帧 chunk tar 流，EOF 内容寻址落库，gRPC-only 无 HTTP 注解——先例 ReceiveWebhook；REST/Console 面随 Console 批次）+ ListUploads（after_upload_id+limit，ADR-0026 惯例）+ DeployRequest 第三源 upload_id+dockerfile（互斥执法、归属同 Project 校验、blob 在盘校验 E_UPLOAD_UNAVAILABLE/410；spec.UploadDeploy 与 gitDeploySpec 同构）。②流式执法链——流式链从仅 authn 升为 authn→freeze：FreezeGuard.Stream() 首帧 RecvMsg 包装执法（Team 锚=meta.project_id，reqField 升级点路径导航嵌套字段；拒绝以首帧读取错误面呈现、handler 零字节写入）；幂等表显式豁免（Upload 不在创建型前缀集，内容寻址天然幂等强于键重放——同 digest 返回同一引用 deduplicated=true，无 24h 窗）；速率预算不计数（unary 面；流面刹车=限额）；无 unary 30s 超时（15m 流硬上限）。③存储——internal/upload blob 面（DataRoot/uploads/<sha256hex> 全局内容寻址 tmp+原子 rename，跨项目共享 blob refcount=行数）+ source_uploads 表 00013（(project_id,digest) 唯一，同项目同内容重传返回同行）+ tar 安全解包（tar-slip 防护：绝对路径/../ 反斜杠/symlink/hardlink/重复条目拒绝，白名单=普通文件+目录，解包总量计数）+ engine 构建链（blob→contexts/<revision> tmp 解包后 rename 幂等落位，与 git 检出同位）。④限额——512MiB 单上传（E_UPLOAD_TOO_LARGE/413 诚实信封带 limit 与已收字节，Q-11 教训）+ 4GiB 项目存量（E_QUOTA_EXCEEDED，distinct digest 求和读在 finalize 事务内）。⑤保留窗——retention janitor 扩三面：7d 未引用行清扫（引用=Revision spec 含 upload id 字面量——Revision 冻结先于 Submit，部署路径天然受保护）+ 末行删除时 blob GC + 孤儿 tmp（>1h）清理。⑥CLI——deploy --from-dir（GetApp 回行 project → 确定性 tar（固定 mtime/uid/gid、.git 不进上传）→ 流式上传 → Deploy）+ uploads put/list 动词组（组 golden 进清单）。事件三链 upload.stored + 守卫喂食（FrozenVerbs + UploadSource；errcodeToHTTP 显式 413/410；AssertAllRegisteredHavePolicy builds:write 注解）。firstBootJobs 执行接线继续挂账 F1.11 部署链批（机制已备）〕
- F1.11 zot 受管自宿：多节点镜像分发（digest/tag 直存）、构建推送目标。
- F1.12 Database 最小集：postgres（含 percona/pgvector 模板）+ redis 模板、默认本地备份目标开箱即用、连接串注入 Secret、`fleetly databases` 命令组。
- F1.13 首批 Skills：`skills/`（deploy-diagnose / task-pool / database-provision），随版本演进说明。
- F1.14 构建器扩展：railpack（钉版本）+ static。
- F1.15 **dogfooding 上线**：torchwood/messageloop 从零部署（staging 双节点重装已提前于 N0 完成，见 docs/runbooks/staging-fleetly.md——归档版退役、新 fleetly 现役）。验收：ADR-0012 清单逐项打勾 + messageloop 经 h2c Route 对外服务 + dispatcher 池租约/补足/回收全语义真机回归。

## N2 数据观测信任 + 最小 Console

- F2.1 Database 全矩阵：mysql/mongo 模板、版本升级路径、跨节点迁移。
- F2.2 备份深化：restic、外置 S3 目标、保留策略；**恢复演练**（verify + 试恢复到临时实例）为验收必过项。
- F2.3 平台升级工具：升级序（Platform Backup 前置→替换→goose 前滚→Managed Provider 逐个 reconcile→解除只读）+ 失败回滚路径；**升级零扰动 e2e**（用户 Workload 零重启/路由零中断，ADR-0015 验收）。
- F2.4 Logging Provider：VictoriaLogs 受管自宿；日志持久化、检索（时间/文本/容器过滤）、脱敏。
- F2.5 Metrics Provider：victoria 系受管自宿；基础图表（容器 CPU/内存）+ 阈值告警 + 通知通道（webhook/telegram 起步）。
- F2.6 最小只读 Console：部署状态/日志/事件流三页（React+Vite，仅消费公共 API）。
- F2.7 dbtemplate 目录化 + 镜像 digest 钉定门禁（DT-9）。
- F2.8 ObjectStore：外置 S3 兼容配置 + RustFS opt-in 自宿；未配异地持续告警"同机备份非灾备"。

## N3 体验全量

- F3.1 Console 全功能：全部读写操作、终端页、quickstart 向导 UI 化、审计/设置页。
- F3.2 exec 子面：RuntimeExec + 反向中继（节点零入站端口）+ `fleetly shell` + Web 终端（票据鉴权、限额、审计）。
- F3.3 模板库：模板 schema（语义变量/自动生成密码/域名）+ 一键部署 + CDN 热更新 + 竞品迁移钩子（`create-from-dokploy` 形态）。
- F3.4 Git 集成扩展：GitLab/Gitea 原生 webhook（届时按需求确认）。

## N4 第二运行时试点

- F4.1 k3s Provider：场景 3 验收（无状态全语义保持 + 有状态 Backup/Restore + 显式数据处置；egress:none 升级为 NetworkPolicy 强隔离）。

## Backlog（v1 外，触发条件见对应 ADR）

promote 跨项目原语（ADR-0011 触发条件）；预览环境（同上）；自动伸缩；独立构建节点/多节点构建缓存（ADR-0019）；SSO/2FA；GitLab/Gitea 深度集成与 PR 预览；旧版数据迁移工具（本期裁定：不需要）。

---

**重量提示**：完整 Team/Role 进 N0 后，N0 ≈ 原 N0 + 0.5 批；实施顺序建议 F0.24（守卫）→ F0.19/21（地基）→ 部署链 → 账号 → 安装引导，安装向导类（F0.1-F0.4）可随 N0 尾部收口。
