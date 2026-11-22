// functionstats.go 是 per-Function 请求观测的核门面（P2-4 per-invocation
// 观测轴消费面，ADR-0103 决策 4 的查询半边）：host→分账键经 Proxy 的
// RouteMetricsSource 子面（routeKey 公式不出 Provider——B1 律），PromQL
// 构造在此单源（traefik_service_requests_total / traefik_service_request_
// duration_seconds_bucket 双查），Metrics.QuerySeries 拉序列。private 函数
// 无请求观测（结构性断口）——observed=false 诚实呈现。采集透传面在
// metrics 应用模块（manager 节点抓 traefik :9091，job=fleetly-traefik）。
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// FunctionRequestStats 实现函数请求观测门面（deliverygrpc GetFunctionStats
// 消费；public 且路由 host 在册才可查——observed=false 是观测面结构性
// 缺席，与「窗口无请求」的空序列分立）。
func (e *Engine) FunctionRequestStats(ctx context.Context, functionID string, window time.Duration) (contract.FunctionRequestStats, error) {
	out := contract.FunctionRequestStats{}
	if window <= 0 || window > 24*time.Hour {
		window = time.Hour
	}
	row, err := e.repos.Functions.Get(ctx, functionID)
	if err != nil {
		return out, err
	}
	metrics := e.MetricsProvider()
	if metrics == nil || row.Exposure != "public" || row.PublicHost == "" {
		return out, nil // 观测面缺席 / private（结构性）——observed=false
	}
	key := ""
	if src := capability.FacesOf(e.proxy).RouteMetrics; src != nil {
		key = src.RouteMetricKey(capability.Route{Host: row.PublicHost})
	}
	if key == "" {
		return out, nil // Proxy 无 per-route 分账面（诚实断口）
	}
	end := e.clock.Now()
	start := end.Add(-window)
	// 请求率：traefik per-service 请求计数 rate（键 = router/service 命名
	// 同源——traefik v3 的 service 维度计数稳定跨路由形状）。
	rateQ := fmt.Sprintf(`sum(rate(traefik_service_requests_total{service="%s"}[2m]))`, key)
	rateSeries, err := metrics.QuerySeries(ctx, rateQ, start, end, 0)
	if err != nil {
		return out, fmt.Errorf("function request rate: %w", err)
	}
	if len(rateSeries) > 0 {
		out.RequestsPerSecond = rateSeries[0].Points
	}
	// 延迟 p95：traefik 请求时长直方图（bucket le 上界秒——毫秒换算在此）。
	p95Q := fmt.Sprintf(
		`histogram_quantile(0.95, sum by (le) (rate(traefik_service_request_duration_seconds_bucket{service="%s"}[2m]))) * 1000`, key)
	p95Series, err := metrics.QuerySeries(ctx, p95Q, start, end, 0)
	if err != nil {
		return out, fmt.Errorf("function p95: %w", err)
	}
	if len(p95Series) > 0 {
		out.P95Ms = p95Series[0].Points
	}
	out.Observed = true
	return out, nil
}
