package alertinggrpc

import "github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

// RegisterEventSchemas 登记告警上下文事件的 payload 形状。
func RegisterEventSchemas(reg grpcapi.EventSchemaRegistrar) {
	reg(EventAlertRuleCreated, AlertRuleEventPayload{})
	reg(EventAlertRuleDeleted, AlertRuleEventPayload{})
	reg(EventChannelCreated, ChannelEventPayload{})
	reg(EventChannelDeleted, ChannelEventPayload{})
}
