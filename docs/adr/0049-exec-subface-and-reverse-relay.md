# ADR-0049: exec 子面——RuntimeExec 契约、反向中继（节点零入站端口）、票据 WS 终端、限额与审计

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-06 | 架构 §5（RuntimeExec 子面预留行）/§7、ADR-0007（词汇冻结）、ADR-0026（票据先例）、ADR-0014（材料分发敏感度）、ADR-0017（freeze 语义边界）、ADR-0016/0024（受理位纪律）、ADR-0025（Workload 观测）、ADR-0035（Team 轴）、ADR-0044（Console 消费面纪律）、ADR-0048（守卫纪律范式）、F3.2（checklist） |

## 背景

F3.2 落 exec 子面：进入运行中的 Workload 载体开交互式会话（Console 终端页 + CLI
`fleetly shell`）与非交互单命令（`fleetly exec`）。架构 §5 早已钉形："RuntimeExec
子面；经反向中继，节点零入站端口（继承 execrelay 模式）"。设计留白在本 ADR 终审：
Runtime 契约扩面形态、中继通道承载、节点中继的装载与鉴权、Web 终端的双向传输与
票据、限额与审计词面、Scope 词汇。

盘点事实（决策依据）：

- **节点→manager 通道为零**：现役三条链全部 manager→集群单向（swarm Watch 事件
  流 / 周期 DescribeCluster 快照 / cadvisor 指标拉取）。无任何 node→manager 出站
  长连可复用——"经既有 watch/观测流回传"不成立：Watch 是 manager 对 docker API
  的订阅，没有节点回程；SSE/NDJSON 是消费面单向流，不承载会话帧。
- **docker exec 是节点本地操作**：exec API 只能落在持有目标容器的 daemon 上；
  manager 侧 API 无法代发到远端节点（swarm 无 exec 代发面）。控制面 Utility 容器
  （ADR-0039）"控制面节点单机假设"不覆盖 exec 的多节点面。
- **swarm 自身就是反向拓扑**：worker 节点出站拨 manager:2377 加入集群；swarm join
  token 是"活材料等价集群成员权"的既有 C3 敏感度锚（Enrollment rotate 即轮换）。
- **gateway 已为长连接预备**：读写超时 0 + 保守读头超时的注释明言"SSE/WS 落地
  前"；原生 handler 三先例（uploads/hooks/events SSE）确立"root mux 精确路径 +
  gateway 回落"挂载族。全仓零 WebSocket 使用——本批为首例。
- **staging 网络形态**：gateway :9081 明文 HTTP、仅 VPC/内网（runbook 端口表）；
  公网 TLS 在 traefik 终结。明文 VPC 出站假设与现役形态一致。
- **Enrollment 命令是唯一节点触达面**：EnrollKit.Command 在节点上人工执行（节点
  零平台安装物原则的边界操作位）；swarm manager 广播地址可从 NodeList 观测。

## 决策

### 1. Runtime 契约：RuntimeExec 子面三方法，会话编排住 engine

```go
type RuntimeExec interface {
    // ExecTarget 管理侧实时解析：WorkloadID 的在跑实例与所在节点。
    ExecTarget(ctx, workloadID string) (ExecTargetInstance, error)
    // ExecWorkload 节点侧执行：在本地 daemon 解析载体并 exec（tty/管道/resize）。
    ExecWorkload(ctx, req ExecWorkloadRequest) (exit int, err error)
    // RunNodeRelay 节点中继循环：拨号 manager gateway、握手、收发会话帧。
    RunNodeRelay(ctx, o NodeRelayOptions) error
}
```

- **窄面纪律维持**：Runtime 只贡献编排器原语（解析/执行/中继循环），会话语义
  （受理、限额、审计、票据、路由、超时）全部住 engine——exec 会话不是资源状态
  迁移，不进 Runtime。未实现子面 = 诚实失败 `E_EXEC_UNSUPPORTED`（与
  NetworkMaintenance"未实现诚实失败"同文化，但 exec 无降级路径，直接拒绝）。
- **ExecTarget 创建时实时解析**（docker 任务列表快照）：观测缓存不参与决策的纪律
  在 exec 同样适用——选首个 running 实例与其实际节点；解析结果（instance/node）
  进会话元数据回显（Console/CLI 头行展示实际命中实例——诚实）。实例漂移（解析后
  载体迁移）不自动重试：会话与载体实例绑定，漂移即诚实失败、客户端重开会话。
- **载体解析住节点侧**：平台永不解析载体命名（§5 铁律）——manager 只发
  （workloadID, instance），节点中继在本节点按平台标记定位容器。
- **目标身份**：会话请求以 App + Process 表达；engine 投影出 WorkloadID（自身
  期望集），Provider 完成（实例, 载体节点 ID）解析，载体节点 ID→平台节点 ID 经
  nodes 权威锚定表反查（与 Watch 锚定同一张表）。

### 2. 反向中继：新增长连通道，WebSocket over 既有公共 gateway

**否决复用观测流**（无回程，见背景）；**否决节点入站端口**（exec 通道 = 任意命令
执行面，节点开监听端口是不可接受的攻击面；NAT 节点也根本不可达）。采纳**出站
长连**：节点中继拨 manager 既有 gateway（零新端口双向成立——manager 端口已开，
节点零入站）。

- **承载 = WebSocket**：本批唯一需要双向流的新面（Web 终端），一条传输同时服务
  节点中继（`GET /v1/relay`，Authorization: Bearer join token）与 Console 终端
  （`GET /v1/exec/stream?session_id=&ticket=`，票据鉴权）。CLI 走 gRPC bidi
  （`StreamExecSession`），authn 拦截器常规鉴权——与 events 面"gRPC 流 + SSE
  原生"双径同构，此处为"gRPC bidi + WS 原生"。
- **节点中继装载 = Enrollment 面扩展**（EnrollKit only-add 字段 `RelayCommand`）：
  生成的幂等脚本在节点上以 busybox:1.37 载体容器运行 `fleetlyd relay` 子命令——
  二进制经 `GET /v1/platform/binary`（原生入口，join-token 鉴权，服务
  os.Executable 字节）下载后 `docker cp` 注入，docker.sock 挂载，restart=
  unless-stopped 自愈。不触节点宿主文件系统（节点零平台安装物的语义边界维持：
  平台产物只在容器层）。脚本幂等（先 rm -f 旧中继）——同时是中继升级/修复的
  重跑通道。
- **鉴权 = swarm join token**（worker 或 manager 皆可）：与 swarm join 材料同
  敏感度同生命周期（C3：活 token 等价集群成员权——中继持 docker.sock 本就是
  节点 root 等价，凭证敏感度对齐）。`Enrollment rotate` 已轮换双 token → 旧
  中继失联直至重跑 RelayCommand = 泄漏处置语义（runbook 记录升级/轮换序）。
  握手经载体节点 ID（docker info）→ 平台锚定表反查平台节点 ID 绑定连接；未锚
  定（刚加入）则中继退避重试。
- **manager 节点 = 进程内回环中继**：控制面节点自身也是 exec 目标（swarm 默认
  调度 manager），daemon 启动即以回环 WS 连自身 gateway 注册为本地节点中继——
  单节点部署零 Enrollment 也具备 exec 面（dind 冒烟腿依赖此路径）；每次重连从
  本地 SwarmInspect 取新 token（rotate 后自愈）。
- **传输安全口径**：明文 HTTP over VPC/内网假设（与 staging 现役 9081 形态、
  swarm 2377 自身同款）；join token 是安全承载。前置 TLS 代理（traefik WS 透
  传）部署形态成立时加配置面（届时 only-add）。
- **帧协议**（版本内稳定，只增）：
  - 会话流（client↔manager；gRPC oneof 与 WS 二进制帧同构）：stdin/stdout/
    stderr（原始字节）/resize（cols,rows）/exit（code）/error（code,message）/
    meta（instance,node_id）。
  - 中继流（manager↔节点中继，单连接多路复用）：首帧文本 hello{carrier_node_id,
    relay_version}；二进制帧 = [1B kind][26B session ULID][payload]，kind 分
    open/ack/stdin/stdout/stderr/resize/exit/error/close。

### 3. 票据：ADR-0026 的 exec 版（秒级单用途，绑会话）

`CreateExecSession`（Bearer + scope `exec:write`）受理即铸造（60s TTL、单用途、
绑定 session_id）；浏览器 WS 无自定义头——与 EventSource 同理走 query 票据
（`?session_id=&ticket=`）。票据存储泛化为 purpose+payload 形态（events 面行为
零变化）。CLI 走 gRPC 不经票据。一个会话至多一个消费端附着（附着竞争即拒绝）。

### 4. 限额与审计

- **限额**（编译期常量，先例 maxAppsPerProject 族）：per-Team 并发会话上限 8
  （`E_QUOTA_EXCEEDED` 拒绝信封）；空闲超时 10min（无输入输出帧）；硬 TTL
  30min。到点收口 = 终止会话 + error 帧明示原因。
- **审计**：`exec.session` 行（四件一拍，受理事务内）+ Detail 列新增（迁移
  only-add：进程/实例/节点/命令/argv/tty——审计无命令面则 exec 审计无牙）；
  同时 outbox `exec.session_opened` 事件（安全可见性：谁在何时进入了哪个进程
  ——Console 活动与 Agent 订阅面同源）。会话结束不落第二行（会话是流不是资源
  行；台账 = 受理审计行 + 活体连接）。
- **freeze 豁免**：exec 不变更资源状态（ADR-0017 刹的是变更型动词），freeze 窗
  内 exec 照常——诊断面恰在冻结窗最需要。语义边界注记进 ADR-0017 关联。
- **Scope**：新 resource 词 `exec`（`exec:write` 开会话；无独立读面 v1——
  会话不可列表不可回读，审计面承载事后查询）。

### 5. 词汇与 CLI 形态

- **词条**：**Exec Session**（进入运行中 Workload 载体的会话；tty 交互或 one-shot
  命令。Avoid: ssh, tunnel, remote shell）与 **Relay**（节点中继到控制面的出站
  长连通道，节点零入站端口。Avoid: tunnel, mesh, agent net）入 CONTEXT.md。
- **CLI 双动词**：`fleetly shell <app>/<process> [-- <argv>]`（交互 TTY，缺省
  argv /bin/sh；原始终端模式 + SIGWINCH resize 传播）与 `fleetly exec
  <app>/<process> -- <argv>`（非交互单命令，输出流式、退出码透传、`--json`
  NDJSON 帧双形态——golden 钉死）。
- **Console 终端页**：xterm.js 本批引入（ADR-0044 预留点）；进程选择复用 catalog；
  断线重连 = 新会话新票据（会话不迁移）；票据/WS 消费为手写客户端（原生入口不进
  swagger——webhook/uploads 先例）。

## 后果

- 节点面出现平台持有的常驻容器（fleetly-relay，docker.sock 挂载 = 节点 root
  等价）——敏感度与 swarm 自身构件（dockerd/swarmkit）同面；凭证（join token）
  泄漏 = 集群成员权泄漏，rotate 处置路径复用。Enrollment 命令从"一句 join"长为
  脚本块（golden 更新）。
- 平台升级后节点中继二进制滞后（旧容器继续跑旧版）：帧协议只增容忍；中继版本
  经 hello 上报、节点视图回显（honest 观测）；升级序入 runbook（重跑
  RelayCommand 即刷新）。
- exec 进程随会话终止尽力收口（close → hijack 关闭 + exec kill 尽力）；载体
  侧孤儿进程（收口失败）不追踪——载体内进程本就是用户域。
- WS 是全仓首例：浏览器 ALPN/h2 回退行为依赖现代浏览器（HTTP/1.1 upgrade 回退
  通行实现）；Go 侧（中继/CLI）不经浏览器路径。staging 走查含 WS 契约锚。
- audit Detail 列是通用面（后续批动作详情可复用）；Audit 查询面/Console 页透出。
- RuntimeExec 三方法使 capability 断言面扩一子面（fakes 同批扩展）；FakeRuntime
  提供确定性 exec（echo argv/固定退出码）——CLI golden 与 apitest 契约经 fake
  驱动全链（真 docker 链路在 e2e 与 staging）。

## 验收锚（2026-10-06 实施批全勾；实录锚 file:line）

- [x] proto：runtime/v1 ExecService（CreateExecSession 注解面 + StreamExecSession
  bidi）+ scope 注解 exec:write；errcode 两新码（E_EXEC_UNSUPPORTED /
  E_NODE_RELAY_OFFLINE，Source 锚点 + suggestion）+ eventcode
  exec.session_opened + schema golden + usage 反扫同步
  （proto/fleetly/runtime/v1/exec.proto 全文；internal/model/errcode/codes.go
  E_EXEC_UNSUPPORTED/E_NODE_RELAY_OFFLINE 条目；events.go exec.session_opened；
  fleetlygrpc/schemareg.go 注册；codes/events/self-description golden 同批）
- [x] capability RuntimeExec 子面 + swarm 实现（ExecTarget 实时解析 / ExecWorkload
  tty+管道+resize / RunNodeRelay）；未实现子面诚实失败（E_EXEC_UNSUPPORTED）
  （internal/capability/exec.go 契约 + 帧协议单源；providers/swarm/exec.go
  ExecTarget/ExecWorkload/ExecClusterToken + noderelay.go RunNodeRelay 重连环；
  faces.go Exec 入 FacesOf/Offered）
- [x] 反向中继：/v1/relay WS（join-token 鉴权、载体 ID→平台 ID 锚定绑定、多路
  复用帧路由）+ /v1/platform/binary 原生入口 + EnrollKit.RelayCommand 幂等脚本
  + manager 回环中继（单节点零 enroll 可 exec）
  （internal/assembly/gateway_exec.go 三原生入口——relay 凭证先于升级校验；
  providers/swarm/runtime.go nodeRelayScript（形状钉板 exec_script_test.go）；
  engine.Options.RelayLoopbackURL 回环中继 + provides.go 派生）
- [x] 受理面：CreateExecSession 四件一拍（audit Detail 列 + exec.session_opened
  事件）+ per-Team 限额 + 空闲/硬 TTL 收口（apitest 契约：票据 TTL/单用途/
  限额拒绝信封/未实现子面）（fleetlygrpc/exec.go；engine/exec.go 限额/TTL/
  慢消费端收口；apitest/exec_test.go 五件——gRPC 全链/WS 票据/拒绝信封/
  Team 限额/坏凭证；00025_audit_detail 迁移）
- [x] CLI `shell`/`exec` 双形态 golden（fake 驱动全链：帧流/退出码透传）
  （cmd/fleetly/cmd/verbs_exec.go + exec_golden_test.go 六 golden + 退出码
  透传 exitCodeFor；进程内假中继 apitest/execrelay.go = 帧协议第二消费方）
- [x] Console 终端页（xterm + 票据 WS + 断线重开 + 进程选择）+ 帧编解码 vitest +
  dist 同 commit（console/src/pages/Terminal.tsx + api/execstream.ts +
  lib/execFrames.ts + execFrames.test.ts 六件；console:verify 零漂移）
- [x] e2e：dind-smoke exec 腿（回环中继全链）+ dind-two-node exec 腿（worker
  中继 enroll + 双节点 exec + tty 交互）（e2e/dind-smoke.sh exec 节——
  relay_online/exec/退出码/shell/审计五锚本地 SMOKE PASSED；dind-two-node.sh
  exec 节——RelayCommand 原样执行/双节点 relay_online/卷钉住+spread 双 exec/
  幂等重装本地 TWO-NODE E2E PASSED）
- [x] staging 真机：worker RelayCommand 落地 + 双节点 CLI exec/shell + WS 契约
  （票据 401/握手）+ Console 终端页走查（HTTP/消费契约级）（runbook 2026-10-06
  记录·十一：双节点 relay_online + worker TTY 交互 shell 实测 + WS 101 升级/
  单用途 401 + 审计/事件 + W1 同日修复——Provider 哨兵未映射 E_INTERNAL 吞错因，
  capability.ErrExecNoRunning 单源收口）

## 附录 B：词汇裁决——节点侧端点定名 node relay，agent 一词收归 AI Agent（2026-10-08）

ADR-0007 词汇冻结的显式出口（ADR-0047 Edge→Proxy 同范式）：**agent 是产品
核心语汇**（CONTEXT.md 首句"人类与 AI Agent 同为一等用户"——Token 服务人与
Agent、事件是 Agent 订阅面、治理刹车为失控 Agent 而设），本 ADR 原文把节点侧
中继端叫 "relay agent / 节点代理" 是与该核心语汇的撞车。裁决：节点侧端点定名
**node relay（节点中继）**，一次性全库更名（含 proto 契约、wire 键、golden、
console 再生成），守卫防回流（wording guard banned 复合形态；reviews 历史走查
快照与本文附录的旧名载录进白名单）。

映射表（旧 → 新）：

| 面 | 旧 | 新 |
|---|---|---|
| proto/REST | `EnrollNodeResponse.agent_command` | `relay_command` |
| proto/REST | `Node.relay_agent_version` | `relay_version` |
| capability | `RunRelayAgent(RelayAgentOptions{AgentVersion})` | `RunNodeRelay(NodeRelayOptions{Version})` |
| wire hello | `agent_version` | `relay_version` |
| capability | `AgentHello / AgentFrame* / AgentSession* / Marshal·Parse·Encode·DecodeAgent*` | `RelayHello / RelayFrame* / RelaySession* / …Relay*` |
| engine | `RelayAgentAttach / RelayAgentWriter.WriteAgentFrame / agents 表 / relayAgentConn / ErrExecAgentOffline / dropAgentConn / deliverAgentFrame·sendAgent / agentGoneFrame / agent_disconnected` | `RelayAttach / RelayWriter.WriteRelayFrame / relays 表 / relayConn / ErrExecRelayOffline / dropRelayConn / deliverRelayFrame·sendRelay / relayGoneFrame / relay_disconnected` |
| errcode | `E_NODE_AGENT_OFFLINE` | `E_NODE_RELAY_OFFLINE` |
| providers | `relayagent.go / agentConn / agentSession / agentFrameWriter / relayAgentScript` | `noderelay.go / relayConn / relaySession / relayFrameWriter / nodeRelayScript` |
| 词汇 | 节点代理 / relay agent / 回环代理 | 节点中继 / node relay / 回环中继 |

兼容边界（一次性，无兼容层——ADR-0047 同判）：

- **REST 字段更名**（`agent_command`/`relay_agent_version`）：字段号不动，
  buf breaking 门禁内通过；消费方（e2e sed 抽取、runbook、Console 再生成）
  同 commit 更新。
- **wire hello 键更名**（`agent_version`→`relay_version`）：升级窗内旧 worker
  中继上报的旧键在新 manager 读为空版本——恰落入"滞后中继照常受理、版本仅
  观测"的既有语义（帧协议只增；重跑 relay_command 即刷新），无行为面变化。
- k3s 外部命令 `k3s agent`（上游子命令）与 docker swarmkit 自身构件的
  agent 是外部词汇，不在更名射程。
