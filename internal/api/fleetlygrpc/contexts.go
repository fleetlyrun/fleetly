package fleetlygrpc

// Runtime / Edge / Telemetry 上下文服务实现。

import (
	"context"
	"database/sql"

	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/capability"
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

func (svc *NodesService) EnrollNode(ctx context.Context, _ *runtimev1.EnrollNodeRequest) (*runtimev1.EnrollNodeResponse, error) {
	kit, err := svc.s.Runtime.Enrollment(ctx)
	if err != nil {
		return nil, mapStateError(err, "enrollment")
	}
	_ = svc.s.Audits // Enrollment 属读面（材料生成）；审计随轮换批接入
	return &runtimev1.EnrollNodeResponse{JoinCommand: kit.Command}, nil
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
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Routes.Create(ctx, tx, row); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "route.create",
			Resource: "route/" + row.ID, AfterFP: row.Host,
		})
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
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Routes.SoftDelete(ctx, tx, req.GetId()); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "route.delete",
			Resource: "route/" + req.GetId(),
		})
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

// ---- Logs（F0.25：RuntimeLogs 直读，流实现随 C8 批接入） ----

type LogsService struct {
	telemetryv1.UnimplementedLogsServiceServer
	s *Services
}
