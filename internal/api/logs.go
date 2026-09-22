package api

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"

	"google.golang.org/protobuf/types/known/timestamppb"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/logs"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/victorialogs"
)

// LogsService 实现 server.v1.LogsService（T2.20）：Follow 接管线实时面
// （ring 回放 + 实时扇出），History 接落盘/构建产物检索。脱敏已在采集端
// （internal/logs），本面不二次处理。
//
// E6 W5-S1 扩展：SearchLogs 统一检索（VL LogsQL 后端——backend=jsonl 或
// VL 不可达都以 E_LOGS_BACKEND_UNAVAILABLE 诚实报错，不返回空列表冒充）；
// GetLogsBackend/SetLogsBackend 日志后端视图与切换（set 即生效——duty 收敛
// 由 victorialogs.Manager 常驻循环承载，本面只落设置）。vl/vm 可为 nil
//（测试/精简装配形态——SearchLogs 如实报后端不可用，backend 面部署态
// 如实报 unknown）。
type LogsService struct {
	serverv1.UnimplementedLogsServiceServer
	st *state.Store
	mg *logs.Manager
	// vl 是 VL 查询/入湖消费端（nil = 未装配——SearchLogs 如实报不可用）。
	vl *victorialogs.Backend
	// vm 是 VL duty 管理器（nil = 未装配——backend 视图部署态 unknown）。
	vm *victorialogs.Manager
}

// NewLogsService 构造 LogsService。
func NewLogsService(st *state.Store, mg *logs.Manager) *LogsService {
	return &LogsService{st: st, mg: mg}
}

// WithVictorialogs 注入 VL 消费端与 duty 管理器（W5-S1；链式装配，nil
// 合法）。
func (s *LogsService) WithVictorialogs(vl *victorialogs.Backend, vm *victorialogs.Manager) *LogsService {
	s.vl = vl
	s.vm = vm
	return s
}

// FollowLogs 实时跟随（server-streaming；ctx 取消即断流，重连 = 重新
// Follow）。
func (s *LogsService) FollowLogs(req *serverv1.FollowLogsRequest, stream serverv1.LogsService_FollowLogsServer) error {
	if _, err := resolveApp(stream.Context(), s.st, req.GetApp()); err != nil {
		return err
	}
	ch, cancel := s.mg.Follow(stream.Context(), req.GetApp(), req.GetService())
	defer cancel()
	for entry := range ch {
		if err := stream.Send(&serverv1.FollowLogsResponse{Entry: logEntryView(entry)}); err != nil {
			return err
		}
	}
	return nil
}

// ListHistoryLogs 历史检索（时间窗/服务/来源过滤）。
func (s *LogsService) ListHistoryLogs(ctx context.Context, req *serverv1.ListHistoryLogsRequest) (*serverv1.ListHistoryLogsResponse, error) {
	if _, err := resolveApp(ctx, s.st, req.GetApp()); err != nil {
		return nil, err
	}
	q := logs.HistoryQuery{
		App:     req.GetApp(),
		Service: req.GetService(),
		Source:  req.GetSource(),
		Limit:   int(req.GetLimit()),
	}
	if req.GetSince() != nil {
		q.Since = req.GetSince().AsTime()
	}
	if req.GetUntil() != nil {
		q.Until = req.GetUntil().AsTime()
	}
	rows, err := s.mg.History(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.LogEntryView, 0, len(rows))
	for _, e := range rows {
		out = append(out, logEntryView(e))
	}
	return &serverv1.ListHistoryLogsResponse{Entries: out}, nil
}

// Search limit 缺省与天花板（与 ListHistoryLogs/proto 契约同口径：
// buf.validate lte:1000 与 internal/logs History 上限同值）。
const (
	defaultSearchLimit = 200
	maxSearchLimit     = 1000
)

// SearchLogs 统一检索（E6 设计 §3.1，W5-S1）：VL LogsQL 后端。诚实边界
//（同码 E_LOGS_BACKEND_UNAVAILABLE 两分支）：① 当前 backend=jsonl（检索
// 面只在日志库——不返回空列表冒充）；② VL 不可达（检索降级，直播面不受
// 影响）。keyword 由 victorialogs.BuildLogsQL 转义为字面量短语（注入安全
// 硬性条款；白名单外的服务名/来源以 InvalidArgument 拒绝）。
func (s *LogsService) SearchLogs(ctx context.Context, req *serverv1.SearchLogsRequest) (*serverv1.SearchLogsResponse, error) {
	if _, err := resolveApp(ctx, s.st, req.GetApp()); err != nil {
		return nil, err
	}
	if s.vl == nil {
		return nil, apperrLogsBackendUnavailable(
			"log search is unavailable: the log backend face is not assembled in this build")
	}
	in, err := s.st.LoadLogsSettings(ctx)
	if err != nil {
		return nil, err
	}
	if in.Backend != state.LogsBackendVictorialogs {
		return nil, apperrLogsBackendUnavailable(
			"log search is unavailable: logs.backend=jsonl has no search face (search covers only the VictoriaLogs window; the JSONL history stays on disk)")
	}
	offset, err := decodeSearchCursor(req.GetCursor())
	if err != nil {
		return nil, statusInvalidArgument("invalid search cursor: "+err.Error())
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	q := victorialogs.SearchQuery{
		Apps:     req.GetApps(),
		Keyword:  req.GetKeyword(),
		Services: req.GetServices(),
		Sources:  req.GetSources(),
		Limit:    limit,
		Offset:   offset,
	}
	if req.GetTimeStart() != nil {
		q.Start = req.GetTimeStart().AsTime()
	}
	if req.GetTimeEnd() != nil {
		q.End = req.GetTimeEnd().AsTime()
	}
	rows, err := s.vl.Search(ctx, q)
	if err != nil {
		if errors.Is(err, victorialogs.ErrBadQuery) {
			return nil, statusInvalidArgument(err.Error())
		}
		return nil, apperrLogsBackendUnavailable(
			"log search is unavailable: the VictoriaLogs backend did not answer (search degraded; live tail is unaffected)")
	}
	hasMore := false
	if len(rows) > limit {
		rows = rows[:limit]
		hasMore = true
	}
	out := make([]*serverv1.SearchLogRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &serverv1.SearchLogRow{
			At:      timestamppb.New(r.At),
			App:     r.App,
			Service: r.Service,
			Source:  r.Source,
			Stderr:  r.Stderr,
			Msg:     r.Msg,
		})
	}
	resp := &serverv1.SearchLogsResponse{Rows: out}
	if hasMore {
		resp.NextCursor = encodeSearchCursor(offset + len(out))
	}
	return resp, nil
}

// encodeSearchCursor 签发分页游标（偏移量的 base64url 不透明形态——
// 客户端只回传服务端签发的值）。
func encodeSearchCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// decodeSearchCursor 解析游标（空 = 第一页；非服务端签发形态以
// InvalidArgument 拒绝——不猜不将就）。
func decodeSearchCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil || n < 0 {
		return 0, errors.New("cursor payload is not a page offset")
	}
	return n, nil
}

// GetLogsBackend 日志后端视图（CLI logs backend show / Console 卡共面）。
func (s *LogsService) GetLogsBackend(ctx context.Context, _ *serverv1.GetLogsBackendRequest) (*serverv1.GetLogsBackendResponse, error) {
	in, err := s.st.LoadLogsSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &serverv1.GetLogsBackendResponse{View: s.backendView(ctx, in)}, nil
}

// SetLogsBackend 切换日志后端（victorialogs | jsonl）：设置保存 + 审计 +
// 事件同事务（state 层 fail-closed）；duty 下一拍按新值收敛（部署或移除，
// 卷保留）。返回保存后的视图。
func (s *LogsService) SetLogsBackend(ctx context.Context, req *serverv1.SetLogsBackendRequest) (*serverv1.SetLogsBackendResponse, error) {
	opts := state.LogsSaveOptions{Actor: "human"}
	if p, ok := PrincipalFromContext(ctx); ok {
		opts.ActorTokenID = p.TokenID
	}
	if err := s.st.SaveLogsSettings(ctx, req.GetBackend(), opts); err != nil {
		return nil, err
	}
	in, err := s.st.LoadLogsSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &serverv1.SetLogsBackendResponse{View: s.backendView(ctx, in)}, nil
}

// backendView 组装后端视图（部署态 + ingest streak + 丢弃计数——设计 §2.3
//「丢弃计数常驻」的诚实口径；mg 为 nil 的测试/精简形态 = 计数恒 0、
// streak 恒未降级）。
func (s *LogsService) backendView(ctx context.Context, in state.LogsSettings) *serverv1.LogsBackendView {
	v := &serverv1.LogsBackendView{
		Backend:      in.Backend,
		BackendSet:   in.Set,
		Deployment:   logsBackendDeploymentUnknown,
		DroppedTotal: s.mg.IngestDroppedTotal(),
	}
	switch {
	case in.Backend != state.LogsBackendVictorialogs:
		v.Deployment = logsBackendDeploymentRemoved
	case s.vm == nil:
		v.Deployment = logsBackendDeploymentUnknown
	default:
		st, err := s.vm.DeploymentStatus(ctx)
		switch {
		case err != nil:
			v.Deployment = logsBackendDeploymentUnknown
		case st.Exists:
			v.Deployment = logsBackendDeploymentDeployed
		default:
			v.Deployment = logsBackendDeploymentPending
		}
	}
	v.IngestDegraded = s.mg.IngestDegraded()
	if since := s.mg.IngestStreakSince(); !since.IsZero() {
		v.IngestDegradedSince = timestamppb.New(since)
	}
	return v
}

// 部署态词表（proto LogsBackendView.deployment 注释同步维护）。
const (
	logsBackendDeploymentDeployed = "deployed"
	logsBackendDeploymentPending  = "pending"
	logsBackendDeploymentRemoved  = "removed"
	logsBackendDeploymentUnknown  = "unknown"
)

// apperrLogsBackendUnavailable 构造 E_LOGS_BACKEND_UNAVAILABLE 信封
//（503——检索面降级的诚实报错，设计 §3.1；不返回空列表冒充）。
func apperrLogsBackendUnavailable(msg string) error {
	return apperr.New("E_LOGS_BACKEND_UNAVAILABLE", "%s", msg).WithStage("logs.search")
}

func logEntryView(e logs.Entry) *serverv1.LogEntryView {
	return &serverv1.LogEntryView{
		App:     e.App,
		Service: e.Service,
		At:      timestamppb.New(e.At),
		Stderr:  e.Stderr,
		Line:    e.Line,
		Source:  e.Source,
	}
}
