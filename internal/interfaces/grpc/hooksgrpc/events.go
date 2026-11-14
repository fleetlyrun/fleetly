package hooksgrpc

import "github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

// RegisterEventSchemas 登记 hook 上下文事件的 payload 形状。
func RegisterEventSchemas(reg grpcapi.EventSchemaRegistrar) {
	reg("hook.push_accepted", HookPushAcceptedPayload{})
	reg("hook.refresh_accepted", HookRefreshAcceptedPayload{})
	reg(EventHookCreated, HookEventPayload{})
	reg(EventHookUpdated, HookEventPayload{})
	reg(EventHookRotated, HookEventPayload{})
	reg(EventHookDeleted, HookEventPayload{})
}
