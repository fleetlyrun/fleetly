# ADR-0065: 负载指标词汇（Workload Metric Vocabulary）

日期：2026-10-11；状态：已采纳（Console App Metrics 不可读走查驱动，同批落地）。

## 背景

staging 走查实证（torchwood App Metrics 页，2026-10-11）：图表图例整面叠字、数值列表按 5 为周期复读，完全不可读。根因链三层，只有一层是前端 bug：

1. **渲染键碰撞（bug）**：console `seriesKey` 以 `s{index%5}` 为图表键——超过 5 条序列即键碰撞：数据点互相覆写、图例按 5 为周期复读、React key 重复渲染错乱。torchwood 一窗内有 55 条序列，必然炸。
2. **序列身份无契约（机制缺口，本案主根因）**：平台在翻译层给一切载体打了 `fleetly.process`/`fleetly.workload.id`/`fleetly.ns.*` 身份标签，cadvisor 也如实导出（真机实证：app 域 55/55 序列 `container_label_fleetly_process` 在场）——但 metrics 域从查询构造到图例展示从未消费它们。产品面序列的唯一可读标识是 cadvisor 的 swarm 容器全名（`fleetly-default-<projectID>-<app>-<workload>.<slot>.<task随机尾>`），前端截断后只剩节点前缀 + 随机尾巴，与 Processes 页的服务名（冻结 Spec process 名）完全对不上。CPU percent 还要在查询侧对 `machine_cpu_cores` 做 `group_left` 多对一除法（F-B1：裸 `on(node)` 被 VM 拒 422），查询语法脆且每个消费方各自手搓。
3. **序列生命周期无概念**：查询按 app 标签全量命中——死任务容器（多代部署残骸，cgroup 未 GC 前一直被 cadvisor 采集）与活副本同权重进窗。即时查询仅 6 条活序列 vs 范围查询 55 条，产品面无任何归属/披露机制。

## 决策

1. **词汇由采集环归一导出（L1，本案核心）**：`metricsStep` 每拍抓取 cadvisor 原文后，同源解析出**负载词汇样本**并随拍导入 VM（`job="fleetly-workload"` extra_label，`node` 同款归因）：
   - `fleetly_container_cpu_percent`——差分率/节点核数×100（gauge，percent of node）；
   - `fleetly_container_cpu_cores`——差分率（gauge，核数）；
   - `fleetly_container_memory_working_set_bytes`——gauge 直取。
   标签集 = 域主体恰一（`app`/`task`/`database`/`browse`）+ 身份三元组（`process`/`workload`/`container`）+ 导入侧 `job`/`node`。射程 = 一切带 `fleetly.workload.id` 的载体（App/Task/Database/Browse/受管系统域全收）；渲染按容器名行序固定。**percent/cores 在引擎差分面预计算**——消费侧查询免 `rate()`/`group_left`，F-B1 故障类从构造上消失。告警评估面（`containerSample`，app 域）语义原样不动；CPU 差分基面两条消费路径共用、恰一次推进。
2. **raw cadvisor 照旧透传（工作台诚实边界）**：全局 Metrics 工作台、CLI `metrics query`、e2e 断言继续消费原始 cadvisor 序列——词汇是**产品面消费契约**，不是存储层替换。proto/API 零变更（词汇经既有 PromQL 透传流转）。
3. **产品面预设切词汇（L2）**：console `METRIC_PRESETS`（app 域）与 database 载体预设的 docker 臂切到词汇查询；k3s 载体臂保留 k8s 形态原文（词汇当前只覆盖 engine 抓取的 cadvisor docker-label 形态——诚实边界见下）。
4. **图例身份归属 + 生命周期披露（L3）**：`buildChartRows` 显示名优先 `process`（词汇与 raw cadvisor 标签双形态识别），同名副本以容器名尾判别；图表键全序列唯一（`s0..sN`），配色按序循环；序列按「窗口尾 5 分钟活跃优先、次按峰值」排序，超出显示上限的死副本计数显式披露（"+N stopped replicas hidden"）——不静默丢数据，也不让残骸淹没活副本。

## 不变量

- 词汇指标名与标签集是注册表：引擎常量单源 + 双端测试钉死（engine 金形态 + console preset 断言），改名即 ADR。
- 告警评估语义零变化：`containerSample` 射程仍是 app 域；词汇导出不参与规则评估。
- raw 透传零变化：`job="fleetly-cadvisor"` 序列族原样；工作台/CLI/e2e 断言不动。
- 值诚实：差分首拍与计数回零拍 cpu 指标缺席（非零值）；percent 无核数观测缺席。

## Consequences

- App/Database 图例与服务名对齐（Processes 页同源），副本可判别；死残骸退居披露行，活副本前排。
- 消费侧查询退化为单行等值匹配，无除法、无 join、无 422 类。
- 词汇序列存量自部署起累积：预设图表在换装后有一个采集节拍起的空窗（诚实断口，15s 自愈）。
- 存储面增量：2-3 条 gauge × 平台容器数 × 15s 节拍——与 raw cadvisor 每容器数百序列相比可忽略。

### 诚实边界

- **词汇只覆盖 engine 抓取的 docker-label 形态节点**：k3s 载体（namespace/pod 标签形态）不在词汇射程——database 预设的 k3s 臂、工作台 k3s 查询继续走 raw 形态；k3s 词汇化挂账。
- **行级过滤责任仍在查询构造方**：`QueryMetrics` 是 PromQL 透传，console 自带 app 标签过滤（ADR-0041 原裁决）。词汇不改变鉴权姿态——「服务端拥有的命名视图 + 行级授权」是后续独立批的候选，不随本案顺手扩。
- **死副本披露上限是显示策略不是数据策略**：窗口内全部序列仍在 VM 可查（工作台/CLI 自由查询）；产品面隐藏的只是图表线与图例行。

## 验收锚

- [x] 引擎：词汇金形态（指标名/标签集/行序/幂等）+ database 域只进词汇不进告警样本 + 首拍 cpu 缺席 + percent 预计算——`TestParseCadvisorWorkloadVocabulary`、`TestParseCadvisor`（夹具扩展双域载体）。
- [x] 引擎：词汇随拍导入（raw + 词汇双导入，失败 warn 下拍重试）——`metricsStep` 接线，`TestMetricsStepCadence` 节拍锚回归。
- [x] console：app/database 预设查询落词汇指标名（含 k3s 臂保留）——`metric-presets.test.ts`、`database-metrics.test.ts`。
- [x] console：键唯一（>5 序列无碰撞回归锚）、process 身份归属、同名副本判别、死副本披露——`metric-chart.test.ts` 扩展。
- [ ] raw 透传零变化：e2e dind 断言（`container_cpu_usage_seconds_total{job="fleetly-cadvisor"}`）原样绿——raw 导入路径零改动，随 CI e2e 常态回归复核。
