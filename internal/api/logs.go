package api

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/logs"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// LogsService 实现 server.v1.LogsService（T2.20）：Follow 接管线实时面
// （ring 回放 + 实时扇出），History 接落盘/构建产物检索。脱敏已在采集端
// （internal/logs），本面不二次处理。
type LogsService struct {
	serverv1.UnimplementedLogsServiceServer
	st *state.Store
	mg *logs.Manager
}

// NewLogsService 构造 LogsService。
func NewLogsService(st *state.Store, mg *logs.Manager) *LogsService {
	return &LogsService{st: st, mg: mg}
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
