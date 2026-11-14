package governancegrpc

import "github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

// RegisterEventSchemas 登记治理上下文事件的 payload 形状。
func RegisterEventSchemas(reg grpcapi.EventSchemaRegistrar) {
	reg(EventFreezeSet, FreezeEventPayload{})
	reg(EventFreezeLifted, FreezeEventPayload{})
}
