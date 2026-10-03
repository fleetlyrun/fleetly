# ADR-0034: App Process 网络别名 = 进程名（compose 服务名互访语义补全）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-02 | ADR-0013（网络成员模型）、ADR-0025 决策 6（Addressing→Provider 别名映射，Task 先例）、ADR-0033（compose intake）、F1.15 dogfooding 评估（torchwood 六服务互访） |

## 背景

多服务 App（torchwood：redis/minio/server/worker/dispatcher/packer；messageloop：
redis/messageloop/mlbridge）的服务间互访靠 compose 服务名（`http://dispatcher:9070`、
`redis:6379`）。新平台的 App Process 载体 DNS 名是 swarm 服务名
`fleetly-<team>-<project>-<app>-<process>`——嵌入平台 ULID（单段 26 字符，
超 swarm 63 字符上限即截断+哈希），compose 作者期不可知也不可读。归档仓
v0.1 有"服务别名 = compose 服务名"语义（dokploy DNS 对齐），新仓未建；
F1.15 dogfooding 评估确认这是多服务栈的必备缺口。App Process 投影目前
**不携带 Addressing**（Task 域有双级 DNS，ADR-0025 决策 6）。

## 决策

1. **App Process 的网络别名 = 进程名**（`projection.Project` 给每个 App
   Process Workload 携带 `Addressing=[{Name: <process name>}]`；Provider
   既有的 Addressing→网络别名通道原样消费——swarm alias per attached
   network）。compose 服务名即进程名（归一化面恒等），**compose 内按服务名
   互访自此成立**——与 dokploy/原生 compose DNS 语义对齐。
2. **挂靠域 = 该 Workload 声明的每张网络**：别名随网络附件生效（swarm
   原生 per-network alias）；Task 域先例同款。未声明网络的 Process 无别名
   （无网络即无 DNS 面）。
3. **同名进程跨 App 共享网络 = DNS RR**（swarm 别名原生语义——多个载体
   共享别名即轮询）。诚实标注而非拒绝：平台网络成员模型（ADR-0013）本就
   允许多 App 挂靠同一项目网；单栈单网络（每栈独占一张项目网）是部署
   纪律，平台不执法。dogfooding 形态：torchwood 与 messageloop 各自项目
   各自网络；跨栈互访走 F1.8 peer 声明/批准（mlbridge → torchwood server）。
4. **不是 API 面**：Task 双级 DNS 是平台 API 可见的稳定名（Task 行
   dns_name）；App 进程别名是网络内可达性面，不进任何 API 消息/golden。
   平台 DNS 名词汇不扩（ADR-0007）。
5. 受管域（Edge/zot/Database 载体）不适用：受管载体寻址已有专有公式
   （db-\<id\> 等），不参与用户别名面。

## 后果

- 存量部署下次 Ensure 滚动获得别名（spec 指纹变化一次）；行为零破坏
  （别名是新增可达名，不改既有名）。
- 真机件①（swarm 跨服务 alias DNS RR）的实证对象就位：同别名多载体
  （resident 池 Runs 的池级名 + 跨 App 同名进程共享网络）均落此机制。
- compose 互访文档口径：服务名互访仅在同网络内成立；跨 Project 引用
  `project:<id>/<name>` 附件上**本服务别名不传播**（Provider 既有注释：
  跨域附件不带别名），目标侧服务在己方网络的别名可解析。

## 验收锚

- [x] projection：App Process Workload 携带 `Addressing=[{Name: 进程名}]`
  （engine 投影测试断言）〔461c05f：internal/engine/projection.go 单点
  `w.Addressing = []capability.Address{{Name: p.GetName()}}` +
  projection_task_test.go（TestProjectTranslatesTaskGroupRefs）断言〕
- [x] Provider 侧别名映射既有测试覆盖（translate_task_test/translate_test
  Addressing→Aliases 先例）；App 域经同一通道无新分支〔toServiceSpec 单通道
  （internal/providers/swarm/translate.go `addressAliases`，逐附件网络落
  Aliases）；translate_task_test.go TestTaskDomainLifecycleMapping 断言
  Addressing→Aliases 排序稳定、translate_test.go 确定性夹具携带 Addressing；
  461c05f 零 Provider 改动=无新分支实证〕
- [x] 存量 golden 零漂移（别名不进 API 面）〔461c05f 改动面=ADR +
  projection.go + 投影测试三文件，零 golden 触碰；全量 cmd golden 绿（该
  commit 门禁）〕
- [x] staging 真机：torchwood 栈内 `redis:6379`/`http://dispatcher:9070`/
  `http://minio:9000` 按服务名互访实证（随 F1.15 dogfooding）〔runbook
  docs/runbooks/staging-fleetly.md 真机八件①：栈内服务名互访
  （redis/minio/dispatcher/server）全靠本 ADR 别名，2026-10-02〕
