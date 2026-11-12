// Package logsgrpc 实现 fleetly.telemetry.v1.LogsService：App/Database/
// PubSub/BucketStore/Run 五轴寻址、行级授权与 Runtime/VictoriaLogs 双径。
package logsgrpc

import (
	"context"
	"slices"
	"time"

	"google.golang.org/grpc"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// Authorizer 是行级授权消费面（fleetlygrpc.Services 实现）。
type Authorizer interface {
	AuthorizeTeamForProject(ctx context.Context, p *project.Project) error
}

// Deps 是日志服务的依赖（窄接口，不持整个 gRPC Services）。
type Deps struct {
	Logging      func() capability.Logging
	Runtime      func() capability.Runtime
	Apps         app.Repo
	Projects     project.Repo
	Databases    database.Repo
	PubSubs      pubsub.Repo
	BucketStores bucketstore.Repo
	Runs         run.Repo
	Tasks        task.Repo
	Auth         Authorizer
}

// Service 满足 telemetryv1.LogsServiceServer。
type Service struct {
	telemetryv1.UnimplementedLogsServiceServer
	deps Deps
}

// New 构造日志服务。
func New(deps Deps) *Service { return &Service{deps: deps} }

// Register 注册日志服务。
func Register(srv *grpc.Server, deps Deps) {
	telemetryv1.RegisterLogsServiceServer(srv, New(deps))
}

var _ telemetryv1.LogsServiceServer = (*Service)(nil)

// logging/runtime 返回动态读面（零值 Deps = nil，双径各自精确失败）。
func (d Deps) logging() capability.Logging {
	if d.Logging == nil {
		return nil
	}
	return d.Logging()
}
func (d Deps) runtime() capability.Runtime {
	if d.Runtime == nil {
		return nil
	}
	return d.Runtime()
}

// ---- Logs（ADR-0040 双径：无 text = Runtime 实时路径——集群面
// ServiceLogs（swarm），字节级保持既有形态；text = 持久化检索路径
//（VictoriaLogs 保留窗 + 全文过滤，Follow 经 /select/logsql/tail 实时
// 尾随）。Logging 面停用时检索路径精确失败、实时路径不受影响。） ----

// StreamLogs 双径路由（寻址三轴互斥：app / database / run——域解析经各自
// 行链 + project 行，Team 轴实取；ADR-0035 行级授权同调用点——两路径共用
// 同一授权链。db/run 轴是 IA v3 T8 的 API 入口补齐：Service 层
// NamespaceRef 域分立（ADR-0025 决策 4 / ADR-0029）本就支持）。
func (svc *Service) StreamLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
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
func (svc *Service) streamAppLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	appRow, err := svc.deps.Apps.Get(ctx, req.GetAppId())
	if err != nil {
		return grpcapi.MapStateError(err, "app")
	}
	// 行级授权（ADR-0035）+ NamespaceRef.Team 实取（App → Project 行，
	// resolveBackend 同族——api 面硬编码 "default" 漏网在此闭合）。
	proj, err := svc.deps.Projects.Get(ctx, appRow.ProjectID)
	if err != nil {
		return grpcapi.MapStateError(err, "project")
	}
	if err := svc.deps.Auth.AuthorizeTeamForProject(ctx, proj); err != nil {
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
func (svc *Service) streamDatabaseLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	dbRow, err := svc.deps.Databases.Get(ctx, req.GetDatabaseId())
	if err != nil {
		return grpcapi.MapStateError(err, "database")
	}
	proj, err := svc.deps.Projects.Get(ctx, dbRow.ProjectID)
	if err != nil {
		return grpcapi.MapStateError(err, "project")
	}
	if err := svc.deps.Auth.AuthorizeTeamForProject(ctx, proj); err != nil {
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
func (svc *Service) streamPubSubLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	row, err := svc.deps.PubSubs.Get(ctx, req.GetPubsubId())
	if err != nil {
		return grpcapi.MapStateError(err, "pubsub")
	}
	proj, err := svc.deps.Projects.Get(ctx, row.ProjectID)
	if err != nil {
		return grpcapi.MapStateError(err, "project")
	}
	if err := svc.deps.Auth.AuthorizeTeamForProject(ctx, proj); err != nil {
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
func (svc *Service) streamBucketStoreLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	row, err := svc.deps.BucketStores.Get(ctx, req.GetBucketStoreId())
	if err != nil {
		return grpcapi.MapStateError(err, "bucket_store")
	}
	proj, err := svc.deps.Projects.Get(ctx, row.ProjectID)
	if err != nil {
		return grpcapi.MapStateError(err, "project")
	}
	if err := svc.deps.Auth.AuthorizeTeamForProject(ctx, proj); err != nil {
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
func (svc *Service) streamRunLogs(req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	ctx := stream.Context()
	runRow, err := svc.deps.Runs.Get(ctx, req.GetRunId())
	if err != nil {
		return grpcapi.MapStateError(err, "run")
	}
	taskRow, err := svc.deps.Tasks.Get(ctx, runRow.TaskID)
	if err != nil {
		return grpcapi.MapStateError(err, "task")
	}
	proj, err := svc.deps.Projects.Get(ctx, runRow.ProjectID)
	if err != nil {
		return grpcapi.MapStateError(err, "project")
	}
	if err := svc.deps.Auth.AuthorizeTeamForProject(ctx, proj); err != nil {
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
func (svc *Service) streamLogsDualPath(q capability.LogQuery, req *telemetryv1.StreamLogsRequest, stream telemetryv1.LogsService_StreamLogsServer) error {
	w := &streamLogWriter{stream: stream}
	if req.GetText() != "" {
		// 检索路径（ADR-0040 决策 4）：行级隔离由查询构造执法——只携带
		// 本域的过滤字段（VL 单租户，凭证不离开 daemon）。
		if svc.deps.logging() == nil {
			// 未配 logging.addr 是显式关态（P4-3 批 3）：精确 503，不再落
			// E_INTERNAL 脱敏 500。
			return apperr.New("E_LOG_STORE_UNAVAILABLE", "log search requires the managed log store; set config logging.addr to enable it")
		}
		if err := svc.deps.logging().Query(stream.Context(), q, w); err != nil {
			return grpcapi.MapStateError(err, "logs")
		}
		return nil
	}
	logs := capability.FacesOf(svc.deps.runtime()).Logs // 日志子面（FacesOf 协商点）
	if logs == nil {
		// 可选子面缺席是 honest failure（P4-3 批 3）：精确 501，不再落
		// E_INTERNAL 脱敏 500。
		return apperr.New("E_RUNTIME_FACE_UNSUPPORTED", "the runtime provider does not expose container logs")
	}
	if err := logs.StreamLogs(stream.Context(), q, w); err != nil {
		return grpcapi.MapStateError(err, "logs")
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
		Time: f.Time.UTC().Format(grpcapi.TimeFormatRFC3339), Line: f.Line,
	})
}
