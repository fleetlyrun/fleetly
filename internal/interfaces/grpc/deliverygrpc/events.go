package deliverygrpc

import "github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

// RegisterEventSchemas 登记交付上下文事件的 payload 形状。
func RegisterEventSchemas(reg grpcapi.EventSchemaRegistrar) {
	reg(EventUploadStored, UploadStoredPayload{})
	// Functions 五枚（ADR-0102 任务④；created/deleted 携 StructureEventPayload
	// 形状，旋钮三枚的 payload 是单键形状——schema 登记按族共用形状面）。
	reg(EventFunctionCreated, grpcapi.StructureEventPayload{})
	reg(EventFunctionDeleted, grpcapi.StructureEventPayload{})
	reg(EventFunctionRevisionChange, grpcapi.StructureEventPayload{})
	reg(EventFunctionScaled, grpcapi.StructureEventPayload{})
	reg(EventFunctionExposureChange, grpcapi.StructureEventPayload{})
	reg(EventFunctionBuildQueued, grpcapi.StructureEventPayload{})
}
