package fleetlygrpc

// wait.go 是等待原语的组合根投影：delivery/automation handler 包共用
// grpcapi.WaitSource（outbox 游标 + Team 过滤 + 订阅核心单源）。

import (
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// waitSource 把 Services 的 outbox/订阅面收窄为 grpcapi.WaitSource。
func waitSource(s *Services) grpcapi.WaitSource {
	return grpcapi.WaitSource{
		LastSeq:    s.Platform.OutboxEvents.LastSeq,
		TeamFilter: eventsTeamFilterOf,
		Subscribe:  s.subscribeEvents,
	}
}
