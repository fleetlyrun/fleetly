package fleetlygrpc

// Runtime / Proxy / Telemetry 上下文服务实现。

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/route"
)

// ---- Nodes（F0.20：观测缓存 + 加入材料） ----

type NodesService struct {
	runtimev1.UnimplementedNodesServiceServer
	s *Services
}

func (svc *NodesService) ListNodes(ctx context.Context, req *runtimev1.ListNodesRequest) (*runtimev1.ListNodesResponse, error) {
	list, err := svc.s.Nodes.ListPage(ctx, svc.s.DB.Runner(),
		req.GetAfterNodeId(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "node")
	}
	// 中继在连状态是活体观测（engine hub 快照，非 nodes 表行——代理断连
	// 即失，无持久化面；ADR-0049）。
	relay := svc.s.Engine.RelayStatuses()
	out := &runtimev1.ListNodesResponse{}
	for _, n := range list {
		msg := nodeMsg(n)
		if st, ok := relay[n.PlatformID]; ok {
			msg.RelayOnline = st.Online
			msg.RelayAgentVersion = st.Version
		}
		out.Nodes = append(out.Nodes, msg)
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
	kit, err := svc.s.Runtime.Enrollment(ctx, req.GetRotate(), capability.EnrollmentOptions{GatewayPort: svc.s.GatewayPort})
	if err != nil {
		return nil, mapStateError(err, "enrollment")
	}
	return &runtimev1.EnrollNodeResponse{JoinCommand: kit.Command, AgentCommand: kit.AgentCommand}, nil
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
	admin := capability.FacesOf(svc.s.Runtime).Admin // 节点管理子面（FacesOf 协商点）
	if admin == nil {
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

// ---- Routes（Proxy & TLS 上下文） ----

type RoutesService struct {
	proxyv1.UnimplementedRoutesServiceServer
	s *Services
}

func (svc *RoutesService) CreateRoute(ctx context.Context, req *proxyv1.CreateRouteRequest) (*proxyv1.CreateRouteResponse, error) {
	if req.GetProjectId() == "" || req.GetHost() == "" || req.GetAppId() == "" || req.GetProcess() == "" || req.GetPort() == 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id, host, app_id, process and port: must not be empty")
	}
	// 受理面校验（安全批 P0）：host/path 原样内插进 traefik 规则的反引号
	// 定界符内（Host(`%s`)），反引号等元字符可注入/劫持路由规则；白名单
	// 与 Proxy Provider 纵深面共用同一真源（capability.ValidateRouteHost）。
	if err := capability.ValidateRouteHost(req.GetHost()); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%v", err)
	}
	if err := capability.ValidateRoutePath(req.GetPath()); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%v", err)
	}
	protocol := capability.Protocol(req.GetProtocol())
	if protocol == "" {
		protocol = capability.ProtocolHTTP
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
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
	// App 会让 Proxy 全量发布把流量钉在 tombstone 上。
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
	svc.s.Engine.PublishRoutesNow() // Proxy 全量发布即时触发
	return &proxyv1.CreateRouteResponse{Route: routeMsg(*row)}, nil
}

// ListRoutes 非 owner 按 Team 过滤（ADR-0035 List 面：行级过滤在内存比对
// 调用方 Team 的 project 集合；owner 全量）。project 过滤下推 SQL 与游标
// 分页叠加（过滤语义不变）；Team 过滤在分页后的页内比对（peers 可见性
// 过滤同款形态——过滤语义不变，非 owner 的页可能稀疏）。
func (svc *RoutesService) ListRoutes(ctx context.Context, req *proxyv1.ListRoutesRequest) (*proxyv1.ListRoutesResponse, error) {
	teamProjects, all, err := svc.s.teamProjectFilter(ctx)
	if err != nil {
		return nil, err
	}
	list, err := svc.s.Routes.ListPage(ctx, svc.s.DB.Runner(),
		req.GetProjectId(), req.GetAfterRouteId(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "route")
	}
	out := &proxyv1.ListRoutesResponse{}
	for _, r := range list {
		if !all && !teamProjects[r.ProjectID] {
			continue
		}
		out.Routes = append(out.Routes, routeMsg(r))
	}
	return out, nil
}

func (svc *RoutesService) DeleteRoute(ctx context.Context, req *proxyv1.DeleteRouteRequest) (*proxyv1.DeleteRouteResponse, error) {
	// 行级授权（ADR-0035）：此前 ID 直删零校验。
	if err := svc.s.authorizeRouteID(ctx, req.GetId()); err != nil {
		return nil, err
	}
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
	return &proxyv1.DeleteRouteResponse{}, nil
}

// ---- Events（F0.23 读路径；流式 follow N1） ----

type EventsService struct {
	telemetryv1.UnimplementedEventsServiceServer
	s *Services
}

func (svc *EventsService) ListEvents(ctx context.Context, req *telemetryv1.ListEventsRequest) (*telemetryv1.ListEventsResponse, error) {
	if err := svc.eventsGoneCheck(ctx, req.GetAfterSeq()); err != nil {
		return nil, err
	}
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
		out.Events = append(out.Events, eventMsg(&ev))
	}
	return out, nil
}

// streamEventsPollInterval 是 follow 模式的轮询拍（outbox 单调 seq 是权威
// 源；拍取舍：Console/Agent 人机尺度下 250ms 足够即时，DB 压力可忽略）。
const streamEventsPollInterval = 250 * time.Millisecond

// eventsGoneCheck 断档判定（ADR-0026）：after_seq 落在保留窗外 → 410 带
// earliest_seq 上下文（客户端重同步：重读所跟踪资源的列表 + 以
// GetEventStatus.last_seq 为新游标）。0 = 从保留窗最早开始，非断档。
// 窗被清空（earliest=0 且 last=0）时任何正游标都判档：游标指向的既成
// 事实已不可达，诚实答案就是重同步（fresh install 同形——resync 同样
// 正确）。seq 是 AUTOINCREMENT，清空后不复用，游标错位不会被掩盖。
func (svc *EventsService) eventsGoneCheck(ctx context.Context, afterSeq int64) error {
	if afterSeq <= 0 {
		return nil
	}
	earliest, err := svc.s.OutboxEvents.EarliestSeq(ctx, svc.s.DB.Runner())
	if err != nil {
		return mapStateError(err, "events")
	}
	last, err := svc.s.OutboxEvents.LastSeq(ctx, svc.s.DB.Runner())
	if err != nil {
		return mapStateError(err, "events")
	}
	gone := (earliest > 0 && afterSeq < earliest) || (earliest == 0 && last == 0)
	if gone {
		return apperr.New("E_EVENTS_GONE",
			"event cursor %d is older than the earliest retained event (the retention window trimmed it)", afterSeq).
			WithContext("earliest_seq", strconv.FormatInt(earliest, 10)).
			WithSuggestion("Resynchronize: re-read the current state of the resources you track, then continue from GetEventStatus.last_seq.")
	}
	return nil
}

// StreamEvents 是订阅面（gRPC server-streaming）：重放保留窗（after_seq
// 起升序），follow 时持续跟随直至客户端取消（流面不经 unary 超时拦截器，
// 生命周期由取消信号管理——架构 §7 等待原语的数据源）。
func (svc *EventsService) StreamEvents(req *telemetryv1.StreamEventsRequest, stream telemetryv1.EventsService_StreamEventsServer) error {
	return svc.s.subscribeEvents(stream.Context(), req.GetAfterSeq(), req.GetFollow(), func(ev *telemetryv1.Event) error {
		return stream.Send(&telemetryv1.StreamEventsResponse{Event: ev})
	})
}

// GetEventStatus 是断档判定与快照重同步的基准（earliest_seq / last_seq）。
func (svc *EventsService) GetEventStatus(ctx context.Context, _ *telemetryv1.GetEventStatusRequest) (*telemetryv1.GetEventStatusResponse, error) {
	earliest, err := svc.s.OutboxEvents.EarliestSeq(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "events")
	}
	last, err := svc.s.OutboxEvents.LastSeq(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "events")
	}
	return &telemetryv1.GetEventStatusResponse{EarliestSeq: earliest, LastSeq: last}, nil
}

// IssueEventTicket 为 SSE 订阅路径换一次性短时票据（ADR-0026：浏览器
// EventSource 不能设自定义头）。
func (svc *EventsService) IssueEventTicket(ctx context.Context, _ *telemetryv1.IssueEventTicketRequest) (*telemetryv1.IssueEventTicketResponse, error) {
	if _, ok := authn.FromContext(ctx); !ok {
		return nil, apperr.New("E_UNAUTHENTICATED", "present a valid token to mint an event ticket")
	}
	ticket, ttl, err := svc.s.eventTickets.issue(ticketPurposeEvents, "")
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "ticket generation failed").WithCause(err)
	}
	return &telemetryv1.IssueEventTicketResponse{Ticket: ticket, ExpiresIn: int32(ttl.Seconds())}, nil //nolint:gosec // TTL 秒级
}

// subscribeEvents 是订阅核心（gRPC 流与 SSE 原生入口同源同口径）：断档
// 判定 → 批次重放 → follow 轮询（ctx 取消收口）。send 返回错误即收流。
func (s *Services) subscribeEvents(ctx context.Context, afterSeq int64, follow bool, send func(*telemetryv1.Event) error) error {
	if err := (&EventsService{s: s}).eventsGoneCheck(ctx, afterSeq); err != nil {
		return err
	}
	cursor := afterSeq
	for {
		batch, err := s.OutboxEvents.ListAfter(ctx, s.DB.Runner(), cursor, 1000)
		if err != nil {
			return mapStateError(err, "events")
		}
		for i := range batch {
			if err := send(eventMsg(&batch[i])); err != nil {
				return err
			}
			cursor = batch[i].Seq
		}
		if !follow {
			return nil
		}
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(streamEventsPollInterval):
			}
		}
	}
}

// eventMsg 是 outbox 行 → 传输形态（单点，List 与订阅面共用）。
func eventMsg(ev *outbox.Event) *telemetryv1.Event {
	return &telemetryv1.Event{
		Seq: ev.Seq, Name: ev.Name, Aggregate: ev.Aggregate, AggregateId: ev.AggregateID,
		Payload: string(ev.Payload), CreatedAt: ev.CreatedAt,
	}
}

// ---- Logs（ADR-0040 双径：无 text = Runtime 实时路径——集群面
// ServiceLogs（swarm），字节级保持既有形态；text = 持久化检索路径
//（VictoriaLogs 保留窗 + 全文过滤，Follow 经 /select/logsql/tail 实时
// 尾随）。Logging 面停用时检索路径精确失败、实时路径不受影响。） ----

type LogsService struct {
	telemetryv1.UnimplementedLogsServiceServer
	s *Services
}

// StreamLogs 双径路由（appID → 隔离域解析经 app 行 + project 行——Team
// 轴实取；ADR-0035 行级授权同调用点——两路径共用同一授权链）。
func (svc *LogsService) StreamLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	if req.GetAppId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty")
	}
	ctx := stream.Context()
	appRow, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return mapStateError(err, "app")
	}
	// 行级授权（ADR-0035）+ NamespaceRef.Team 实取（App → Project 行，
	// resolveBackend 同族——api 面硬编码 "default" 漏网在此闭合）。
	proj, err := svc.s.Projects.Get(ctx, svc.s.DB.Runner(), appRow.ProjectID)
	if err != nil {
		return mapStateError(err, "project")
	}
	if err := svc.s.authorizeTeamForProject(ctx, proj); err != nil {
		return err
	}
	q, err := logQueryFromRequest(proj.TeamID, appRow, req)
	if err != nil {
		return err
	}
	w := &streamLogWriter{stream: stream}
	if req.GetText() != "" {
		// 检索路径（ADR-0040 决策 4）：行级隔离由查询构造执法——只携带
		// 本 App 的域字段（VL 单租户，凭证不离开 daemon）。
		if svc.s.Logging == nil {
			return apperr.New("E_INTERNAL", "log search requires the managed log store; set config logging.addr to enable it")
		}
		if err := svc.s.Logging.Query(stream.Context(), q, w); err != nil {
			return mapStateError(err, "logs")
		}
		return nil
	}
	logs := capability.FacesOf(svc.s.Runtime).Logs // 日志子面（FacesOf 协商点）
	if logs == nil {
		return apperr.New("E_INTERNAL", "the runtime provider does not expose container logs")
	}
	if err := logs.StreamLogs(stream.Context(), q, w); err != nil {
		return mapStateError(err, "logs")
	}
	return nil
}

// logQueryFromRequest 把 RPC 请求翻译为 RuntimeLogs 查询：process 过滤经
// 投影公式合成 Workload ID（公式真源 engine.WorkloadID，不散拼）；时间窗
// RFC3339（空 = 不限，坏值精确拒绝）。team 是 App 归属 Project 的 Team 轴
// （ADR-0028 resolveBackend 同族——调用方实取传入）。
func logQueryFromRequest(team string, appRow *app.App, req *telemetryv1.StreamLogsRequest) (capability.LogQuery, error) {
	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: team, Project: appRow.ProjectID, App: appRow.ID},
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
	q.Text = req.GetText()
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
