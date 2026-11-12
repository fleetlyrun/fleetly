// Package metricsgrpc 实现 fleetly.telemetry.v1.MetricsService：raw PromQL
// 平台管理员诊断面与租户 scoped 图表面（服务端固定模板 + 行级授权）。
package metricsgrpc

import (
	"context"
	"errors"
	"regexp"
	"time"

	"google.golang.org/grpc"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// Authorizer 是行级授权消费面（fleetlygrpc.Services 实现）。
type Authorizer interface {
	AuthorizeAppID(ctx context.Context, appID string) (*app.App, error)
	AuthorizeProjectID(ctx context.Context, projectID string) error
}

// Deps 是指标查询服务的依赖（窄接口，不持整个 gRPC Services）。
type Deps struct {
	// Metrics 是动态读面（apitest 会在构造后替换 Services.Metrics 注入
	// 假件；闭包保持晚绑定）。
	Metrics      func() capability.Metrics
	Databases    database.Repo
	PubSubs      pubsub.Repo
	BucketStores bucketstore.Repo
	Auth         Authorizer
}

// Service 满足 telemetryv1.MetricsServiceServer。
type Service struct {
	telemetryv1.UnimplementedMetricsServiceServer
	deps Deps
}

// New 构造指标查询服务。
func New(deps Deps) *Service { return &Service{deps: deps} }

// Register 注册指标查询服务。
func Register(srv *grpc.Server, deps Deps) {
	telemetryv1.RegisterMetricsServiceServer(srv, New(deps))
}

var _ telemetryv1.MetricsServiceServer = (*Service)(nil)

// metrics 返回动态指标存储读面（零值 Deps = nil，精确 503）。
func (d Deps) metrics() capability.Metrics {
	if d.Metrics == nil {
		return nil
	}
	return d.Metrics()
}

// MetricsService 是指标查询面。

// QueryMetrics 透传 PromQL 到受管 VM（平台管理员诊断面；Metrics 面停用时
// 精确失败）。
//
// 授权（H8 阶段 A 止血，设计文档 docs/design/2026-10-19-security-backlog-
// design.md §5.2）：raw PromQL 让调用方自带表达式与标签，服务端不引入
// PromQL 解析器就无法约束行级作用域；单存储 + 平台凭证下这是跨 Team
// 遥测读，故本面收归平台管理员（default Team 的 builtin-owner）。门在
// Metrics nil 检查之前：非平台管理员不得用 403/503 差异探测存储是否启用，
// 拒绝零副作用。
//
// 阶段 B（H8）：租户图表面走 QueryScopedMetrics（服务端固定模板 + 行级
// 授权）；本函数保留为平台管理员诊断面，仍受体/序列/超时上限。
func (svc *Service) QueryMetrics(ctx context.Context, req *telemetryv1.QueryMetricsRequest) (*telemetryv1.QueryMetricsResponse, error) {
	if err := grpcapi.RequirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	if svc.deps.metrics() == nil {
		// 未配 metrics.addr 是显式关态（P4-3 批 3）：精确 503，不再落
		// E_INTERNAL 脱敏 500。
		return nil, apperr.New("E_METRICS_STORE_UNAVAILABLE", "metrics queries require the managed metrics store; set config metrics.addr to enable it")
	}
	// query 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致；start/end
	// 时间窗是动态文案（RFC3339 解析带 got 回显），保留手写。
	start, end, err := parseMetricsWindow(req.GetStart(), req.GetEnd())
	if err != nil {
		return nil, err
	}
	// raw 步长：与 scoped 共用符号校验，但诊断面不钳制 24h/点数（管理员
	// 可查长窗；体/序列/超时上限兜底）。
	if req.GetStepSeconds() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "step_seconds: must not be negative (got %d)", req.GetStepSeconds())
	}
	step := time.Duration(req.GetStepSeconds()) * time.Second
	if step == 0 {
		step = capability.ScopedMetricsMinStep
	}
	series, err := svc.deps.metrics().QuerySeries(ctx, req.GetQuery(), start, end, step)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "metrics")
	}
	return &telemetryv1.QueryMetricsResponse{Series: metricSeriesMsgs(series)}, nil
}

// scopedProcessPattern 是 process 词法的受理面形态（与 Process 名字符集
// 同源：小写字母数字与内部连字符，首尾字母数字；设计文档 §5.2）。
var scopedProcessPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// QueryScopedMetrics 是租户图表面（H8 阶段 B）：调用方只给 scope（App、
// Database 或 Project 恰一）+ 指标白名单 + 时间窗；PromQL 由 capability
// 固定模板构造，服务端不接受表达式。
//
// 处理顺序（设计文档 §5.2；未授权调用方不得用 503/400 差异探测资源）：
// protovalidate（拦截器）→ 行级授权 → Metrics store 状态 → process/
// cpu_percent 动态校验 → 窗口/step 钳制 → QueryScoped → 映射。
func (svc *Service) QueryScopedMetrics(ctx context.Context, req *telemetryv1.QueryScopedMetricsRequest) (*telemetryv1.QueryScopedMetricsResponse, error) {
	q := capability.ScopedQuery{Metric: req.GetMetric()}
	var projectID string
	switch {
	case req.GetAppId() != "":
		// project_id 不由客户端给：从 App 行解析，避免"伪造 project +
		// 合法 app"的组合；行不存在 404 / 他队 403 与既有单源同源。
		a, err := svc.deps.Auth.AuthorizeAppID(ctx, req.GetAppId())
		if err != nil {
			return nil, err
		}
		projectID = a.ProjectID
		q.AppID = a.ID
		q.AppName = a.Name
	case req.GetDatabaseId() != "":
		db, err := svc.deps.Databases.Get(ctx, req.GetDatabaseId())
		if err != nil {
			return nil, grpcapi.MapStateError(err, "database")
		}
		if err := svc.deps.Auth.AuthorizeProjectID(ctx, db.ProjectID); err != nil {
			return nil, err
		}
		projectID = db.ProjectID
		q.DatabaseID = db.ID
		q.DatabaseName = db.Name
	case req.GetPubsubId() != "":
		// pubsub scope（B4b，ADR-0096 决策 2：双臂正则连坐——swarm 词汇臂
		// pubsub 标签 / k3s pod 名臂 fleetly-pub-<name>-<id>）。
		ps, err := svc.deps.PubSubs.Get(ctx, req.GetPubsubId())
		if err != nil {
			return nil, grpcapi.MapStateError(err, "pubsub")
		}
		if err := svc.deps.Auth.AuthorizeProjectID(ctx, ps.ProjectID); err != nil {
			return nil, err
		}
		projectID = ps.ProjectID
		q.PubSubID = ps.ID
		q.PubSubName = ps.Name
	case req.GetBucketStoreId() != "":
		// bucket store scope（B5，ADR-0096 决策 2：双臂正则连坐——swarm 词汇
		// 臂 bucketstore 标签 / k3s pod 名臂 fleetly-bkt-<name>-<id>）。
		bs, err := svc.deps.BucketStores.Get(ctx, req.GetBucketStoreId())
		if err != nil {
			return nil, grpcapi.MapStateError(err, "bucket_store")
		}
		if err := svc.deps.Auth.AuthorizeProjectID(ctx, bs.ProjectID); err != nil {
			return nil, err
		}
		projectID = bs.ProjectID
		q.BucketStoreID = bs.ID
		q.BucketStoreName = bs.Name
	case req.GetProjectId() != "":
		// project scope：行级授权沿 anchor 链（不存在 404 / 跨队 403 /
		// 平台管理员豁免），与 app/database 同源同序——授权先于 store
		// 状态，未授权调用方探测不到 503/400 差异。
		if err := svc.deps.Auth.AuthorizeProjectID(ctx, req.GetProjectId()); err != nil {
			return nil, err
		}
		projectID = req.GetProjectId()
	default:
		// protovalidate 的 message CEL 已在受理面拦截（scope 恰一）；防御
		// 分支保持同一可编程错误。
		return nil, apperr.New("E_INVALID_ARGUMENT", "exactly one of app_id, database_id, pubsub_id, bucket_store_id or project_id must be set")
	}
	// store 状态在授权之后暴露（503 不泄漏资源存在性）。
	if svc.deps.metrics() == nil {
		return nil, apperr.New("E_METRICS_STORE_UNAVAILABLE", "metrics queries require the managed metrics store; set config metrics.addr to enable it")
	}
	// 动态参数校验：database/project 页没有 process/cpu_percent 消费面，
	// 显式拒绝而非静默忽略（设计文档 §5.7；project 汇总没有明确的 CPU
	// 分母，故 cpu_percent 只留 app scope）。
	if req.GetProcess() != "" {
		if q.AppID == "" {
			return nil, apperr.New("E_INVALID_ARGUMENT", "process: only available for app scope (database and project scopes have no process dimension)")
		}
		if !scopedProcessPattern.MatchString(req.GetProcess()) {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"process: must be a dns label (lowercase letters, digits, inner hyphens; start and end alphanumeric, got %q)", req.GetProcess())
		}
		q.Process = req.GetProcess()
	}
	if q.AppID == "" && q.Metric == capability.MetricCPUPercent {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"metric: cpu_percent is only available for app scope (database and project scopes support %s and %s)",
			capability.MetricCPUCores, capability.MetricMemoryWorkingSet)
	}
	start, end, err := parseMetricsWindow(req.GetStart(), req.GetEnd())
	if err != nil {
		return nil, err
	}
	step, err := capability.ClampScopedStep(start, end, req.GetStepSeconds())
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%s", err.Error())
	}
	q.ProjectID = projectID
	q.Start, q.End, q.Step = start, end, step
	q.MaxSeries = capability.ScopedMetricsMaxSeries
	page, err := svc.deps.metrics().QueryScoped(ctx, q)
	if err != nil {
		// scoped 模板是服务端固定的：VM 4xx 是平台缺陷，不能沿用 raw 的
		// E_INVALID_ARGUMENT（调用方无法修模板）；带 cause 供排查。
		var qe *capability.MetricsQueryError
		if errors.As(err, &qe) {
			return nil, apperr.New("E_INTERNAL",
				"scoped metrics query was rejected by the metrics store (server-owned template): %s", qe.Message).WithCause(err)
		}
		// 超体/超序列与超时走共享映射（E_METRICS_RESULT_TOO_LARGE /
		// E_METRICS_QUERY_TIMEOUT）。
		return nil, grpcapi.MapStateError(err, "metrics")
	}
	return &telemetryv1.QueryScopedMetricsResponse{
		Series:      metricSeriesMsgs(page.Series),
		Truncated:   page.Truncated,
		SeriesTotal: int32(page.Total), //nolint:gosec // 命中数受 VM 响应体上限约束（远小于 int32）
	}, nil
}

// parseMetricsWindow 解析 raw/scoped 共用的时间窗（RFC3339；空 end = now，
// 空 start = end 前 1h），并校验 start < end。
func parseMetricsWindow(startRaw, endRaw string) (time.Time, time.Time, error) {
	end := time.Now().UTC()
	if raw := endRaw; raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, apperr.New("E_INVALID_ARGUMENT", "end: must be RFC3339 (got %q)", raw)
		}
		end = t
	}
	start := end.Add(-time.Hour)
	if raw := startRaw; raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, apperr.New("E_INVALID_ARGUMENT", "start: must be RFC3339 (got %q)", raw)
		}
		start = t
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, apperr.New("E_INVALID_ARGUMENT", "start: must be before end")
	}
	return start, end, nil
}

// metricSeriesMsgs 是 capability.Series → proto 传输形态（raw/scoped 共用）。
func metricSeriesMsgs(series []capability.Series) []*telemetryv1.MetricSeries {
	out := make([]*telemetryv1.MetricSeries, 0, len(series))
	for _, s := range series {
		msg := &telemetryv1.MetricSeries{Labels: s.Metric}
		for _, p := range s.Points {
			msg.Points = append(msg.Points, &telemetryv1.MetricPoint{
				Time:  p.Time.Format(time.RFC3339),
				Value: p.Value,
			})
		}
		out = append(out, msg)
	}
	return out
}
