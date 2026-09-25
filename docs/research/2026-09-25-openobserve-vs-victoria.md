# 评估：OpenObserve 替换 Victoria 系列（VL/VM/vmalert）

| 状态 | 日期 | 关联 |
|---|---|---|
| 已完成（裁决轮实录见 §5） | 2026-09-25 | [v0.3 规划](../plan/2026-09-23-v0.3-plan.md)；[观测设计](2026-09-22-observability.md)（V2-1 VL 默认捆绑 / D-W5-2 metrics opt-in）；[台账](../runbooks/image-prepull.md) #14-#17 |

## 0. 触发与结论

用户评估项（2026-09-25，W5 启动时追加）：能否用 OpenObserve 替换 Victoria 系列组件。**结论：两个角色分别裁决——默认日志面（VL）不可行（预算红线硬碰撞）；opt-in 观测面（VM 三件套+vmalert）可行但不值得（组件数不净减+查询面重写+能力缺口）。维持 Victoria 系列，OpenObserve 挂账为「观测高级层」候选，重评条件三件（§5）。**

## 1. OpenObserve 是什么（2026-09 现状，v1.0.4）

- 单 Rust 二进制全栈观测平台：logs+metrics+traces+UI+告警+仪表盘，单节点 SQLite 元数据+本地盘/S3，无 etcd/zk（HA 才要 PostgreSQL+NATS）。22.1k stars；OpenObserve Inc. 2026-04 完成 $10M A 轮；v1.0.0 GA 2026-09-11，补丁节奏极密（9 月 5 个 release，集中在告警与 PromQL 修复——收敛期）。
- 许可 **AGPL-3.0**（2023-11 从 Apache-2.0 转换）+ CLA + 企业版分割（SSO/高级 RBAC/审计日志/联邦搜索/脱敏/异常检测告警在企业面；ingest/存储/查询/告警/UI 核心在开源面）。对 fleetly 捆绑方式（不改源码、API 集成、拉官方镜像）无传染风险。
- 官方存储后端支持列表点名 **RustFS**（fleetly opt-in 对象存储在列）。

## 2. 关键实测与事实（决策主轴）

### 2.1 内存画像（本次实测，2026-09-25，v1.0.4，本机 docker）

| 形态 | idle RSS（75s 热身后） |
|---|---|
| 默认 env | **340.2MiB** |
| maintainer 调参配方（memtable 128MB/缓存 256MB×2/compaction 收紧） | **345.9MiB**（调参作用于负载期缓冲，不降基线） |

对照：VictoriaLogs 实测 **12.7MB**（台账 #14 实录），VictoriaMetrics 单机几十 MB 级。2024 年社区实测（v0.10.x）512MB 限制 OOM/1GB 报错/4GB 才稳——v1.0.4 基线已显著改善（340MB idle）但仍与 600MB 默认预算硬碰撞：**默认面 492MB − VL 12.7MB + OO 340MB ≈ 820MB，超顶 37%**。且负载期 memtable/查询缓存会再涨（默认查询缓存上限=系统内存 50%）。

### 2.2 协议与查询面

- 摄入：ES `_bulk` 兼容（`/api/{org}/_bulk`，Basic auth）——hub 推送路径协议层可直换；**但摄入失败静默返回 HTTP 200**（issue #1681），推送端必须逐条检查 items 错误。OTLP/Loki 端点另有。5 小时前旧数据拒收（ZO_INGEST_ALLOWED_UPTO 缺省）。
- 日志查询：**SQL 方言，不兼容 LogsQL**——SearchLogs 的 LogsQL 透传+短语转义防注入需整体重写为 SQL 生成器。
- PromQL：`/prometheus/api/v1/query|query_range` 可用（remote_write 摄入）；解析器停在 **Prometheus ≈v2.45 语义**（v2.45 后函数缺口，如 present_over_times v1.1.0 才补）；`/series` 无时间参数 500。**无 scrape 能力**——node_exporter/cadvisor 无法被其主动抓取，必须另配 vmagent/Prometheus/OTel Collector 做 remote_write。
- 告警：scheduled/real-time/composite/SLO burn-rate 内建，pending period+cooldown+dedup 有；**resolved/恢复通知缺失**（两个独立第三方集成文档一致，官方未正面声明）；通知目的地 webhook/Slack/Email 原生（与 fleetly 自有通道能力重叠）；异常检测=企业版。

### 2.3 运维面

- 单节点 local disk：数据目录 wal/db/stream/cache 文档化；**无官方备份文档**（整目录冷备属推断可行，SQLite 元数据与 WAL 时点一致性无官方说法）。TTL 全局 env（缺省 10 年，最小 3 天）。
- 升级史有两次架构级调整（v0.5.3 sled→SQLite、v0.10 集群元数据变更）+ v1.0.0 多项架构性移除——需钉版本逐级走。
- 多架构 manifest（amd64/arm64）可 digest 钉定；官方下载页现主推 enterprise 镜像（o2cr.ai），OSS 镜像在 Docker Hub/ECR 公共库。

## 3. 两角色适配度

### 角色 A：替换 VictoriaLogs（默认捆绑，预算敏感）——**否决**

预算数学是淘汰性的：12.7MB vs 340MB+（且负载期更高）。除非推翻 600MB 默认预算裁决（用户明裁 opt-in 豁免、默认面从严），否则不成立。查询面还要付 LogsQL→SQL 重写成本。协议兼容性再好也改变不了主轴。

### 角色 B：替换 VM 三件套+vmalert（opt-in，豁免预算）——**可行但不值得（v0.3）**

- 拓扑：OO 无 scrape →「node_exporter+cadvisor+vmagent+OO」四件 vs 现「三件套+vmalert」四件——**组件数不净减**，vmalert 换成 vmagent 而已。
- 换取的：单 UI 覆盖 logs+metrics+traces、内建告警（含 SQL 日志告警——vmalert 做不到）、composite/SLO。代价的：PromQL v2.45 兼容面收敛期、resolved 通知缺口、若日志面也并进来则 SQL 重写、升级钉版逐级走。
- v0.3 时点（vmalert 刚裁、metrics 面 v0.2 才稳定）换栈的迁移成本 > 收益。

## 4. 顺带收获（无论裁决如何）

- VL 的内存优势（12.7MB vs 340MB idle）再次实证——V2-1「VL 默认捆绑」裁决的数据背书。
- OO 官方支持 RustFS 作对象存储后端——若未来引入（观测高级层），与既有 rustfs 栈直接对接。
- 「摄入失败静默 200」与「导出管道首段失败掩蔽」（W4 mysqldump 教训）同属**管道错误传播**类坑——平台侧任何新后端接入都要检查逐项错误。

## 5. 裁决轮实录（2026-09-25）

| # | 议题 | 结论 |
|---|---|---|
| V3W5-E1 | OpenObserve 替换评估 | **维持 Victoria 系列**：默认日志面 VL 不动（预算红线）；opt-in 观测面 VM+vmalert 按既定 W5 计划走。OpenObserve 挂账为「观测高级层」候选（单二进制全栈+单 UI 的定位与未来付费增值面天然契合），**重评条件三件**：①v1.x 稳定期（告警/PromQL 修复节奏收敛）②idle RSS ≤150MB 或官方单节点内存基准发布 ③resolved/恢复通知补齐（或官方正面文档）。三者齐备可在 v0.4+ 立项「opt-in 观测高级层」（logs+metrics+traces 一站式，rustfs 后端）。 |

## 6. 数据缺口（留档）

- v1.0.4 负载期（10GB/天）RSS 未实测（本次只测 idle）——重评时补。
- resolved 通知现状无官方文档（二手×2）——重评时以 webhook payload 实测验证。
- PromQL 兼容矩阵无官方声明（v2.45 为二手口径）——重评时按 fleetly 实际查询子集逐条验证。
