package fleetlygrpc

// EventStreamSource 是 SSE 原生入口（assembly 挂载，ADR-0026）的事件源：
// 票据兑换（单用途）+ 订阅投递——与 gRPC StreamEvents 同源同口径
//（subscribeEvents 单一实现，两面不得漂移）。HTTP 帧化在 assembly。

import (
	"context"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
)

// EventStreamSource 暴露给 SSE 原生入口的两个动作。
type EventStreamSource struct {
	s *Services
}

// NewEventStreamSource 构造事件源。
func NewEventStreamSource(s *Services) *EventStreamSource {
	return &EventStreamSource{s: s}
}

// RedeemTicket 兑换一次性票据（单用途：命中即删；过期/未知一律拒绝）。
func (src *EventStreamSource) RedeemTicket(ticket string) bool {
	return src.s.eventTickets.redeem(ticketPurposeEvents, "", ticket)
}

// Gone 断档预检（SSE 入口必须在写出 200/SSE 头之前判定——状态行一旦
// 发出，错误只能以 SSE error 帧收口，410 语义就丢了）。
func (src *EventStreamSource) Gone(ctx context.Context, afterSeq int64) error {
	return (&EventsService{s: src.s}).eventsGoneCheck(ctx, afterSeq)
}

// Subscribe 订阅事件（与 gRPC StreamEvents 同一核心）。
func (src *EventStreamSource) Subscribe(ctx context.Context, afterSeq int64, follow bool, send func(*telemetryv1.Event) error) error {
	return src.s.subscribeEvents(ctx, afterSeq, follow, send)
}
