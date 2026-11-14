package fleetlygrpc

// eventsbridge.go 是 eventsgrpc 的既名委托：wait/automation 等仍住在
// fleetlygrpc 的消费点经本文件复用同一订阅核心，避免第二份实现。

import (
	"context"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/eventsgrpc"
)

// eventsTeamFilterOf 是 eventsgrpc.TeamFilterOf 的既名委托。
func eventsTeamFilterOf(ctx context.Context) (string, error) {
	return eventsgrpc.TeamFilterOf(ctx)
}

// subscribeEvents 是 eventsgrpc 订阅核心的既名委托（与 gRPC/SSE 同源）。
func (s *Services) subscribeEvents(ctx context.Context, team string, afterSeq int64, follow bool, send func(*telemetryv1.Event) error) error {
	return eventsgrpc.New(eventsgrpc.Deps{Outbox: s.Platform.OutboxEvents, Tickets: s}).Subscribe(ctx, team, afterSeq, follow, send)
}
