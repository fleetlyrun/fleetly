package engine

// exposition 纯函数族（P3-2 收编自 metrics.go）：负载词汇导出的组装
//（composeWorkloadExposition/workloadLabelSet）与 exposition 文本的解析
//（parseExpositionLine/parseExpositionLabels/splitLabels）——零状态零
// 接收者；采集环（metrics.go 的 metricsStep/parseCadvisor）是唯一消费方。

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// 负载词汇指标名（ADR-0065 词汇注册表）：采集环归一导出的平台指标——
// 产品面（console presets、CLI、后续 per-process 告警）一律经词汇查询；
// cadvisor 原始序列只是工作台透传面，不是消费契约。词条真源已迁
// capability（H8 阶段 B：查询构造与采集环共用）；本处保留同名 alias。
const (
	MetricWorkloadCPUPercent = capability.MetricWorkloadCPUPercent
	MetricWorkloadCPUCores   = capability.MetricWorkloadCPUCores
	MetricWorkloadMemory     = capability.MetricWorkloadMemory
)

// composeWorkloadExposition 把词汇样本渲染为 exposition 文本（VM 流式导入
// 端点同款形态）。已知量才导出：cpu 差分首拍缺席、percent 无核数观测缺席
// ——零值与缺席在时序库里是两件事。
func composeWorkloadExposition(samples []workloadSample) []byte {
	var b bytes.Buffer
	for _, w := range samples {
		lbl := workloadLabelSet(w)
		if w.memKnown {
			fmt.Fprintf(&b, "%s{%s} %s\n", MetricWorkloadMemory, lbl, expositionFloat(w.memBytes))
		}
		if w.cpuKnown {
			fmt.Fprintf(&b, "%s{%s} %s\n", MetricWorkloadCPUCores, lbl, expositionFloat(w.cpuCores))
			if w.pctKnown {
				fmt.Fprintf(&b, "%s{%s} %s\n", MetricWorkloadCPUPercent, lbl, expositionFloat(w.cpuPercent))
			}
		}
	}
	return b.Bytes()
}

// workloadLabelSet 渲染身份标签集：域主体恰一在前 + 身份三元组收尾
// （process/workload/container 恒在——词汇查询的身份锚）。
func workloadLabelSet(w workloadSample) string {
	pairs := make([]string, 0, 6)
	for _, kv := range [...][2]string{
		{"app", w.app}, {"task", w.task}, {"database", w.database}, {"browse", w.browse},
	} {
		if kv[1] != "" {
			pairs = append(pairs, kv[0]+"="+expositionLabelValue(kv[1]))
		}
	}
	pairs = append(pairs,
		"process="+expositionLabelValue(w.process),
		"workload="+expositionLabelValue(w.workload),
		"container="+expositionLabelValue(w.container),
	)
	return strings.Join(pairs, ",")
}

// expositionLabelValue 按 Prometheus 文本格式转义并包裹标签值（载体名含
// 点号；转义面按规范全带上，不赌标签域）。
func expositionLabelValue(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v) + `"`
}

// expositionFloat 渲染样本值（最短十进制浮点——整数值不带小数尾巴）。
func expositionFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// parseExpositionLine 解析一行 exposition：name{labels} value [ts]。
// 标签值按 Prometheus 文本格式转义（\\ \" \n）。注释行（# 前缀）在此拒收
// （调用方也跳过——双保险）。
func parseExpositionLine(line string) (name string, labels map[string]string, value string, ok bool) {
	if strings.HasPrefix(line, "#") {
		return "", nil, "", false
	}
	open := strings.IndexByte(line, '{')
	if open < 0 {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return "", nil, "", false
		}
		return parts[0], nil, parts[1], true
	}
	closeIdx := strings.LastIndexByte(line, '}')
	if closeIdx < open {
		return "", nil, "", false
	}
	name = line[:open]
	labels = parseExpositionLabels(line[open+1 : closeIdx])
	rest := strings.Fields(strings.TrimSpace(line[closeIdx+1:]))
	if len(rest) == 0 {
		return "", nil, "", false
	}
	return name, labels, rest[0], true
}

// parseExpositionLabels 解析 label 集（k="v" 逗号连接；值内转义还原）。
// 诚实边界：不处理值内嵌入引号/逗号（fleetly 标签域——ULID/消毒名/
// 镜像引用——不产生；遇即原样透传不误读键）。
func parseExpositionLabels(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range splitLabels(s) {
		k, v, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		v = strings.Trim(v, `"`)
		// 转义还原（Prometheus 文本格式）：\" → "，\\ → \（单趟非重叠）。
		v = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(v)
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// splitLabels 按逗号切分（值内逗号被引号包裹——不切）。
func splitLabels(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && (i == 0 || s[i-1] != '\\'):
			inQuote = !inQuote
			b.WriteByte(c)
		case c == ',' && !inQuote:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
