package api

import (
	"strconv"
	"time"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// EventsService 实现 server.v1.EventsService（T2.17）：Watch 是 seq 游标
// server-streaming（architecture §2.3）。实现 = 周期轮询 state.EventsSince
// （Outbox 行读，事件与业务写同事务落库）：
//   - 断线重连：客户端带 since_seq 续读（seq > since_seq 升序）；
//   - 游标过期：**流上发一帧 cursor_expired 信封错误帧（带 oldest_seq）
//     后正常关闭流**（取舍见 events.proto 注释——gateway chunked-JSON 面
//     中途改状态码不可行，显式断档帧让消费方重新对齐）；
//   - 无限流：有界批量（每轮至多 500 条）+ 轮询间隔 1s。
type EventsService struct {
	serverv1.UnimplementedEventsServiceServer
	st *state.Store
	// interval 可注入（测试加速）。
	interval time.Duration
}

// watchPollInterval 是事件流轮询周期。
const watchPollInterval = time.Second

// watchBatchSize 是单轮拉取批量上限。
const watchBatchSize = 500

// NewEventsService 构造 EventsService。
func NewEventsService(st *state.Store) *EventsService {
	return &EventsService{st: st, interval: watchPollInterval}
}

// WatchEvents 实现事件流。
func (s *EventsService) WatchEvents(req *serverv1.WatchEventsRequest, stream serverv1.EventsService_WatchEventsServer) error {
	cursor := req.GetSinceSeq()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		rows, err := s.st.EventsSince(stream.Context(), cursor, watchBatchSize)
		if err != nil {
			if expired, oldest, ok := cursorExpiredOf(err); ok {
				// 410 信封错误帧后正常关闭（不回 gRPC error status）。
				return stream.Send(&serverv1.WatchEventsResponse{Frame: &serverv1.WatchEventsResponse_CursorExpired{
					CursorExpired: &serverv1.CursorExpiredView{
						OldestSeq: oldest,
						Message:   expired.Message(),
					},
				}})
			}
			return err
		}
		for _, ev := range rows {
			if err := stream.Send(&serverv1.WatchEventsResponse{Frame: &serverv1.WatchEventsResponse_Event{
				Event: eventView(ev),
			}}); err != nil {
				return err // 客户端断开（ctx canceled 等）
			}
			cursor = ev.Seq
		}
		select {
		case <-stream.Context().Done():
			return nil // 断线重连语义：客户端以最后 seq 续读
		case <-ticker.C:
		}
	}
}

// eventView 构造事件投影。
func eventView(ev state.Event) *serverv1.EventView {
	return &serverv1.EventView{
		Seq:     ev.Seq,
		At:      tstamp(ev.At),
		Name:    ev.Name,
		Subject: ev.Subject,
		Payload: ev.Payload,
	}
}

// cursorExpiredOf 判定 err 是否为 E_EVENT_CURSOR_EXPIRED，并解出
// oldest_seq（state 层 context 携带）。
func cursorExpiredOf(err error) (*apperr.Error, int64, bool) {
	e, ok := apperr.FromError(err)
	if !ok || e.Code() != "E_EVENT_CURSOR_EXPIRED" {
		return nil, 0, false
	}
	oldest, _ := strconv.ParseInt(e.Context()["oldest_seq"], 10, 64)
	return e, oldest, true
}
