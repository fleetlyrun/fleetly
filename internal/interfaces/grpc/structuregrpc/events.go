package structuregrpc

import "github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

// RegisterEventSchemas 登记结构上下文事件的 payload 形状（结构面/peer/重建）。
func RegisterEventSchemas(reg grpcapi.EventSchemaRegistrar) {
	for _, name := range []string{
		EventProjectCreated, EventProjectDeleted,
		EventAppCreated, EventAppDeleted, EventAppTeardownAborted,
		EventSecretUpdated, EventSecretDeleted,
		EventConfigUpdated,
		EventVariableUpdated, EventVariableDeleted,
		EventAppVarUpdated, EventAppVarDeleted,
		EventVolumeCreated, EventVolumeDeleted, EventVolumePurged,
		EventNetworkCreated,
	} {
		reg(name, grpcapi.StructureEventPayload{})
	}
	for _, name := range []string{
		EventNetworkPeerDeclared, EventNetworkPeerApproved, EventNetworkPeerRevoked,
	} {
		reg(name, NetworkPeerEventPayload{})
	}
	reg(EventNetworkRebuilt, NetworkRebuiltEventPayload{})
}
