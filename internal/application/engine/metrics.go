package engine

// Metrics 采集环（第 10 收敛环，F2.5/ADR-0041 决策 2/3）：每节拍（15s 粗
// 节拍——tick 1s 内自持锚）抓取各节点 cadvisor 端点（host 发布 8080），
// 原文透传 VM（平台归因 extra_label）+ 同一遍在手法评估阈值规则（内存
// 最新样本——评估零查询依赖）。状态迁移沿才通知（fired/resolved 各一次）。
//
// 诚实边界（ADR-0041）：评估是 15s 采样近似（for-window 连续性按采样序
// 判定；重启重置 pending 面）；节点下线 = 该节点序列停写（VM 断口诚实
// 呈现）。
//
// 域文件分组（P3-2 收编，729 行拆四）：本文件持采集环骨架（节拍/抓取/
// 解析）与域实例态；exposition 组装/标签/解析纯函数族见 exposition.go；
// 告警评估面（规则迁移/事件）见 alerting.go；通道派发面见 channelnotify.go。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
)

// 采集环参数（环本地常量；无运维面诉求——同 logging 环纪律）。
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

// metrics 是采集环实例态（C5：实例内聚合）。P3-2 收编：metricsDomain →
// metrics（复数普通名词零后缀，ADR-0075 命名法则）；Engine 字段名
// metricsDom 保持不变（域字段壳收口留批 8）。
type metrics struct {
	// lastScrapeAt 是节拍锚（15s 粗节拍在 1s tick 内自持）。
	lastScrapeAt time.Time
	// above 是规则越限起始时刻（pending 持续窗的内存面；重启重置——采样
	// 近似语义的一部分，ADR-0041）。键 = 规则 ID。
	above map[string]time.Time
	// lastCPU 是容器 CPU 计数器差分基面（cadvisor counter → 率）。键 =
	// node|containerID。
	lastCPU map[string]cpuCounter
	// cores 是节点核数观测（cpu_percent 的分母）。
	cores map[string]float64
	// sysOffsiteAbove 是内置规则的越限起始（daemon 生命期内存面）。
	sysOffsiteAbove  time.Time
	sysOffsiteFiring bool
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
// workloadLabels 同构）；cpu 率与 percent 由采集环差分面预计算，产品面
// 查询免 rate()/group_left（F-B1 故障类从构造上消失）。
type workloadSample struct {
	node       string
	app        string // 域主体（恰一非空；sanitizeNamePart 小写形态）
	task       string
	database   string
	browse     string
	process    string // fleetly.process（人读服务名—— Processes 页同源）
	workload   string // fleetly.workload.id（载体身份，跨部署稳定）
	container  string // docker 容器名（cadvisor name 标签，副本判别）
	cpuCores   float64
	cpuKnown   bool // 差分首拍/计数回零无率——cpu 指标该拍缺席（诚实断口）
	pctKnown   bool // percent 另需节点核数观测在场
	cpuPercent float64
	memBytes   float64
	memKnown   bool
}

// metricsStep 是采集环收敛步：节拍锚 → 集群视图 → 逐节点抓取/透传/解析
// → 规则评估（含内置规则）。
func (e *Engine) metricsStep(ctx context.Context) {
	if e.metrics == nil {
		return
	}
	now := e.clock.Now()
	if now.Sub(e.metricsDom.lastScrapeAt) < metricsScrapeInterval {
		return // 粗节拍锚：tick 1s 内自持 15s
	}
	e.metricsDom.lastScrapeAt = now
	view, err := e.runtime.DescribeCluster(ctx)
	if err != nil {
		e.log.Error("metrics step: describe cluster", "err", err)
		return
	}
	var samples []containerSample
	for _, node := range view.Nodes {
		if !node.Available || node.Addr == "" || node.NodeID == "" {
			continue
		}
		body, err := e.scrapeNode(ctx, node.Addr)
		if err != nil {
			e.log.Warn("metrics step: scrape node", "node", node.NodeID, "addr", node.Addr, "err", err)
			continue
		}
		// 原文透传（平台归因 extra_label；失败下拍重试——样本断口诚实）。
		if err := e.metrics.ImportPrometheus(ctx, body, map[string]string{
			"job":  "fleetly-cadvisor",
			"node": node.NodeID,
		}); err != nil {
			e.log.Warn("metrics step: import node samples", "node", node.NodeID, "err", err)
		}
		appSamples, vocabSamples := e.parseCadvisor(node.NodeID, body, now)
		samples = append(samples, appSamples...)
		// 负载词汇导出（ADR-0065）：平台归一指标随拍入仓——raw cadvisor
		// 照旧透传（工作台面），词汇是产品面消费契约（console presets 等）。
		// 失败下拍重试（raw 同款语义）。
		if vocab := composeWorkloadExposition(vocabSamples); len(vocab) > 0 {
			if err := e.metrics.ImportPrometheus(ctx, vocab, map[string]string{
				"job":  "fleetly-workload",
				"node": node.NodeID,
			}); err != nil {
				e.log.Warn("metrics step: import workload vocabulary", "node", node.NodeID, "err", err)
			}
		}
	}
	e.evaluateRules(ctx, samples, now)
	e.evaluateSystemRules(ctx, now)
}

// scrapeNode 抓取单节点 cadvisor 端点（host 发布）。
func (e *Engine) scrapeNode(ctx context.Context, addr string) ([]byte, error) {
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

// parseCadvisor 解析 exposition 文本为双消费面样本（最小解析面：只认三个
// metric 家族 + 平台容器标签；非 /docker/ 载体的 cgroup 聚合行跳过）：
//   - app 域样本（告警评估面，语义自 ADR-0041 原样）；
//   - 负载词汇样本（ADR-0065：一切 fleetly.workload.id 载体，域主体恰一）。
//
// 同步维护 CPU 差分基面与节点核数观测（两条消费路径共用同一差分基面——
// 分叉两套会在同一节拍上漂移出两份真值）。
func (e *Engine) parseCadvisor(nodeID string, body []byte, now time.Time) ([]containerSample, []workloadSample) {
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
				e.metricsDom.cores[nodeID] = v
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
					node:      nodeID,
					app:       app,
					task:      strings.ToLower(labels["container_label_fleetly_ns_task"]),
					database:  strings.ToLower(labels["container_label_fleetly_ns_database"]),
					browse:    strings.ToLower(labels["container_label_fleetly_ns_browse"]),
					process:   strings.ToLower(labels["container_label_fleetly_process"]),
					workload:  workloadID,
					container: labels["name"],
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
			prev, had := e.metricsDom.lastCPU[key]
			e.metricsDom.lastCPU[key] = cpuCounter{value: v, at: now}
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
	cores := e.metricsDom.cores[nodeID]
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

// SystemAlertStates 返回内置规则的现行状态（ListAlertStates 消费）。
type SystemAlertState struct {
	RuleID     string
	State      string
	StateSince time.Time
	System     bool
}

// SystemOffsiteAlertState 是内置规则的观测面（server 组装 states 用）。
func (e *Engine) SystemOffsiteAlertState() SystemAlertState {
	st := alertrule.StateOK
	since := time.Time{}
	if e.metricsDom.sysOffsiteFiring {
		st = alertrule.StateFiring
	}
	if !e.metricsDom.sysOffsiteAbove.IsZero() {
		since = e.metricsDom.sysOffsiteAbove
	}
	return SystemAlertState{RuleID: systemOffsiteRuleID, State: st, StateSince: since, System: true}
}

// AlertRuleRepo / NotificationChannelRepo 是告警面 repo 访问器（API 受理面
// 经 commit 原语持 tx 写行——repo 的 Runner 参数即 tx）。
func (e *Engine) AlertRuleRepo() alertrule.Repo         { return e.alertRules }
func (e *Engine) NotificationChannelRepo() channel.Repo { return e.channels }

// MetricsProvider 返回受管指标存储（nil = Metrics 面停用；API 查询面消费，
// ADR-0041）。
func (e *Engine) MetricsProvider() capability.Metrics { return e.metrics }
