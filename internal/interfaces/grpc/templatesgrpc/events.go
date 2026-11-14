package templatesgrpc

import "github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

// RegisterEventSchemas 登记模板上下文事件的 payload 形状。
func RegisterEventSchemas(reg grpcapi.EventSchemaRegistrar) {
	reg(EventTemplateInstantiated, TemplateEventPayload{})
	reg(EventTemplateRefreshed, TemplateRefreshedPayload{})
}
