package fleetlygrpc

// Runtime / Proxy / Telemetry 上下文服务实现。

import (
	"context"
	"slices"
	"time"

	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
)

// ---- Routes（Proxy & TLS 上下文） ----

type RoutesService struct {
	proxyv1.UnimplementedRoutesServiceServer
	s *Services
}

func (svc *RoutesService) CreateRoute(ctx context.Context, req *proxyv1.CreateRouteRequest) (*proxyv1.CreateRouteResponse, error) {
	// 五要素组合必填已退役 descriptor 注解（P4-1 批 1 Run B2）：拆 per-field
	// required（原 "project_id, host, app_id, process and port: must not
	// be empty" → 各字段独立报缺，文案契约变化在案），由 protovalidate
	// 拦截器在受理面拦截。host/path 语义校验是白名单动态面，保留手写。
	// 受理面校验（安全批 P0）：host/path 原样内插进 traefik 规则的反引号
	// 定界符内（Host(`%s`)），反引号等元字符可注入/劫持路由规则；白名单
	// 与 Proxy Provider 纵深面共用同一真源（capability.ValidateRouteHost）。
	// host 统一小写（R07-M2）：traefik 按小写请求 host 匹配，大写规则值
	// 永不命中——受理面即归一，落库/返回都是小写形态。
	host := capability.NormalizeRouteHost(req.GetHost())
	if err := capability.ValidateRouteHost(host); err != nil {
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
		ProjectID: req.GetProjectId(), Host: host, Path: req.GetPath(),
		AppID: req.GetAppId(), Process: req.GetProcess(), Port: req.GetPort(),
		Protocol: protocol, TLSMode: tlsMode,
	}
	// 父资源存活校验（批 0 复核，同族面）：路由挂在不存在/已删的
	// Project 或 App 下此前直接成功（routes 无 FK）——活路由指向已删
	// App 会让 Proxy 全量发布把流量钉在 tombstone 上。
	err := svc.s.commit(ctx, writeFact{
		Checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.projectAppAlive(req.GetProjectId(), req.GetAppId()),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Routes.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：Route CRUD 未入事件流（路由发布事实
			// 由 Proxy 全量发布面承载；A 化前亦无事件行，行集合零漂移）。
			func() writes.FactRow {
				return writes.FactRow{
					Action: "route.create", Resource: "route/" + row.ID, AfterFP: row.Host,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
				}
			},
		},
	})
	if err != nil {
		return nil, mapStateError(err, "route")
	}
	svc.s.System.PublishRoutesNow() // Proxy 全量发布即时触发
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
	list, err := svc.s.Routes.ListPage(ctx,
		req.GetProjectId(), req.GetAfterRouteSeq(), listLimit(req.GetLimit()))
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
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Routes.SoftDelete(ctx, req.GetId())
		},
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：Route CRUD 未入事件流（同 CreateRoute——
			// 路由发布事实由 Proxy 全量发布面承载；行集合零漂移）。
			func() writes.FactRow {
				return writes.FactRow{
					Action: "route.delete", Resource: "route/" + req.GetId(),
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
				}
			},
		},
	})
	if err != nil {
		return nil, mapStateError(err, "route")
	}
	svc.s.System.PublishRoutesNow()
	return &proxyv1.DeleteRouteResponse{}, nil
}

// ---- Logs（ADR-0040 双径：无 text = Runtime 实时路径——集群面
// ServiceLogs（swarm），字节级保持既有形态；text = 持久化检索路径
//（VictoriaLogs 保留窗 + 全文过滤，Follow 经 /select/logsql/tail 实时
// 尾随）。Logging 面停用时检索路径精确失败、实时路径不受影响。） ----

type LogsService struct {
	telemetryv1.UnimplementedLogsServiceServer
	s *Services
}

// StreamLogs 双径路由（寻址三轴互斥：app / database / run——域解析经各自
// 行链 + project 行，Team 轴实取；ADR-0035 行级授权同调用点——两路径共用
// 同一授权链。db/run 轴是 IA v3 T8 的 API 入口补齐：Service 层
// NamespaceRef 域分立（ADR-0025 决策 4 / ADR-0029）本就支持）。
func (svc *LogsService) StreamLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	axes := 0
	for _, v := range []string{req.GetAppId(), req.GetDatabaseId(), req.GetRunId(), req.GetPubsubId(), req.GetBucketStoreId()} {
		if v != "" {
			axes++
		}
	}
	if axes == 0 {
		return apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty (or exactly one of database_id / pubsub_id / bucket_store_id / run_id)")
	}
	if axes > 1 {
		return apperr.New("E_INVALID_ARGUMENT", "app_id / database_id / pubsub_id / bucket_store_id / run_id: exactly one addressing axis is allowed")
	}
	switch {
	case req.GetDatabaseId() != "":
		return svc.streamDatabaseLogs(req, stream)
	case req.GetPubsubId() != "":
		return svc.streamPubSubLogs(req, stream)
	case req.GetBucketStoreId() != "":
		return svc.streamBucketStoreLogs(req, stream)
	case req.GetRunId() != "":
		return svc.streamRunLogs(req, stream)
	default:
		return svc.streamAppLogs(req, stream)
	}
}

// streamAppLogs 是 App 轴的原通路（app 行 → project 行 → 授权 → 双径）。
func (svc *LogsService) streamAppLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	appRow, err := svc.s.Apps.Get(ctx, req.GetAppId())
	if err != nil {
		return mapStateError(err, "app")
	}
	// 行级授权（ADR-0035）+ NamespaceRef.Team 实取（App → Project 行，
	// resolveBackend 同族——api 面硬编码 "default" 漏网在此闭合）。
	proj, err := svc.s.Projects.Get(ctx, appRow.ProjectID)
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
	return svc.streamLogsDualPath(q, req, stream)
}

// streamDatabaseLogs 是 Database 轴（IA v3 T8）：库行 → project 行 → 授权
// → NamespaceRef.Database 域；WorkloadID 收敛本库载体（载体 workload id =
// databaseID，projection 投影公式），Runtime 与 VL 双径同锚。
func (svc *LogsService) streamDatabaseLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	dbRow, err := svc.s.Databases.Get(ctx, req.GetDatabaseId())
	if err != nil {
		return mapStateError(err, "database")
	}
	proj, err := svc.s.Projects.Get(ctx, dbRow.ProjectID)
	if err != nil {
		return mapStateError(err, "project")
	}
	if err := svc.s.authorizeTeamForProject(ctx, proj); err != nil {
		return err
	}
	if req.GetProcess() != "" || len(req.GetProcesses()) > 0 {
		return apperr.New("E_INVALID_ARGUMENT", "process/processes: app-axis only (database carriers are single-workload)")
	}
	containers, err := logContainerFilter(req.GetContainers())
	if err != nil {
		return err
	}
	q := capability.LogQuery{
		Namespace:   capability.NamespaceRef{Team: proj.TeamID, Project: dbRow.ProjectID, Database: dbRow.ID, DatabaseName: dbRow.Name},
		WorkloadIDs: []string{dbRow.ID},
		Containers:  containers,
		TailLines:   req.GetTailLines(),
		Follow:      req.GetFollow(),
		Text:        req.GetText(),
	}
	if err := applyLogWindow(&q, req); err != nil {
		return err
	}
	return svc.streamLogsDualPath(q, req, stream)
}

// streamPubSubLogs 是 PubSub 轴（B4b，ADR-0096 决策 2）：实例行 → project
// 行 → 授权 → NamespaceRef.PubSub 域；WorkloadID = 实例行 ID（投影公式）。
func (svc *LogsService) streamPubSubLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	row, err := svc.s.PubSubs.Get(ctx, req.GetPubsubId())
	if err != nil {
		return mapStateError(err, "pubsub")
	}
	proj, err := svc.s.Projects.Get(ctx, row.ProjectID)
	if err != nil {
		return mapStateError(err, "project")
	}
	if err := svc.s.authorizeTeamForProject(ctx, proj); err != nil {
		return err
	}
	if req.GetProcess() != "" || len(req.GetProcesses()) > 0 {
		return apperr.New("E_INVALID_ARGUMENT", "process/processes: app-axis only (pubsub carriers are single-workload)")
	}
	containers, err := logContainerFilter(req.GetContainers())
	if err != nil {
		return err
	}
	q := capability.LogQuery{
		Namespace:   capability.NamespaceRef{Team: proj.TeamID, Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name},
		WorkloadIDs: []string{row.ID},
		Containers:  containers,
		TailLines:   req.GetTailLines(),
		Follow:      req.GetFollow(),
		Text:        req.GetText(),
	}
	if err := applyLogWindow(&q, req); err != nil {
		return err
	}
	return svc.streamLogsDualPath(q, req, stream)
}

// streamBucketStoreLogs 是 Bucket Store 轴（B5，ADR-0096 决策 2）：实例行
// → project 行 → 授权 → NamespaceRef.BucketStore 域；WorkloadID = 实例行 ID。
func (svc *LogsService) streamBucketStoreLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	row, err := svc.s.BucketStores.Get(ctx, req.GetBucketStoreId())
	if err != nil {
		return mapStateError(err, "bucket_store")
	}
	proj, err := svc.s.Projects.Get(ctx, row.ProjectID)
	if err != nil {
		return mapStateError(err, "project")
	}
	if err := svc.s.authorizeTeamForProject(ctx, proj); err != nil {
		return err
	}
	if req.GetProcess() != "" || len(req.GetProcesses()) > 0 {
		return apperr.New("E_INVALID_ARGUMENT", "process/processes: app-axis only (bucket store carriers are single-workload)")
	}
	containers, err := logContainerFilter(req.GetContainers())
	if err != nil {
		return err
	}
	q := capability.LogQuery{
		Namespace:   capability.NamespaceRef{Team: proj.TeamID, Project: row.ProjectID, BucketStore: row.ID, BucketStoreName: row.Name},
		WorkloadIDs: []string{row.ID},
		Containers:  containers,
		TailLines:   req.GetTailLines(),
		Follow:      req.GetFollow(),
		Text:        req.GetText(),
	}
	if err := applyLogWindow(&q, req); err != nil {
		return err
	}
	return svc.streamLogsDualPath(q, req, stream)
}

// streamRunLogs 是 Run 轴（IA v3 T8）：run 行 → task 行（NamespaceRef.Task
// 域主键——ADR-0025 决策 4 拒塞 .App）→ project 行 → 授权；WorkloadID =
// run 载体（workload id = runID，投影公式）。
func (svc *LogsService) streamRunLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	runRow, err := svc.s.Runs.Get(ctx, req.GetRunId())
	if err != nil {
		return mapStateError(err, "run")
	}
	taskRow, err := svc.s.TaskRepo.Get(ctx, runRow.TaskID)
	if err != nil {
		return mapStateError(err, "task")
	}
	proj, err := svc.s.Projects.Get(ctx, runRow.ProjectID)
	if err != nil {
		return mapStateError(err, "project")
	}
	if err := svc.s.authorizeTeamForProject(ctx, proj); err != nil {
		return err
	}
	if req.GetProcess() != "" || len(req.GetProcesses()) > 0 {
		return apperr.New("E_INVALID_ARGUMENT", "process/processes: app-axis only (run carriers are single-workload)")
	}
	containers, err := logContainerFilter(req.GetContainers())
	if err != nil {
		return err
	}
	q := capability.LogQuery{
		Namespace:   capability.NamespaceRef{Team: proj.TeamID, Project: runRow.ProjectID, Task: taskRow.ID},
		WorkloadIDs: []string{runRow.WorkloadID},
		Containers:  containers,
		TailLines:   req.GetTailLines(),
		Follow:      req.GetFollow(),
		Text:        req.GetText(),
	}
	if runRow.WorkloadID == "" {
		// 观测对账锚缺席（历史行）：收敛到 Run ID（投影公式同锚）。
		q.WorkloadIDs = []string{runRow.ID}
	}
	if err := applyLogWindow(&q, req); err != nil {
		return err
	}
	return svc.streamLogsDualPath(q, req, stream)
}

// streamLogsDualPath 是 App 轴收敛后的双径共享体（检索径要求 Logging 在场；
// 实时径要求 Runtime Logs 子面——ADR-0040）。
func (svc *LogsService) streamLogsDualPath(q capability.LogQuery, req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	w := &streamLogWriter{stream: stream}
	if req.GetText() != "" {
		// 检索路径（ADR-0040 决策 4）：行级隔离由查询构造执法——只携带
		// 本域的过滤字段（VL 单租户，凭证不离开 daemon）。
		if svc.s.Logging == nil {
			// 未配 logging.addr 是显式关态（P4-3 批 3）：精确 503，不再落
			// E_INTERNAL 脱敏 500。
			return apperr.New("E_LOG_STORE_UNAVAILABLE", "log search requires the managed log store; set config logging.addr to enable it")
		}
		if err := svc.s.Logging.Query(stream.Context(), q, w); err != nil {
			return mapStateError(err, "logs")
		}
		return nil
	}
	logs := capability.FacesOf(svc.s.Runtime).Logs // 日志子面（FacesOf 协商点）
	if logs == nil {
		// 可选子面缺席是 honest failure（P4-3 批 3）：精确 501，不再落
		// E_INTERNAL 脱敏 500。
		return apperr.New("E_RUNTIME_FACE_UNSUPPORTED", "the runtime provider does not expose container logs")
	}
	if err := logs.StreamLogs(stream.Context(), q, w); err != nil {
		return mapStateError(err, "logs")
	}
	return nil
}

// applyLogWindow 解析 since/until 时间窗（RFC3339；空 = 不限，坏值精确拒绝）。
func applyLogWindow(q *capability.LogQuery, req *telemetryv1.StreamLogsRequest) error {
	if raw := req.GetSince(); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return apperr.New("E_INVALID_ARGUMENT", "since: must be RFC3339 (got %q)", raw)
		}
		q.Since = t
	}
	if raw := req.GetUntil(); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return apperr.New("E_INVALID_ARGUMENT", "until: must be RFC3339 (got %q)", raw)
		}
		q.Until = t
	}
	return nil
}

// logQueryFromRequest 把 RPC 请求翻译为 RuntimeLogs 查询：单值 process 与
// 多值 processes 并集去重后经投影公式合成 Workload ID 集（公式真源
// api.WorkloadID，不散拼；ADR-0060 多进程过滤）；containers 实例锚
// 交集（ADR-0067：载体归因标识直锚，三轴通用——条目非空校验同纪律）。
// 时间窗 RFC3339（空 = 不限，坏值精确拒绝）。team 是 App 归属 Project 的
// Team 轴（ADR-0028 resolveBackend 同族——调用方实取传入）。
func logQueryFromRequest(team string, appRow *app.App, req *telemetryv1.StreamLogsRequest) (capability.LogQuery, error) {
	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: team, Project: appRow.ProjectID, App: appRow.ID, AppName: appRow.Name},
		TailLines: req.GetTailLines(),
		Follow:    req.GetFollow(),
	}
	workloads := make([]string, 0, len(req.GetProcesses())+1)
	if p := req.GetProcess(); p != "" {
		workloads = append(workloads, api.WorkloadID(appRow.ID, p))
	}
	for _, p := range req.GetProcesses() {
		if p == "" {
			return q, apperr.New("E_INVALID_ARGUMENT", "processes: entries must be non-empty (exact rejection over silent drop)")
		}
		if id := api.WorkloadID(appRow.ID, p); !slices.Contains(workloads, id) {
			workloads = append(workloads, id)
		}
	}
	q.WorkloadIDs = workloads
	containers, err := logContainerFilter(req.GetContainers())
	if err != nil {
		return q, err
	}
	q.Containers = containers
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

// logContainerFilter 归一 containers 实例锚（去重 + 非空精确拒绝；ADR-0067）。
func logContainerFilter(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		if c == "" {
			return nil, apperr.New("E_INVALID_ARGUMENT", "containers: entries must be non-empty (exact rejection over silent drop)")
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
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
