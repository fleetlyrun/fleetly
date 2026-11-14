package fleetlygrpc

// 事件 payload schema 自描述注册（F1.4，架构 §7"扩展面各自注入合并"）：
// payload 形状由拥有事件的 handler 包导出 RegisterEventSchemas，本文件只做
// 聚合；summary 单源取 eventcode 注册表，完备性由 internal/assembly 守卫
// 对账。新增事件只改 owning 包，不再回中心表加条目。

import (
	"github.com/fleetlyrun/fleetly/internal/application/schema"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/eventcode"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/alertinggrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/datagrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/deliverygrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/execgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/githubappsgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/governancegrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/hooksgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/identitygrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/structuregrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/templatesgrpc"
)

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
	structuregrpc.RegisterEventSchemas(registerEventPayload)
	datagrpc.RegisterEventSchemas(registerEventPayload)
	deliverygrpc.RegisterEventSchemas(registerEventPayload)
	identitygrpc.RegisterEventSchemas(registerEventPayload)
	hooksgrpc.RegisterEventSchemas(registerEventPayload)
	governancegrpc.RegisterEventSchemas(registerEventPayload)
	execgrpc.RegisterEventSchemas(registerEventPayload)
	templatesgrpc.RegisterEventSchemas(registerEventPayload)
	githubappsgrpc.RegisterEventSchemas(registerEventPayload)
	alertinggrpc.RegisterEventSchemas(registerEventPayload)
}
