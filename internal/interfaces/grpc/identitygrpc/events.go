package identitygrpc

import "github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

// RegisterEventSchemas 登记身份上下文事件的 payload 形状。
func RegisterEventSchemas(reg grpcapi.EventSchemaRegistrar) {
	reg("user.created", NameEventPayload{})
	reg("team.created", NameEventPayload{})
	reg("role.created", RoleEventPayload{})
	reg("token.created", NameEventPayload{})
	reg("token.revoked", nil) // 恒 null 载荷
	reg("invitation.created", InvitationCreatedPayload{})
	reg("invitation.accepted", InvitationAcceptedPayload{})
}
