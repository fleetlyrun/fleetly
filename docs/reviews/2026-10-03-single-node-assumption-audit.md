# 单机假设审计（P3）——2026-10-03

来源：`docs/design/2026-10-03-optimization-proposals.md` P3 裁决（N2 开工前一次性）。动机：zane 的单机假设渗透到健康检查（读本节点容器 DNS）、端口探测（busybox 绑 `0.0.0.0`）、volume 测量（`docs/research/2026-10-03-competitor-architecture-deep-dive.md` §4 G4）——这类假设不炸单机、专炸多节点。fleetly 双节点 e2e 已存在，但没有系统性的假设清单。本审计逐面核对"隐含控制面所在节点"的代码路径，处置三选一：**多节点安全（补断言）/ 隐式假设（修复归宿）/ 显式单机裁决（记录边界）**。

## 发现清单

### A.【隐式假设·真缺陷】StreamLogs 容器发现 = manager 本节点视角

- **位置**：`internal/providers/swarm/logs.go` `listNsContainers` —— `p.cli.ContainerList`（注释自述"docker 直连形态"）。
- **机理**：swarm 的容器（runtime 对象）是 per-node 的——manager 的 docker API 只返回**本节点**容器；service/task 才是集群对象。调度到 worker 节点的 Workload 容器不在列表里，日志流**静默缺失**（不报错，就是没有那个 Workload 的帧）——zane 同款病灶。
- **影响**：双节点起，`fleetly logs --app`（及未来 Console 日志页）只见 manager 侧副本。当前 staging 现役双节点，日志面处于半瞎状态而无人察觉（真机八件未覆盖跨节点日志）。
- **处置**：修复归宿 = **F2.4 Logging Provider 批**（VictoriaLogs 受管自宿会重做日志采集管线——容器级采集面天然多节点）。F2.4 落地前的诚实边界：*日志流仅覆盖 manager 节点容器*（写入 F2.4 条目注记）。`e2e/dind-two-node.sh` 已补现状下限断言（manager 侧副本日志可见），F2.4 落地后升级为两节点全覆盖断言。
- **不现在打补丁的理由**：per-container 流改为 `docker service logs` 聚合是 F2.4 要重做的同一个面，先补丁后重做是二次动土。

### B.【多节点安全】exec 探针 = swarm 原生 healthcheck

- **位置**：`internal/engine/projection.go` `resolvedHealthcheck` → Workload.Healthcheck.Exec；dbtemplate `Probe()`（`pg_isready` 等）同通道。
- **机理**：探针翻译为 swarm service 的原生 HealthConfig，由 **daemon 在 task 所在节点执行**，L1 门读 task 状态健康面——"DB 探针引擎原生"（F1.15 修复批）的语义。控制面不做任何本机 docker exec。
- **处置**：安全；staging 真机锚⑥（pgvector/redis 跨节点）已实证。

### C.【多节点安全】全部集群对象 API 面

- Watch 的 `TaskList`（L1 就绪权威数据源）、节点锚定扫描（`NodeList` + ULID 铸 ID）、Ensure 的 `ServiceCreate/Update`、`InspectWorkloads` 的 spec 对照（`ServiceList`）、孤儿 Secret 清扫（`ServiceList` 现役集比对 + 集群 Secret 删除）、`DescribeCluster`、`Addresses`（VIP 集群面）——全部经 manager API 操作**集群对象**，节点拓扑透明。双节点 e2e 的 spread/pin 断言已覆盖调度面。

### D.【显式单机裁决·合法】构建恒在控制面节点

- ADR-0019 既定裁决（BuildKit + 本机 daemon，多节点构建缓存延后）。升级路径已在提案 P12（工具链容器化触发条件）挂账。不属违例。

### E.【显式单机裁决·合法】控制面数据根三件

- SQLite/上传 blob（`internal/upload` 内容寻址落盘）/KEK 密封密钥全在控制面数据根——"控制面单点诚实暴露"口径（架构 §8）的组成部分，Platform Backup 承载。不属违例。

### F.【多节点安全·挂账已记】zot 受管自宿的 Placement 钉住

- zot 是集群调度的 Workload + 卷；F2.3 已挂账"受管域 Placement 钉住收口"（F1.15 尾注：受管 zot 无钉住，spec 变更滚动替换可把 registry 漂到无卷节点 preparing 打转）。现状 = runbook 临时操作序（drain node2 不可长持）。已有归宿，本审计不新增处置。

### G.【多节点安全】跨 Project 互访与 Task 网络组

- 网络挂靠/别名（ADR-0013/0034）是 swarm 集群网络语义；跨进程 DNS 经 swarm 服务发现（节点无关）。mlbridge→torchwood 跨 Project 挂靠在 staging 双节点现役（F1.15 真机）。安全。

## 结论

- 违例清零：唯一真缺陷是 **A（StreamLogs 本节点视角）**，修复归宿 F2.4（挂账 + e2e 现状断言钉边界）。
- 双节点 e2e 补断言：`dind-two-node.sh` 尾部新增"日志流覆盖 manager 侧副本"下限断言（发现 A 的现状锚）。
- 附加产出：P2 注入守卫核对中确认 git clone（`--` 终结 + 禁 ext/file 传输）与 traefik 规则内插（ValidateRouteHost/Path 双面白名单）两处执行/定界面防线已在位（安全批遗产），P2 增量为 shellguard 静态红线 + image ref 字符级校验。
