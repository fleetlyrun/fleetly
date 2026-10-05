package fleetlygrpc

// 事件 payload schema 自描述注册（F1.4，架构 §7"扩展面各自注入合并"）：
// api 层拥有结构面/身份面/触发面事件 payload 形状，在本 init 反射进
// internal/schema 注册表。身份面 payload 原为 ad-hoc map——schema 钉扎
// 要求形状具名单源（下方结构体），发射点共用同一类型，wire 字节不变。
// summary 单源取 eventcode 注册表；完备性由 internal/assembly 守卫对账。

import (
	"github.com/fleetlyrun/fleetly/internal/model/eventcode"
	"github.com/fleetlyrun/fleetly/internal/schema"
)

// ---- 身份面 payload（字段只增；与原 map 构造的字节形态逐一等同） ----

// nameEventPayload 是 user.created / team.created / token.created 的最小
// 载荷。
type nameEventPayload struct {
	Name string `json:"name"`
}

// roleEventPayload 是 role.created 的载荷（scope 集）。
type roleEventPayload struct {
	Scopes []string `json:"scopes"`
}

// invitationCreatedPayload 是 invitation.created 的载荷（绑定的角色）。
type invitationCreatedPayload struct {
	Role string `json:"role"`
}

// invitationAcceptedPayload 是 invitation.accepted 的载荷（兑取成为的用户名）。
type invitationAcceptedPayload struct {
	User string `json:"user"`
}

// hookPushAcceptedPayload 是 hook.push_accepted 的载荷（触发的部署锚）。
type hookPushAcceptedPayload struct {
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	Deployment string `json:"deployment"`
}

// registerEventPayload 登记一个事件名的 payload schema（summary 取
// eventcode 单源；未在册事件名 panic）。
func registerEventPayload(name string, payload any) {
	e, ok := eventcode.Get(name)
	if !ok {
		panic("fleetlygrpc: schema registration for event " + name + " which is not in the eventcode registry")
	}
	if payload == nil {
		schema.Register(schema.KindEvent, name, e.Summary, schema.Null())
		return
	}
	schema.Register(schema.KindEvent, name, e.Summary, schema.Reflect(payload))
}

func init() {
	// 结构面（structureEventPayload，structure.go 单源）。
	for _, name := range []string{
		eventProjectCreated,
		eventProjectDeleted,
		eventAppCreated,
		eventAppDeleted,
		eventAppTeardownAborted,
		eventSecretUpdated,
		eventSecretDeleted,
		eventConfigUpdated,
		eventVariableUpdated,
		eventVariableDeleted,
		eventVolumeCreated,
		eventNetworkCreated,
	} {
		registerEventPayload(name, structureEventPayload{})
	}
	// 跨 Project peer 三拍（networkPeerEventPayload，structure.go 单源）。
	for _, name := range []string{
		eventNetworkPeerDeclared,
		eventNetworkPeerApproved,
		eventNetworkPeerRevoked,
	} {
		registerEventPayload(name, networkPeerEventPayload{})
	}
	// 网络重建（ADR-0046；networkRebuiltEventPayload，structure.go 单源）。
	registerEventPayload(eventNetworkRebuilt, networkRebuiltEventPayload{})
	// 身份面。
	registerEventPayload("user.created", nameEventPayload{})
	registerEventPayload("team.created", nameEventPayload{})
	registerEventPayload("role.created", roleEventPayload{})
	registerEventPayload("token.created", nameEventPayload{})
	registerEventPayload("token.revoked", nil) // 恒 null 载荷
	registerEventPayload("invitation.created", invitationCreatedPayload{})
	registerEventPayload("invitation.accepted", invitationAcceptedPayload{})
	// 触发面。
	registerEventPayload("hook.push_accepted", hookPushAcceptedPayload{})
	// 治理面（freezeEventPayload，governance.go 单源）。
	registerEventPayload(eventFreezeSet, freezeEventPayload{})
	registerEventPayload(eventFreezeLifted, freezeEventPayload{})
	// 上传产物面（uploadStoredPayload，uploads.go 单源；F1.10）。
	registerEventPayload(eventUploadStored, uploadStoredPayload{})
	// 数据库面（structureEventPayload，databases.go 单源；F1.12/ADR-0029）。
	registerEventPayload(eventDatabaseCreated, structureEventPayload{})
	registerEventPayload(eventDatabaseDeleted, structureEventPayload{})
}
