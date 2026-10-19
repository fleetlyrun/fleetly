package capability

// 指标查询构造面（H8 阶段 B，设计文档
// docs/design/2026-10-19-security-backlog-design.md §5.3）：scoped 查询的
// 服务端固定模板与时间/步长钳制。纯函数、零 I/O——调用方（provider）只
// 执行，不解释用户表达式；指标名/标签名/窗口/步长全部由本文件常量或
// 调用方已授权的 scope 决定。

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// 指标查询的服务端上限（H8 阶段 B；与设计文档 §5.3.3 同值）。
const (
	// ScopedMetricsMaxWindow 是 scoped 查询时间窗上限（含）。
	ScopedMetricsMaxWindow = 24 * time.Hour
	// ScopedMetricsMaxPoints 是单序列采样点上限的折算基数（step 钳制保证
	// 每序列 ≤ 约 1000 点）。
	ScopedMetricsMaxPoints = 1000
	// ScopedMetricsAutoStepPoints 是请求 step=0 时服务端自动步长的目标
	// 点数（与 Console METRIC_RANGES 的 step 口径一致）。
	ScopedMetricsAutoStepPoints = 240
	// ScopedMetricsMinStep 是步长下限（VM query_range 的 step 为整数秒）。
	ScopedMetricsMinStep = 15 * time.Second
	// ScopedMetricsMaxSeries 是 scoped 查询返回的序列数上限（超出截断）。
	ScopedMetricsMaxSeries = 50
	// ScopedMetricsRawMaxSeries 是 raw 平台管理员诊断查询的序列数上限
	//（超出直接拒绝，不做静默截断）。
	ScopedMetricsRawMaxSeries = 1000
	// ScopedMetricsMaxBodyBytes 是查询响应体的读取上限（8 MiB，raw/scoped
	// 共用）。
	ScopedMetricsMaxBodyBytes = 8 << 20
)

// scoped 指标白名单的源值域（与告警规则 metric 词表同形；proto 字段级
// CEL 是受理面镜像）。
const (
	MetricCPUPercent       = "cpu_percent"
	MetricCPUCores         = "cpu_cores"
	MetricMemoryWorkingSet = "memory_working_set_bytes"
)

// 负载词汇指标名（ADR-0065 词汇注册表；原居 engine/exposition.go，H8 阶段
// B 迁至 capability 供查询构造与采集环共用——engine 保留同名 alias）。
const (
	MetricWorkloadCPUPercent = "fleetly_container_cpu_percent"
	MetricWorkloadCPUCores   = "fleetly_container_cpu_cores"
	MetricWorkloadMemory     = "fleetly_container_memory_working_set_bytes"
)

// cadvisor/k3s raw 指标名（k3s 词汇化挂账，ADR-0065:40；scoped 的 k3s 臂
// 直接查 raw 序列）。
const (
	MetricRawContainerCPU    = "container_cpu_usage_seconds_total"
	MetricRawContainerMemory = "container_memory_working_set_bytes"
	MetricRawMachineCPUCores = "machine_cpu_cores"
)

// 指标标签名（读侧词汇）：swarm 词汇臂用 app/database/process；cAdvisor
// 投影标签是写侧 label 的点号转下划线形态（drift guard 在 swarm/k3s
// translate 测试里钉死）；k3s raw 臂用 namespace/pod/container。
const (
	MetricLabelApp                 = "app"
	MetricLabelDatabase            = "database"
	MetricLabelProcess             = "process"
	MetricLabelNamespace           = "namespace"
	MetricLabelPod                 = "pod"
	MetricLabelContainer           = "container"
	MetricLabelNode                = "node"
	MetricLabelProjectOnContainer  = "container_label_fleetly_ns_project"
	MetricLabelAppOnContainer      = "container_label_fleetly_ns_app"
	MetricLabelDatabaseOnContainer = "container_label_fleetly_ns_database"
)

// k3sNamespacePrefix 与 k3s provider namespaceName 同源（fleetly-<project>；
// 写侧公式的读侧镜像，drift guard 钉死）。
const k3sNamespacePrefix = "fleetly-"

// workloadNamePrefix 与 k3s provider namePrefix 同源（fleetly-<app>-<proc>）。
const workloadNamePrefix = "fleetly-"

// databaseNamePrefix 与 k3s provider dbNamePrefix 同源（fleetly-db-<db>）。
const databaseNamePrefix = "fleetly-db-"

// BuildScopedPromQL 按 scope + 指标白名单构造固定模板（swarm 词汇臂
// `or` k3s raw 臂；两臂在同一 VM 互不匹配对方的标签集，失败臂是空集）。
// 参数只来自已授权行/白名单；标签值经 quoteMetricLabel 转义，k3s pod 正则
// 的实体段经 regexp.QuoteMeta 收敛。调用方不提供表达式。
func BuildScopedPromQL(q ScopedQuery) (string, error) {
	switch q.Metric {
	case MetricCPUPercent, MetricCPUCores, MetricMemoryWorkingSet:
	default:
		return "", fmt.Errorf("unknown scoped metric %q (want %s, %s or %s)",
			q.Metric, MetricCPUPercent, MetricCPUCores, MetricMemoryWorkingSet)
	}
	if (q.AppID == "") == (q.DatabaseID == "") {
		return "", fmt.Errorf("exactly one of app id or database id must be set")
	}
	if q.ProjectID == "" {
		return "", fmt.Errorf("project id is required for a scoped query")
	}
	if q.Process != "" && q.DatabaseID != "" {
		return "", fmt.Errorf("process is only available for app scope")
	}
	if q.Metric == MetricCPUPercent && q.DatabaseID != "" {
		return "", fmt.Errorf("%s is only available for app scope", MetricCPUPercent)
	}

	rateWindow := scopedRateWindow(q.Step)
	swarmArm, k3sArm := buildScopeArms(q, rateWindow)
	return "(" + swarmArm + ") or (" + k3sArm + ")", nil
}

// buildScopeArms 返回 (swarm 词汇臂, k3s raw 臂)。
func buildScopeArms(q ScopedQuery, rateWindow time.Duration) (string, string) {
	namespace := quoteMetricLabel(k3sNamespacePrefix + q.ProjectID)
	var podRe string
	if q.AppID != "" {
		app := regexp.QuoteMeta(q.AppID)
		if q.Process != "" {
			podRe = workloadNamePrefix + app + "-" + regexp.QuoteMeta(q.Process) + "(-.*)?"
		} else {
			podRe = workloadNamePrefix + app + "-.*"
		}
	} else {
		podRe = databaseNamePrefix + regexp.QuoteMeta(q.DatabaseID) + "(-.*)?"
	}
	pod := quoteMetricLabel(podRe)

	selector := fmt.Sprintf("{%s=%s,%s=~%s,%s!=\"\"}",
		MetricLabelNamespace, namespace, MetricLabelPod, pod, MetricLabelContainer)
	rawCPU := metricRate(selector, rateWindow)

	if q.AppID != "" {
		labels := MetricLabelApp + "=" + quoteMetricLabel(q.AppID)
		if q.Process != "" {
			labels += "," + MetricLabelProcess + "=" + quoteMetricLabel(q.Process)
		}
		var swarmMetric string
		switch q.Metric {
		case MetricCPUPercent:
			swarmMetric = MetricWorkloadCPUPercent
		case MetricCPUCores:
			swarmMetric = MetricWorkloadCPUCores
		default:
			swarmMetric = MetricWorkloadMemory
		}
		swarmArm := swarmMetric + "{" + labels + "}"

		var k3sArm string
		switch q.Metric {
		case MetricCPUPercent:
			k3sArm = "100 * " + rawCPU + " / on(" + MetricLabelNode + ") group_left " + MetricRawMachineCPUCores
		case MetricCPUCores:
			k3sArm = rawCPU
		default:
			k3sArm = MetricRawContainerMemory + selector
		}
		return swarmArm, k3sArm
	}

	labels := MetricLabelDatabase + "=" + quoteMetricLabel(q.DatabaseID)
	var swarmArm, k3sArm string
	switch q.Metric {
	case MetricCPUCores:
		swarmArm = MetricWorkloadCPUCores + "{" + labels + "}"
		k3sArm = rawCPU
	default:
		swarmArm = MetricWorkloadMemory + "{" + labels + "}"
		k3sArm = MetricRawContainerMemory + selector
	}
	return swarmArm, k3sArm
}

// metricRate 渲染 rate(<selector>[<window>])。
func metricRate(selector string, window time.Duration) string {
	return "rate(" + MetricRawContainerCPU + selector + "[" + promDuration(window) + "])"
}

// scopedRateWindow 返回 rate 窗口：max(2m, 4*step)，上限 1h（设计文档
// §5.3.2；step 已由 ClampScopedStep 钳制到 ≥15s 且取整到秒）。
func scopedRateWindow(step time.Duration) time.Duration {
	window := 4 * step
	if window < 2*time.Minute {
		window = 2 * time.Minute
	}
	if window > time.Hour {
		window = time.Hour
	}
	return window
}

// promDuration 把时长渲染为 PromQL 可读的整数单位（m/h/s；step 已取整到
// 秒，rateWindow 是 2m 的整数倍或 1h）。
func promDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return fmt.Sprintf("%ds", d/time.Second)
	}
}

// quoteMetricLabel 按 PromQL 字符串字面转义并包裹标签值（反斜杠、双引号
// 与换行）。模板中的实体 ID/Process 名本是 dns 安全字符，转义是防御面。
func quoteMetricLabel(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v) + `"`
}

// ClampScopedStep 校验时间窗并返回生效步长（整数秒）：
//   - requested < 0 → 错误；
//   - start >= end → 错误；窗口 > 24h → 错误；
//   - requested == 0 → max(15s, ceil(window/240))；
//   - requested > 0 → max(requested, 15s, ceil(window/1000))；
//   - 取整到秒（VM query_range 的 step 是整数秒）。
func ClampScopedStep(start, end time.Time, requestedSeconds int64) (time.Duration, error) {
	if requestedSeconds < 0 {
		return 0, fmt.Errorf("step_seconds must not be negative (got %d)", requestedSeconds)
	}
	// 防 time.Duration 溢出：秒数必须能安全折算成纳秒。
	if requestedSeconds > math.MaxInt64/int64(time.Second) {
		return 0, fmt.Errorf("step_seconds is too large (got %d)", requestedSeconds)
	}
	if !start.Before(end) {
		return 0, fmt.Errorf("start must be before end")
	}
	window := end.Sub(start)
	if window > ScopedMetricsMaxWindow {
		return 0, fmt.Errorf("the time window must not exceed %s (got %s); narrow start/end",
			ScopedMetricsMaxWindow, window)
	}
	windowSecs := int64((window + time.Second - 1) / time.Second)
	floor := ceilDiv(windowSecs, ScopedMetricsMaxPoints)
	effective := floor
	if requestedSeconds == 0 {
		auto := ceilDiv(windowSecs, ScopedMetricsAutoStepPoints)
		if auto > effective {
			effective = auto
		}
	} else if requestedSeconds > effective {
		effective = requestedSeconds
	}
	minSecs := int64(ScopedMetricsMinStep / time.Second)
	if effective < minSecs {
		effective = minSecs
	}
	return time.Duration(effective) * time.Second, nil
}

// ceilDiv 返回 ceil(a/b)（b > 0，a ≥ 0）。
func ceilDiv(a, b int64) int64 {
	return (a + b - 1) / b
}
