package fleetlygrpc

// Runtime / Edge / Telemetry 上下文服务实现。

import (
	"context"
	"database/sql"
	"errors"
	"time"

	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/route"
)

// ---- Nodes（F0.20：观测缓存 + 加入材料） ----

type NodesService struct {
	runtimev1.UnimplementedNodesServiceServer
	s *Services
}

func (svc *NodesService) ListNodes(ctx context.Context, _ *runtimev1.ListNodesRequest) (*runtimev1.ListNodesResponse, error) {
	list, err := svc.s.Nodes.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "node")
	}
	out := &runtimev1.ListNodesResponse{}
	for _, n := range list {
		out.Nodes = append(out.Nodes, nodeMsg(n))
	}
	return out, nil
}

func (svc *NodesService) EnrollNode(ctx context.Context, req *runtimev1.EnrollNodeRequest) (*runtimev1.EnrollNodeResponse, error) {
	// 材料生成与轮换都是集群面敏感动作：审计如实区分（EnrollNode 是
	// platform:admin 档，C3）。
	action := "node.enroll"
	if req.GetRotate() {
		action = "node.rotate_join_tokens"
	}
	// 轮换是破坏性动作（全部既有 join token 作废）：先审计后轮换
	//（N0.1 P2-11）——审计落账失败时材料绝不作废；轮换失败时审计已在
	// 场（意图可见，比"已轮换无痕"诚实）。普通材料生成只读，同样先记
	// 后做不损失语义（读取意图本身敏感）。
	if err := svc.s.commit(ctx, writeFact{
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: action, Resource: "cluster/join-material",
		}},
	}); err != nil {
		return nil, err
	}
	kit, err := svc.s.Runtime.Enrollment(ctx, req.GetRotate())
	if err != nil {
		return nil, mapStateError(err, "enrollment")
	}
	return &runtimev1.EnrollNodeResponse{JoinCommand: kit.Command}, nil
}

// DrainNode 把节点置为排空（F0.19 RuntimeAdmin 面；平台节点 ID 为锚）。
func (svc *NodesService) DrainNode(ctx context.Context, req *runtimev1.DrainNodeRequest) (*runtimev1.DrainNodeResponse, error) {
	if err := svc.nodeAdmin(ctx, req.GetNodeId(), "node.drain", func(a capability.RuntimeAdmin, nodeID string) error {
		return a.Drain(ctx, nodeID)
	}); err != nil {
		return nil, err
	}
	return &runtimev1.DrainNodeResponse{}, nil
}

// CordonNode 封锁节点（拒绝新调度，存量不动）。
func (svc *NodesService) CordonNode(ctx context.Context, req *runtimev1.CordonNodeRequest) (*runtimev1.CordonNodeResponse, error) {
	if err := svc.nodeAdmin(ctx, req.GetNodeId(), "node.cordon", func(a capability.RuntimeAdmin, nodeID string) error {
		return a.Cordon(ctx, nodeID)
	}); err != nil {
		return nil, err
	}
	return &runtimev1.CordonNodeResponse{}, nil
}

// UncordonNode 解除封锁。
func (svc *NodesService) UncordonNode(ctx context.Context, req *runtimev1.UncordonNodeRequest) (*runtimev1.UncordonNodeResponse, error) {
	if err := svc.nodeAdmin(ctx, req.GetNodeId(), "node.uncordon", func(a capability.RuntimeAdmin, nodeID string) error {
		return a.Uncordon(ctx, nodeID)
	}); err != nil {
		return nil, err
	}
	return &runtimev1.UncordonNodeResponse{}, nil
}

// nodeAdmin 是三个 RuntimeAdmin 动词的公共骨架：校验锚点 → 判子面可用 →
// 执行 → 落审计行。副作用在编排器侧、不可与审计同事务：先变更后留痕，
// 审计失败如实报错（F0.7 全部写操作留痕契约）；结果即动作本身，无前后
// 值指纹可记。
func (svc *NodesService) nodeAdmin(ctx context.Context, nodeID, action string, op func(capability.RuntimeAdmin, string) error) error {
	if nodeID == "" {
		return apperr.New("E_INVALID_ARGUMENT", "node_id: must not be empty")
	}
	admin, ok := svc.s.Runtime.(capability.RuntimeAdmin)
	if !ok {
		return apperr.New("E_INTERNAL", "the runtime provider does not expose node administration")
	}
	if err := op(admin, nodeID); err != nil {
		if errors.Is(err, capability.ErrNodeNotFound) {
			return apperr.New("E_NOT_FOUND", "node not found").WithCause(err)
		}
		return apperr.New("E_INTERNAL", "node administration failed").WithCause(err)
	}
	return svc.s.commit(ctx, writeFact{
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: action, Resource: "node/" + nodeID,
		}},
	})
}

// ---- Routes（Edge & TLS 上下文） ----

type RoutesService struct {
	edgev1.UnimplementedRoutesServiceServer
	s *Services
}

func (svc *RoutesService) CreateRoute(ctx context.Context, req *edgev1.CreateRouteRequest) (*edgev1.CreateRouteResponse, error) {
	if req.GetProjectId() == "" || req.GetHost() == "" || req.GetAppId() == "" || req.GetProcess() == "" || req.GetPort() == 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id, host, app_id, process and port: must not be empty")
	}
	protocol := capability.Protocol(req.GetProtocol())
	if protocol == "" {
		protocol = capability.ProtocolHTTP
	}
	tlsMode := req.GetTlsMode()
	if tlsMode == "" {
		tlsMode = "auto"
	}
	row := &route.Route{
		ID: newID(), ProjectID: req.GetProjectId(), Host: req.GetHost(), Path: req.GetPath(),
		AppID: req.GetAppId(), Process: req.GetProcess(), Port: req.GetPort(),
		Protocol: protocol, TLSMode: tlsMode,
	}
	// 父资源存活校验（批 0 复核，同族面）：路由挂在不存在/已删的
	// Project 或 App 下此前直接成功（routes 无 FK）——活路由指向已删
	// App 会让 Edge 全量发布把流量钉在 tombstone 上。
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.projectAppAlive(req.GetProjectId(), req.GetAppId()),
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Routes.Create(ctx, tx, row)
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "route.create",
			Resource: "route/" + row.ID, AfterFP: row.Host,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "route")
	}
	svc.s.Engine.PublishRoutesNow() // Edge 全量发布即时触发
	return &edgev1.CreateRouteResponse{Route: routeMsg(*row)}, nil
}

func (svc *RoutesService) ListRoutes(ctx context.Context, req *edgev1.ListRoutesRequest) (*edgev1.ListRoutesResponse, error) {
	list, err := svc.s.Routes.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "route")
	}
	out := &edgev1.ListRoutesResponse{}
	for _, r := range list {
		if req.GetProjectId() != "" && r.ProjectID != req.GetProjectId() {
			continue
		}
		out.Routes = append(out.Routes, routeMsg(r))
	}
	return out, nil
}

func (svc *RoutesService) DeleteRoute(ctx context.Context, req *edgev1.DeleteRouteRequest) (*edgev1.DeleteRouteResponse, error) {
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Routes.SoftDelete(ctx, tx, req.GetId())
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "route.delete",
			Resource: "route/" + req.GetId(),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "route")
	}
	svc.s.Engine.PublishRoutesNow()
	return &edgev1.DeleteRouteResponse{}, nil
}

// ---- Events（F0.23 读路径；流式 follow N1） ----

type EventsService struct {
	telemetryv1.UnimplementedEventsServiceServer
	s *Services
}

func (svc *EventsService) ListEvents(ctx context.Context, req *telemetryv1.ListEventsRequest) (*telemetryv1.ListEventsResponse, error) {
	limit := req.GetLimit()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	events, err := svc.s.OutboxEvents.ListAfter(ctx, svc.s.DB.Runner(), req.GetAfterSeq(), int(limit))
	if err != nil {
		return nil, mapStateError(err, "events")
	}
	lastSeq, err := svc.s.OutboxEvents.LastSeq(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "events")
	}
	out := &telemetryv1.ListEventsResponse{LastSeq: lastSeq}
	for _, ev := range events {
		out.Events = append(out.Events, &telemetryv1.Event{
			Seq: ev.Seq, Name: ev.Name, Aggregate: ev.Aggregate, AggregateId: ev.AggregateID,
			Payload: string(ev.Payload), CreatedAt: ev.CreatedAt,
		})
	}
	return out, nil
}

// ---- Logs（F0.25：RuntimeLogs 直读；诚实边界——仅实时+最近缓冲，持久化
// 检索 N2。Runtime 未实现 RuntimeLogs 子面时显式降级 E_INTERNAL →） ----

type LogsService struct {
	telemetryv1.UnimplementedLogsServiceServer
	s *Services
}

// StreamLogs 转发 RuntimeLogs 流（appID → 隔离域解析经 app 行）。
func (svc *LogsService) StreamLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	logs, ok := svc.s.Runtime.(capability.RuntimeLogs)
	if !ok {
		return apperr.New("E_INTERNAL", "the runtime provider does not expose container logs")
	}
	if req.GetAppId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty")
	}
	appRow, err := svc.s.Apps.Get(stream.Context(), svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return mapStateError(err, "app")
	}
	q, err := logQueryFromRequest(appRow, req)
	if err != nil {
		return err
	}
	w := &streamLogWriter{stream: stream}
	if err := logs.StreamLogs(stream.Context(), q, w); err != nil {
		return mapStateError(err, "logs")
	}
	return nil
}

// logQueryFromRequest 把 RPC 请求翻译为 RuntimeLogs 查询：process 过滤经
// 投影公式合成 Workload ID（公式真源 engine.WorkloadID，不散拼）；时间窗
// RFC3339（空 = 不限，坏值精确拒绝）。
func logQueryFromRequest(appRow *app.App, req *telemetryv1.StreamLogsRequest) (capability.LogQuery, error) {
	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: "default", Project: appRow.ProjectID, App: appRow.ID},
		TailLines: req.GetTailLines(),
		Follow:    req.GetFollow(),
	}
	if p := req.GetProcess(); p != "" {
		q.WorkloadID = engine.WorkloadID(appRow.ID, p)
	}
	if raw := req.GetSince(); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return q, apperr.New("E_INVALID_ARGUMENT", "since: must be RFC3339 (got %q)", raw)
		}
		q.Since = t
	}
	if raw := req.GetUntil(); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return q, apperr.New("E_INVALID_ARGUMENT", "until: must be RFC3339 (got %q)", raw)
		}
		q.Until = t
	}
	return q, nil
}

// streamLogWriter 把 RuntimeLogs 帧转发为 RPC 流帧。
type streamLogWriter struct {
	stream telemetryv1.LogsService_StreamLogsServer
}

func (w *streamLogWriter) WriteLog(ctx context.Context, f capability.LogFrame) error {
	return w.stream.Send(&telemetryv1.StreamLogsResponse{
		WorkloadId: f.WorkloadID, Container: f.Container, Node: f.Node,
		Time: f.Time.UTC().Format(timeFormatRFC3339), Line: f.Line,
	})
}

// timeFormatRFC3339 是日志帧时间形态（ADR-0018 UTC）。
const timeFormatRFC3339 = "2006-01-02T15:04:05.000Z07:00"
