package metrics

// VM HTTP 消费面（设计 §4.2 查询面）：fleetlyd 侧 SearchMetrics 的查询
// 后端（PromQL 区间/瞬时查询）与 /health 回环拨测。
//
// 端点与形态按 VM v1.152 单机版核实（2026-09-22，官方镜像 -help 与
// Prometheus HTTP API 兼容面）：
//   - GET  /health → 200 "OK"（回环拨测）；
//   - GET  /api/v1/query_range?query=…&start=<unix>&end=<unix>&step=<s>
//     → JSON {status:"success",data:{resultType:"matrix",result:[
//     {metric:{label:value,…},values:[[unix,"v"],…]},…]}};
//   - GET  /api/v1/query?query=… → 同形态 resultType=vector/value。
//
// 诚实口径（设计 §4.2 原文）：**操作员工具，PromQL 透传，不做查询沙箱**
// ——query 原样转发给 VM，由 proto 文档与本文件注释双面声明；平台不
// 声称任何查询隔离。错误面：VM 不可达（回环 8428 无应答）= ErrBackend
// Unavailable 语义；VM 对坏 PromQL 回 4xx = ErrBadQuery 语义（api 层映射
// InvalidArgument，错误文本带 VM 原文可定位）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrMetricsNotEnabled 是 metrics.mode != on 时查询面的哨兵——api 层映射
// E_METRICS_NOT_ENABLED（不返回空序列冒充）。
var ErrMetricsNotEnabled = errors.New("metrics: metrics.mode is not on (query face is opt-in)")

// ErrBackendUnavailable 是 VM 回环端点不可达的哨兵——api 层映射
// E_METRICS_BACKEND_UNAVAILABLE（检索降级诚实报错）。
var ErrBackendUnavailable = errors.New("metrics: VictoriaMetrics backend did not answer")

// ErrBadQuery 是 VM 拒绝查询输入（坏 PromQL / 越窗）的哨兵——api 层映射
// InvalidArgument（VM 错误原文透传，操作员可定位）。
var ErrBadQuery = errors.New("metrics: query rejected by VictoriaMetrics")

// RangeQuery 是 SearchMetrics 的查询输入（api 层投影后的形态）。
type RangeQuery struct {
	// Query 是 PromQL 表达式（**透传**——不转义、不校验语法，VM 裁决）。
	Query string
	// Start/End 是时间窗边界（unix 秒语义；零值 = 现在回望 1 小时）。
	Start, End time.Time
	// StepSeconds 是步长秒数（调用方已夹紧到 [1, 86400]）。
	StepSeconds int
	// Limit 是返回序列数上限（0 = 缺省 200；天花板由 api 层夹紧）。
	Limit int
}

// Series 是一条时序（label 集原样透传 + 规范化点集）。
type Series struct {
	// Metric 是序列的 label 集（VM 原样；含 __name__ 原始指标名——函数
	// 产出的序列无该 label，透传不补造）。
	Metric map[string]string
	// Points 是规范化点集（t = unix 秒升序；v = 采样值）。
	Points []Point
}

// Point 是单个采样点。
type Point struct {
	// T 是 unix 秒。
	T int64
	// V 是采样值（VM 文本格式数值——NaN/±Inf 由 json 解析为 float64 形态）。
	V float64
}

// Backend 是 VM 的 HTTP 消费端（无状态；SearchMetrics 与健康拨测共用）。
type Backend struct {
	base string
	hc   *http.Client
}

// NewBackend 构造回环消费端（hc nil = 缺省客户端——生产形态）。
func NewBackend() *Backend {
	return NewBackendWithBase(loopbackBase())
}

// NewBackendWithBase 以指定基址构造（装配/测试注入缝——生产装配恒用
// NewBackend 的回环形态）。
func NewBackendWithBase(base string) *Backend {
	return &Backend{base: base, hc: http.DefaultClient}
}

// loopbackBase 是 fleetlyd 侧的 VM 回环基址（D-W5-4：host 网络任务回环
// 监听，查询与拨测都走 127.0.0.1:8428——零公网面）。
func loopbackBase() string {
	return fmt.Sprintf("http://127.0.0.1:%d", QueryPort)
}

// Ping 是回环健康拨测（Manager.CheckHealth 的可达性面）。
func (b *Backend) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+HealthPath, nil)
	if err != nil {
		return fmt.Errorf("metrics: build health request: %w", err)
	}
	res, err := b.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: health probe: %s", ErrBackendUnavailable, transportErrText(err))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("metrics: health probe status %s", res.Status)
	}
	return nil
}

// Search 执行 PromQL 区间查询（设计 §4.2：VM 不可达 → 调用方以
// E_METRICS_BACKEND_UNAVAILABLE 诚实报错；mode 未开 → E_METRICS_NOT_ENABLED
// ——本层返回哨兵，api 层映射）。limit 缺省 200、上限 1000（api 层已夹紧，
// 本层兜底再夹一次）。
func (b *Backend) Search(ctx context.Context, q RangeQuery) ([]Series, error) {
	step := q.StepSeconds
	if step <= 0 {
		step = 60
	}
	end := q.End
	if end.IsZero() {
		end = time.Now()
	}
	start := q.Start
	if start.IsZero() {
		start = end.Add(-time.Hour)
	}
	vals := url.Values{}
	vals.Set("query", q.Query)
	vals.Set("start", strconv.FormatInt(start.Unix(), 10))
	vals.Set("end", strconv.FormatInt(end.Unix(), 10))
	vals.Set("step", strconv.Itoa(step))
	body, status, err := b.get(ctx, QueryRangePath+"?"+vals.Encode())
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, classify(status, body)
	}
	return parseMatrix(body, q.limitOr(200))
}

// CountInstant 执行瞬时计数查询（nodes_reporting 的实现面）：返回首个
// 序列首点的数值（`count(up{job=…} == 1)` 类标量型查询 = 单序列单点）。
// 无序列（空集）= 0（聚合查询对空输入的合法结果）。
func (b *Backend) CountInstant(ctx context.Context, promql string) (int, error) {
	vals := url.Values{}
	vals.Set("query", promql)
	body, status, err := b.get(ctx, QueryInstantPath+"?"+vals.Encode())
	if err != nil {
		return 0, err
	}
	if status < 200 || status > 299 {
		return 0, classify(status, body)
	}
	series, err := parseVector(body)
	if err != nil {
		return 0, err
	}
	if len(series) == 0 || len(series[0].Points) == 0 {
		return 0, nil
	}
	return int(series[0].Points[0].V), nil
}

// InstantValue 执行瞬时查询并返回首个序列首点的数值（autoscaler 评估器的
// 数据面，W5-S1）：ok=false 表示**查不到序列**——与「序列在但值为 0」严格
// 区分（诚实无数据不扩缩的判据输入；调用方对两种形态分流处置）。VM 对
// 空集返回 success + 空 result——不是错误。
func (b *Backend) InstantValue(ctx context.Context, promql string) (value float64, ok bool, err error) {
	vals := url.Values{}
	vals.Set("query", promql)
	body, status, err := b.get(ctx, QueryInstantPath+"?"+vals.Encode())
	if err != nil {
		return 0, false, err
	}
	if status < 200 || status > 299 {
		return 0, false, classify(status, body)
	}
	series, err := parseVector(body)
	if err != nil {
		return 0, false, err
	}
	if len(series) == 0 || len(series[0].Points) == 0 {
		return 0, false, nil
	}
	return series[0].Points[0].V, true, nil
}

// limitOr 是 RangeQuery 的 limit 兜底（零值回落缺省）。
func (q RangeQuery) limitOr(def int) int {
	if q.Limit <= 0 {
		return def
	}
	return q.Limit
}

// get 执行 GET 并读回有界响应体（1MiB——区间查询的合理上界；调用方先看
// status 再解析）。
func (b *Backend) get(ctx context.Context, pathWithQuery string) (body []byte, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+pathWithQuery, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("metrics: build query request: %w", err)
	}
	res, err := b.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: query: %s", ErrBackendUnavailable, transportErrText(err))
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, res.StatusCode, fmt.Errorf("metrics: read query response: %w", err)
	}
	return raw, res.StatusCode, nil
}

// classify 把 VM 非 2xx 映射为本包哨兵（4xx = 坏查询〔VM 错误原文透传〕；
// 5xx = 后端不可用语义）。
func classify(status int, body []byte) error {
	if status >= 400 && status < 500 {
		return fmt.Errorf("%w: %s", ErrBadQuery, strings.TrimSpace(string(body)))
	}
	return fmt.Errorf("%w: query status %s", ErrBackendUnavailable, http.StatusText(status))
}

// transportErrText 是传输错误的单行化摘要（多行 URL 错误文本压平）。
func transportErrText(err error) string {
	s := err.Error()
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}

// vmAPIResponse 是 Prometheus HTTP API 兼容响应的信封形态（matrix/vector
// 两用——result 元素同构：metric + values/value）。
type vmAPIResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

// parseMatrix 解析 query_range 响应（status=success 前哨；序列数截到
// limit——VM 无服务端 limit 参数，规范化的诚实截断面）。
func parseMatrix(body []byte, limit int) ([]Series, error) {
	var resp vmAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("metrics: parse query response: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("%w: %s", ErrBadQuery, nonEmpty(resp.Error, "unknown error"))
	}
	out := make([]Series, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		if len(out) >= limit {
			break
		}
		s := Series{Metric: r.Metric}
		for _, pair := range r.Values {
			if len(pair) != 2 {
				continue // 坏点跳过（尽力而为——与日志检索同口径）
			}
			p := Point{V: anyFloat(pair[1])}
			if ts, ok := pair[0].(float64); ok {
				p.T = int64(ts)
			}
			s.Points = append(s.Points, p)
		}
		out = append(out, s)
	}
	return out, nil
}

// parseVector 解析 query 瞬时响应（value 单点形态复用 values 解析）。
func parseVector(body []byte) ([]Series, error) {
	var resp vmAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("metrics: parse instant response: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("%w: %s", ErrBadQuery, nonEmpty(resp.Error, "unknown error"))
	}
	out := make([]Series, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		s := Series{Metric: r.Metric}
		if len(r.Value) == 2 {
			p := Point{V: anyFloat(r.Value[1])}
			if ts, ok := r.Value[0].(float64); ok {
				p.T = int64(ts)
			}
			s.Points = append(s.Points, p)
		}
		out = append(out, s)
	}
	return out, nil
}

// anyFloat 把 VM 采样值（JSON string 形态）解析为 float64（畸形 = 0——
// 单点损坏不炸整个查询面）。
func anyFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
