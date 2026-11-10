// Package metrics 是指标采集+告警域应用模块（F2.5/ADR-0041 决策 2/3）：每节拍
// （15s 粗节拍——tick 1s 内自持锚）抓取各节点 cadvisor 端点（host 发布
// 8080），原文透传 VM（平台归因 extra_label）+ 同一遍在手法评估阈值规则
// （内存最新样本——评估零查询依赖）。状态迁移沿才通知（fired/resolved
// 各一次）。
//
// 诚实边界（ADR-0041）：评估是 15s 采样近似（for-window 连续性按采样序
// 判定；pending 起算点落 alert_rules.pending_since 列，重启续窗——B2a
// W1b 起「重启重置 pending 面」边界收窄为采样连续性近似）；节点下线 =
// 该节点序列停写（VM 断口诚实呈现）。
//
// 域文件分组（P3-2 收编，729 行拆四）：本文件持采集循环骨架（节拍/抓取/
// 解析）与域实例态；exposition 组装/标签/解析纯函数族见 exposition.go；
// 告警评估面（规则迁移/事件）见 alerting.go；通道派发面见 channelnotify.go；
// crash-loop 通知投递面见 crashnotify.go。
//
// P4 wave-1b（ADR-0088 决策 2）：采集+告警域接收者下域，Engine 嵌入
// metrics 应用模块 struct。P5 wave-2（决策 2 拆包形态，2026-11-01）：嵌入
// 解开——应用模块独立成包，engine 核经构造注入持有（New 装配面），循环表 step
// 经应用模块方法值（Step）、gRPC AlertRule/Channel API 面与 observ 的
// crash-loop 评估点经核门面/应用模块导出面；依赖显式逐字段注入 + 内核端口
// （观测窄接口闭包族——签名为基础类型/域包类型，函数字段类型若引 engine
// 类型则改用基础类型，ownerOf/ownerTeam 收敛为 appOwnerOf）；containerExitFact
// 与 DiagnoseWindow 事实契约单源 engine/contract。engine 去应用模块 import
// 第五批（2026-11-10）：engine 核只持 contract.Metrics 接口，本模块经
// contract.MetricsFactory 由组合根装配注入——核不再 import 本包。禁回引
// *engine.Engine（enginebackref 跨包执法）。
package metrics

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
)

// 采集循环参数（循环本地常量；无运维面诉求——同 logging 循环纪律）。
const (
	// metricsScrapeInterval 是抓取节拍（与 cadvisor --housekeeping_interval
	// 对齐）。
	metricsScrapeInterval = 15 * time.Second
	// metricsScrapeTimeout 是单节点抓取上界。
	metricsScrapeTimeout = 10 * time.Second
	// cadvisorPort 是受管 cadvisor 端点的宿主发布端口（Provider 声明同源）。
	metricsCadvisorPort = 8080
	// systemOffsiteRuleID 是系统内置规则 id（platform-offsite-backup——
	// F2.2 挂账消化：s3 未配持续告警；不落行、不可删）。
	systemOffsiteRuleID = "platform-offsite-backup"
	// systemOffsiteFor 是内置规则的持续窗（s3 缺席满一天才轰——升级窗口
	// 与临时摘仓不误报）。
	systemOffsiteFor = 24 * time.Hour
)

// Metrics 是采集+告警域应用模块实例态（C5：实例内聚合，不进包级全局；P3-2
// 收编命名 metricsDomain → metrics，P4 wave-1b 嵌入改形，P5 wave-2 拆包
// 导出更名）。行为随域、依赖显式逐字段 + 内核端口注入（New 装配面，
// engine construct.go 组合根经 MetricsFactory 调用），禁回引 *engine.Engine
// （enginebackref 跨包执法）。Metrics capability 与域自有 repo（alertRules/
// channels——transact.Stores 注入，ADR-0088 决策 3）随应用模块持有；
// diagnose 域的规则只读消费经 AlertRuleRepo 访问器（engine 核门面委托）。
//
// 锁面边界：mu 只护跨 goroutine 的 sysOffsite 状态对（采集循环写、gRPC
// handler 经 SystemOffsiteAlertState 读）；map 面（lastCPU/cores/
// lastScrapeAt）由采集循环单写者纪律承载（metricsLoop 单 goroutine 触达），
// 不加锁。用户规则的 pending 起算点不在此处——B2a W1b 起直接落
// alert_rules.pending_since 列（重启续窗）；仅内置系统规则（不落行）保留
// daemon 生命期内存面。
type Metrics struct {
	// lastScrapeAt 是节拍锚（15s 粗节拍在 1s tick 内自持）。
	lastScrapeAt time.Time
	// lastCPU 是容器 CPU 计数器差分基面（cadvisor counter → 率）。键 =
	// node|containerID。
	lastCPU map[string]cpuCounter
	// cores 是节点核数观测（cpu_percent 的分母）。
	cores map[string]float64
	// sysOffsiteAbove 是内置规则的越限起始（daemon 生命期内存面）。
	mu               sync.Mutex
	sysOffsiteAbove  time.Time
	sysOffsiteFiring bool

	// 显式依赖（实测消费面，逐字段注入）：metrics 是受管指标存储 Provider
	//（nil = Metrics 面停用，采集循环空转零噪音）；runtime 是集群视图
	//（DescribeCluster）；repos 是只读聚合目录（crash-loop 沿 marker 扫描
	// 经 Outbox；eventTeamOfApp 的解析入参）；alertRules/channels 是域自有
	// repo（规则行写与通道行——行写经自有 repo，非 transit 家族）；cipher
	// 是通道配置解封；log/clock 域内日志与节拍。
	metrics    capability.Metrics
	runtime    capability.Runtime
	repos      transact.Stores
	alertRules alertrule.Repo
	channels   channel.Repo
	cipher     *material.Cipher
	log        *slog.Logger
	clock      state.Clock
	// offsiteConfigured 是内置规则的只读事实端口（s3 外置仓已配；闭包逐拍
	// 读核 opts——live 读语义由核侧闭包承载，测试形态经注入闭包翻转）。
	offsiteConfigured func() bool

	// 内核端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// emitObservation 是 emitter 端口（emit* 前缀名使 enginefuncregistry
	// 判据零漂移）；eventTeamOfApp 是 emitter 侧 team 解析器；appOwnerOf
	// 是 obs 域归属解析的 App 域窄面（P5 wave-2 基础类型化：函数字段类型
	// 不引 engine 类型——App 域过滤在核侧绑定闭包内裁决）；workloadProcess
	// 是 obs 域导出面（进程名——diagnosbypass 契约测试的 obs 私有缓存唯一
	// 合法出口）；rootCtx/spawn 是派生根和排水纪律端口（logpipes 同款：
	// Add/Done 配对单源留核）。
	emitObservation func(ctx context.Context, team, name, aggregate, id string, payload []byte) error
	eventTeamOfApp  func(ctx context.Context, s transact.Stores, appID string) string
	appOwnerOf      func(workloadID string) (appID string, owned bool)
	workloadProcess func(workloadID string) (string, bool)
	rootCtx         func() context.Context
	spawn           func(fn func())
}

// Deps 是应用模块依赖与内核端口的注入面（组合根 = engine construct.go
// 绑定 contract.MetricsPorts 后经 MetricsFactory 装配，ADR-0088 决策 2 P5
// 形态：嵌入已解开，核经构造注入持有应用模块；依赖显式逐字段注入 + 内核
// 端口绑定为核方法值/闭包）。
type Deps struct {
	Metrics    capability.Metrics
	Runtime    capability.Runtime
	Repos      transact.Stores
	AlertRules alertrule.Repo
	Channels   channel.Repo
	Cipher     *material.Cipher
	Log        *slog.Logger
	Clock      state.Clock
	// OffsiteConfigured 是内置规则的只读事实端口（核侧闭包逐拍读 opts）。
	OffsiteConfigured func() bool
	// EmitObservation 是观测事实发射端口（核 emitObservation 方法值）。
	EmitObservation func(ctx context.Context, team, name, aggregate, id string, payload []byte) error
	// EventTeamOfApp 是 App 轴 team 解析端口（核 eventTeamOfApp 方法值）。
	EventTeamOfApp func(ctx context.Context, s transact.Stores, appID string) string
	// AppOwnerOf 是载体归属的 App 域窄面（核侧绑定 obs.ownerOf + ownerApp
	// 域过滤；非 App 域/不归属 = owned false）。
	AppOwnerOf func(workloadID string) (appID string, owned bool)
	// WorkloadProcess 是进程模板名窄导出端口（核 WorkloadProcess 方法值）。
	WorkloadProcess func(workloadID string) (string, bool)
	// RootCtx 是 lifecycle 派生根端口（engine 侧绑定 buildRootCtx）。
	RootCtx func() context.Context
	// Spawn 是监督 goroutine 起拍端口（Add/Done 配对单源留核）。
	Spawn func(fn func())
}

// New 构造采集+告警域应用模块（差分基面账本初始化；不启动——step 由 engine
// 循环表驱动）。
func New(deps Deps) *Metrics {
	return &Metrics{
		lastCPU:           make(map[string]cpuCounter),
		cores:             make(map[string]float64),
		metrics:           deps.Metrics,
		runtime:           deps.Runtime,
		repos:             deps.Repos,
		alertRules:        deps.AlertRules,
		channels:          deps.Channels,
		cipher:            deps.Cipher,
		log:               deps.Log,
		clock:             deps.Clock,
		offsiteConfigured: deps.OffsiteConfigured,
		emitObservation:   deps.EmitObservation,
		eventTeamOfApp:    deps.EventTeamOfApp,
		appOwnerOf:        deps.AppOwnerOf,
		workloadProcess:   deps.WorkloadProcess,
		rootCtx:           deps.RootCtx,
		spawn:             deps.Spawn,
	}
}

// sysOffsiteState 快照内置规则状态对（跨 goroutine 读面——gRPC handler）。
func (m *Metrics) sysOffsiteState() (firing bool, above time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sysOffsiteFiring, m.sysOffsiteAbove
}

// sysOffsiteReset 配置归位：清越限窗，并原子地报告是否需要 resolved 迁移
// 沿（firing 翻转与清窗同临界区）。
func (m *Metrics) sysOffsiteReset() (resolved bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sysOffsiteAbove = time.Time{}
	if m.sysOffsiteFiring {
		m.sysOffsiteFiring = false
		resolved = true
	}
	return resolved
}

// sysOffsiteObserve 推进内置规则越限窗：首见起窗；持续窗满且未 firing 时
// 翻转 firing 并报告迁移沿。事件/派发（I/O）由调用方在锁外执行。
func (m *Metrics) sysOffsiteObserve(now time.Time) (fired bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sysOffsiteAbove.IsZero() {
		m.sysOffsiteAbove = now
		return false
	}
	if now.Sub(m.sysOffsiteAbove) < systemOffsiteFor || m.sysOffsiteFiring {
		return false // 未满窗或已 firing（不逐拍重发——迁移沿语义）
	}
	m.sysOffsiteFiring = true
	return true
}

type cpuCounter struct {
	value float64
	at    time.Time
}

// containerSample 是评估面的容器样本（cadvisor 序列标签还原平台归因）。
type containerSample struct {
	node     string
	app      string // fleetly.ns.app（container_label_fleetly_ns_app）
	id       string // docker 容器 ID（cadvisor id 标签）
	cpuRate  float64
	memBytes float64
}

// workloadSample 是负载词汇导出面的样本（ADR-0065）：与 containerSample
// 同源解析、射程更宽——一切带 fleetly.workload.id 的平台载体（App/Task/
// Database/Browse/受管系统域全收）。域主体标签恰一非空（translate.go
// workloadLabels 同构）；cpu 率与 percent 由采集循环差分面预计算，产品面
// 查询免 rate()/group_left（F-B1 故障类从构造上消失）。
type workloadSample struct {
	node        string
	app         string // 域主体（恰一非空；sanitizeNamePart 小写形态）
	task        string
	database    string
	pubsub      string
	bucketstore string
	browse      string
	process     string // fleetly.process（人读服务名—— Processes 页同源）
	workload    string // fleetly.workload.id（载体身份，跨部署稳定）
	container   string // docker 容器名（cadvisor name 标签，副本判别）
	cpuCores    float64
	cpuKnown    bool // 差分首拍/计数回零无率——cpu 指标该拍缺席（诚实断口）
	pctKnown    bool // percent 另需节点核数观测在场
	cpuPercent  float64
	memBytes    float64
	memKnown    bool
}

// Step 是采集循环收敛步（循环表 step 面——engine construct.go 循环表以方法值
// 挂载；P4 wave-1b 前名 metricsStep，拆包导出更名）：节拍锚 → 集群视图 →
// 逐节点抓取/透传/解析 → 规则评估（含内置规则）。
func (m *Metrics) Step(ctx context.Context) {
	if m.metrics == nil {
		return
	}
	now := m.clock.Now()
	if now.Sub(m.lastScrapeAt) < metricsScrapeInterval {
		return // 粗节拍锚：tick 1s 内自持 15s
	}
	m.lastScrapeAt = now
	view, err := m.runtime.DescribeCluster(ctx)
	if err != nil {
		m.log.Error("metrics step: describe cluster", "err", err)
		return
	}
	var samples []containerSample
	for _, node := range view.Nodes {
		if !node.Available || node.Addr == "" || node.NodeID == "" {
			continue
		}
		body, err := m.scrapeNode(ctx, node.Addr)
		if err != nil {
			m.log.Warn("metrics step: scrape node", "node", node.NodeID, "addr", node.Addr, "err", err)
			continue
		}
		// 原文透传（平台归因 extra_label；失败下拍重试——样本断口诚实）。
		if err := m.metrics.ImportPrometheus(ctx, body, map[string]string{
			"job":  "fleetly-cadvisor",
			"node": node.NodeID,
		}); err != nil {
			m.log.Warn("metrics step: import node samples", "node", node.NodeID, "err", err)
		}
		appSamples, vocabSamples := m.parseCadvisor(node.NodeID, body, now)
		samples = append(samples, appSamples...)
		// 负载词汇导出（ADR-0065）：平台归一指标随拍入仓——raw cadvisor
		// 照旧透传（工作台面），词汇是产品面消费契约（console presets 等）。
		// 失败下拍重试（raw 同款语义）。
		if vocab := composeWorkloadExposition(vocabSamples); len(vocab) > 0 {
			if err := m.metrics.ImportPrometheus(ctx, vocab, map[string]string{
				"job":  "fleetly-workload",
				"node": node.NodeID,
			}); err != nil {
				m.log.Warn("metrics step: import workload vocabulary", "node", node.NodeID, "err", err)
			}
		}
	}
	m.evaluateRules(ctx, samples, now)
	m.evaluateSystemRules(ctx, now)
}

// scrapeNode 抓取单节点 cadvisor 端点（host 发布）。
func (m *Metrics) scrapeNode(ctx context.Context, addr string) ([]byte, error) {
	sctx, cancel := context.WithTimeout(ctx, metricsScrapeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(sctx, http.MethodGet,
		fmt.Sprintf("http://%s:%d/metrics", addr, metricsCadvisorPort), nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{}).Do(req) //nolint:gosec // 集群内网 HTTP（VPC-only 边界，ADR-0041）
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("cadvisor endpoint status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32MiB 上界（超大家庭防御）
}

// parseCadvisor 解析 exposition 文本为双消费面样本（最小解析器：只认三个
// metric 家族 + 平台容器标签；非 /docker/ 载体的 cgroup 聚合行跳过）：
//   - app 域样本（告警评估面，语义自 ADR-0041 原样）；
//   - 负载词汇样本（ADR-0065：一切 fleetly.workload.id 载体，域主体恰一）。
//
// 同步维护 CPU 差分基面与节点核数观测（两条消费路径共用同一差分基面——
// 分叉两套会在同一节拍上漂移出两份真值）。
func (m *Metrics) parseCadvisor(nodeID string, body []byte, now time.Time) ([]containerSample, []workloadSample) {
	byContainer := map[string]*containerSample{}
	byWorkload := map[string]*workloadSample{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		name, labels, value, ok := parseExpositionLine(line)
		if !ok {
			continue
		}
		switch name {
		case "machine_cpu_cores":
			if v, err := strconv.ParseFloat(value, 64); err == nil && v > 0 {
				m.cores[nodeID] = v
			}
		case "container_cpu_usage_seconds_total", "container_memory_working_set_bytes":
			id := labels["id"]
			app := strings.ToLower(labels["container_label_fleetly_ns_app"])
			workloadID := strings.ToLower(labels["container_label_fleetly_workload_id"])
			if (app == "" && workloadID == "") || id == "/" {
				// 平台 Workload 载体之外（cgroup 聚合/系统容器）——fleetly
				// 归属标签非空即平台过滤器（只有平台载体带 fleetly 标签；
				// id 形态因 cgroup 驱动而异：/docker/<id>（v1）或
				// /system.slice/docker-<id>.scope（v2 systemd，staging 实证），
				// 不做前缀硬编码）。label 值经 sanitizeNamePart 已小写——
				// 大写 ULID 在消费面同归一。
				continue
			}
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				continue
			}
			isMem := name == "container_memory_working_set_bytes"
			s := byContainer[id]
			if app != "" && s == nil {
				s = &containerSample{node: nodeID, app: app, id: id}
				byContainer[id] = s
			}
			w := byWorkload[id]
			if workloadID != "" && w == nil {
				w = &workloadSample{
					node:        nodeID,
					app:         app,
					task:        strings.ToLower(labels["container_label_fleetly_ns_task"]),
					database:    strings.ToLower(labels["container_label_fleetly_ns_database"]),
					pubsub:      strings.ToLower(labels["container_label_fleetly_ns_pubsub"]),
					bucketstore: strings.ToLower(labels["container_label_fleetly_ns_bucketstore"]),
					browse:      strings.ToLower(labels["container_label_fleetly_ns_browse"]),
					process:     strings.ToLower(labels["container_label_fleetly_process"]),
					workload:    workloadID,
					container:   labels["name"],
				}
				byWorkload[id] = w
			}
			if isMem {
				// memory 是 gauge：直取（差分基面不碰——只载 CPU counter）。
				if s != nil {
					s.memBytes = v
				}
				if w != nil {
					w.memBytes, w.memKnown = v, true
				}
				continue
			}
			// CPU counter → 率（差分基面；首拍无率）。
			key := nodeID + "|" + id
			prev, had := m.lastCPU[key]
			m.lastCPU[key] = cpuCounter{value: v, at: now}
			if !had || now.Sub(prev.at) <= 0 {
				continue
			}
			rate := (v - prev.value) / now.Sub(prev.at).Seconds()
			if rate < 0 {
				continue // 容器重启计数回零
			}
			if s != nil {
				s.cpuRate = rate
			}
			if w != nil {
				w.cpuCores, w.cpuKnown = rate, true
			}
		}
	}
	cores := m.cores[nodeID]
	appOut := make([]containerSample, 0, len(byContainer))
	for _, s := range byContainer {
		if cores > 0 {
			s.cpuRate = s.cpuRate / cores * 100 // 百分比形态（docker stats 同口径）
		}
		appOut = append(appOut, *s)
	}
	vocabOut := make([]workloadSample, 0, len(byWorkload))
	for _, w := range byWorkload {
		if w.cpuKnown && cores > 0 {
			w.cpuPercent, w.pctKnown = w.cpuCores/cores*100, true
		}
		vocabOut = append(vocabOut, *w)
	}
	// 行序按容器名固定——导入幂等与测试断言都吃确定形态。
	sort.Slice(vocabOut, func(i, j int) bool { return vocabOut[i].container < vocabOut[j].container })
	return appOut, vocabOut
}

// SystemOffsiteAlertState 是内置规则的观测面（server 组装 states 用）。
// 读侧经锁快照——写侧在采集循环 goroutine（evaluateSystemRules），两侧无
// happens-before，直读即数据竞争。状态形态单源在契约叶子
// （contract.SystemAlertState——engine 去应用模块 import 第五批随契约面
// 下沉）。
func (m *Metrics) SystemOffsiteAlertState() contract.SystemAlertState {
	st := alertrule.StateOK
	since := time.Time{}
	firing, above := m.sysOffsiteState()
	if firing {
		st = alertrule.StateFiring
	}
	if !above.IsZero() {
		since = above
	}
	return contract.SystemAlertState{RuleID: systemOffsiteRuleID, State: st, StateSince: since, System: true}
}

// AlertRuleRepo / NotificationChannelRepo 是告警面 repo 访问器（API 受理面
// 经 commit 原语持 tx 写行——repo 的 Runner 参数即 tx）。
func (m *Metrics) AlertRuleRepo() alertrule.Repo         { return m.alertRules }
func (m *Metrics) NotificationChannelRepo() channel.Repo { return m.channels }

// MetricsProvider 返回受管指标存储（nil = Metrics 面停用；API 查询面消费，
// ADR-0041）。capability 持有权随应用模块——核内消费（managed 重启面/
// componentrestart providers）经 engine 核门面委托读。
func (m *Metrics) MetricsProvider() capability.Metrics { return m.metrics }
