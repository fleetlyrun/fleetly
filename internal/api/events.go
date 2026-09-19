package api

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc/codes"

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
	// A5（S18）：per-token 常驻流并发上限——Watch 是长驻轮询流，单 token
	// 无上限开流会放大每流的周期 EventsSince 查询；同一 token 至多
	// maxWatchStreamsPerToken 条并发流，超限 ResourceExhausted（429 信封
	// 退化形态）。计数进程内有界：键空间 = 活跃 token 数（受 tokens 表
	// 约束），槽位随流结束/ctx 取消经 defer 释放。未鉴权流（拦截器缺席
	// 的进程内测试面）无 Principal，不计不限。
	watchMu    sync.Mutex
	watchSlots map[string]int
}

// maxWatchStreamsPerToken 是单 token 的并发 Watch 流上限（A5）。
const maxWatchStreamsPerToken = 5

// watchPollInterval 是事件流轮询周期。
const watchPollInterval = time.Second

// watchBatchSize 是单轮拉取批量上限。
const watchBatchSize = 500

// NewEventsService 构造 EventsService。
func NewEventsService(st *state.Store) *EventsService {
	return &EventsService{st: st, interval: watchPollInterval, watchSlots: make(map[string]int)}
}

// acquireWatchSlot 占用一个 token 的 Watch 流槽位；已满返回 false（A5）。
func (s *EventsService) acquireWatchSlot(tokenID string) bool {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.watchSlots[tokenID] >= maxWatchStreamsPerToken {
		return false
	}
	s.watchSlots[tokenID]++
	return true
}

// releaseWatchSlot 归还槽位（计数归零即删键，防 map 无谓增长）。
func (s *EventsService) releaseWatchSlot(tokenID string) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if n := s.watchSlots[tokenID]; n <= 1 {
		delete(s.watchSlots, tokenID)
	} else {
		s.watchSlots[tokenID] = n - 1
	}
}

// WatchEvents 实现事件流（入口先过 A5 per-token 并发闸：超限
// ResourceExhausted，流结束/ctx 取消时 defer 释放槽位）。
func (s *EventsService) WatchEvents(req *serverv1.WatchEventsRequest, stream serverv1.EventsService_WatchEventsServer) error {
	if p, ok := PrincipalFromContext(stream.Context()); ok {
		if !s.acquireWatchSlot(p.TokenID) {
			return statusEnvelope(codes.ResourceExhausted,
				fmt.Sprintf("watch streams limit reached for token (max %d concurrent)", maxWatchStreamsPerToken))
		}
		defer s.releaseWatchSlot(p.TokenID)
	}
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
